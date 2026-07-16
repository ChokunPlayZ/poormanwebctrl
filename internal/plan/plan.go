package plan

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

type Kind string

const (
	Command   Kind = "command"
	Directory Kind = "directory"
	File      Kind = "file"
	Line      Kind = "line"
)

type Step struct {
	Description   string
	Kind          Kind
	Command       string
	Args          []string
	Input         string
	Path          string
	Content       string
	Mode          uint32
	Owner         string
	RunAs         string
	NeedsRoot     bool
	Sensitive     bool
	SQLSecrets    bool
	UnlessCommand string
	UnlessArgs    []string
}

type Plan struct {
	Platform string
	Steps    []Step
	Warnings []string
}

func (p *Plan) Add(steps ...Step) { p.Steps = append(p.Steps, steps...) }

func (p *Plan) Warn(message string) {
	for _, existing := range p.Warnings {
		if existing == message {
			return
		}
	}
	p.Warnings = append(p.Warnings, message)
}

func (p Plan) Print(w io.Writer) {
	fmt.Fprintf(w, "Plan for %s: %d step(s)\n", p.Platform, len(p.Steps))
	for i, step := range p.Steps {
		prefix := ""
		if step.RunAs != "" {
			prefix = "sudo -u " + step.RunAs + " -- "
		} else if step.NeedsRoot {
			prefix = "sudo "
		}
		var detail string
		switch step.Kind {
		case Directory:
			detail = fmt.Sprintf("%sinstall -d -m %04o %s", prefix, mode(step.Mode, 0o755), step.Path)
		case File:
			detail = fmt.Sprintf("%smanage-file -m %04o %s", prefix, mode(step.Mode, 0o644), step.Path)
		case Line:
			detail = fmt.Sprintf("%sensure-line %s", prefix, step.Path)
		default:
			detail = prefix + renderCommand(step.Command, step.Args)
			if step.Sensitive {
				detail += " <sensitive input from environment>"
			}
		}
		fmt.Fprintf(w, "  %d. %s\n     %s\n", i+1, step.Description, detail)
	}
	if len(p.Warnings) > 0 {
		fmt.Fprintln(w, "Warnings:")
		warnings := append([]string(nil), p.Warnings...)
		sort.Strings(warnings)
		for _, warning := range warnings {
			fmt.Fprintln(w, "  -", warning)
		}
	}
}

func mode(value, fallback uint32) uint32 {
	if value == 0 {
		return fallback
	}
	return value
}

func renderCommand(command string, args []string) string {
	parts := append([]string{command}, args...)
	for i, part := range parts {
		if strings.ContainsAny(part, " \t\n\"'") {
			parts[i] = fmt.Sprintf("%q", part)
		}
	}
	return strings.Join(parts, " ")
}

func Cmd(description, command string, root bool, args ...string) Step {
	return Step{Description: description, Kind: Command, Command: command, Args: args, NeedsRoot: root}
}

func AsUser(description, user, command string, args ...string) Step {
	return Step{Description: description, Kind: Command, Command: command, Args: args, RunAs: user}
}

func Dir(description, path, owner string, mode uint32) Step {
	return Step{Description: description, Kind: Directory, Path: path, Owner: owner, Mode: mode, NeedsRoot: true}
}

func ManagedFile(description, path, content, owner string, mode uint32) Step {
	return Step{Description: description, Kind: File, Path: path, Content: content, Owner: owner, Mode: mode, NeedsRoot: true}
}

func EnsureLine(description, path, line string) Step {
	return Step{Description: description, Kind: Line, Path: path, Content: line, NeedsRoot: true}
}
