package app

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// CommandHandler is the common boundary between CLI routing and a command's
// implementation. Commands receive only their own arguments and shared IO.
type CommandHandler func(context.Context, []string, io.Reader, io.Writer, io.Writer) error

// Command describes one top-level CLI feature. Aliases and usage are kept next
// to the handler so adding or removing a command does not require editing a
// switch statement and a separate help template.
type Command struct {
	Name        string
	Aliases     []string
	Usage       string
	Description string
	Run         CommandHandler
}

// BuiltInCommands returns a fresh command registry for the standard CLI.
func BuiltInCommands() []Command {
	commands := []Command{
		{
			Name:        "init",
			Usage:       "poorman init [-f poorman.json]",
			Description: "write a starter configuration",
			Run: func(_ context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
				return initCommand(args, out)
			},
		},
		{
			Name:        "plan",
			Usage:       "poorman plan [-f poorman.json]",
			Description: "preview every action",
			Run: func(_ context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
				return planCommand(args, out)
			},
		},
		{
			Name:        "apply",
			Usage:       "poorman apply [-f poorman.json] [--yes]",
			Description: "apply a configuration locally",
			Run:         applyCommand,
		},
		{
			Name:        "tui",
			Usage:       "poorman tui [-f poorman.json]",
			Description: "open guided setup or operations",
			Run: func(ctx context.Context, args []string, in io.Reader, out, _ io.Writer) error {
				return tuiCommand(ctx, args, in, out)
			},
		},
		{
			Name:        "status",
			Usage:       "poorman status [-f poorman.json]",
			Description: "run local health checks",
			Run: func(ctx context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
				return statusCommand(ctx, args, out)
			},
		},
		{
			Name: "replica",
			Usage: "poorman replica setup [-f poorman.json]\n" +
				"poorman replica setup -f remote-replica.json --from primary.json\n" +
				"poorman replica status [-f poorman.json]\n" +
				"poorman replica promote [-f poorman.json] [--yes]",
			Description: "configure and operate database replication",
			Run:         replicaCommand,
		},
		{
			Name:        "backup",
			Usage:       "poorman backup [-f poorman.json] [--yes]",
			Description: "run the configured backup job",
			Run:         backupCommand,
		},
		{
			Name:        "version",
			Aliases:     []string{"--version", "-v"},
			Usage:       "poorman version",
			Description: "print the current version",
			Run: func(_ context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
				if len(args) > 0 {
					return fmt.Errorf("unexpected argument %q", args[0])
				}
				fmt.Fprintln(out, version)
				return nil
			},
		},
	}
	commands = append(commands, Command{
		Name:        "help",
		Aliases:     []string{"--help", "-h"},
		Usage:       "poorman help",
		Description: "show command help",
		Run: func(_ context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
			if len(args) > 0 {
				return fmt.Errorf("unexpected argument %q", args[0])
			}
			writeUsage(out, commands)
			return nil
		},
	})
	return commands
}

// RunWithCommands runs an explicit command registry. This is the extension
// point for alternate builds and tests that add or remove top-level features.
func RunWithCommands(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer, commands []Command) error {
	lookup := make(map[string]Command, len(commands))
	for index, command := range commands {
		if command.Name == "" {
			return fmt.Errorf("command %d has an empty name", index+1)
		}
		if command.Run == nil {
			return fmt.Errorf("command %q has no handler", command.Name)
		}
		names := append([]string{command.Name}, command.Aliases...)
		for _, name := range names {
			if _, exists := lookup[name]; exists {
				return fmt.Errorf("duplicate command or alias %q", name)
			}
			lookup[name] = command
		}
	}
	if len(args) == 0 {
		writeUsage(out, commands)
		return nil
	}
	command, ok := lookup[args[0]]
	if !ok {
		return fmt.Errorf("unknown command %q", args[0])
	}
	if command.Name == "help" {
		if len(args) > 1 {
			return fmt.Errorf("unexpected argument %q", args[1])
		}
		writeUsage(out, commands)
		return nil
	}
	return command.Run(ctx, args[1:], in, out, errOut)
}

func writeUsage(w io.Writer, commands []Command) {
	fmt.Fprintln(w, "poorman makes a server match a small, auditable configuration.")
	fmt.Fprintln(w, "\nUsage:")
	for _, command := range commands {
		if command.Usage == "" || command.Name == "help" {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(command.Usage), "\n") {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	fmt.Fprintln(w, `
Start with "poorman init", edit the file, then preview with "poorman plan".`)
}
