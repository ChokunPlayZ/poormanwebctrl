package app

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/executor"
	"github.com/chokunplayz/poormanwebctrl/internal/health"
	"github.com/chokunplayz/poormanwebctrl/internal/ops"
	"github.com/chokunplayz/poormanwebctrl/internal/plan"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
	"github.com/chokunplayz/poormanwebctrl/internal/provider"
)

const version = "1.0.0-dev"

func Run(args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		usage(out)
		return nil
	}
	switch args[0] {
	case "init":
		return initCommand(args[1:], out)
	case "plan":
		return planCommand(args[1:], out)
	case "apply":
		return applyCommand(args[1:], in, out, errOut)
	case "tui":
		return tuiCommand(args[1:], in, out)
	case "status":
		return statusCommand(args[1:], out)
	case "replica":
		return replicaCommand(args[1:], in, out, errOut)
	case "backup":
		return backupCommand(args[1:], in, out, errOut)
	case "version", "--version", "-v":
		fmt.Fprintln(out, version)
		return nil
	case "help", "--help", "-h":
		usage(out)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `poorman makes a server match a small, auditable configuration.

Usage:
  poorman init [-f poorman.json]
  poorman plan [-f poorman.json]
  poorman apply [-f poorman.json] [--yes]
  poorman tui [-f poorman.json]
  poorman status [-f poorman.json]
  poorman replica status|promote [-f poorman.json] [--yes]
  poorman backup [--yes]
  poorman version

Start with "poorman init", edit the file, then preview with "poorman plan".`)
}

func statusCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("f", "poorman.json", "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	p, err := platform.Detect()
	if err != nil {
		return err
	}
	return health.Report(context.Background(), c, p, out)
}

func replicaCommand(args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("replica requires status or promote")
	}
	action := args[0]
	fs := flag.NewFlagSet("replica "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("f", "poorman.json", "configuration file")
	yes := fs.Bool("yes", false, "confirm replica promotion")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	p, err := platform.Detect()
	if err != nil {
		return err
	}
	var operation plan.Plan
	if action == "status" {
		operation, err = provider.ReplicaStatus(c, p)
	} else if action == "promote" {
		operation, err = provider.PromoteReplica(c, p)
	} else {
		return fmt.Errorf("unknown replica action %q", action)
	}
	if err != nil {
		return err
	}
	operation.Print(out)
	if action == "promote" && !*yes {
		fmt.Fprint(out, "Type PROMOTE to confirm the old primary is fenced: ")
		answer, _ := bufio.NewReader(in).ReadString('\n')
		if strings.TrimSpace(answer) != "PROMOTE" {
			fmt.Fprintln(out, "Cancelled.")
			return nil
		}
	}
	return executor.Apply(context.Background(), operation, in, out, errOut)
}

func backupCommand(args []string, in io.Reader, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	yes := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	operation := plan.Plan{Platform: "local", Steps: []plan.Step{plan.Cmd("Run configured backup", "/usr/local/sbin/poorman-backup", true)}}
	operation.Print(out)
	if !*yes {
		fmt.Fprint(out, "Run backup now? [y/N] ")
		answer, _ := bufio.NewReader(in).ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Fprintln(out, "Cancelled.")
			return nil
		}
	}
	return executor.Apply(context.Background(), operation, in, out, errOut)
}

