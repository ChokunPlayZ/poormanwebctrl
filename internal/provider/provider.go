package provider

import (
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/plan"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

func Build(c config.Config, p platform.Platform) (plan.Plan, error) {
	if c.WebServer.Provider == "openlitespeed" && p.Family == "alpine" {
		return plan.Plan{}, fmt.Errorf("OpenLiteSpeed supports Debian/Ubuntu and RHEL-family packages, not Alpine")
	}
	if c.Database != nil && isManagedMariaDBInstance(*c.Database) && p.Family == "alpine" {
		return plan.Plan{}, fmt.Errorf("same-machine MariaDB replicas require systemd; Alpine/OpenRC is not supported yet")
	}
	result := plan.Plan{Platform: p.Distro}
	packages := packageSet(c, p)
	addPackageSteps(&result, p, packages)
	if c.WebServer.Provider == "openlitespeed" {
		addOpenLiteSpeedInstall(&result, c, p)
	}
	addUsers(&result, c, p)
	addDatabase(&result, c, p)
	addSites(&result, c, p)
	addFTP(&result, c, p)
	addFirewall(&result, c, p)
	addTLS(&result, c, p)
	addBackups(&result, c, p)
	return result, nil
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
	return result, nil
}

func PromoteReplica(c config.Config, p platform.Platform) (plan.Plan, error) {
	if c.Database == nil || c.Database.Role != "replica" {
		return plan.Plan{}, fmt.Errorf("promotion requires database.role=replica")
	}
	result := plan.Plan{Platform: p.Distro}
	if c.Database.Provider == "postgresql" {
		result.Add(plan.AsUser("Promote PostgreSQL replica", "postgres", "pg_ctl", "promote", "-D", databaseDataDir(*c.Database, p)))
	} else {
		if isLocalMariaDBReplica(*c.Database) {
			layout := mariaDBReplicaLayout(*c.Database)
			result.Add(plan.ManagedFile("Persist promoted MariaDB instance as writable", layout.Config, mariaDBInstanceConfig(*c.Database, false), "root", 0o644))
		}
		step := plan.Cmd("Promote MariaDB replica", "mariadb", true)
		if isLocalMariaDBReplica(*c.Database) {
			layout := mariaDBReplicaLayout(*c.Database)
			step.Args = []string{"--protocol=socket", "--socket=" + layout.Socket}
		}
		step.Input = "STOP REPLICA;\nRESET REPLICA ALL;\nSET GLOBAL read_only=OFF;\n"
		result.Add(step)
	}
	result.Warn("Promotion is not automatic failover: fence the old primary first, redirect clients, verify writes, and update database.role in the config")
	return result, nil
}

func packageSet(c config.Config, p platform.Platform) []string {
	set := map[string]bool{}
	web := c.WebServer.Provider
	if web == "apache" {
		if p.Family == "debian" {
			web = "apache2"
		} else {
			web = "httpd"
		}
	}
	if web == "openlitespeed" {
		set["wget"] = true
	} else {
		set[web] = true
	}
	for _, s := range c.Sites {
		if s.Runtime == "php" || s.WordPress != nil {
			if c.WebServer.Provider == "openlitespeed" {
				continue
			}
			for _, pkg := range phpPackages(p, c.WebServer.Provider, c.Database) {
				set[pkg] = true
			}
		}
	}
	if c.Database != nil {
		for _, pkg := range databasePackages(*c.Database, p) {
			set[pkg] = true
		}
	}
	if c.Access.FTP.Enabled {
		set["vsftpd"] = true
	}
	if c.TLS.Enabled {
		set["certbot"] = true
		if c.WebServer.Provider == "nginx" {
			if p.Family == "alpine" {
				set["certbot-nginx"] = true
			} else {
				set["python3-certbot-nginx"] = true
			}
		} else if c.WebServer.Provider == "apache" {
			if p.Family == "alpine" {
				set["certbot-apache"] = true
			} else {
				set["python3-certbot-apache"] = true
			}
		}
	}
	if c.Firewall.Enabled {
		if p.Family == "debian" {
			set["ufw"] = true
		} else if p.Family == "rhel" {
			set["firewalld"] = true
		}
	}
	if c.Backups.Enabled {
		set["rsync"] = true
	}
	writableWordPress := anyWordPress(c) && wordpressInitializationAllowed(c)
	if writableWordPress {
		set["curl"] = true
		set["tar"] = true
	}
	items := make([]string, 0, len(set))
	for item := range set {
		items = append(items, item)
	}
	sort.Strings(items)
	return items
}

func phpPackages(p platform.Platform, web string, database *config.Database) []string {
	if web == "openlitespeed" {
		packages := []string{"lsphp84", "lsphp84-common", "lsphp84-curl", "lsphp84-gd", "lsphp84-mbstring", "lsphp84-xml", "lsphp84-zip"}
		if database != nil {
			if database.Provider == "postgresql" {
				packages = append(packages, "lsphp84-pgsql")
			} else if p.Family == "rhel" {
				packages = append(packages, "lsphp84-mysqlnd")
			} else {
				packages = append(packages, "lsphp84-mysql")
			}
		}
		return packages
	}
	if p.Family == "debian" && web == "apache" {
		packages := []string{"libapache2-mod-php", "php-curl", "php-gd", "php-mbstring", "php-xml", "php-zip"}
		if database != nil {
			if database.Provider == "postgresql" {
				packages = append(packages, "php-pgsql")
			} else {
				packages = append(packages, "php-mysql")
			}
		}
		return packages
	}
	if p.Family == "alpine" {
		packages := []string{"php84-fpm", "php84-curl", "php84-gd", "php84-mbstring", "php84-xml", "php84-zip"}
		if database != nil {
			if database.Provider == "postgresql" {
				packages = append(packages, "php84-pgsql")
			} else {
				packages = append(packages, "php84-mysqli")
			}
		}
		return packages
	}
	packages := []string{"php-fpm", "php-curl", "php-gd", "php-mbstring", "php-xml", "php-zip"}
	if database != nil {
		if database.Provider == "postgresql" {
			packages = append(packages, "php-pgsql")
		} else if p.Family == "rhel" {
			packages = append(packages, "php-mysqlnd")
		} else {
			packages = append(packages, "php-mysql")
		}
	}
	return packages
}

func databasePackages(d config.Database, p platform.Platform) []string {
	if d.Provider == "postgresql" {
		if p.Family == "rhel" {
			return []string{"postgresql", "postgresql-server"}
		}
		return []string{"postgresql", "postgresql-client"}
	}
	if p.Family == "rhel" {
		return []string{"mariadb", "mariadb-server"}
	}
	if p.Family == "alpine" {
		return []string{"mariadb", "mariadb-client"}
	}
	return []string{"mariadb-client", "mariadb-server"}
}

func addOpenLiteSpeedInstall(pn *plan.Plan, c config.Config, p platform.Platform) {
	pn.Add(plan.Cmd("Download the official LiteSpeed repository installer", "wget", true, "-qO", "/tmp/poorman-litespeed-repo.sh", "https://repo.litespeed.sh"))
	pn.Add(plan.Cmd("Enable the official LiteSpeed package repository", "bash", true, "/tmp/poorman-litespeed-repo.sh"))
	packages := append([]string{"openlitespeed"}, phpPackages(p, "openlitespeed", c.Database)...)
	addPackageSteps(pn, p, packages)
	pn.Warn("The LiteSpeed repository bootstrap is a network-delivered upstream script; inspect the plan and source before production use")
}

func addPackageSteps(pn *plan.Plan, p platform.Platform, packages []string) {
	switch p.Family {
	case "debian":
		// Disable apt's pseudo-terminal progress handling. It emits carriage
		// return-only status lines that look frozen when forwarded by the TUI.
		aptOptions := []string{"-o", "Dpkg::Use-Pty=0"}
		pn.Add(plan.Cmd("Refresh package metadata", "apt-get", true, append(aptOptions, "update")...))
		pn.Add(plan.Cmd("Install required packages", "apt-get", true, append(append(aptOptions, "install", "-y"), packages...)...))
	case "rhel":
		pn.Add(plan.Cmd("Install required packages", "dnf", true, append([]string{"install", "-y"}, packages...)...))
	case "alpine":
		pn.Add(plan.Cmd("Install required packages", "apk", true, append([]string{"add"}, packages...)...))
	}
}

func addUsers(pn *plan.Plan, c config.Config, p platform.Platform) {
	for _, u := range c.Access.Users {
		home := u.Home
		if home == "" {
			home = "/home/" + u.Name
		}
		shell := "/bin/bash"
		if u.SFTPOnly {
			shell = "/usr/sbin/nologin"
		}
		step := plan.Cmd("Create system user "+u.Name, "useradd", true, "-m", "-d", home, "-s", shell, u.Name)
		step.UnlessCommand, step.UnlessArgs = "id", []string{"-u", u.Name}
		pn.Add(step)
		if len(u.PublicKeys) > 0 {
			pn.Add(plan.Dir("Create SSH directory for "+u.Name, filepath.Join(home, ".ssh"), u.Name, 0o700))
			pn.Add(plan.ManagedFile("Install authorized SSH keys for "+u.Name, filepath.Join(home, ".ssh", "authorized_keys"), strings.Join(u.PublicKeys, "\n")+"\n", u.Name, 0o600))
		}
	}
	if hasSFTPOnly(c) {
		content := "# Managed by poorman\nMatch Group poorman-sftp\n    ForceCommand internal-sftp\n    AllowTcpForwarding no\n    X11Forwarding no\n"
		pn.Add(plan.Cmd("Create SFTP-only group", "groupadd", true, "-f", "poorman-sftp"))
		for _, u := range c.Access.Users {
			if u.SFTPOnly {
				pn.Add(plan.Cmd("Add "+u.Name+" to SFTP-only group", "usermod", true, "-aG", "poorman-sftp", u.Name))
			}
		}
		pn.Add(plan.ManagedFile("Configure SFTP-only access", "/etc/ssh/sshd_config.d/90-poorman-sftp.conf", content, "root", 0o644))
		sshService := "sshd"
		if p.Family == "debian" {
			sshService = "ssh"
		}
		pn.Add(plan.Cmd("Validate SSH configuration", "sshd", true, "-t"), restartService(p, sshService))
	}
}

func addDatabase(pn *plan.Plan, c config.Config, p platform.Platform) {
	if c.Database == nil {
		return
	}
	d := c.Database
	service := "mariadb"
	if d.Provider == "postgresql" {
		service = "postgresql"
	}
	if d.Provider == "postgresql" && d.Role != "replica" && p.Family == "rhel" {
		step := plan.Cmd("Initialize PostgreSQL data directory", "postgresql-setup", true, "--initdb")
		step.UnlessCommand, step.UnlessArgs = "test", []string{"-e", "/var/lib/pgsql/data/PG_VERSION"}
		pn.Add(step)
	}
	if d.Provider == "postgresql" && d.Role != "replica" && p.Family == "alpine" {
		step := plan.Cmd("Initialize PostgreSQL data directory", "rc-service", true, "postgresql", "setup")
		step.UnlessCommand, step.UnlessArgs = "test", []string{"-e", "/var/lib/postgresql/data/PG_VERSION"}
		pn.Add(step)
	}
	if isManagedMariaDBInstance(*d) && d.Role == "primary" {
		addPromotedMariaDBPrimary(pn, *d, p)
		return
	}
	if !(d.Provider == "postgresql" && d.Role == "replica") && !isLocalMariaDBReplica(*d) {
		pn.Add(enableService(p, service))
	}
	if d.Role != "replica" && d.Name != "" && d.User != "" {
		if d.Provider == "postgresql" {
			input := fmt.Sprintf("SELECT format('CREATE ROLE %%I LOGIN PASSWORD %%L', '%s', '${%s}') WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s')\\gexec\nSELECT format('CREATE DATABASE %%I OWNER %%I', '%s', '%s') WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '%s')\\gexec\n", d.User, d.PasswordEnv, d.User, d.Name, d.User, d.Name)
			step := plan.AsUser("Create PostgreSQL application database and user", "postgres", "psql")
			step.Input, step.Sensitive, step.SQLSecrets = input, true, true
			pn.Add(step)
		} else {
			input := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`;\nCREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY '${%s}';\nALTER USER '%s'@'localhost' IDENTIFIED BY '${%s}';\nGRANT ALL ON `%s`.* TO '%s'@'localhost';\nFLUSH PRIVILEGES;\n", d.Name, d.User, d.PasswordEnv, d.User, d.PasswordEnv, d.Name, d.User)
			step := plan.Cmd("Create MariaDB application database and user", "mariadb", true, "--batch", "--skip-column-names", "--connect-timeout=10")
			step.Input, step.Sensitive, step.SQLSecrets = input, true, true
			step.TimeoutSeconds = 60
			pn.Add(step)
		}
	}
	if d.Role != "standalone" {
		addReplication(pn, *d, p)
	}
}

func addReplication(pn *plan.Plan, d config.Database, p platform.Platform) {
	r := d.Replication
	if d.Provider == "mariadb" {
		if isLocalMariaDBReplica(d) {
			addLocalMariaDBReplica(pn, d, p)
			return
		}
		serverID := fmt.Sprint(r.NodeID)
		readOnly := "OFF"
		if d.Role == "replica" {
			readOnly = "ON"
		}
		port := ""
		if d.Port > 0 {
			port = fmt.Sprintf("port=%d\n", d.Port)
		}
		conf := fmt.Sprintf("# Managed by poorman\n[mariadb]\nserver_id=%s\n%slog_bin=mysql-bin\nbinlog_format=ROW\ngtid_strict_mode=ON\nread_only=%s\nbind_address=0.0.0.0\n", serverID, port, readOnly)
		pn.Add(plan.ManagedFile("Configure MariaDB "+d.Role, mariaDBReplicationConfigPath(p), conf, "root", 0o644))
		// Load the GTID, read-only, and optional port settings before sending
		// replication SQL to this server.
		pn.Add(restartService(p, "mariadb"))
		if d.Role == "primary" {
			input := fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '${%s}';\nGRANT REPLICATION SLAVE ON *.* TO '%s'@'%%';\nFLUSH PRIVILEGES;\n", r.User, r.PasswordEnv, r.User)
			step := plan.Cmd("Create MariaDB replication user", "mariadb", true)
			step.Input, step.Sensitive, step.SQLSecrets = input, true, true
			pn.Add(step)
		} else {
			primaryPort := ""
			if r.PrimaryPort > 0 {
				primaryPort = fmt.Sprintf(", MASTER_PORT=%d", r.PrimaryPort)
			}
			input := fmt.Sprintf("STOP REPLICA;\nCHANGE MASTER TO MASTER_HOST='%s', MASTER_USER='%s', MASTER_PASSWORD='${%s}'%s, MASTER_USE_GTID=slave_pos;\nSTART REPLICA;\n", r.PrimaryHost, r.User, r.PasswordEnv, primaryPort)
			step := plan.Cmd("Attach MariaDB replica to primary", "mariadb", true)
			step.Input, step.Sensitive, step.SQLSecrets = input, true, true
			pn.Add(step)
		}
		return
	}
	if d.Role == "primary" {
		pn.Add(plan.AsUser("Allow PostgreSQL to listen for replicas", "postgres", "psql", "-c", "ALTER SYSTEM SET listen_addresses = '*';"))
		pn.Add(plan.AsUser("Enable PostgreSQL replication settings", "postgres", "psql", "-c", "ALTER SYSTEM SET wal_level = 'replica';"))
		pn.Add(plan.AsUser("Set PostgreSQL WAL senders", "postgres", "psql", "-c", "ALTER SYSTEM SET max_wal_senders = '10';"))
		input := fmt.Sprintf("SELECT format('CREATE ROLE %%I WITH REPLICATION LOGIN PASSWORD %%L', '%s', '${%s}') WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s')\\gexec\n", r.User, r.PasswordEnv, r.User)
		step := plan.AsUser("Create PostgreSQL replication role", "postgres", "psql")
		step.Input, step.Sensitive, step.SQLSecrets = input, true, true
		pn.Add(step)
		pn.Warn(fmt.Sprintf("Add this exact PostgreSQL pg_hba.conf rule, then reload: host replication %s %s scram-sha-256", r.User, r.AllowedCIDR))
	} else {
		dataDir := databaseDataDir(d, p)
		// A loopback primary is the local system instance that pg_basebackup
		// needs to contact, so leave it running while bootstrapping the separate
		// replica data directory.
		if !isLoopbackHost(r.PrimaryHost) {
			pn.Add(stopService(p, "postgresql"))
		}
		args := []string{"-h", r.PrimaryHost, "-U", r.User, "-D", dataDir, "-R", "-X", "stream", "-P"}
		if r.PrimaryPort > 0 {
			args = append(args, "-p", strconv.Itoa(r.PrimaryPort))
		}
		if r.Slot != "" {
			args = append(args, "-C", "-S", r.Slot)
		}
		step := plan.AsUser("Bootstrap PostgreSQL replica from primary", "postgres", "pg_basebackup", args...)
		step.Input, step.Sensitive = "${"+r.PasswordEnv+"}\n", true
		step.UnlessCommand, step.UnlessArgs = "test", []string{"-e", filepath.Join(dataDir, "PG_VERSION")}
		pn.Add(step)
		if d.Port > 0 {
			pn.Add(plan.EnsureLine("Set PostgreSQL replica port", filepath.Join(dataDir, "postgresql.conf"), fmt.Sprintf("port = %d", d.Port)))
		}
		pn.Warn("PostgreSQL replica bootstrap requires an empty data directory and a maintenance window; take a verified backup first")
	}
	if d.Role == "replica" && d.Port > 0 {
		step := plan.AsUser("Start PostgreSQL replica instance", "postgres", "pg_ctl", "-D", databaseDataDir(d, p), "-l", filepath.Join(databaseDataDir(d, p), "poorman.log"), "start")
		step.UnlessCommand, step.UnlessArgs = "pg_isready", []string{"-h", "127.0.0.1", "-p", strconv.Itoa(d.Port)}
		pn.Add(step)
	} else {
		pn.Add(restartService(p, "postgresql"))
	}
}

type mariaDBInstanceLayout struct {
	Service, Config, Unit, DataDir, RuntimeDir, Socket, PID, Log, Seed, SeedMarker string
}

func isLocalMariaDBReplica(d config.Database) bool {
	return d.Role == "replica" && isManagedMariaDBInstance(d)
}

func isManagedMariaDBInstance(d config.Database) bool {
	return d.Provider == "mariadb" && d.Port > 0 && d.DataDir != "" && isLoopbackHost(d.Replication.PrimaryHost)
}

func mariaDBReplicaLayout(d config.Database) mariaDBInstanceLayout {
	service := fmt.Sprintf("poorman-mariadb-replica-%d", d.Port)
	runtimeDir := filepath.Join("/run", service)
	return mariaDBInstanceLayout{
		Service:    service,
		Config:     filepath.Join("/etc/poorman", service+".cnf"),
		Unit:       filepath.Join("/etc/systemd/system", service+".service"),
		DataDir:    d.DataDir,
		RuntimeDir: runtimeDir,
		Socket:     filepath.Join(runtimeDir, "mariadb.sock"),
		PID:        filepath.Join(runtimeDir, "mariadb.pid"),
		Log:        filepath.Join(d.DataDir, "mariadb.log"),
		Seed:       filepath.Join(d.DataDir, ".poorman-replica-seed.sql"),
		SeedMarker: filepath.Join(d.DataDir, ".poorman-replica-seeded"),
	}
}

func mariaDBInstanceConfig(d config.Database, readOnly bool) string {
	layout := mariaDBReplicaLayout(d)
	readOnlyValue := "OFF"
	if readOnly {
		readOnlyValue = "ON"
	}
	return fmt.Sprintf("# Managed by poorman\n[mariadbd]\ndatadir=%s\nport=%d\nsocket=%s\npid-file=%s\nlog-error=%s\nserver_id=%d\nlog_bin=%s\nrelay_log=%s\nbinlog_format=ROW\ngtid_strict_mode=ON\nlog_slave_updates=ON\nrelay_log_recovery=ON\nsync_binlog=1\ninnodb_flush_log_at_trx_commit=1\nread_only=%s\nbind_address=127.0.0.1\nskip_name_resolve=ON\n", layout.DataDir, d.Port, layout.Socket, layout.PID, layout.Log, d.Replication.NodeID, filepath.Join(layout.DataDir, "mysql-bin"), filepath.Join(layout.DataDir, "relay-bin"), readOnlyValue)
}

func addLocalMariaDBReplica(pn *plan.Plan, d config.Database, p platform.Platform) {
	layout := addMariaDBInstanceService(pn, d, p, true)

	dump := plan.Cmd("Seed MariaDB replica from local primary", "mariadb-dump", true,
		"--protocol=socket", "--all-databases", "--single-transaction", "--routines", "--events", "--triggers", "--flush-privileges", "--master-data=2", "--gtid", "--result-file="+layout.Seed)
	dump.UnlessCommand, dump.UnlessArgs = "test", []string{"-e", layout.SeedMarker}
	pn.Add(dump)
	load := plan.Cmd("Load primary snapshot into MariaDB replica", "mariadb", true, "--protocol=socket", "--socket="+layout.Socket)
	load.Input = fmt.Sprintf("SOURCE %s;\nFLUSH PRIVILEGES;\n", layout.Seed)
	load.UnlessCommand, load.UnlessArgs = "test", []string{"-e", layout.SeedMarker}
	pn.Add(load)
	mark := plan.Cmd("Mark MariaDB replica snapshot loaded", "touch", true, layout.SeedMarker)
	mark.UnlessCommand, mark.UnlessArgs = "test", []string{"-e", layout.SeedMarker}
	pn.Add(mark)
	cleanup := plan.Cmd("Remove temporary MariaDB replica snapshot", "unlink", true, layout.Seed)
	cleanup.UnlessCommand, cleanup.UnlessArgs = "test", []string{"!", "-e", layout.Seed}
	pn.Add(cleanup)

	primaryPort := d.Replication.PrimaryPort
	if primaryPort == 0 {
		primaryPort = 3306
	}
	input := fmt.Sprintf("STOP REPLICA;\nCHANGE MASTER TO MASTER_HOST='%s', MASTER_USER='%s', MASTER_PASSWORD='${%s}', MASTER_PORT=%d, MASTER_USE_GTID=slave_pos;\nSTART REPLICA;\n", d.Replication.PrimaryHost, d.Replication.User, d.Replication.PasswordEnv, primaryPort)
	attach := plan.Cmd("Attach independent MariaDB replica to local primary", "mariadb", true, "--protocol=socket", "--socket="+layout.Socket)
	attach.Input, attach.Sensitive, attach.SQLSecrets = input, true, true
	pn.Add(attach)
	pn.Warn("Same-machine replication keeps database processes independent, but it does not protect against host, disk, kernel, or power failure")
}

func addMariaDBInstanceService(pn *plan.Plan, d config.Database, p platform.Platform, readOnly bool) mariaDBInstanceLayout {
	layout := mariaDBReplicaLayout(d)
	serverBinary := "/usr/sbin/mariadbd"
	if p.Family == "rhel" {
		serverBinary = "/usr/libexec/mariadbd"
	}
	conf := mariaDBInstanceConfig(d, readOnly)
	unit := fmt.Sprintf("[Unit]\nDescription=Poorman MariaDB replica on port %d\nWants=network-online.target\nAfter=network-online.target mariadb.service\n\n[Service]\nType=simple\nUser=mysql\nGroup=mysql\nRuntimeDirectory=%s\nRuntimeDirectoryMode=0750\nExecStart=%s --defaults-file=%s\nRestart=on-failure\nRestartSec=5s\nTimeoutStopSec=900s\nLimitNOFILE=32768\n\n[Install]\nWantedBy=multi-user.target\n", d.Port, layout.Service, serverBinary, layout.Config)

	pn.Add(plan.Dir("Create MariaDB replica data directory", layout.DataDir, "mysql", 0o700))
	pn.Add(plan.Dir("Create MariaDB replica runtime directory", layout.RuntimeDir, "mysql", 0o750))
	pn.Add(plan.ManagedFile("Configure independent MariaDB instance", layout.Config, conf, "root", 0o644))
	initialize := plan.Cmd("Initialize MariaDB replica data directory", "mariadb-install-db", true, "--defaults-file="+layout.Config, "--user=mysql", "--datadir="+layout.DataDir, "--skip-test-db")
	initialize.UnlessCommand, initialize.UnlessArgs = "test", []string{"-d", filepath.Join(layout.DataDir, "mysql")}
	pn.Add(initialize)
	pn.Add(plan.ManagedFile("Install independent MariaDB replica service", layout.Unit, unit, "root", 0o644))
	pn.Add(plan.Cmd("Reload systemd for MariaDB replica", "systemctl", true, "daemon-reload"))
	pn.Add(plan.Cmd("Enable independent MariaDB replica service", "systemctl", true, "enable", layout.Service))
	pn.Add(plan.Cmd("Restart independent MariaDB replica service", "systemctl", true, "restart", layout.Service))
	return layout
}

func addPromotedMariaDBPrimary(pn *plan.Plan, d config.Database, p platform.Platform) {
	layout := addMariaDBInstanceService(pn, d, p, false)
	clientArgs := []string{"--protocol=socket", "--socket=" + layout.Socket}
	if d.Name != "" && d.User != "" {
		input := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`;\nCREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY '${%s}';\nALTER USER '%s'@'localhost' IDENTIFIED BY '${%s}';\nGRANT ALL ON `%s`.* TO '%s'@'localhost';\nFLUSH PRIVILEGES;\n", d.Name, d.User, d.PasswordEnv, d.User, d.PasswordEnv, d.Name, d.User)
		step := plan.Cmd("Update application database on promoted MariaDB instance", "mariadb", true, clientArgs...)
		step.Input, step.Sensitive, step.SQLSecrets = input, true, true
		step.TimeoutSeconds = 60
		pn.Add(step)
	}
	input := fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '${%s}';\nGRANT REPLICATION SLAVE ON *.* TO '%s'@'%%';\nFLUSH PRIVILEGES;\n", d.Replication.User, d.Replication.PasswordEnv, d.Replication.User)
	step := plan.Cmd("Update replication user on promoted MariaDB instance", "mariadb", true, clientArgs...)
	step.Input, step.Sensitive, step.SQLSecrets = input, true, true
	pn.Add(step)
	pn.Warn("This promoted MariaDB instance remains an independent service; redirect clients to its configured port")
}

func mariaDBReplicationConfigPath(p platform.Platform) string {
	if p.Family == "debian" {
		return "/etc/mysql/mariadb.conf.d/90-poorman-replication.cnf"
	}
	return "/etc/my.cnf.d/90-poorman-replication.cnf"
}

func addSites(pn *plan.Plan, c config.Config, p platform.Platform) {
	service := c.WebServer.Provider
	if service == "apache" {
		if p.Family == "debian" {
			service = "apache2"
		} else {
			service = "httpd"
		}
	}
	if service == "openlitespeed" {
		service = "lsws"
	}
	writableWordPress := anyWordPress(c) && wordpressInitializationAllowed(c)
	if writableWordPress {
		download := plan.Cmd("Download wp-cli", "curl", true, "-fsSL", "-o", "/tmp/poorman-wp-cli.phar", "https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar")
		download.UnlessCommand, download.UnlessArgs = "wp", []string{"--info"}
		install := plan.Cmd("Install wp-cli", "install", true, "-m", "0755", "/tmp/poorman-wp-cli.phar", "/usr/local/bin/wp")
		install.UnlessCommand, install.UnlessArgs = "wp", []string{"--info"}
		pn.Add(download, install)
	}
	if c.WebServer.Provider == "openlitespeed" {
		pn.Add(plan.EnsureLine("Enable poorman OpenLiteSpeed includes", "/usr/local/lsws/conf/httpd_config.conf", "include /usr/local/lsws/conf/poorman.conf"))
		pn.Add(plan.ManagedFile("Register OpenLiteSpeed virtual hosts and listener", "/usr/local/lsws/conf/poorman.conf", openLiteSpeedServerConfig(c), "root", 0o600))
	}
	if c.WebServer.Provider == "nginx" && p.Family == "alpine" && hasPHPSite(c) {
		pool := "[www]\nuser = nginx\ngroup = nginx\nlisten = 127.0.0.1:9000\npm = dynamic\npm.max_children = 10\npm.start_servers = 2\npm.min_spare_servers = 1\npm.max_spare_servers = 3\n"
		pn.Add(plan.ManagedFile("Configure Alpine PHP-FPM pool", "/etc/php84/php-fpm.d/zz-poorman.conf", pool, "root", 0o644))
	}
	for _, s := range c.Sites {
		owner := s.Owner
		if owner == "" {
			owner = "www-data"
			if p.Family != "debian" {
				owner = "apache"
			}
		}
		pn.Add(plan.Dir("Create document root for "+s.Domain, s.Root, owner, 0o750))
		path, content := siteConfig(c.WebServer.Provider, s, p)
		pn.Add(plan.ManagedFile("Configure virtual host "+s.Domain, path, content, "root", 0o644))
		if s.WordPress != nil && writableWordPress {
			pn.Add(plan.AsUser("Download WordPress for "+s.Domain, owner, "wp", "core", "download", "--path="+s.Root))
			if c.Database != nil {
				step := plan.AsUser("Create WordPress configuration for "+s.Domain, owner, "wp", "config", "create", "--path="+s.Root, "--dbname="+c.Database.Name, "--dbuser="+c.Database.User, "--prompt=dbpass")
				step.Input, step.Sensitive = "${"+c.Database.PasswordEnv+"}\n", true
				pn.Add(step)
			}
			wp := s.WordPress
			scheme := "http"
			if c.TLS.Enabled {
				scheme = "https"
			}
			step := plan.AsUser("Install WordPress for "+s.Domain, owner, "wp", "core", "install", "--path="+s.Root, "--url="+scheme+"://"+s.Domain, "--title="+defaultString(wp.Title, s.Domain), "--admin_user="+defaultString(wp.AdminUser, "admin"), "--admin_email="+wp.AdminEmail, "--prompt=admin_password")
			step.Input, step.Sensitive = "${"+wp.AdminPassEnv+"}\n", true
			pn.Add(step)
		}
	}
	if anyWordPress(c) && !writableWordPress {
		pn.Warn("Skipped WordPress initialization because this database is a replica or promoted independent instance")
	}
	pn.Add(plan.Cmd("Validate "+c.WebServer.Provider+" configuration", validationCommand(c.WebServer.Provider), true, validationArgs(c.WebServer.Provider)...))
	pn.Add(restartService(p, service))
	if c.WebServer.Provider == "openlitespeed" {
		pn.Warn("OpenLiteSpeed include-managed configuration is edited as files and will not appear as editable state in WebAdmin")
	}
	if hasPHPSite(c) && c.WebServer.Provider == "nginx" {
		if p.Family == "debian" {
			pn.Warn("Verify the distro's versioned PHP-FPM service and /run/php/php-fpm.sock compatibility after installation")
		} else {
			phpService := "php-fpm"
			if p.Family == "alpine" {
				phpService = "php-fpm84"
			}
			pn.Add(enableService(p, phpService))
		}
	}
}

func siteConfig(web string, s config.Site, p platform.Platform) (string, string) {
	aliases := strings.Join(s.Aliases, " ")
	if web == "nginx" {
		php := "\n    location / { try_files $uri $uri/ =404; }"
		if s.Runtime == "php" {
			socket := "/run/php/php-fpm.sock"
			if p.Family == "rhel" {
				socket = "/run/php-fpm/www.sock"
			} else if p.Family == "alpine" {
				socket = "127.0.0.1:9000"
			}
			upstream := "unix:" + socket
			if p.Family == "alpine" {
				upstream = socket
			}
			php = fmt.Sprintf("\n    index index.php index.html;\n    location / { try_files $uri $uri/ /index.php?$args; }\n    location ~ \\.php$ { include fastcgi_params; fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name; fastcgi_pass %s; }", upstream)
		}
		content := fmt.Sprintf("# Managed by poorman\nserver {\n    listen 80;\n    server_name %s %s;\n    root %s;%s\n}\n", s.Domain, aliases, s.Root, php)
		return "/etc/nginx/conf.d/poorman-" + s.Domain + ".conf", content
	}
	if web == "apache" {
		content := fmt.Sprintf("# Managed by poorman\n<VirtualHost *:80>\n  ServerName %s\n  ServerAlias %s\n  DocumentRoot %s\n  <Directory %s>\n    AllowOverride All\n    Require all granted\n  </Directory>\n</VirtualHost>\n", s.Domain, aliases, s.Root, s.Root)
		base := "/etc/httpd/conf.d"
		if p.Family == "debian" {
			base = "/etc/apache2/sites-enabled"
		}
		return filepath.Join(base, "poorman-"+s.Domain+".conf"), content
	}
	content := fmt.Sprintf("docRoot                   %s\nvhDomain                  %s\nvhAliases                 %s\nenableGzip                1\nindex  {\n  useServer               0\n  indexFiles              index.php,index.html\n}\nrewrite  {\n  enable                  1\n  autoLoadHtaccess        1\n}\nextprocessor lsphp {\n  type                    lsapi\n  address                 uds://tmp/lshttpd/%s.sock\n  maxConns                10\n  env                     LSAPI_CHILDREN=10\n  initTimeout             60\n  retryTimeout            0\n  persistConn             1\n  respBuffer              0\n  autoStart               1\n  path                    /usr/local/lsws/lsphp84/bin/lsphp\n  backlog                 100\n  instances               1\n}\nscriptHandler  {\n  add                     lsapi:lsphp php\n}\n", s.Root, s.Domain, strings.Join(s.Aliases, ","), s.Domain)
	return "/usr/local/lsws/conf/vhosts/" + s.Domain + "/vhconf.conf", content
}

func openLiteSpeedServerConfig(c config.Config) string {
	var b strings.Builder
	b.WriteString("# Managed by poorman\nlistener poormanHTTP {\n  address                 *:80\n  secure                  0\n")
	for _, s := range c.Sites {
		domains := append([]string{s.Domain}, s.Aliases...)
		fmt.Fprintf(&b, "  map                     %s %s\n", s.Domain, strings.Join(domains, ","))
	}
	b.WriteString("}\n")
	for _, s := range c.Sites {
		fmt.Fprintf(&b, "virtualhost %s {\n  vhRoot                  %s\n  configFile              conf/vhosts/%s/vhconf.conf\n  allowSymbolLink         1\n  enableScript            1\n  restrained              1\n}\n", s.Domain, s.Root, s.Domain)
	}
	return b.String()
}

func addFTP(pn *plan.Plan, c config.Config, p platform.Platform) {
	if !c.Access.FTP.Enabled {
		return
	}
	conf := "# Managed by poorman\nlisten=YES\nanonymous_enable=NO\nlocal_enable=YES\nwrite_enable=YES\nchroot_local_user=YES\nallow_writeable_chroot=YES\n"
	pn.Add(plan.ManagedFile("Configure explicitly enabled plaintext FTP", "/etc/vsftpd.conf", conf, "root", 0o600), enableService(p, "vsftpd"))
	pn.Warn("Plain FTP exposes credentials and data; migrate clients to SFTP")
}

func addFirewall(pn *plan.Plan, c config.Config, p platform.Platform) {
	if !c.Firewall.Enabled {
		return
	}
	if p.Family == "debian" {
		for _, port := range []string{"OpenSSH", "80/tcp", "443/tcp"} {
			pn.Add(plan.Cmd("Allow "+port+" through firewall", "ufw", true, "allow", port))
		}
		pn.Add(plan.Cmd("Enable firewall", "ufw", true, "--force", "enable"))
		addDatabaseFirewall(pn, c, p)
	} else if p.Family == "rhel" {
		pn.Add(enableService(p, "firewalld"))
		for _, service := range []string{"ssh", "http", "https"} {
			pn.Add(plan.Cmd("Allow "+service+" through firewall", "firewall-cmd", true, "--permanent", "--add-service="+service))
		}
		pn.Add(plan.Cmd("Reload firewall", "firewall-cmd", true, "--reload"))
		addDatabaseFirewall(pn, c, p)
	} else {
		pn.Warn("Alpine firewall policy must be configured explicitly; no default rules were guessed")
	}
}

func addDatabaseFirewall(pn *plan.Plan, c config.Config, p platform.Platform) {
	if c.Database == nil || c.Database.Role != "primary" {
		return
	}
	port := "5432"
	if c.Database.Provider == "mariadb" {
		port = "3306"
	}
	cidr := c.Database.Replication.AllowedCIDR
	if p.Family == "debian" {
		pn.Add(plan.Cmd("Allow database replication network", "ufw", true, "allow", "from", cidr, "to", "any", "port", port, "proto", "tcp"))
	}
	if p.Family == "rhel" {
		rule := fmt.Sprintf("rule family=ipv4 source address=%s port port=%s protocol=tcp accept", cidr, port)
		pn.Add(plan.Cmd("Allow database replication network", "firewall-cmd", true, "--permanent", "--add-rich-rule="+rule), plan.Cmd("Reload database firewall rule", "firewall-cmd", true, "--reload"))
	}
}

func addTLS(pn *plan.Plan, c config.Config, p platform.Platform) {
	if !c.TLS.Enabled {
		return
	}
	for _, s := range c.Sites {
		args := []string{"--non-interactive", "--agree-tos", "--email", c.TLS.Email, "-d", s.Domain}
		for _, alias := range s.Aliases {
			args = append(args, "-d", alias)
		}
		if c.WebServer.Provider == "nginx" {
			args = append([]string{"--nginx", "--redirect"}, args...)
		} else if c.WebServer.Provider == "apache" {
			args = append([]string{"--apache", "--redirect"}, args...)
		} else {
			args = append([]string{"certonly", "--webroot", "-w", s.Root}, args...)
			pn.Add(plan.Cmd("Obtain TLS certificate for "+s.Domain, "certbot", true, args...))
			pn.Warn("Attach the issued Let's Encrypt fullchain.pem and privkey.pem to the OpenLiteSpeed HTTPS listener, then reload")
			continue
		}
		pn.Add(plan.Cmd("Obtain and attach TLS certificate for "+s.Domain, "certbot", true, args...))
	}
}

func addBackups(pn *plan.Plan, c config.Config, p platform.Platform) {
	if !c.Backups.Enabled {
		return
	}
	destination := c.Backups.Destination
	scriptPath := "/usr/local/sbin/poorman-backup"
	cronPath := "/etc/cron.d/poorman-backup"
	sites := c.Sites
	managedMariaDBInstance := c.Database != nil && isManagedMariaDBInstance(*c.Database)
	if managedMariaDBInstance {
		layout := mariaDBReplicaLayout(*c.Database)
		destination = filepath.Join(destination, layout.Service)
		scriptPath += "-" + layout.Service
		cronPath += "-" + layout.Service
		// The primary configuration owns same-host website backups. This
		// instance-specific job protects only the independent replica data.
		sites = nil
	}
	pn.Add(plan.Dir("Create backup destination", destination, "root", 0o700))
	var database string
	if c.Database != nil {
		if c.Database.Provider == "postgresql" {
			database = "sudo -u postgres pg_dumpall | gzip > \"$DEST/database.sql.gz\""
		} else if managedMariaDBInstance {
			layout := mariaDBReplicaLayout(*c.Database)
			database = fmt.Sprintf("mariadb-dump --protocol=socket --socket=%s --all-databases --single-transaction | gzip > \"$DEST/database.sql.gz\"", shellQuote(layout.Socket))
		} else {
			database = "mariadb-dump --all-databases --single-transaction | gzip > \"$DEST/database.sql.gz\""
		}
	}
	script := fmt.Sprintf("#!/bin/sh\nset -eu\nDEST=%q/$(date +%%F-%%H%%M%%S)\ninstall -d -m 700 \"$DEST\"\n%s\n", destination, database)
	for _, s := range sites {
		script += fmt.Sprintf("tar -C %q -czf \"$DEST/%s-files.tar.gz\" .\n", s.Root, s.Domain)
	}
	script += "find " + shellQuote(destination) + " -mindepth 1 -maxdepth 1 -type d -mtime +14 -exec rm -rf -- {} +\n"
	pn.Add(plan.ManagedFile("Install backup script", scriptPath, script, "root", 0o700))
	cron := c.Backups.Schedule + " root " + scriptPath + "\n"
	pn.Add(plan.ManagedFile("Schedule backups", cronPath, cron, "root", 0o600))
}

func enableService(p platform.Platform, service string) plan.Step {
	if p.Family == "alpine" {
		return plan.Cmd("Start "+service, "rc-service", true, service, "start")
	}
	return plan.Cmd("Enable and start "+service, "systemctl", true, "enable", "--now", service)
}

func restartService(p platform.Platform, service string) plan.Step {
	if p.Family == "alpine" {
		return plan.Cmd("Restart "+service, "rc-service", true, service, "restart")
	}
	return plan.Cmd("Reload or restart "+service, "systemctl", true, "reload-or-restart", service)
}

func stopService(p platform.Platform, service string) plan.Step {
	if p.Family == "alpine" {
		return plan.Cmd("Stop "+service, "rc-service", true, service, "stop")
	}
	return plan.Cmd("Stop "+service, "systemctl", true, "stop", service)
}

func validationCommand(web string) string {
	if web == "nginx" {
		return "nginx"
	}
	if web == "apache" {
		return "apachectl"
	}
	return "/usr/local/lsws/bin/openlitespeed"
}
func validationArgs(web string) []string {
	if web == "openlitespeed" {
		return []string{"-t"}
	}
	return []string{"-t"}
}
func postgresDataDir(p platform.Platform) string {
	if p.Family == "debian" {
		return "/var/lib/postgresql/data"
	}
	return "/var/lib/pgsql/data"
}
func databaseDataDir(d config.Database, p platform.Platform) string {
	if d.DataDir != "" {
		return d.DataDir
	}
	return postgresDataDir(p)
}
func anyWordPress(c config.Config) bool {
	for _, s := range c.Sites {
		if s.WordPress != nil {
			return true
		}
	}
	return false
}
func wordpressInitializationAllowed(c config.Config) bool {
	if c.Database == nil {
		return true
	}
	if c.Database.Role == "replica" {
		return false
	}
	return !isManagedMariaDBInstance(*c.Database)
}
func hasSFTPOnly(c config.Config) bool {
	for _, u := range c.Access.Users {
		if u.SFTPOnly {
			return true
		}
	}
	return false
}
func hasPHPSite(c config.Config) bool {
	for _, s := range c.Sites {
		if s.Runtime == "php" {
			return true
		}
	}
	return false
}
func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func shellQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\\''") + "'" }
