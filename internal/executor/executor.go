package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/plan"
)

func Apply(ctx context.Context, p plan.Plan, in io.Reader, out, errOut io.Writer) error {
	for i, step := range p.Steps {
		fmt.Fprintf(out, "[%d/%d] %s\n", i+1, len(p.Steps), step.Description)
		var err error
		switch step.Kind {
		case plan.Directory:
			err = applyDirectory(ctx, step, in, out, errOut)
		case plan.File:
			err = applyFile(ctx, step, in, out, errOut)
		case plan.Line:
			err = applyLine(ctx, step, in, out, errOut)
		default:
			err = run(ctx, step, in, out, errOut)
		}
		if err != nil {
			return fmt.Errorf("step %d (%s): %w", i+1, step.Description, err)
		}
	}
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
	s.Kind, s.Content, s.Mode, s.Owner = plan.File, updated, 0o600, "root"
	return applyFile(ctx, s, in, out, errOut)
}

func applyDirectory(ctx context.Context, s plan.Step, in io.Reader, out, errOut io.Writer) error {
	args := []string{"-d", "-m", fmt.Sprintf("%04o", fallbackMode(s.Mode, 0o755))}
	if s.Owner != "" {
		args = append(args, "-o", s.Owner, "-g", s.Owner)
	}
	args = append(args, s.Path)
	s.Command, s.Args = "install", args
	return run(ctx, s, in, out, errOut)
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
	if s.Owner != "" {
		args = append(args, "-o", s.Owner, "-g", s.Owner)
	}
	args = append(args, name, filepath.Clean(s.Path))
	s.Command, s.Args = "install", args
	return run(ctx, s, in, out, errOut)
}

func run(ctx context.Context, s plan.Step, in io.Reader, out, errOut io.Writer) error {
	if s.UnlessCommand != "" {
		check := exec.CommandContext(ctx, s.UnlessCommand, s.UnlessArgs...)
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
		args = append([]string{command}, args...)
		command = "sudo"
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdout, cmd.Stderr = out, errOut
	if s.Input != "" {
		resolved, err := resolveEnv(s.Input, s.SQLSecrets)
		if err != nil {
			return err
		}
		cmd.Stdin = bytes.NewBufferString(resolved)
	} else {
		cmd.Stdin = in
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
