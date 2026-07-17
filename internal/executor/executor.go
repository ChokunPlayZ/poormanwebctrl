package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chokunplayz/poormanwebctrl/internal/managed"
	"github.com/chokunplayz/poormanwebctrl/internal/plan"
)

func Apply(ctx context.Context, p plan.Plan, in io.Reader, out, errOut io.Writer) error {
	for i, step := range p.Steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprintf(out, "[%d/%d] %s\n", i+1, len(p.Steps), step.Description)
		var err error
		switch step.Kind {
		case plan.Directory:
			err = applyDirectory(ctx, step, in, out, errOut)
		case plan.File:
			err = applyFile(ctx, step, in, out, errOut)
		case plan.Line:
			err = applyLine(ctx, step, in, out, errOut)
		case plan.State:
			err = applyManagedState(ctx, step, in, out, errOut)
		case plan.Reconcile:
			err = reconcileManagedState(ctx, step, out, errOut)
		default:
			err = run(ctx, step, in, out, errOut)
		}
		if err != nil {
			return fmt.Errorf("step %d (%s): %w", i+1, step.Description, err)
		}
	}
	return nil
}

func applyManagedState(ctx context.Context, s plan.Step, in io.Reader, out, errOut io.Writer) error {
	inventory, err := managed.Load(s.StatePath)
	if err != nil {
		return err
	}
	var services []managed.Service
	if err := json.Unmarshal([]byte(s.StateContent), &services); err != nil {
		return fmt.Errorf("decode desired managed services: %w", err)
	}
	inventory = managed.Apply(inventory, s.StateKey, services)
	content, err := managed.Marshal(inventory)
	if err != nil {
		return err
	}
	file := plan.ManagedFile("Update managed service inventory", s.StatePath, string(content), "root", 0o644)
	return applyFile(ctx, file, in, out, errOut)
}

func reconcileManagedState(ctx context.Context, s plan.Step, out, errOut io.Writer) error {
	inventory, err := managed.Load(s.StatePath)
	if err != nil {
		return err
	}
	var desired []managed.Service
	if err := json.Unmarshal([]byte(s.StateContent), &desired); err != nil {
		return fmt.Errorf("decode desired managed services: %w", err)
	}
	wanted := map[string]managed.Service{}
	for _, service := range desired {
		wanted[service.Key] = service
	}
	protectedFiles := map[string]bool{}
	for _, service := range inventory.Services {
		if service.ConfigPath == managed.ConfigKey(s.StateKey) {
			continue
		}
		for _, path := range service.Files {
			protectedFiles[filepath.Clean(path)] = true
		}
	}
	for _, previous := range inventory.Services {
		if previous.ConfigPath != managed.ConfigKey(s.StateKey) {
			continue
		}
		current, ok := wanted[previous.Key]
		if err := removeObsoleteManagedFiles(ctx, previous, current, ok, protectedFiles, out, errOut); err != nil {
			return err
		}
		if ok && !managedServiceChanged(previous, current) {
			continue
		}
		if previous.Name == "" {
			continue
		}
		if err := stopManagedService(ctx, s.ServiceManager, previous, out, errOut); err != nil {
			return err
		}
	}
	return nil
}

func removeObsoleteManagedFiles(ctx context.Context, previous, current managed.Service, hasCurrent bool, protected map[string]bool, out, errOut io.Writer) error {
	wanted := map[string]bool{}
	if hasCurrent {
		for _, path := range current.Files {
			wanted[filepath.Clean(path)] = true
		}
	}
	for _, path := range previous.Files {
		path = filepath.Clean(path)
		if wanted[path] || protected[path] {
			continue
		}
		if !safeManagedConfigPath(path) {
			return fmt.Errorf("refuse unsafe managed file path %q", path)
		}
		if err := removeManagedFile(ctx, path, out, errOut); err != nil {
			return err
		}
	}
	return nil
}

func safeManagedConfigPath(path string) bool {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return false
	}
	base := filepath.Base(path)
	if strings.HasPrefix(path, "/etc/nginx/conf.d/") || strings.HasPrefix(path, "/etc/apache2/sites-enabled/") || strings.HasPrefix(path, "/etc/httpd/conf.d/") {
		return strings.HasPrefix(base, "poorman-") && strings.HasSuffix(base, ".conf")
	}
	if path == "/usr/local/lsws/conf/poorman.conf" {
		return true
	}
	if strings.HasPrefix(path, "/usr/local/lsws/conf/vhosts/") {
		rel, err := filepath.Rel("/usr/local/lsws/conf/vhosts", path)
		return err == nil && len(strings.Split(rel, string(filepath.Separator))) == 2 && base == "vhconf.conf"
	}
	// Unit tests exercise the same deletion path without touching host config.
	return strings.HasPrefix(path, filepath.Clean(os.TempDir())+string(filepath.Separator)) && strings.HasPrefix(base, "poorman-")
}

