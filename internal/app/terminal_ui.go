package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/health"
	"github.com/chokunplayz/poormanwebctrl/internal/managed"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

type terminalUI struct {
	io.Writer
	ansi               bool
	input              *os.File
	confirmSetupCancel bool
	setupCanceled      bool
}

const (
	panelInnerWidth       = 70
	dashboardLabelWidth   = 26
	maxDashboardSelection = 12
)

func newTerminalUI(w io.Writer) *terminalUI {
	ansi := false
	if f, ok := w.(*os.File); ok {
		if info, err := f.Stat(); err == nil {
			ansi = info.Mode()&os.ModeCharDevice != 0 && os.Getenv("TERM") != "dumb"
		}
	}
	return &terminalUI{Writer: w, ansi: ansi}
}

func attachTerminalInput(ui *terminalUI, in io.Reader) {
	if file, ok := in.(*os.File); ok && isTerminal(file) {
		ui.input = file
	}
}

func (ui *terminalUI) paint(code, value string) string {
	if !ui.ansi {
		return value
	}
	return "\033[" + code + "m" + value + "\033[0m"
}

func (ui *terminalUI) clear() {
	if ui.ansi {
		fmt.Fprint(ui, "\033[2J\033[H")
	}
}

func (ui *terminalUI) brand(section, subtitle string) {
	if !ui.ansi {
		fmt.Fprintln(ui, "poorman "+section)
		fmt.Fprintln(ui, subtitle)
		fmt.Fprintln(ui, strings.Repeat("-", 72))
		return
	}
	fmt.Fprintln(ui, ui.paint("38;5;45;1", "◆ POORMAN")+ui.paint("38;5;244", "  /  ")+ui.paint("38;5;255;1", section))
	fmt.Fprintln(ui, ui.paint("38;5;244", "  "+subtitle))
	fmt.Fprintln(ui, ui.paint("38;5;238", "  "+strings.Repeat("─", 72)))
}

func (ui *terminalUI) panel(title, body string) {
	lines := strings.Split(body, "\n")
	innerWidth := panelInnerWidth
	for _, line := range lines {
		if width := displayWidth(line); width > innerWidth {
			innerWidth = width
		}
	}
	lineWidth := innerWidth - displayWidth(title) - 1
	if lineWidth < 0 {
		lineWidth = 0
	}
	fmt.Fprintln(ui, ui.paint("38;5;238", "╭─ ")+ui.paint("38;5;45;1", title)+ui.paint("38;5;238", " "+strings.Repeat("─", lineWidth)+"╮"))
	for _, line := range lines {
		fmt.Fprintf(ui, "%s %s %s\n", ui.paint("38;5;238", "│"), padPanelLine(line, innerWidth), ui.paint("38;5;238", "│"))
	}
	fmt.Fprintln(ui, ui.paint("38;5;238", "╰"+strings.Repeat("─", innerWidth+2)+"╯"))
}

func displayWidth(value string) int {
	return utf8.RuneCountInString(stripANSI(value))
}

func stripANSI(value string) string {
	var clean strings.Builder
	for i := 0; i < len(value); {
		if value[i] == 0x1b && i+1 < len(value) && value[i+1] == '[' {
			i += 2
			for i < len(value) {
				b := value[i]
				i++
				if b >= '@' && b <= '~' {
					break
				}
			}
			continue
		}
		_, size := utf8.DecodeRuneInString(value[i:])
		clean.WriteString(value[i : i+size])
		i += size
	}
	return clean.String()
}

func padPanelLine(value string, width int) string {
	return value + strings.Repeat(" ", width-displayWidth(value))
}

func (ui *terminalUI) dashboard(ctx context.Context, c config.Config, path string) {
	ui.dashboardSelected(c, path, 1, dashboardServiceStatuses(ctx, c, path))
}

