package provider

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/plan"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

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
			input := fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '${%s}';\nALTER USER '%s'@'%%' IDENTIFIED BY '${%s}';\nGRANT REPLICATION SLAVE ON *.* TO '%s'@'%%';\nFLUSH PRIVILEGES;\n", r.User, r.PasswordEnv, r.User, r.PasswordEnv, r.User)
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
			pn.Add(plan.EnsureLineOwnedBy("Set PostgreSQL replica port", filepath.Join(dataDir, "postgresql.conf"), fmt.Sprintf("port = %d", d.Port), "postgres", "postgres", 0o600))
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
	wait := plan.Cmd("Wait for MariaDB replica socket", "mariadb-admin", true,
		"--protocol=socket", "--socket="+layout.Socket, "--connect-timeout=1", "--wait=1", "ping")
	wait.TimeoutSeconds = 60
	pn.Add(wait)

	dump := plan.Cmd("Seed MariaDB replica from local primary", "mariadb-dump", true,
		"--protocol=socket", "--all-databases", "--single-transaction", "--routines", "--events", "--triggers", "--flush-privileges", "--master-data=2", "--gtid", "--result-file="+layout.Seed)
	dump.UnlessCommand, dump.UnlessArgs = "test", []string{"-e", layout.SeedMarker}
	pn.Add(dump)
	load := plan.Cmd("Load primary snapshot into MariaDB replica", "mariadb", true, "--protocol=socket", "--socket="+layout.Socket)
	load.Input = fmt.Sprintf("SOURCE %s;\nFLUSH PRIVILEGES;\n", layout.Seed)
	load.TimeoutSeconds = 60
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
	input := fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '${%s}';\nALTER USER '%s'@'%%' IDENTIFIED BY '${%s}';\nGRANT REPLICATION SLAVE ON *.* TO '%s'@'%%';\nFLUSH PRIVILEGES;\n", d.Replication.User, d.Replication.PasswordEnv, d.Replication.User, d.Replication.PasswordEnv, d.Replication.User)
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
