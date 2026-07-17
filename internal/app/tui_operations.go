package app

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/executor"
	"github.com/chokunplayz/poormanwebctrl/internal/managed"
	"github.com/chokunplayz/poormanwebctrl/internal/ops"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
	"github.com/chokunplayz/poormanwebctrl/internal/provider"
)

func operationsTUI(ctx context.Context, c config.Config, path string, reader *bufio.Reader, ui *terminalUI) error {
	services := configuredServicesFor(c, path)
	for {
		ui.clear()
		ui.brand("Long-term operations", "Inspect the host and keep services healthy")
		ui.panel("READ-ONLY", "These views do not change server state")
		backupAction := "backup inventory"
		if !c.Backups.Enabled {
			backupAction += " (disabled)"
		}
		ui.panel("ACTIONS", "1  host resource stats\n2  recent service logs\n3  "+backupAction+"\n0  back")
		switch selectMenu(reader, ui, "Long-term operations", "1",
			selectorChoice{Value: "1", Label: "host resource stats"},
			selectorChoice{Value: "2", Label: "recent service logs"},
			selectorChoice{Value: "3", Label: backupAction},
			selectorChoice{Value: "0", Label: "back"},
		) {
		case "1":
			ui.clear()
			ui.brand("Host resource stats", "A point-in-time view of capacity and service failures")
			if err := ops.Stats(ctx, ui); err != nil {
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
			serviceOptions := append([]string(nil), services...)
			serviceOptions = append(serviceOptions, "system", "0")
			choice := selectOption(reader, ui, "Service", services[0], serviceOptions...)
			if choice == "0" {
				continue
			}
			service := ""
			if choice == "system" {
				service = "system"
			} else {
				service = choice
			}
			lineCount := prompt(reader, ui, "Lines", "50")
			lines := 50
			if n, err := parsePositive(lineCount); err == nil {
				lines = n
			}
			var logs bytes.Buffer
			if err := ops.Logs(ctx, service, lines, &logs); err != nil {
				ui.warn(err.Error())
			} else {
				ui.logOutput(logs.String())
			}
			pause(reader, ui)
		case "3":
			ui.clear()
			ui.brand("Backup inventory", "Review artifacts produced by the configured backup job")
			if !c.Backups.Enabled {
				ui.warn("Backups are disabled in Stack settings.")
				pause(reader, ui)
				continue
			}
			ui.muted("Destination: " + c.Backups.Destination)
			if err := ops.BackupFiles(ctx, c.Backups.Destination, ui); err != nil {
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
	return configuredServicesFor(c, "")
}

func databaseInstances(c config.Config, path string) []managed.Service {
	inventory := managed.Inventory{}
	if inventory, err := managed.Load(managed.StatePath); err == nil {
		return databaseInstancesFrom(inventory, c, path)
	}
	return databaseInstancesFrom(inventory, c, path)
}

func databaseInstancesFrom(inventory managed.Inventory, c config.Config, path string) []managed.Service {
	instances := make([]managed.Service, 0)
	seen := map[string]bool{}
	for _, service := range inventory.Services {
		if service.Kind != "database" || seen[service.Key] {
			continue
		}
		instances = append(instances, service)
		seen[service.Key] = true
	}
	for _, service := range managed.DesiredServices(c, path) {
		if service.Kind != "database" || seen[service.Key] {
			continue
		}
		instances = append(instances, service)
		seen[service.Key] = true
	}
	sort.Slice(instances, func(i, j int) bool {
		if instances[i].Name == instances[j].Name {
			return instances[i].Key < instances[j].Key
		}
		return instances[i].Name < instances[j].Name
	})
	return instances
}

func managedServices(c config.Config, path string) []managed.Service {
	inventory, err := managed.Load(managed.StatePath)
	if err != nil {
		inventory = managed.Inventory{}
	}
	return managedServicesFrom(inventory, c, path)
}

func managedServicesFrom(inventory managed.Inventory, c config.Config, path string) []managed.Service {
	services := make([]managed.Service, 0, len(inventory.Services)+3)
	seenKeys := map[string]bool{}
	seenShared := map[string]bool{}
	add := func(service managed.Service) {
		if seenKeys[service.Key] {
			return
		}
		// Database records represent distinct managed instances/configurations.
		// Shared host services such as nginx and vsftpd only need one status row.
		if service.Kind != "database" && seenShared[service.Kind+":"+service.Name] {
			return
		}
		services = append(services, service)
		seenKeys[service.Key] = true
		if service.Kind != "database" {
			seenShared[service.Kind+":"+service.Name] = true
		}
	}
	for _, service := range inventory.Services {
		add(service)
	}
	for _, service := range managed.DesiredServices(c, path) {
		add(service)
	}
	sort.SliceStable(services, func(i, j int) bool {
		kindOrder := func(kind string) int {
			switch kind {
			case "database":
				return 0
			case "web":
				return 1
			default:
				return 2
			}
		}
		if kindOrder(services[i].Kind) != kindOrder(services[j].Kind) {
			return kindOrder(services[i].Kind) < kindOrder(services[j].Kind)
		}
		if services[i].Name != services[j].Name {
			return services[i].Name < services[j].Name
		}
		return services[i].Key < services[j].Key
	})
	return services
}

func configuredServicesFor(c config.Config, path string) []string {
	services := []string{webServiceName(c.WebServer.Provider)}
	seen := map[string]bool{}
	for _, service := range databaseInstances(c, path) {
		if seen[service.Name] {
			continue
		}
		seen[service.Name] = true
		services = append(services, service.Name)
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

func firewallTUI(ctx context.Context, path string, in io.Reader, ui *terminalUI) error {
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
		policySuffix := ""
		if !c.Firewall.Enabled {
			policySuffix = " (disabled)"
		}
		ui.panel("ACTIONS", "1  show firewall status\n2  preview configured policy"+policySuffix+"\n3  apply configured policy"+policySuffix+"\n4  disable firewall\n0  back")
		switch selectMenu(reader, ui, "Firewall management", "1",
			selectorChoice{Value: "1", Label: "show firewall status"},
			selectorChoice{Value: "2", Label: "preview configured policy" + policySuffix},
			selectorChoice{Value: "3", Label: "apply configured policy" + policySuffix},
			selectorChoice{Value: "4", Label: "disable firewall"},
			selectorChoice{Value: "0", Label: "back"},
		) {
		case "1":
			operation, err := provider.FirewallStatus(p)
			if err != nil {
				return err
			}
			operation.Print(ui)
			if err := executor.Apply(ctx, operation, reader, ui, ui); err != nil {
				ui.warn("Status check failed: " + err.Error())
			}
		case "2":
			if !c.Firewall.Enabled {
				ui.warn("Firewall policy is disabled in Stack settings.")
				continue
			}
			operation, err := provider.Firewall(c, p)
			if err != nil {
				return err
			}
			operation.Print(ui)
		case "3":
			if !c.Firewall.Enabled {
				ui.warn("Firewall policy is disabled in Stack settings.")
				continue
			}
			operation, err := provider.Firewall(c, p)
			if err != nil {
				return err
			}
			operation.Print(ui)
			if yesNo(selectOption(reader, ui, "Apply firewall policy?", "n", "y", "n")) {
				if err := executor.Apply(ctx, operation, reader, ui, ui); err != nil {
					return err
				}
			} else {
				ui.muted("Cancelled.")
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
			if err := executor.Apply(ctx, operation, reader, ui, ui); err != nil {
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