func (ui *terminalUI) dashboardSelected(c config.Config, path string, selected int, statuses []health.ServiceStatus) {
	ui.brand("operations", "A calm control surface for your self-hosted stack")
	db := "none"
	role := "—"
	if c.Database != nil {
		db, role = c.Database.Provider, c.Database.Role
	}
	site := "no sites"
	if len(c.Sites) > 0 {
		site = c.Sites[0].Domain
		if len(c.Sites) > 1 {
			site += fmt.Sprintf(" + %d more", len(c.Sites)-1)
		}
	}
	databaseLine := fmt.Sprintf("%s (%s)", db, role)
	instances := databaseInstances(c, path)
	if len(instances) > 1 {
		labels := make([]string, 0, len(instances))
		for _, instance := range instances {
			labels = append(labels, managed.InstanceLabel(instance))
		}
		databaseLine += "\ninstances " + strings.Join(labels, ", ")
	}
	ui.panel("STACK", fmt.Sprintf("web       %s\ndatabase  %s\nsite      %s\nconfig    %s", c.WebServer.Provider, databaseLine, site, path))
	ui.panel("MANAGED SERVICES", ui.managedServiceStatusLines(statuses))
	ui.panel("GUARDRAILS", fmt.Sprintf("https   %s     firewall  %s     backups  %s", ui.status(enabledLabel(c.TLS.Enabled), c.TLS.Enabled), ui.status(enabledLabel(c.Firewall.Enabled), c.Firewall.Enabled), ui.status(enabledLabel(c.Backups.Enabled), c.Backups.Enabled)))
	replicationAction := "replication status"
	if c.Database == nil || c.Database.Role == "standalone" || c.Database.Role == "" {
		replicationAction += " (not configured)"
	}
	backupAction := "run backup"
	if !c.Backups.Enabled {
		backupAction += " (disabled)"
	}
	actions := []string{
		dashboardActionLine(1, -1, selected, "preview plan", ""),
		dashboardActionLine(2, -1, selected, "apply configuration", ""),
		dashboardActionLine(3, -1, selected, "health status", ""),
		dashboardActionLine(4, -1, selected, backupAction, ""),
		dashboardActionLine(5, -1, selected, replicationAction, ""),
		dashboardActionLine(6, -1, selected, "Firewall management", ""),
		dashboardActionLine(7, -1, selected, "long-term operations", ""),
		dashboardActionLine(8, -1, selected, "Virtual hosts", ""),
		dashboardActionLine(9, -1, selected, "Stack settings", ""),
		dashboardActionLine(10, -1, selected, "guided replica setup", ""),
		dashboardActionLine(11, -1, selected, "guardrails & backups", ""),
		dashboardActionLine(12, -1, selected, "Database management", ""),
		dashboardActionLine(0, -1, selected, "exit", ""),
	}
	ui.panel("ACTIONS", strings.Join(actions, "\n"))
	fmt.Fprintln(ui, ui.paint("38;5;244", "  ↑/↓ choose  ·  enter confirm  ·  q exit"))
}

func dashboardActionLine(left, right, selected int, leftLabel, rightLabel string) string {
	leftMarker, rightMarker := "  ", "  "
	if selected == left {
		leftMarker = "> "
	}
	if selected == right {
		rightMarker = "> "
	}
	if right < 0 {
		return fmt.Sprintf("%s%-2d  %s", leftMarker, left, leftLabel)
	}
	return fmt.Sprintf("%s%-2d  %-*s%s%-2d  %s", leftMarker, left, dashboardLabelWidth, leftLabel, rightMarker, right, rightLabel)
}

func dashboardChoice(ctx context.Context, in io.Reader, reader *bufio.Reader, ui *terminalUI, c config.Config, path string) string {
	statuses := dashboardServiceStatuses(ctx, c, path)
	file, ok := in.(*os.File)
	if ok && isTerminal(file) && rawTerminalAvailable(file) {
		if choice, rawOK := rawDashboardChoice(file, ui, c, path, statuses); rawOK {
			return choice
		}
	}
	ui.dashboardSelected(c, path, 1, statuses)
	return selectMenu(reader, ui, "Select action", "1",
		selectorChoice{Value: "1", Label: "preview plan"},
		selectorChoice{Value: "2", Label: "apply changes"},
		selectorChoice{Value: "3", Label: "health check"},
		selectorChoice{Value: "4", Label: "backup and restore"},
		selectorChoice{Value: "5", Label: "replication"},
		selectorChoice{Value: "6", Label: "firewall"},
		selectorChoice{Value: "7", Label: "long-term operations"},
		selectorChoice{Value: "8", Label: "virtual hosts"},
		selectorChoice{Value: "9", Label: "stack settings"},
		selectorChoice{Value: "10", Label: "guided replica setup"},
		selectorChoice{Value: "11", Label: "guardrails and backups"},
		selectorChoice{Value: "12", Label: "database management"},
		selectorChoice{Value: "0", Label: "exit"},
	)
}