func removeManagedFile(ctx context.Context, path string, out, errOut io.Writer) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil && os.Geteuid() != 0 {
		cmd := exec.CommandContext(ctx, "sudo", "-n", "rm", "-f", "--", path)
		cmd.Stdout, cmd.Stderr = out, errOut
		if sudoErr := cmd.Run(); sudoErr != nil {
			return fmt.Errorf("remove obsolete managed file %s: %w", path, sudoErr)
		}
	} else if err != nil {
		return fmt.Errorf("remove obsolete managed file %s: %w", path, err)
	}
	fmt.Fprintf(out, "  removed obsolete managed file %s\n", path)
	return nil
}

func managedServiceChanged(previous, current managed.Service) bool {
	if previous.Name != current.Name || previous.Provider != current.Provider {
		return true
	}
	if previous.Kind == "database" {
		return previous.Port != current.Port || previous.DataDir != current.DataDir
	}
	return false
}

func stopManagedService(ctx context.Context, manager string, service managed.Service, out, errOut io.Writer) error {
	serviceName := service.Name
	args := []string{"disable", "--now", serviceName}
	if service.Provider == "postgresql" && service.Role == "replica" && service.DataDir != "" {
		manager = "pg_ctl"
		args = []string{"-D", service.DataDir, "stop", "-m", "fast"}
	}
	if manager == "" {
		manager = "systemctl"
	}
	if manager == "rc-service" {
		args = []string{serviceName, "stop"}
	}
	command := manager
	if manager == "pg_ctl" {
		if os.Geteuid() == 0 {
			args = append([]string{"-u", "postgres", "--", manager}, args...)
			command = "runuser"
		} else {
			args = append([]string{"-n", "-u", "postgres", "--", manager}, args...)
			command = "sudo"
		}
	}
	if os.Geteuid() != 0 {
		if command != "sudo" {
			args = append([]string{"-n", manager}, args...)
			command = "sudo"
		}
	}
	cmd := exec.CommandContext(ctx, command, args...)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		detail := strings.ToLower(output.String())
		if strings.Contains(detail, "not found") || strings.Contains(detail, "does not exist") || strings.Contains(detail, "loaded: not-found") || strings.Contains(detail, "no server running") || strings.Contains(detail, "not running") {
			fmt.Fprintf(out, "  old managed service %s is already absent; skipped\n", serviceName)
			return nil
		}
		return fmt.Errorf("stop old managed service %s: %w: %s", serviceName, err, strings.TrimSpace(output.String()))
	}
	fmt.Fprintf(out, "  stopped old managed service %s\n", serviceName)
	return nil
}

func applyLine(ctx context.Context, s plan.Step, in io.Reader, out, errOut io.Writer) error {
	content, err := os.ReadFile(s.Path)
	if err != nil && os.Geteuid() != 0 {
		cmd := exec.CommandContext(ctx, "sudo", "cat", s.Path)
		content, err = cmd.Output()
	}
	if err != nil {
		return err
	}
	line := strings.TrimSpace(s.Content)
	for _, existing := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(existing) == line {
			fmt.Fprintln(out, "  already satisfied; skipped")
			return nil
		}
	}
	updated := strings.TrimRight(string(content), "\n") + "\n" + line + "\n"
	s.Kind, s.Content = plan.File, updated
	if s.Mode == 0 {
		s.Mode = 0o600
	}
	if s.Owner == "" {
		s.Owner = "root"
	}
	if s.Group == "" {
		s.Group = s.Owner
	}
	return applyFile(ctx, s, in, out, errOut)
}

func applyDirectory(ctx context.Context, s plan.Step, in io.Reader, out, errOut io.Writer) error {
	args := []string{"-d", "-m", fmt.Sprintf("%04o", fallbackMode(s.Mode, 0o755))}
	args = appendOwnershipArgs(args, s)
	args = append(args, s.Path)
	s.Command, s.Args = "install", args
	return run(ctx, s, in, out, errOut)
}

func appendOwnershipArgs(args []string, s plan.Step) []string {
	if s.Owner != "" {
		args = append(args, "-o", s.Owner)
	}
	if s.Group != "" {
		args = append(args, "-g", s.Group)
	}
	return args
}

