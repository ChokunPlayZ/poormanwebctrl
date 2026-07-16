package health

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

type Check struct {
	Name, Command string
	Args          []string
}

func Checks(c config.Config, p platform.Platform) []Check {
	services := []string{webService(c.WebServer.Provider, p)}
	if c.Database != nil {
		services = append(services, c.Database.Provider)
		if c.Database.Provider == "postgresql" {
			services[len(services)-1] = "postgresql"
		}
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