func rawTerminalAvailable(file *os.File) bool {
	check := exec.Command("stty", "-g")
	check.Stdin = file
	return check.Run() == nil
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func rawDashboardChoice(file *os.File, ui *terminalUI, c config.Config, path string, statuses []health.ServiceStatus) (string, bool) {
	getState := exec.Command("stty", "-g")
	getState.Stdin = file
	state, err := getState.Output()
	if err != nil {
		return "", false
	}
	defer func() {
		restore := exec.Command("stty", strings.TrimSpace(string(state)))
		restore.Stdin = file
		_ = restore.Run()
	}()
	raw := exec.Command("stty", "-icanon", "-echo", "min", "1", "time", "0")
	raw.Stdin = file
	if err := raw.Run(); err != nil {
		return "", false
	}

	selected := 1
	typed := ""
	ui.clear()
	ui.dashboardSelected(c, path, selected, statuses)
	for {
		b, ok := readRawByte(file)
		if !ok {
			return "", false
		}
		switch b {
		case '\r', '\n':
			if typed != "" {
				return typed, true
			}
			return strconv.Itoa(selected), true
		case 'q', 'Q':
			return "q", true
		case 0x1b:
			prefix, ok := readRawMaybeByte(file)
			if !ok {
				return "0", true
			}
			if prefix != '[' {
				return "0", true
			}
			code, ok := readRawMaybeByte(file)
			if !ok {
				return "0", true
			}
			switch code {
			case 'A':
				selected--
				if selected < 0 {
					selected = maxDashboardSelection
				}
			case 'B':
				selected++
				if selected > maxDashboardSelection {
					selected = 0
				}
			default:
				continue
			}
			ui.clear()
			ui.dashboardSelected(c, path, selected, statuses)
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			typed += string(b)
		case 8, 127:
			if len(typed) > 0 {
				typed = typed[:len(typed)-1]
			}
		}
	}
}

func dashboardServiceStatuses(ctx context.Context, c config.Config, path string) []health.ServiceStatus {
	services := managedServices(c, path)
	p, err := platform.Detect()
	if err != nil {
		return health.ServiceStatuses(ctx, services, platform.Platform{})
	}
	return health.ServiceStatuses(ctx, services, p)
}

func (ui *terminalUI) managedServiceStatusLines(statuses []health.ServiceStatus) string {
	if len(statuses) == 0 {
		return ui.paint("38;5;244", "No managed services recorded")
	}
	lines := make([]string, 0, len(statuses)+1)
	up, down, changing, unknown := 0, 0, 0, 0
	for _, status := range statuses {
		label := managed.InstanceLabel(status.Service)
		if status.Service.Role != "" {
			label += " [" + status.Service.Role + "]"
		}
		if status.Service.Kind == "database" && status.Service.ConfigPath != "" {
			label += " · " + filepath.Base(status.Service.ConfigPath)
		}
		stateLabel := strings.ToUpper(string(status.State))
		state := "● " + fmt.Sprintf("%-8s", stateLabel)
		switch status.State {
		case health.ServiceUp:
			up++
			state = ui.paint("38;5;42;1", state)
		case health.ServiceDown:
			down++
			state = ui.paint("38;5;196;1", state)
		case health.ServiceChanging:
			changing++
			state = ui.paint("38;5;214;1", state)
		default:
			unknown++
			state = ui.paint("38;5;244", state)
		}
		lines = append(lines, fmt.Sprintf("%s  %-8s  %s", state, status.Service.Kind, label))
	}
	summary := fmt.Sprintf("%d up · %d down · %d changing · %d unknown", up, down, changing, unknown)
	lines = append(lines, ui.paint("38;5;244", summary))
	return strings.Join(lines, "\n")
}

func (ui *terminalUI) status(label string, good bool) string {
	if good {
		return ui.paint("38;5;42;1", "● "+label)
	}
	return ui.paint("38;5;214;1", "● "+label)
}

func (ui *terminalUI) success(message string) {
	fmt.Fprintln(ui, ui.paint("38;5;42;1", "✓ ")+message)
}
func (ui *terminalUI) warn(message string)  { fmt.Fprintln(ui, ui.paint("38;5;214;1", "! ")+message) }
func (ui *terminalUI) muted(message string) { fmt.Fprintln(ui, ui.paint("38;5;244", message)) }

func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func prompt(reader *bufio.Reader, ui *terminalUI, label, fallback string) string {
	if ui.setupCanceled {
		return fallback
	}
	if ui.confirmSetupCancel && ui.input != nil && isTerminal(ui.input) && reader.Buffered() == 0 {
		if value, ok := rawPrompt(ui, label, fallback); ok {
			return value
		}
	}
	fmt.Fprintf(ui, "%s [%s]: ", label, fallback)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return fallback
	}
	return answer
}

