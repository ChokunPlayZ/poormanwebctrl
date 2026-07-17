package provider

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/managed"
	"github.com/chokunplayz/poormanwebctrl/internal/plan"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

const managedServerMessage = "This Webserver is managed via poorman CLI, any changes to the config made outside Poorman WILL BE OVERWRITTEN"

const managedConfigHeader = "# Managed by poorman CLI. Changes made outside Poorman WILL BE OVERWRITTEN.\n"

func Build(c config.Config, p platform.Platform) (plan.Plan, error) {
	return BuildWithFeatures(c, p, BuiltInFeatures())
}

// BuildWithFeatures builds a plan from an explicit feature pipeline. The
// feature order is the plan order, so dependencies remain visible and easy to
// review.
func BuildWithFeatures(c config.Config, p platform.Platform, features []Feature) (plan.Plan, error) {
	return build(c, p, "", features)
}

// BuildForConfig adds the host-local managed-service reconciliation around the
// normal desired-state plan. The legacy Build entry point remains useful for
// callers that only need a plan and do not want to persist host inventory.
func BuildForConfig(c config.Config, p platform.Platform, configPath string) (plan.Plan, error) {
	return BuildForConfigWithFeatures(c, p, configPath, BuiltInFeatures())
}

// BuildForConfigWithFeatures adds managed-service reconciliation to a custom
// feature pipeline.
func BuildForConfigWithFeatures(c config.Config, p platform.Platform, configPath string, features []Feature) (plan.Plan, error) {
	return build(c, p, configPath, features)
}

func build(c config.Config, p platform.Platform, configPath string, features []Feature) (plan.Plan, error) {
	if err := validateFeatures(c, p, features); err != nil {
		return plan.Plan{}, err
	}
	result := plan.Plan{Platform: p.Distro}
	packages := packageSetForFeatures(c, p, features)
	if len(packages) > 0 {
		addPackageSteps(&result, p, packages)
	}
	addManagedMOTD(&result, p)
	if configPath != "" {
		services := desiredManagedServices(c, p, configPath)
		content, err := json.Marshal(services)
		if err != nil {
			return plan.Plan{}, fmt.Errorf("encode managed service inventory: %w", err)
		}
		result.Add(plan.Dir("Create poorman managed state directory", managed.StateDir, "root", 0o755))
		manager := "systemctl"
		if p.Family == "alpine" {
			manager = "rc-service"
		}
		result.Add(plan.ReconcileManagedStateWithManager("Reconcile poorman-managed services", managed.StatePath, configPath, string(content), manager))
	}
	for _, feature := range features {
		if err := feature.Plan(&result, c, p); err != nil {
			return plan.Plan{}, fmt.Errorf("feature %s: %w", feature.Name(), err)
		}
	}
	if configPath != "" {
		services := desiredManagedServices(c, p, configPath)
		content, err := json.Marshal(services)
		if err != nil {
			return plan.Plan{}, fmt.Errorf("encode managed service inventory: %w", err)
		}
		result.Add(plan.ManagedState("Record poorman-managed services", managed.StatePath, configPath, string(content)))
	}
	return result, nil
}

func addManagedMOTD(pn *plan.Plan, p platform.Platform) {
	switch p.Family {
	case "debian":
		pn.Add(plan.Dir("Create dynamic MOTD directory", "/etc/update-motd.d", "root", 0o755))
		content := "#!/bin/sh\nprintf '%s\\n' '" + managedServerMessage + "'\n"
		pn.Add(plan.ManagedFile("Install poorman managed-server MOTD", "/etc/update-motd.d/99-poorman", content, "root", 0o755))
	case "rhel":
		pn.Add(plan.Dir("Create MOTD fragment directory", "/etc/motd.d", "root", 0o755))
		pn.Add(plan.ManagedFile("Install poorman managed-server MOTD", "/etc/motd.d/99-poorman", managedServerMessage+"\n", "root", 0o644))
	default:
		pn.Add(plan.ManagedFile("Install poorman managed-server MOTD", "/etc/motd", managedServerMessage+"\n", "root", 0o644))
	}
}

func desiredManagedServices(c config.Config, p platform.Platform, configPath string) []managed.Service {
	services := managed.DesiredServices(c, configPath)
	for i := range services {
		if services[i].Kind == "web" {
			services[i].Name = webServiceName(c.WebServer.Provider, p)
			services[i].Files = managedWebConfigFiles(c, p)
			break
		}
	}
	return services
}

func webServiceName(web string, p platform.Platform) string {
	switch web {
	case "apache":
		if p.Family == "debian" {
			return "apache2"
		}
		return "httpd"
	case "openlitespeed":
		return "lsws"
	default:
		return "nginx"
	}
}

func managedWebConfigFiles(c config.Config, p platform.Platform) []string {
	files := make([]string, 0, len(c.Sites)+1)
	if c.WebServer.Provider == "openlitespeed" {
		files = append(files, "/usr/local/lsws/conf/poorman.conf")
	}
	for _, site := range c.Sites {
		path, _ := siteConfig(c.WebServer.Provider, site, p)
		files = append(files, path)
	}
	sort.Strings(files)
	return files
}

// WebServer remains as a compatibility entry point for early callers.
func WebServer(c config.Config, p platform.Platform) (plan.Plan, error) { return Build(c, p) }

