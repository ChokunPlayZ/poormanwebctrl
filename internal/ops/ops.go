// Package ops contains read-only inspections used by the operations UI.
package ops

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Stats prints a compact snapshot of the host. Each command is optional so a
// minimal Linux install can still provide the information it has available.
func Stats(ctx context.Context, out io.Writer) error {
	checks := []struct {
		label string
		cmd   string
		args  []string
	}{
		{"uptime", "uptime", nil},
		{"disk", "df", []string{"-h", "/"}},
		{"memory", "free", []string{"-h"}},
		{"failed services", "systemctl", []string{"--failed", "--no-legend", "--plain"}},
	}
	failed := 0
	for _, check := range checks {
		output, err := exec.CommandContext(ctx, check.cmd, check.args...).CombinedOutput()
		if err != nil {
			failed++
			fmt.Fprintf(out, "[unavailable] %-16s %s\n", check.label, strings.TrimSpace(string(output)))
			continue
		}
		fmt.Fprintf(out, "[%s]\n%s\n", check.label, strings.TrimSpace(string(output)))
	}
	if failed == len(checks) {
		return fmt.Errorf("host statistics are unavailable")
	}
	return nil
}

// Logs shows the most recent journal entries for a service. The service name
// is passed as an argument, never through a shell.
func Logs(ctx context.Context, service string, lines int, out io.Writer) error {
	if lines < 1 || lines > 500 {
		return fmt.Errorf("log line count must be between 1 and 500")
	}
	args := []string{"-n", fmt.Sprint(lines), "--no-pager", "--output=short-iso"}
	if service == "system" {
		args = append(args, "-b")
	} else {
		args = append(args, "-u", service)
	}
	command := exec.CommandContext(ctx, "journalctl", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("read %s logs: %s", service, detail)
	}
	fmt.Fprint(out, string(output))
	return nil
}

// BackupFiles lists recent backup artifacts without modifying the destination.
func BackupFiles(ctx context.Context, destination string, out io.Writer) error {
	if destination == "" {
		return fmt.Errorf("no backup destination is configured")
	}
	output, err := exec.CommandContext(ctx, "find", destination, "-maxdepth", "1", "-type", "f", "-printf", "%TY-%Tm-%Td %TH:%TM  %s bytes  %f\\n").CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("list backups: %s", detail)
	}
	if strings.TrimSpace(string(output)) == "" {
		fmt.Fprintln(out, "No backup files found.")
		return nil
	}
	fmt.Fprint(out, string(output))
	return nil
}
