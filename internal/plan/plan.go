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
	State     Kind = "state"
	Reconcile Kind = "reconcile"
)

type Step struct {
	Description    string
	Kind           Kind
	Command        string
	Args           []string
	Input          string
	Path           string
	Content        string
	Mode           uint32
	Owner          string
	Group          string
	RunAs          string
	NeedsRoot      bool
	Sensitive      bool
	SQLSecrets     bool
	TimeoutSeconds int
	UnlessCommand  string
	UnlessArgs     []string
	StatePath      string
	StateKey       string
	StateContent   string
	ServiceManager string
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
			detail = fmt.Sprintf("%sinstall -d -m %04o%s %s", prefix, mode(step.Mode, 0o755), ownershipDetail(step), step.Path)
		case File:
			detail = fmt.Sprintf("%smanage-file -m %04o%s %s", prefix, mode(step.Mode, 0o644), ownershipDetail(step), step.Path)
		case Line:
			detail = fmt.Sprintf("%sensure-line%s %s", prefix, ownershipDetail(step), step.Path)
		case State:
			detail = fmt.Sprintf("%supdate managed service inventory %s", prefix, step.StatePath)
		case Reconcile:
			detail = fmt.Sprintf("%sreconcile managed services %s", prefix, step.StatePath)
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

func ownershipDetail(step Step) string {
	detail := ""
	if step.Owner != "" {
		detail += " -o " + step.Owner
	}
	if step.Group != "" {
		detail += " -g " + step.Group
	}
	return detail
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
	return DirOwnedBy(description, path, owner, owner, mode)
}

func DirOwnedBy(description, path, owner, group string, mode uint32) Step {
	return Step{Description: description, Kind: Directory, Path: path, Owner: owner, Group: group, Mode: mode, NeedsRoot: true}
}

func ManagedFile(description, path, content, owner string, mode uint32) Step {
	return ManagedFileOwnedBy(description, path, content, owner, owner, mode)
}

func ManagedFileOwnedBy(description, path, content, owner, group string, mode uint32) Step {
	return Step{Description: description, Kind: File, Path: path, Content: content, Owner: owner, Group: group, Mode: mode, NeedsRoot: true}
}

func EnsureLine(description, path, line string) Step {
	return EnsureLineOwnedBy(description, path, line, "root", "root", 0o600)
}

func EnsureLineOwnedBy(description, path, line, owner, group string, mode uint32) Step {
	return Step{Description: description, Kind: Line, Path: path, Content: line, Owner: owner, Group: group, Mode: mode, NeedsRoot: true}
}

// ManagedState records the desired services for one configuration after the
// rest of the plan has completed. The executor merges this value with other
// configuration entries instead of replacing the whole inventory.
func ManagedState(description, path, key, content string) Step {
	return Step{Description: description, Kind: State, StatePath: path, StateKey: key, StateContent: content, NeedsRoot: true}
}

// ReconcileManagedState lets the executor stop old services belonging to this
// configuration before the new instance is created. This makes a changed
// replica port, data directory, or provider converge cleanly.
func ReconcileManagedState(description, path, key, content string) Step {
	return Step{Description: description, Kind: Reconcile, StatePath: path, StateKey: key, StateContent: content, NeedsRoot: true}
}

func ReconcileManagedStateWithManager(description, path, key, content, manager string) Step {
	step := ReconcileManagedState(description, path, key, content)
	step.ServiceManager = manager
	return step
}