func tuiCommand(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("f", "poorman.json", "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ui := newTerminalUI(out)
	if _, err := os.Stat(*path); err == nil {
		return tuiDashboard(*path, in, ui)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	reader := bufio.NewReader(in)
	ui.clear()
	ui.brand("guided setup", "Build a safe, auditable server configuration")
	ui.panel("WEB SERVER", "1  nginx                 recommended\n2  apache\n3  openlitespeed")
	choice := prompt(reader, ui, "Selection", "1")
	providerName := "nginx"
	if choice == "2" {
		providerName = "apache"
	} else if choice == "3" {
		providerName = "openlitespeed"
	}

	domain := prompt(reader, ui, "First site domain", "example.com")
	root := prompt(reader, ui, "Document root", "/var/www/"+domain)
	owner := prompt(reader, ui, "System/SFTP user", "webadmin")
	runtimeName := prompt(reader, ui, "Runtime (php/static)", "php")
	dbChoice := prompt(reader, ui, "Database (mariadb/postgresql/none)", "mariadb")
	var database *config.Database
	if dbChoice != "none" {
		database = &config.Database{Provider: dbChoice, Role: "standalone", Name: prompt(reader, ui, "Database name", "website"), User: prompt(reader, ui, "Database user", "website"), PasswordEnv: "POORMAN_DB_PASSWORD"}
	}
	var wordpress *config.WordPress
	if runtimeName == "php" && database != nil && strings.EqualFold(prompt(reader, ui, "Install WordPress? (y/N)", "n"), "y") {
		wordpress = &config.WordPress{Title: domain, AdminUser: "admin", AdminEmail: prompt(reader, ui, "WordPress admin email", "admin@"+domain), AdminPassEnv: "POORMAN_WP_ADMIN_PASSWORD"}
	}
	tlsEnabled := strings.EqualFold(prompt(reader, ui, "Enable HTTPS with Let's Encrypt? (Y/n)", "y"), "y")
	backupEnabled := strings.EqualFold(prompt(reader, ui, "Enable nightly backups? (Y/n)", "y"), "y")

	c := config.Config{
		Version:   1,
		WebServer: config.WebServer{Provider: providerName},
		Database:  database,
		Access:    config.Access{Users: []config.User{{Name: owner, Home: "/home/" + owner, SFTPOnly: true}}},
		Sites:     []config.Site{{Domain: domain, Root: root, Owner: owner, Runtime: runtimeName, WordPress: wordpress}},
		TLS:       config.TLS{Enabled: tlsEnabled, Email: "admin@" + domain},
		Firewall:  config.Firewall{Enabled: true},
		Backups:   config.Backup{Enabled: backupEnabled, Destination: "/var/backups/poorman", Schedule: "0 3 * * *"},
	}
	if err := config.Write(*path, c); err != nil {
		return err
	}
	ui.success(fmt.Sprintf("Created %s", *path))
	ui.muted(fmt.Sprintf("Preview it on the target Linux server with: poorman plan -f %s", *path))
	return nil
}

func tuiDashboard(path string, in io.Reader, ui *terminalUI) error {
	c, err := config.Load(path)
	if err != nil {
		return err
	}
	reader := bufio.NewReader(in)
	for {
		ui.clear()
		ui.dashboard(c, path)
		switch prompt(reader, ui, "Select action", "1") {
		case "1":
			if err := planCommand([]string{"-f", path}, ui); err != nil {
				return err
			}
		case "2":
			if err := applyCommand([]string{"-f", path}, reader, ui, ui); err != nil {
				return err
			}
		case "3":
			if err := statusCommand([]string{"-f", path}, ui); err != nil {
				ui.warn("Health warning: " + err.Error())
			}
		case "4":
			if err := backupCommand(nil, reader, ui, ui); err != nil {
				return err
			}
		case "5":
			if err := replicaCommand([]string{"status", "-f", path}, reader, ui, ui); err != nil {
				ui.warn("Replication status unavailable: " + err.Error())
			}
		case "6":
			if err := firewallTUI(path, reader, ui); err != nil {
				ui.warn("Firewall operation unavailable: " + err.Error())
			}
		case "7":
			if err := operationsTUI(c, reader, ui); err != nil {
				ui.warn("Operations unavailable: " + err.Error())
			}
		case "0", "q", "Q":
			return nil
		default:
			ui.warn("Unknown selection.")
		}
	}
}

func operationsTUI(c config.Config, reader *bufio.Reader, ui *terminalUI) error {
	services := configuredServices(c)
	for {
		ui.clear()
		ui.brand("Long-term operations", "Inspect the host and keep services healthy")
		ui.panel("READ-ONLY", "These views do not change server state")
		ui.panel("ACTIONS", "1  host resource stats\n2  recent service logs\n3  backup inventory\n0  back")
		switch prompt(reader, ui, "Select action", "1") {
		case "1":
			ui.clear()
			ui.brand("Host resource stats", "A point-in-time view of capacity and service failures")
			if err := ops.Stats(context.Background(), ui); err != nil {
				ui.warn(err.Error())
			}
			pause(reader, ui)
		case "2":
			ui.clear()
			ui.brand("Service logs", "Recent entries from the system journal")
			for i, service := range services {
				fmt.Fprintf(ui, "%d  %s\n", i+1, service)
			}
			fmt.Fprintln(ui, "s  system boot log\n0  back")
			choice := prompt(reader, ui, "Service", "1")
			if choice == "0" {
				continue
			}
			service := ""
			if choice == "s" || choice == "S" {
				service = "system"
			} else if n, err := parseChoice(choice, len(services)); err == nil {
				service = services[n-1]
			} else {
				ui.warn("Unknown service.")
				pause(reader, ui)
				continue
			}
			lineCount := prompt(reader, ui, "Lines", "50")
			lines := 50
			if n, err := parsePositive(lineCount); err == nil {
				lines = n
			}
			if err := ops.Logs(context.Background(), service, lines, ui); err != nil {
				ui.warn(err.Error())
			}
			pause(reader, ui)
		case "3":
			ui.clear()
			ui.brand("Backup inventory", "Review artifacts produced by the configured backup job")
			ui.muted("Destination: " + c.Backups.Destination)
			if err := ops.BackupFiles(context.Background(), c.Backups.Destination, ui); err != nil {
				ui.warn(err.Error())
			}
			pause(reader, ui)
		case "0", "q", "Q":
			return nil
		default:
			ui.warn("Unknown selection.")
		}
	}
}

func configuredServices(c config.Config) []string {
	services := []string{webServiceName(c.WebServer.Provider)}
	if c.Database != nil {
		services = append(services, c.Database.Provider)
	}
	if c.Access.FTP.Enabled {
		services = append(services, "vsftpd")
	}
	return services
}

func webServiceName(providerName string) string {
	switch providerName {
	case "apache":
		return "apache2"
	case "openlitespeed":
		return "lsws"
	default:
		return "nginx"
	}
}

func parseChoice(value string, max int) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 1 || n > max {
		return 0, fmt.Errorf("invalid choice")
	}
	return n, nil
}

func parsePositive(value string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 1 || n > 500 {
		return 0, fmt.Errorf("invalid line count")
	}
	return n, nil
}

func pause(reader *bufio.Reader, ui *terminalUI) {
	prompt(reader, ui, "Press enter to continue", "")
}

func firewallTUI(path string, in io.Reader, ui *terminalUI) error {
	c, err := config.Load(path)
	if err != nil {
		return err
	}
	p, err := platform.Detect()
	if err != nil {
		return err
	}
	reader := bufio.NewReader(in)
	for {
		ui.clear()
		ui.brand("Firewall management", "Review and apply the host access policy")
		ui.panel("POLICY", "Configured policy  "+ui.status(enabledLabel(c.Firewall.Enabled), c.Firewall.Enabled))
		ui.panel("ACTIONS", "1  show firewall status\n2  preview configured policy\n3  apply configured policy\n4  disable firewall\n0  back")
		switch prompt(reader, ui, "Select action", "1") {
		case "1":
			operation, err := provider.FirewallStatus(p)
			if err != nil {
				return err
			}
			operation.Print(ui)
			if err := executor.Apply(context.Background(), operation, reader, ui, ui); err != nil {
				ui.warn("Status check failed: " + err.Error())
			}
		case "2":
			operation, err := provider.Firewall(c, p)
			if err != nil {
				return err
			}
			operation.Print(ui)
		case "3":
			operation, err := provider.Firewall(c, p)
			if err != nil {
				return err
			}
			operation.Print(ui)
			fmt.Fprint(ui, "Apply firewall policy? [y/N] ")
			answer, _ := reader.ReadString('\n')
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				ui.muted("Cancelled.")
				break
			}
			if err := executor.Apply(context.Background(), operation, reader, ui, ui); err != nil {
				return err
			}
		case "4":
			operation, err := provider.DisableFirewall(p)
			if err != nil {
				return err
			}
			operation.Print(ui)
			fmt.Fprint(ui, "Type DISABLE to turn off the system firewall: ")
			answer, _ := reader.ReadString('\n')
			if strings.TrimSpace(answer) != "DISABLE" {
				ui.muted("Cancelled.")
				break
			}
			if err := executor.Apply(context.Background(), operation, reader, ui, ui); err != nil {
				return err
			}
		case "0", "q", "Q":
			return nil
		default:
			ui.warn("Unknown selection.")
		}
	}
}

