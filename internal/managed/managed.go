package managed

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
)

const (
	StatePath = "/var/lib/poorman/managed.json"
	StateDir  = "/var/lib/poorman"
)

type Service struct {
	Key        string   `json:"key"`
	ConfigPath string   `json:"config_path"`
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	Provider   string   `json:"provider,omitempty"`
	Role       string   `json:"role,omitempty"`
	Port       int      `json:"port,omitempty"`
	DataDir    string   `json:"data_dir,omitempty"`
	Database   string   `json:"database,omitempty"`
	Files      []string `json:"files,omitempty"`
}

type Inventory struct {
	Version  int       `json:"version"`
	Services []Service `json:"services,omitempty"`
}

func ConfigKey(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func ServiceKey(configPath, kind string) string {
	return ConfigKey(configPath) + ":" + kind
}

func Load(path string) (Inventory, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Inventory{Version: 1}, nil
	}
	if err != nil {
		return Inventory{}, fmt.Errorf("read managed inventory: %w", err)
	}
	var inventory Inventory
	if err := json.Unmarshal(b, &inventory); err != nil {
		return Inventory{}, fmt.Errorf("parse managed inventory: %w", err)
	}
	if inventory.Version == 0 {
		inventory.Version = 1
	}
	return inventory, nil
}

func Marshal(inventory Inventory) ([]byte, error) {
	inventory.Version = 1
	for i := range inventory.Services {
		sort.Strings(inventory.Services[i].Files)
	}
	sort.Slice(inventory.Services, func(i, j int) bool { return inventory.Services[i].Key < inventory.Services[j].Key })
	b, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode managed inventory: %w", err)
	}
	return append(b, '\n'), nil
}

func Apply(inventory Inventory, configPath string, services []Service) Inventory {
	configPath = ConfigKey(configPath)
	updated := make([]Service, 0, len(inventory.Services)+len(services))
	for _, service := range inventory.Services {
		if service.ConfigPath == configPath {
			continue
		}
		updated = append(updated, service)
	}
	updated = append(updated, services...)
	inventory.Services = updated
	return inventory
}

func DesiredServices(c config.Config, configPath string) []Service {
	configPath = ConfigKey(configPath)
	services := []Service{{Key: ServiceKey(configPath, "web"), ConfigPath: configPath, Kind: "web", Name: webService(c.WebServer.Provider), Provider: c.WebServer.Provider}}
	if c.Database != nil {
		d := c.Database
		name := d.Provider
		if d.Provider == "mariadb" && d.Port > 0 && d.DataDir != "" && isLoopback(d.Replication.PrimaryHost) {
			name = fmt.Sprintf("poorman-mariadb-replica-%d", d.Port)
		}
		services = append(services, Service{Key: ServiceKey(configPath, "database"), ConfigPath: configPath, Kind: "database", Name: name, Provider: d.Provider, Role: d.Role, Port: d.Port, DataDir: d.DataDir, Database: d.Name})
	}
	if c.Access.FTP.Enabled {
		services = append(services, Service{Key: ServiceKey(configPath, "ftp"), ConfigPath: configPath, Kind: "ftp", Name: "vsftpd"})
	}
	return services
}

func webService(provider string) string {
	switch provider {
	case "apache":
		return "apache2"
	case "openlitespeed":
		return "lsws"
	default:
		return "nginx"
	}
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// InstanceLabel includes the port and data directory so two instances with
// the same system service name remain visible as separate TUI entries.
func InstanceLabel(service Service) string {
	label := service.Name
	if service.Kind != "database" {
		return label
	}
	if service.Port > 0 {
		label += fmt.Sprintf(" (port %d)", service.Port)
	}
	if service.DataDir != "" && strings.Contains(service.Name, "poorman-") {
		label += " " + service.DataDir
	}
	return label
}
