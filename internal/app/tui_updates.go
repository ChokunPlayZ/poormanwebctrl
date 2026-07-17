package app

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/executor"
	"github.com/chokunplayz/poormanwebctrl/internal/ops"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

func updateManagerTUI(ctx context.Context, reader *bufio.Reader, ui *terminalUI) error {
	p, err := platform.Detect()
	if err != nil {
		return err
	}
	ui.clear()
	ui.brand("Update manager", "Choose exactly which operating-system packages to update")
	ui.muted("Checking the current package metadata...")
	updates, err := ops.AvailableUpdates(ctx, p)
	if err != nil {
		return err
	}
	if len(updates) == 0 {
		ui.success("All installed packages are up to date")
		pause(reader, ui)
		return nil
	}

	selected := map[string]bool{}
	for {
		ui.clear()
		ui.brand("Update manager", "Choose exactly which operating-system packages to update")
		lines := make([]string, 0, len(updates))
		choices := make([]selectorChoice, 0, len(updates)+3)
		for _, update := range updates {
			marker := "[ ]"
			if selected[update.Name] {
				marker = "[x]"
			}
			version := update.Available
			if update.Current != "" {
				version = update.Current + " → " + update.Available
			}
			label := fmt.Sprintf("%s %-28s %s", marker, update.Name, version)
			lines = append(lines, label)
			choices = append(choices, selectorChoice{Value: "package:" + update.Name, Label: label})
		}
		ui.panel("AVAILABLE UPDATES", strings.Join(lines, "\n"))
		choices = append(choices,
			selectorChoice{Value: "all", Label: "select all / clear all"},
			selectorChoice{Value: "continue", Label: fmt.Sprintf("review %d selected package(s)", selectedCount(selected))},
			selectorChoice{Value: "0", Label: "back"},
		)
		choice := selectMenu(reader, ui, "Toggle a package or continue", "0", choices...)
		switch choice {
		case "0", "q", "Q":
			return nil
		case "all":
			selectAll := selectedCount(selected) != len(updates)
			for _, update := range updates {
				selected[update.Name] = selectAll
			}
		case "continue":
			packages := selectedPackages(updates, selected)
			if len(packages) == 0 {
				ui.warn("Select at least one package before continuing.")
				pause(reader, ui)
				continue
			}
			operation, err := ops.UpdatePlan(p, packages)
			if err != nil {
				return err
			}
			ui.clear()
			ui.brand("Review updates", "Only these selected packages will be changed")
			operation.Print(ui)
			if !yesNo(selectOption(reader, ui, "Apply these updates now?", "n", "y", "n")) {
				ui.warn("Update cancelled.")
				pause(reader, ui)
				return nil
			}
			if err := executor.Apply(ctx, operation, reader, ui, ui); err != nil {
				return err
			}
			ui.success(fmt.Sprintf("Updated %d package(s)", len(packages)))
			pause(reader, ui)
			return nil
		default:
			if name, ok := strings.CutPrefix(choice, "package:"); ok {
				selected[name] = !selected[name]
			}
		}
	}
}

func selectedCount(selected map[string]bool) int {
	count := 0
	for _, enabled := range selected {
		if enabled {
			count++
		}
	}
	return count
}

func selectedPackages(updates []ops.PackageUpdate, selected map[string]bool) []string {
	packages := make([]string, 0, len(selected))
	for _, update := range updates {
		if selected[update.Name] {
			packages = append(packages, update.Name)
		}
	}
	return packages
}
