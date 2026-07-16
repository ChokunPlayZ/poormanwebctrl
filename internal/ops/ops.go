// Package ops contains read-only inspections used by the operations UI.
package ops

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
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
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return fmt.Errorf("list backups: %w", err)
	}
	type artifact struct {
		name  string
		mode  os.FileMode
		size  int64
		mtime time.Time
	}
	artifacts := make([]artifact, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect backup %s: %w", entry.Name(), err)
		}
		artifacts = append(artifacts, artifact{name: entry.Name(), mode: info.Mode(), size: info.Size(), mtime: info.ModTime()})
	}
	if len(artifacts) == 0 {
		fmt.Fprintln(out, "No backup runs found.")
		return nil
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].mtime.Equal(artifacts[j].mtime) {
			return artifacts[i].name < artifacts[j].name
		}
		return artifacts[i].mtime.After(artifacts[j].mtime)
	})
	for _, item := range artifacts {
		kind := fmt.Sprintf("%d bytes", item.size)
		if item.mode.IsDir() {
			kind = "backup run"
		}
		fmt.Fprintf(out, "%s  %-12s  %s\n", item.mtime.Format("2006-01-02 15:04"), kind, item.name)
	}
	return nil
}