// selectOption is the finite-choice counterpart to prompt. Interactive TUI
// sessions get a real arrow-key selector; pipes, tests, and scripted setup
// retain a deterministic numbered/value input mode.
func selectOption(reader *bufio.Reader, ui *terminalUI, label, fallback string, options ...string) string {
	choices := make([]selectorChoice, 0, len(options))
	for _, option := range options {
		choiceLabel := option
		if option == "0" {
			choiceLabel = "back"
		}
		choices = append(choices, selectorChoice{Value: option, Label: choiceLabel})
	}
	return selectChoices(reader, ui, label, fallback, choices...)
}

type selectorChoice struct {
	Value string
	Label string
}

func selectMenu(reader *bufio.Reader, ui *terminalUI, label, fallback string, options ...selectorChoice) string {
	return selectChoices(reader, ui, label, fallback, options...)
}

func selectChoices(reader *bufio.Reader, ui *terminalUI, label, fallback string, options ...selectorChoice) string {
	if ui.setupCanceled {
		return escapeOption(fallback, options)
	}
	if len(options) == 0 {
		return prompt(reader, ui, label, fallback)
	}
	if ui.input != nil && isTerminal(ui.input) && reader.Buffered() == 0 {
		if value, ok := rawSelectOption(ui, label, fallback, options); ok {
			return value
		}
	}
	fmt.Fprintf(ui, "%s [%s]\n", label, fallback)
	for i, option := range options {
		fmt.Fprintf(ui, "  %d  %s\n", i+1, option.Label)
	}
	answer, err := reader.ReadString('\n')
	if err == io.EOF {
		return escapeOption(fallback, options)
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return fallback
	}
	return selectedOptionValue(answer, fallback, options)
}

func selectedOptionValue(answer, fallback string, options []selectorChoice) string {
	for i, option := range options {
		if answer == strconv.Itoa(i+1) || strings.EqualFold(answer, option.Value) || strings.EqualFold(answer, option.Label) {
			return option.Value
		}
	}
	lower := strings.ToLower(answer)
	for _, option := range options {
		if (lower == "yes" && strings.EqualFold(option.Value, "y")) || (lower == "no" && strings.EqualFold(option.Value, "n")) {
			return option.Value
		}
	}
	return fallback
}

func rawSelectOption(ui *terminalUI, label, fallback string, options []selectorChoice) (string, bool) {
	file := ui.input
	getState := exec.Command("stty", "-g")
	getState.Stdin = file
	state, err := getState.Output()
	if err != nil {
		return "", false
	}
	defer func() {
		restore := exec.Command("stty", strings.TrimSpace(string(state)))
		restore.Stdin = file
		_ = restore.Run()
	}()
	rawArgs := []string{"-icanon", "-echo", "min", "1", "time", "0"}
	if ui.confirmSetupCancel {
		rawArgs = append(rawArgs, "-isig")
	}
	raw := exec.Command("stty", rawArgs...)
	raw.Stdin = file
	if err := raw.Run(); err != nil {
		return "", false
	}

	selected := 0
	for i, option := range options {
		if strings.EqualFold(option.Value, fallback) {
			selected = i
			break
		}
	}
	render := func() {
		fmt.Fprint(ui, "\033[2J\033[H")
		fmt.Fprintln(ui, ui.paint("38;5;45;1", label))
		for i, option := range options {
			marker := "  "
			if i == selected {
				marker = "> "
			}
			fmt.Fprintf(ui, "%s%s\n", marker, option.Label)
		}
		fmt.Fprintln(ui, ui.paint("38;5;244", "Use ↑/↓ and Enter"))
	}
	render()
	for {
		b, ok := readRawByte(file)
		if !ok {
			return "", false
		}
		switch b {
		case 0x03:
			if ui.confirmSetupCancel && confirmSetupCancellation(ui, file) {
				ui.setupCanceled = true
				return escapeOption(fallback, options), true
			}
			render()
		case '\r', '\n':
			fmt.Fprintf(ui, "Selected: %s\n", options[selected].Label)
			return options[selected].Value, true
		case 'q', 'Q':
			return escapeOption(fallback, options), true
		case 'k':
			selected--
			if selected < 0 {
				selected = len(options) - 1
			}
			render()
		case 'j':
			selected = (selected + 1) % len(options)
			render()
		case 0x1b:
			prefix, ok := readRawMaybeByte(file)
			if !ok {
				return escapeOption(fallback, options), true
			}
			if prefix != '[' {
				return escapeOption(fallback, options), true
			}
			code, ok := readRawMaybeByte(file)
			if !ok {
				return escapeOption(fallback, options), true
			}
			switch code {
			case 'A':
				selected--
				if selected < 0 {
					selected = len(options) - 1
				}
				render()
			case 'B':
				selected = (selected + 1) % len(options)
				render()
			}
		}
	}
}