// Firewall returns only the firewall-related portion of the desired-state plan.
// It is used by the TUI so operators can inspect and apply firewall changes
// without re-running the complete server configuration.
func Firewall(c config.Config, p platform.Platform) (plan.Plan, error) {
	if !c.Firewall.Enabled {
		return plan.Plan{}, fmt.Errorf("firewall policy is disabled in the configuration")
	}
	if c.Firewall.Enabled && p.Family != "debian" && p.Family != "rhel" {
		return plan.Plan{}, fmt.Errorf("firewall policy management is not supported for %s", p.Distro)
	}
	result := plan.Plan{Platform: p.Distro}
	if c.Firewall.Enabled {
		if p.Family == "debian" {
			result.Add(plan.Cmd("Install firewall package", "apt-get", true, "install", "-y", "ufw"))
		} else if p.Family == "rhel" {
			result.Add(plan.Cmd("Install firewall package", "dnf", true, "install", "-y", "firewalld"))
		}
	}
	addFirewall(&result, c, p)
	return result, nil
}

func FirewallStatus(p platform.Platform) (plan.Plan, error) {
	result := plan.Plan{Platform: p.Distro}
	if p.Family == "debian" {
		result.Add(plan.Cmd("Show UFW status", "ufw", false, "status", "verbose"))
	} else if p.Family == "rhel" {
		result.Add(plan.Cmd("Show firewalld status", "firewall-cmd", false, "--state"))
	} else {
		return plan.Plan{}, fmt.Errorf("firewall status is not supported for %s", p.Distro)
	}
	return result, nil
}

func DisableFirewall(p platform.Platform) (plan.Plan, error) {
	result := plan.Plan{Platform: p.Distro}
	switch p.Family {
	case "debian":
		result.Add(plan.Cmd("Disable UFW", "ufw", true, "disable"))
	case "rhel":
		result.Add(plan.Cmd("Stop and disable firewalld", "systemctl", true, "disable", "--now", "firewalld"))
	default:
		return plan.Plan{}, fmt.Errorf("firewall management is not supported for %s", p.Distro)
	}
	result.Warn("The configured firewall policy remains enabled; the next configuration apply will enable it again")
	return result, nil
}

func ReplicaStatus(c config.Config, p platform.Platform) (plan.Plan, error) {
	if c.Database == nil || c.Database.Role == "standalone" {
		return plan.Plan{}, fmt.Errorf("database is not configured for replication")
	}
	result := plan.Plan{Platform: p.Distro}
	if c.Database.Provider == "postgresql" {
		query := "SELECT status, sender_host, slot_name, latest_end_lsn, latest_end_time FROM pg_stat_wal_receiver;"
		if c.Database.Role == "primary" {
			query = "SELECT application_name, client_addr, state, sync_state, write_lag, flush_lag, replay_lag FROM pg_stat_replication;"
		}
		args := []string{"-x", "-c", query}
		if c.Database.Port > 0 {
			args = append([]string{"-p", strconv.Itoa(c.Database.Port)}, args...)
		}
		result.Add(plan.AsUser("Show PostgreSQL replication status", "postgres", "psql", args...))
	} else {
		query := "SHOW REPLICA STATUS\\G"
		if c.Database.Role == "primary" {
			query = "SHOW MASTER STATUS\\G"
		}
		step := plan.Cmd("Show MariaDB replication status", "mariadb", true)
		if isManagedMariaDBInstance(*c.Database) {
			layout := mariaDBReplicaLayout(*c.Database)
			step.Args = []string{"--protocol=socket", "--socket=" + layout.Socket}
		} else if c.Database.Port > 0 {
			step.Args = []string{"--port", strconv.Itoa(c.Database.Port)}
		}
		step.Input = query + "\n"
		result.Add(step)
	}
	if c.Database.Role == "primary" {
		if replica, ok := c.Database.LocalReplicaDatabase(); ok {
			if replica.Provider == "postgresql" {
				query := "SELECT status, sender_host, slot_name, latest_end_lsn, latest_end_time FROM pg_stat_wal_receiver;"
				result.Add(plan.AsUser("Show local PostgreSQL replica status", "postgres", "psql", "-p", strconv.Itoa(replica.Port), "-x", "-c", query))
			} else {
				layout := mariaDBReplicaLayout(replica)
				step := plan.Cmd("Show local MariaDB replica status", "mariadb", true, "--protocol=socket", "--socket="+layout.Socket)
				step.Input = "SHOW REPLICA STATUS\\G\n"
				result.Add(step)
			}
		}
	}
	return result, nil
}

func PromoteReplica(c config.Config, p platform.Platform) (plan.Plan, error) {
	if c.Database == nil {
		return plan.Plan{}, fmt.Errorf("promotion requires database.role=replica")
	}
	database := *c.Database
	if database.Role != "replica" {
		local, ok := database.LocalReplicaDatabase()
		if !ok {
			return plan.Plan{}, fmt.Errorf("promotion requires database.role=replica or database.local_replica")
		}
		database = local
	}
	result := plan.Plan{Platform: p.Distro}
	if database.Provider == "postgresql" {
		result.Add(plan.AsUser("Promote PostgreSQL replica", "postgres", "pg_ctl", "promote", "-D", databaseDataDir(database, p)))
	} else {
		if isLocalMariaDBReplica(database) {
			layout := mariaDBReplicaLayout(database)
			result.Add(plan.ManagedFile("Persist promoted MariaDB instance as writable", layout.Config, mariaDBInstanceConfig(database, false), "root", 0o644))
		}
		step := plan.Cmd("Promote MariaDB replica", "mariadb", true)
		if isLocalMariaDBReplica(database) {
			layout := mariaDBReplicaLayout(database)
			step.Args = []string{"--protocol=socket", "--socket=" + layout.Socket}
		}
		step.Input = "STOP REPLICA;\nRESET REPLICA ALL;\nSET GLOBAL read_only=OFF;\n"
		result.Add(step)
	}
	result.Warn("Promotion is not automatic failover: fence the old primary first, redirect clients, verify writes, and update database.role in the config")
	return result, nil
}