func applyFile(ctx context.Context, s plan.Step, in io.Reader, out, errOut io.Writer) error {
	tmp, err := os.CreateTemp("", "poorman-managed-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := io.WriteString(tmp, s.Content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	args := []string{"-D", "-m", fmt.Sprintf("%04o", fallbackMode(s.Mode, 0o644))}
	args = appendOwnershipArgs(args, s)
	args = append(args, name, filepath.Clean(s.Path))
	s.Command, s.Args = "install", args
	return run(ctx, s, in, out, errOut)
}

func run(ctx context.Context, s plan.Step, in io.Reader, out, errOut io.Writer) error {
	if s.SkipIfNotEmpty != "" {
		command := "find"
		args := []string{s.SkipIfNotEmpty, "!", "-path", s.SkipIfNotEmpty, "-prune", "-print", "-quit"}
		if s.NeedsRoot && os.Geteuid() != 0 {
			args = append([]string{"-n", command}, args...)
			command = "sudo"
		}
		check := exec.CommandContext(ctx, command, args...)
		var found bytes.Buffer
		check.Stdout, check.Stderr = &found, errOut
		if err := check.Run(); err != nil {
			return fmt.Errorf("inspect directory %s: %w", s.SkipIfNotEmpty, err)
		}
		if strings.TrimSpace(found.String()) != "" {
			fmt.Fprintln(out, "  document root is not empty; skipped")
			return nil
		}
	}
	if s.UnlessCommand != "" {
		checkCommand, checkArgs := s.UnlessCommand, append([]string(nil), s.UnlessArgs...)
		if s.RunAs != "" {
			checkArgs = append([]string{"-u", s.RunAs, "--", checkCommand}, checkArgs...)
			if os.Geteuid() == 0 {
				checkCommand = "runuser"
			} else {
				checkCommand = "sudo"
				checkArgs = append([]string{"-n"}, checkArgs...)
			}
		} else if s.NeedsRoot && os.Geteuid() != 0 {
			checkArgs = append([]string{"-n", checkCommand}, checkArgs...)
			checkCommand = "sudo"
		}
		check := exec.CommandContext(ctx, checkCommand, checkArgs...)
		if check.Run() == nil {
			fmt.Fprintln(out, "  already satisfied; skipped")
			return nil
		}
	}
	command, args := s.Command, s.Args
	if s.RunAs != "" {
		args = append([]string{"-u", s.RunAs, "--", command}, args...)
		if os.Geteuid() == 0 {
			command = "runuser"
		} else {
			command = "sudo"
		}
	} else if s.NeedsRoot && os.Geteuid() != 0 {
		prefix := []string{command}
		if s.Input != "" {
			// Never let sudo prompt while stdin may contain command data (for
			// example SQL). An authentication prompt would consume that data or
			// appear to hang forever in the TUI.
			prefix = append([]string{"-n"}, prefix...)
		}
		args = append(prefix, args...)
		command = "sudo"
	}
	if s.Command == "mariadb" && s.Input != "" {
		fmt.Fprintln(out, "  applying MariaDB SQL (up to 60 seconds)...")
	}
	commandContext := ctx
	var cancel context.CancelFunc
	if s.TimeoutSeconds > 0 {
		commandContext, cancel = context.WithTimeout(ctx, time.Duration(s.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(commandContext, command, args...)
	cmd.Stdout, cmd.Stderr = out, errOut
	if s.Input != "" {
		resolved, err := resolveEnv(s.Input, s.SQLSecrets)
		if err != nil {
			return err
		}
		cmd.Stdin = bytes.NewBufferString(resolved)
	} else {
		// Provisioning commands must not inherit the TUI's input stream. A key
		// pressed while a step is running would otherwise be consumed by the
		// command and make the workflow advance unexpectedly.
		// Steps that need input declare it explicitly through Input.
		cmd.Stdin = nil
	}
	return cmd.Run()
}

func resolveEnv(input string, sqlEscape bool) (string, error) {
	var missing string
	resolved := os.Expand(input, func(key string) string {
		value, ok := os.LookupEnv(key)
		if !ok {
			missing = key
		}
		if sqlEscape {
			value = strings.ReplaceAll(value, "'", "''")
		}
		return value
	})
	if missing != "" {
		return "", fmt.Errorf("required environment variable %s is not set", missing)
	}
	return resolved, nil
}

func fallbackMode(value, fallback uint32) uint32 {
	if value == 0 {
		return fallback
	}
	return value
}

func ParseMode(value string) (uint32, error) {
	n, err := strconv.ParseUint(strings.TrimPrefix(value, "0"), 8, 32)
	return uint32(n), err
}