func rawPrompt(ui *terminalUI, label, fallback string) (string, bool) {
	file := ui.input
	getState := exec.Command("stty", "-g")
	getState.Stdin = file
	state, err := getState.Output()
	if err != nil {
		return "", false
	}
	defer func() {
		restore := exec.Command("stty", strings.TrimSpace(string(state)))
		restore.Stdin = file
		_ = restore.Run()
	}()
	raw := exec.Command("stty", "-icanon", "-echo", "-isig", "min", "1", "time", "0")
	raw.Stdin = file
	if err := raw.Run(); err != nil {
		return "", false
	}

	fmt.Fprintf(ui, "%s [%s]: ", label, fallback)
	value := make([]byte, 0, len(fallback))
	for {
		b, ok := readRawByte(file)
		if !ok {
			return "", false
		}
		switch b {
		case 0x03:
			if confirmSetupCancellation(ui, file) {
				ui.setupCanceled = true
				return fallback, true
			}
			fmt.Fprintf(ui, "%s [%s]: %s", label, fallback, string(value))
		case '\r', '\n':
			fmt.Fprintln(ui)
			answer := strings.TrimSpace(string(value))
			if answer == "" {
				return fallback, true
			}
			return answer, true
		case 4:
			fmt.Fprintln(ui)
			if len(value) == 0 {
				return fallback, true
			}
			return strings.TrimSpace(string(value)), true
		case 8, 127:
			if len(value) > 0 {
				_, size := utf8.DecodeLastRune(value)
				value = value[:len(value)-size]
				fmt.Fprint(ui, "\b \b")
			}
		default:
			if b >= 0x20 {
				value = append(value, b)
				fmt.Fprint(ui, string([]byte{b}))
			}
		}
	}
}

func confirmSetupCancellation(ui *terminalUI, input io.Reader) bool {
	fmt.Fprint(ui, "\nCancel setup? [y/N] ")
	answer := byte(0)
	for {
		b, ok := readRawByte(input)
		if !ok {
			fmt.Fprintln(ui)
			return answer == 'y' || answer == 'Y'
		}
		switch b {
		case 'y', 'Y':
			answer = b
			fmt.Fprint(ui, string([]byte{b}))
		case 'n', 'N':
			answer = b
			fmt.Fprint(ui, string([]byte{b}))
		case 8, 127:
			if answer != 0 {
				answer = 0
				fmt.Fprint(ui, "\b \b")
			}
		case '\r', '\n':
			fmt.Fprintln(ui)
			return answer == 'y' || answer == 'Y'
		case 0x03:
			fmt.Fprintln(ui)
			return false
		}
	}
}

func readRawByte(input io.Reader) (byte, bool) {
	for {
		var b [1]byte
		n, err := input.Read(b[:])
		if n == 1 {
			return b[0], true
		}
		if err != nil {
			return 0, false
		}
	}
}

func readRawMaybeByte(file *os.File) (byte, bool) {
	fd := int(file.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		return 0, false
	}
	defer func() { _ = syscall.SetNonblock(fd, false) }()
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		var b [1]byte
		n, err := syscall.Read(fd, b[:])
		if n == 1 {
			return b[0], true
		}
		if err != syscall.EAGAIN && err != syscall.EWOULDBLOCK && err != nil {
			return 0, false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return 0, false
}

func escapeOption(fallback string, options []selectorChoice) string {
	for _, option := range options {
		if option.Value == "0" {
			return option.Value
		}
	}
	return fallback
}
