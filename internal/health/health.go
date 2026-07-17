package health

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/managed"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

type Check struct {
	Name, Command string
	Args          []string
}

type ServiceState string

const (
	ServiceUp       ServiceState = "up"
	ServiceDown     ServiceState = "down"
	ServiceChanging ServiceState = "changing"
	ServiceUnknown  ServiceState = "unknown"
)

type ServiceStatus struct {
	Service managed.Service
	State   ServiceState
}

// ServiceStatuses returns a fast point-in-time snapshot for dashboard use.
// A timeout is applied to every service so an unhealthy service manager cannot
// make the operations dashboard unusable.
func ServiceStatuses(ctx context.Context, services []managed.Service, p platform.Platform) []ServiceStatus {
	statuses := make([]ServiceStatus, len(services))
	var checks sync.WaitGroup
	for i, service := range services {
		statuses[i] = ServiceStatus{Service: service, State: ServiceUnknown}
		if p.Family == "" {
			continue
		}
		checks.Add(1)
		go func(index int) {
			defer checks.Done()
			serviceCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			statuses[index].State = serviceStatus(serviceCtx, services[index].Name, p)
		}(i)
	}
	checks.Wait()
	return statuses
}

func serviceStatus(ctx context.Context, name string, p platform.Platform) ServiceState {
	command := "systemctl"
	args := []string{"is-active", name}
	if p.Family == "alpine" {
		command = "rc-service"
		args = []string{name, "status"}
	}
	output, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	return serviceState(string(output), err == nil, p.Family)
}

func serviceState(output string, succeeded bool, family string) ServiceState {
	value := strings.ToLower(strings.TrimSpace(output))
	if family == "alpine" {
		switch {
		case succeeded || strings.Contains(value, "started"):
			return ServiceUp
		case strings.Contains(value, "starting") || strings.Contains(value, "stopping"):
			return ServiceChanging
		case strings.Contains(value, "stopped") || strings.Contains(value, "crashed"):
			return ServiceDown
		default:
			return ServiceUnknown
		}
	}
	switch value {
	case "active":
		return ServiceUp
	case "activating", "reloading", "deactivating":
		return ServiceChanging
	case "inactive", "failed", "dead":
		return ServiceDown
	default:
		return ServiceUnknown
	}
}

func Checks(c config.Config, p platform.Platform) []Check {
	services := []string{webService(c.WebServer.Provider, p)}
	if c.Database != nil {
		databaseService := c.Database.Provider
		if isLocalMariaDBReplica(*c.Database) {
			databaseService = fmt.Sprintf("poorman-mariadb-replica-%d", c.Database.Port)
		}
		services = append(services, databaseService)
	}
	if c.Access.FTP.Enabled {
		services = append(services, "vsftpd")
	}
	if c.Firewall.Enabled && p.Family == "rhel" {
		services = append(services, "firewalld")
	}
	checks := make([]Check, 0, len(services)+len(c.Sites)+1)
	for _, service := range services {
		if p.Family == "alpine" {
			checks = append(checks, Check{Name: service + " service", Command: "rc-service", Args: []string{service, "status"}})
		} else {
			checks = append(checks, Check{Name: service + " service", Command: "systemctl", Args: []string{"is-active", "--quiet", service}})
		}
	}
	command := "nginx"
	args := []string{"-t"}
	if c.WebServer.Provider == "apache" {
		command = "apachectl"
	} else if c.WebServer.Provider == "openlitespeed" {
		command = "/usr/local/lsws/bin/openlitespeed"
	}
	checks = append(checks, Check{Name: "web server configuration", Command: command, Args: args})
	for _, site := range c.Sites {
		checks = append(checks, Check{Name: site.Domain + " local HTTP", Command: "curl", Args: []string{"-fsS", "-o", "/dev/null", "-H", "Host: " + site.Domain, "http://127.0.0.1/"}})
	}
	return checks
}

func isLocalMariaDBReplica(d config.Database) bool {
	if d.Provider != "mariadb" || d.Port == 0 || d.DataDir == "" {
		return false
	}
	if strings.EqualFold(d.Replication.PrimaryHost, "localhost") {
		return true
	}
	ip := net.ParseIP(d.Replication.PrimaryHost)
	return ip != nil && ip.IsLoopback()
}

func Report(ctx context.Context, c config.Config, p platform.Platform, out io.Writer) error {
	failed := 0
	for _, check := range Checks(c, p) {
		cmd := exec.CommandContext(ctx, check.Command, check.Args...)
		output, err := cmd.CombinedOutput()
		if err == nil {
			fmt.Fprintf(out, "[ok]   %s\n", check.Name)
			continue
		}
		failed++
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		fmt.Fprintf(out, "[fail] %s: %s\n", check.Name, detail)
	}
	if failed > 0 {
		return fmt.Errorf("%d health check(s) failed", failed)
	}
	return nil
}

func webService(web string, p platform.Platform) string {
	if web == "apache" {
		if p.Family == "debian" {
			return "apache2"
		}
		return "httpd"
	}
	if web == "openlitespeed" {
		return "lsws"
	}
	return "nginx"
}
