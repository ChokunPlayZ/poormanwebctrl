package ops

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/plan"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

// PackageUpdate is one installed package for which the distribution publishes
// a newer version. Current may be empty when the package manager does not
// include it in its normal update listing.
type PackageUpdate struct {
	Name      string
	Current   string
	Available string
}

// AvailableUpdates asks the host package manager for its current update list.
// It does not refresh metadata or change server state.
func AvailableUpdates(ctx context.Context, p platform.Platform) ([]PackageUpdate, error) {
	var command string
	var args []string
	switch p.Family {
	case "debian":
		command, args = "apt", []string{"list", "--upgradable"}
	case "rhel":
		command, args = "dnf", []string{"-q", "list", "--upgrades"}
	case "alpine":
		command, args = "apk", []string{"list", "--upgradable"}
	default:
		return nil, fmt.Errorf("updates are not supported for platform family %q", p.Family)
	}
	output, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("check for package updates: %s", detail)
	}
	return parseAvailableUpdates(p.Family, string(output)), nil
}

func parseAvailableUpdates(family, output string) []PackageUpdate {
	updates := make([]PackageUpdate, 0)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "WARNING:") || strings.HasPrefix(line, "Listing...") || strings.EqualFold(line, "Available Upgrades") || strings.HasPrefix(line, "Last metadata expiration check:") {
			continue
		}
		var update PackageUpdate
		switch family {
		case "debian":
			fields := strings.Fields(line)
			if len(fields) < 2 || !strings.Contains(fields[0], "/") {
				continue
			}
			update.Name, _, _ = strings.Cut(fields[0], "/")
			update.Available = fields[1]
			const marker = "[upgradable from: "
			if start := strings.Index(line, marker); start >= 0 {
				current := line[start+len(marker):]
				update.Current = strings.TrimSuffix(current, "]")
			}
		case "rhel":
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			update = PackageUpdate{Name: fields[0], Available: fields[1]}
		case "alpine":
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			name, available := splitAlpinePackage(fields[0])
			if name == "" {
				continue
			}
			update = PackageUpdate{Name: name, Available: available}
			const marker = "[upgradable from: "
			if start := strings.Index(line, marker); start >= 0 {
				current := line[start+len(marker):]
				current = strings.TrimSuffix(current, "]")
				_, update.Current = splitAlpinePackage(current)
			}
		}
		if validPackageName(update.Name) && update.Available != "" {
			updates = append(updates, update)
		}
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].Name < updates[j].Name })
	return updates
}

func splitAlpinePackage(value string) (string, string) {
	// Alpine versions begin at the last dash followed by a digit. Package names
	// themselves may contain dashes.
	for i := len(value) - 2; i > 0; i-- {
		if value[i] == '-' && value[i+1] >= '0' && value[i+1] <= '9' {
			return value[:i], value[i+1:]
		}
	}
	return "", ""
}

func validPackageName(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune(".+:_@-", r) {
			continue
		}
		return false
	}
	return true
}

// UpdatePlan creates one explicit package-manager step for only the selected
// packages. The executor supplies the existing root/sudo handling.
func UpdatePlan(p platform.Platform, packages []string) (plan.Plan, error) {
	unique := make([]string, 0, len(packages))
	seen := map[string]bool{}
	for _, name := range packages {
		if !validPackageName(name) {
			return plan.Plan{}, fmt.Errorf("invalid package name %q", name)
		}
		if !seen[name] {
			unique = append(unique, name)
			seen[name] = true
		}
	}
	if len(unique) == 0 {
		return plan.Plan{}, fmt.Errorf("select at least one package to update")
	}
	sort.Strings(unique)
	var command string
	var args []string
	switch p.Family {
	case "debian":
		command = "apt-get"
		args = append([]string{"-o", "Dpkg::Use-Pty=0", "install", "--only-upgrade", "-y", "--"}, unique...)
	case "rhel":
		command = "dnf"
		args = append([]string{"upgrade", "-y", "--"}, unique...)
	case "alpine":
		command = "apk"
		args = append([]string{"upgrade", "--"}, unique...)
	default:
		return plan.Plan{}, fmt.Errorf("updates are not supported for platform family %q", p.Family)
	}
	return plan.Plan{Platform: p.Distro, Steps: []plan.Step{plan.Cmd("Update selected packages", command, true, args...)}}, nil
}
