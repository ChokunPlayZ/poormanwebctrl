package provider

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/plan"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

func phpPackages(p platform.Platform, web string, database *config.Database) []string {
	if web == "openlitespeed" {
		// LiteSpeed's Debian/Ubuntu builds bundle GD, mbstring, XML, and ZIP
		// into lsphpXX-common; unlike the RPM repository, apt does not publish
		// separate packages for those extensions.
		packages := []string{"lsphp84", "lsphp84-common", "lsphp84-curl"}
		if p.Family == "rhel" {
			packages = append(packages, "lsphp84-gd", "lsphp84-mbstring", "lsphp84-xml", "lsphp84-zip")
		}
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
	managedUsers := make(map[string]bool, len(c.Access.Users))
	for _, u := range c.Access.Users {
		managedUsers[u.Name] = true
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
	verifiedUsers := map[string]bool{}
	for _, site := range c.Sites {
		owner := site.Owner
		if owner == "" || managedUsers[owner] || verifiedUsers[owner] {
			continue
		}
		pn.Add(plan.Cmd("Verify existing system user "+owner, "id", false, "-u", owner))
		verifiedUsers[owner] = true
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
		if len(d.Databases) > 0 || len(d.Users) > 0 || len(d.ManagedPermissions()) > 0 {
			addDatabaseObjects(pn, *d)
		}
		return
	}
	if !(d.Provider == "postgresql" && d.Role == "replica") && !isLocalMariaDBReplica(*d) {
		pn.Add(enableService(p, service))
	}
	if d.Role != "replica" {
		addDatabaseObjects(pn, *d)
	}
	if d.Role != "standalone" {
		addReplication(pn, *d, p)
	}
	if d.Role == "replica" {
		// MariaDB can have explicitly local accounts on a replica. PostgreSQL
		// hot standbys cannot write role catalogs, and all ordinary accounts on
		// either provider arrive through replication.
		addDatabaseUsers(pn, *d)
	}
}

func addDatabaseObjects(pn *plan.Plan, d config.Database) {
	databases := d.ManagedDatabases()
	users := d.ManagedUsers()
	if len(databases) == 0 && len(users) == 0 && len(d.Permissions) == 0 {
		return
	}
	addDatabaseUsers(pn, d)
	if d.Provider == "postgresql" {
		for _, database := range databases {
			var sql strings.Builder
			owner := database.Owner
			if owner == "" {
				owner = d.User
			}
			create := fmt.Sprintf("SELECT 'CREATE DATABASE %s' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '%s')\\gexec\n", quotePostgres(database.Name), database.Name)
			if owner != "" {
				create = fmt.Sprintf("SELECT 'CREATE DATABASE %s OWNER %s' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '%s')\\gexec\nALTER DATABASE %s OWNER TO %s;\n", quotePostgres(database.Name), quotePostgres(owner), database.Name, quotePostgres(database.Name), quotePostgres(owner))
			}
			sql.WriteString(create)
			step := plan.AsUser("Create PostgreSQL database "+database.Name, "postgres", "psql", "-v", "ON_ERROR_STOP=1")
			step.Input = sql.String()
			pn.Add(step)
			addPostgresTables(pn, database)
		}
		addPostgresPermissions(pn, d.ManagedPermissions())
		return
	}
	hosts := map[string]string{}
	for _, user := range users {
		host := user.Host
		if host == "" {
			host = "localhost"
		}
		hosts[user.Name] = host
	}
	for _, database := range databases {
		var sql strings.Builder
		charset := ""
		if database.Charset != "" {
			charset = " CHARACTER SET " + database.Charset
		}
		collation := ""
		if database.Collation != "" {
			collation = " COLLATE " + database.Collation
		}
		fmt.Fprintf(&sql, "CREATE DATABASE IF NOT EXISTS %s%s%s;\n", quoteMaria(database.Name), charset, collation)
		if database.Owner != "" {
			host := hosts[database.Owner]
			if host == "" {
				host = "localhost"
			}
			fmt.Fprintf(&sql, "GRANT ALL PRIVILEGES ON %s.* TO '%s'@'%s';\n", quoteMaria(database.Name), database.Owner, host)
		}
		for _, table := range database.Tables {
			writeMariaTable(&sql, database.Name, table)
		}
		sql.WriteString("FLUSH PRIVILEGES;\n")
		step := plan.Cmd("Create MariaDB database "+database.Name+" and tables", "mariadb", true, mariaDBCommandArgs(d, "--batch", "--skip-column-names", "--connect-timeout=10")...)
		step.Input, step.Sensitive, step.SQLSecrets = sql.String(), true, true
		step.TimeoutSeconds = 60
		pn.Add(step)
	}
	addMariaDBPermissions(pn, d.ManagedPermissions(), users, d)
}

func addDatabaseUsers(pn *plan.Plan, d config.Database) {
	users := d.ManagedUsers()
	if d.Role == "replica" {
		users = nil
		for _, user := range d.Users {
			if user.Local {
				users = append(users, user)
			}
		}
	}
	if len(users) == 0 {
		return
	}
	if d.Role == "replica" && d.Provider == "postgresql" {
		return
	}
	if d.Provider == "postgresql" {
		var sql strings.Builder
		for _, user := range users {
			fmt.Fprintf(&sql, "DO $poorman$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s') THEN CREATE ROLE %s LOGIN PASSWORD '${%s}'; ELSE ALTER ROLE %s LOGIN PASSWORD '${%s}'; END IF; END $poorman$;\n", user.Name, quotePostgres(user.Name), user.PasswordEnv, quotePostgres(user.Name), user.PasswordEnv)
		}
		description := "Create PostgreSQL database users"
		if len(d.ManagedDatabases()) == 1 && len(users) == 1 && d.Name != "" && d.User != "" {
			description = "Create PostgreSQL application database and user"
		}
		step := plan.AsUser(description, "postgres", "psql", "-v", "ON_ERROR_STOP=1")
		step.Input, step.Sensitive, step.SQLSecrets = sql.String(), true, true
		pn.Add(step)
		return
	}
	for _, user := range users {
		host := user.Host
		if host == "" {
			host = "localhost"
		}
		input := ""
		if d.Role == "replica" {
			// Keep this account local even if the replica later becomes an
			// upstream in a chained GTID topology.
			input = "SET SESSION sql_log_bin=0;\n"
		}
		input += fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'%s' IDENTIFIED BY '${%s}';\nALTER USER '%s'@'%s' IDENTIFIED BY '${%s}';\n", user.Name, host, user.PasswordEnv, user.Name, host, user.PasswordEnv)
		step := plan.Cmd("Create MariaDB database user "+user.Name, "mariadb", true, mariaDBCommandArgs(d, "--batch", "--skip-column-names", "--connect-timeout=10")...)
		step.Input, step.Sensitive, step.SQLSecrets = input, true, true
		step.TimeoutSeconds = 60
		pn.Add(step)
	}
}

func mariaDBCommandArgs(d config.Database, args ...string) []string {
	if isManagedMariaDBInstance(d) {
		prefix := []string{"--protocol=socket", "--socket=" + mariaDBReplicaLayout(d).Socket}
		return append(prefix, args...)
	}
	return args
}

func addPostgresTables(pn *plan.Plan, database config.DatabaseSpec) {
	for _, table := range database.Tables {
		schema := table.Schema
		if schema == "" {
			schema = "public"
		}
		var sql strings.Builder
		fmt.Fprintf(&sql, "CREATE SCHEMA IF NOT EXISTS %s;\nCREATE TABLE IF NOT EXISTS %s.%s (", quotePostgres(schema), quotePostgres(schema), quotePostgres(table.Name))
		for i, column := range table.Columns {
			if i > 0 {
				sql.WriteString(", ")
			}
			fmt.Fprintf(&sql, "%s %s", quotePostgres(column.Name), strings.TrimSpace(column.Type))
			if !column.Nullable {
				sql.WriteString(" NOT NULL")
			}
			if column.DefaultExpr != "" {
				fmt.Fprintf(&sql, " DEFAULT %s", column.DefaultExpr)
			}
		}
		if len(table.PrimaryKey) > 0 {
			sql.WriteString(", PRIMARY KEY (")
			for i, key := range table.PrimaryKey {
				if i > 0 {
					sql.WriteString(", ")
				}
				sql.WriteString(quotePostgres(key))
			}
			sql.WriteString(")")
		}
		sql.WriteString(");\n")
		step := plan.AsUser("Create PostgreSQL table "+schema+"."+table.Name, "postgres", "psql", "-v", "ON_ERROR_STOP=1", "-d", database.Name)
		step.Input = sql.String()
		pn.Add(step)
	}
}

func addPostgresPermissions(pn *plan.Plan, permissions []config.DatabasePermission) {
	for _, permission := range permissions {
		privileges := normalizedPrivileges(permission.Privileges)
		target := "DATABASE " + quotePostgres(permission.Database)
		if permission.Schema != "" {
			target = "SCHEMA " + quotePostgres(permission.Schema)
		}
		if permission.Table != "" {
			schema := permission.Schema
			if schema == "" {
				schema = "public"
			}
			target = "TABLE " + quotePostgres(schema) + "." + quotePostgres(permission.Table)
		}
		grantOption := ""
		if permission.GrantOption {
			grantOption = " WITH GRANT OPTION"
		}
		input := fmt.Sprintf("GRANT %s ON %s TO %s%s;\n", privileges, target, quotePostgres(permission.User), grantOption)
		step := plan.AsUser("Grant PostgreSQL permissions to "+permission.User, "postgres", "psql", "-v", "ON_ERROR_STOP=1", "-d", permission.Database)
		step.Input = input
		pn.Add(step)
	}
}

func writeMariaTable(sql *strings.Builder, database string, table config.DatabaseTable) {
	fmt.Fprintf(sql, "CREATE TABLE IF NOT EXISTS %s.%s (", quoteMaria(database), quoteMaria(table.Name))
	for i, column := range table.Columns {
		if i > 0 {
			sql.WriteString(", ")
		}
		fmt.Fprintf(sql, "%s %s", quoteMaria(column.Name), strings.TrimSpace(column.Type))
		if !column.Nullable {
			sql.WriteString(" NOT NULL")
		}
		if column.DefaultExpr != "" {
			fmt.Fprintf(sql, " DEFAULT %s", column.DefaultExpr)
		}
	}
	if len(table.PrimaryKey) > 0 {
		sql.WriteString(", PRIMARY KEY (")
		for i, key := range table.PrimaryKey {
			if i > 0 {
				sql.WriteString(", ")
			}
			sql.WriteString(quoteMaria(key))
		}
		sql.WriteString(")")
	}
	sql.WriteString(");\n")
}

func addMariaDBPermissions(pn *plan.Plan, permissions []config.DatabasePermission, users []config.DatabaseUser, database config.Database) {
	hosts := map[string]string{}
	for _, user := range users {
		host := user.Host
		if host == "" {
			host = "localhost"
		}
		hosts[user.Name] = host
	}
	for _, permission := range permissions {
		host := hosts[permission.User]
		if host == "" {
			host = "localhost"
		}
		target := quoteMaria(permission.Database) + ".*"
		if permission.Table != "" {
			target = quoteMaria(permission.Database) + "." + quoteMaria(permission.Table)
		}
		grantOption := ""
		if permission.GrantOption {
			grantOption = " WITH GRANT OPTION"
		}
		input := fmt.Sprintf("GRANT %s ON %s TO '%s'@'%s'%s;\nFLUSH PRIVILEGES;\n", normalizedPrivileges(permission.Privileges), target, permission.User, host, grantOption)
		step := plan.Cmd("Grant MariaDB permissions to "+permission.User, "mariadb", true, mariaDBCommandArgs(database, "--batch", "--skip-column-names", "--connect-timeout=10")...)
		step.Input, step.Sensitive, step.SQLSecrets = input, true, true
		step.TimeoutSeconds = 60
		pn.Add(step)
	}
}

func normalizedPrivileges(privileges []string) string {
	values := make([]string, 0, len(privileges))
	for _, privilege := range privileges {
		values = append(values, strings.ToUpper(strings.TrimSpace(privilege)))
	}
	return strings.Join(values, ", ")
}

func quotePostgres(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
func quoteMaria(value string) string    { return "`" + strings.ReplaceAll(value, "`", "``") + "`" }