// terminalUI is intentionally small: the TUI remains dependency-light, but has
// one place for its visual language and can gracefully fall back to plain text.
type terminalUI struct {
	io.Writer
	ansi bool
}

func newTerminalUI(w io.Writer) *terminalUI {
	ansi := false
	if f, ok := w.(*os.File); ok {
		if info, err := f.Stat(); err == nil {
			ansi = info.Mode()&os.ModeCharDevice != 0 && os.Getenv("TERM") != "dumb"
		}
	}
	return &terminalUI{Writer: w, ansi: ansi}
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
	fmt.Fprintln(ui, ui.paint("38;5;238", "╭─ ")+ui.paint("38;5;45;1", title)+ui.paint("38;5;238", " "+strings.Repeat("─", 65-len(title))+"╮"))
	for _, line := range strings.Split(body, "\n") {
		fmt.Fprintf(ui, "%s %s %s\n", ui.paint("38;5;238", "│"), line, ui.paint("38;5;238", "│"))
	}
	fmt.Fprintln(ui, ui.paint("38;5;238", "╰"+strings.Repeat("─", 72)+"╯"))
}

func (ui *terminalUI) dashboard(c config.Config, path string) {
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
	ui.panel("STACK", fmt.Sprintf("web       %s\ndatabase  %s (%s)\nsite      %s\nconfig    %s", c.WebServer.Provider, db, role, site, path))
	ui.panel("GUARDRAILS", fmt.Sprintf("https   %s     firewall  %s     backups  %s", ui.status(enabledLabel(c.TLS.Enabled), c.TLS.Enabled), ui.status(enabledLabel(c.Firewall.Enabled), c.Firewall.Enabled), ui.status(enabledLabel(c.Backups.Enabled), c.Backups.Enabled)))
	ui.panel("ACTIONS", "1  preview plan          5  replication status\n2  apply configuration    6  Firewall management\n3  health status           7  long-term operations\n4  run backup\n0  exit")
	fmt.Fprintln(ui, ui.paint("38;5;244", "  ↑/↓ choose  ·  enter confirm  ·  q exit"))
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

func prompt(reader *bufio.Reader, out io.Writer, label, fallback string) string {
	fmt.Fprintf(out, "%s [%s]: ", label, fallback)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return fallback
	}
	return answer
}

func initCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("f", "poorman.json", "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := os.Stat(*path); err == nil {
		return fmt.Errorf("%s already exists", *path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := config.WriteDefault(*path); err != nil {
		return err
	}
	fmt.Fprintf(out, "Created %s\n", *path)
	return nil
}

func buildPlan(path string) (interface{ Print(io.Writer) }, error) {
	c, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	p, err := platform.Detect()
	if err != nil {
		return nil, err
	}
	return provider.WebServer(c, p)
}

func planCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("f", "poorman.json", "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := buildPlan(*path)
	if err != nil {
		return err
	}
	p.Print(out)
	return nil
}

func applyCommand(args []string, in io.Reader, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("f", "poorman.json", "configuration file")
	yes := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	plat, err := platform.Detect()
	if err != nil {
		return err
	}
	p, err := provider.WebServer(c, plat)
	if err != nil {
		return err
	}
	p.Print(out)
	if !*yes {
		fmt.Fprint(out, "Apply this plan? [y/N] ")
		answer, _ := bufio.NewReader(in).ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Fprintln(out, "Cancelled.")
			return nil
		}
	}
	return executor.Apply(context.Background(), p, in, out, errOut)
}
