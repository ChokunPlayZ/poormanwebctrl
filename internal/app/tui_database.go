package app

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
)

func databaseManagementTUI(path string, reader *bufio.Reader, ui *terminalUI) error {
	for {
		c, err := config.Load(path)
		if err != nil {
			return err
		}
		if c.Database == nil {
			return fmt.Errorf("database is not configured")
		}
		d := c.Database
		ensureDeclarativeDatabase(d)
		ui.clear()
		ui.brand("Database management", "Manage databases, database users, tables, and permissions")
		if d.Role == "replica" {
			ui.panel("REPLICA", "This database is read-only. Schema, users, and grants are sourced from the primary through replication.")
		}
		databases := d.ManagedDatabases()
		options := []string{"Create new database"}
		for _, database := range databases {
			options = append(options, database.Name)
		}
		options = append(options, "Manage database users")
		options = append(options, "0")
		choice := selectOption(reader, ui, "Database", options[0], options...)
		switch {
		case choice == "Create new database":
			if d.Role == "replica" {
				ui.warn("Edit the primary configuration; replicas do not accept database writes.")
				pause(reader, ui)
				continue
			}
			if err := createManagedDatabase(path, c, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
			}
		case choice == "Manage database users":
			if err := databaseUsersTUI(path, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
			}
		case choice == "0":
			return nil
		default:
			if err := selectedDatabaseTUI(path, choice, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
			}
		}
	}
}

func createManagedDatabase(path string, c config.Config, reader *bufio.Reader, ui *terminalUI) error {
	d := c.Database
	if d == nil {
		return fmt.Errorf("database is not configured")
	}
	ensureDeclarativeDatabase(d)
	name := prompt(reader, ui, "New database name", "app")
	owner := ""
	createUserChoice := selectOption(reader, ui, "Create a new database user for this database?", "n", "y", "n", "0")
	if createUserChoice == "0" {
		return nil
	}
	if yesNo(createUserChoice) {
		user := config.DatabaseUser{
			Name:        prompt(reader, ui, "New database username", name+"_user"),
			PasswordEnv: prompt(reader, ui, "Password environment variable", "POORMAN_DB_PASSWORD"),
		}
		if d.Provider == "mariadb" {
			user.Host = selectOption(reader, ui, "MariaDB user host", "localhost", "localhost", "%")
		}
		d.Users = append(d.Users, user)
		owner = user.Name
	}
	d.Databases = append(d.Databases, config.DatabaseSpec{Name: name, Owner: owner})
	if err := config.Write(path, c); err != nil {
		return err
	}
	ui.success("Database created")
	return nil
}

func databaseUsersTUI(path string, reader *bufio.Reader, ui *terminalUI) error {
	for {
		c, err := config.Load(path)
		if err != nil {
			return err
		}
		if c.Database == nil {
			return fmt.Errorf("database is not configured")
		}
		d := c.Database
		ensureDeclarativeDatabase(d)
		users := d.ManagedUsers()
		mutableUsers := users
		if d.Role == "replica" {
			mutableUsers = localDatabaseUsers(users)
		}

		ui.clear()
		ui.brand("Database users", "Manage database accounts and their credentials")
		if d.Role == "replica" {
			ui.panel("REPLICA", "This database is read-only. Manage users on the primary; this view reflects replicated accounts.")
		}
		if len(users) == 0 {
			ui.muted("No database users configured.")
		} else {
			for _, user := range users {
				host := defaultValue(user.Host, "localhost")
				scope := ""
				if user.Local {
					scope = "  local replica account"
				}
				fmt.Fprintf(ui, "%-24s %-16s %s%s\n", user.Name, host, defaultValue(user.PasswordEnv, "password not configured"), scope)
			}
		}
		ui.panel("ACTIONS", "1  create database user\n2  edit database user\n3  remove database user\n0  back")
		choice := selectMenu(reader, ui, "Database users", "1",
			selectorChoice{Value: "1", Label: "create database user"},
			selectorChoice{Value: "2", Label: "edit database user"},
			selectorChoice{Value: "3", Label: "remove database user"},
			selectorChoice{Value: "0", Label: "back"},
		)
		switch choice {
		case "1":
			if d.Role == "replica" {
				if d.Provider != "mariadb" {
					ui.warn("PostgreSQL hot standbys cannot create local users; create the user on the primary and let replication carry it.")
					pause(reader, ui)
					continue
				}
				if !yesNo(selectOption(reader, ui, "Create this database user locally on the replica?", "n", "y", "n")) {
					continue
				}
			}
			if err := createDatabaseUser(d, d.Role == "replica", reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
				continue
			}
			if err := config.Write(path, c); err != nil {
				return err
			}
			if d.Role == "replica" {
				ui.success("Database user created locally on replica")
			} else {
				ui.success("Database user created")
			}
		case "2":
			if len(mutableUsers) == 0 {
				ui.warn("No editable database users exist here; manage replicated users on the primary.")
				pause(reader, ui)
				continue
			}
			if err := editDatabaseUser(d, mutableUsers, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
				continue
			}
			if err := config.Write(path, c); err != nil {
				return err
			}
			if d.Role == "replica" {
				ui.success("Local replica user definition updated")
			} else {
				ui.success("Database user updated")
			}
		case "3":
			if len(mutableUsers) == 0 {
				ui.warn("No removable database users exist here; manage replicated users on the primary.")
				pause(reader, ui)
				continue
			}
			if err := removeDatabaseUser(d, mutableUsers, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
				continue
			}
			if err := config.Write(path, c); err != nil {
				return err
			}
			ui.success("Database user and its ACL rules removed from configuration; the live account was kept")
		case "0", "q", "Q":
			return nil
		}
	}
}

func localDatabaseUsers(users []config.DatabaseUser) []config.DatabaseUser {
	local := make([]config.DatabaseUser, 0, len(users))
	for _, user := range users {
		if user.Local {
			local = append(local, user)
		}
	}
	return local
}

func createDatabaseUser(d *config.Database, local bool, reader *bufio.Reader, ui *terminalUI) error {
	name := prompt(reader, ui, "New database username", "app_user")
	for _, user := range d.ManagedUsers() {
		if user.Name == name {
			return fmt.Errorf("database user %q already exists", name)
		}
	}
	user := config.DatabaseUser{
		Name:        name,
		PasswordEnv: prompt(reader, ui, "Password environment variable", "POORMAN_DB_PASSWORD"),
		Local:       local,
	}
	if d.Provider == "mariadb" {
		user.Host = selectOption(reader, ui, "MariaDB user host", "localhost", "localhost", "%")
	}
	d.Users = append(d.Users, user)
	return nil
}

func editDatabaseUser(d *config.Database, users []config.DatabaseUser, reader *bufio.Reader, ui *terminalUI) error {
	options := make([]string, 0, len(users)+1)
	for _, user := range users {
		options = append(options, user.Name)
	}
	options = append(options, "0")
	name := selectOption(reader, ui, "Database user", options[0], options...)
	if name == "0" {
		return nil
	}
	for i := range d.Users {
		if d.Users[i].Name != name {
			continue
		}
		d.Users[i].PasswordEnv = prompt(reader, ui, "Password environment variable", d.Users[i].PasswordEnv)
		if d.Provider == "mariadb" {
			d.Users[i].Host = selectOption(reader, ui, "MariaDB user host", defaultValue(d.Users[i].Host, "localhost"), "localhost", "%")
		}
		return nil
	}
	return fmt.Errorf("database user %q is not explicitly managed", name)
}

func removeDatabaseUser(d *config.Database, users []config.DatabaseUser, reader *bufio.Reader, ui *terminalUI) error {
	options := make([]string, 0, len(users)+1)
	for _, user := range users {
		options = append(options, user.Name)
	}
	options = append(options, "0")
	name := selectOption(reader, ui, "Database user", options[0], options...)
	if name == "0" {
		return nil
	}
	owned := 0
	for _, database := range d.Databases {
		if database.Owner == name {
			owned++
		}
	}
	confirmation := "Remove database user " + name + " from the configuration? The live account will be kept."
	if owned > 0 {
		confirmation = fmt.Sprintf("Remove database user %s and clear it as owner of %d database(s)? The live account will be kept.", name, owned)
	}
	if !yesNo(selectOption(reader, ui, confirmation, "n", "y", "n")) {
		return nil
	}
	filtered := d.Users[:0]
	for _, user := range d.Users {
		if user.Name != name {
			filtered = append(filtered, user)
		}
	}
	d.Users = filtered
	for i := range d.Databases {
		if d.Databases[i].Owner == name {
			d.Databases[i].Owner = ""
		}
	}
	if d.User == name {
		// ensureDeclarativeDatabase has already materialized the shorthand
		// database and user. Clear all three coupled legacy fields so the user
		// is not synthesized again by ManagedUsers on the next reload.
		d.Name = ""
		d.User = ""
		d.PasswordEnv = ""
	}
	d.Permissions = filterDatabasePermissionsByUser(d.Permissions, name)
	d.ACL = filterDatabasePermissionsByUser(d.ACL, name)
	return nil
}

func filterDatabasePermissionsByUser(permissions []config.DatabasePermission, user string) []config.DatabasePermission {
	filtered := permissions[:0]
	for _, permission := range permissions {
		if permission.User != user {
			filtered = append(filtered, permission)
		}
	}
	return filtered
}

func selectedDatabaseTUI(path, databaseName string, reader *bufio.Reader, ui *terminalUI) error {
	for {
		c, err := config.Load(path)
		if err != nil {
			return err
		}
		if c.Database == nil {
			return fmt.Errorf("database is not configured")
		}
		ensureDeclarativeDatabase(c.Database)
		index := databaseIndex(*c.Database, databaseName)
		if index < 0 {
			return fmt.Errorf("database %q is no longer configured", databaseName)
		}
		database := &c.Database.Databases[index]
		ui.clear()
		ui.brand("Database / "+database.Name, "Manage this database, its tables, and access control")
		ui.panel("DATABASE", fmt.Sprintf("provider  %s (%s)\nowner     %s\ntables    %d\nacl rules %d", c.Database.Provider, defaultValue(c.Database.Role, "standalone"), defaultValue(database.Owner, "none"), len(database.Tables), countDatabasePermissions(*c.Database, database.Name)))
		if c.Database.Role == "replica" {
			ui.panel("REPLICA", "This database is read-only. Manage tables and permissions on the primary.")
		}
		ui.panel("ACTIONS", "1  create table\n2  remove table definition\n3  set user permissions\n4  view current ACLs\n5  remove ACL rule\n6  delete database\n0  back")
		choice := selectMenu(reader, ui, "Database / "+database.Name, "1",
			selectorChoice{Value: "1", Label: "create table"},
			selectorChoice{Value: "2", Label: "remove table definition"},
			selectorChoice{Value: "3", Label: "set user permissions"},
			selectorChoice{Value: "4", Label: "view current ACLs"},
			selectorChoice{Value: "5", Label: "remove ACL rule"},
			selectorChoice{Value: "6", Label: "delete database"},
			selectorChoice{Value: "0", Label: "back"},
		)
		switch choice {
		case "1":
			if c.Database.Role == "replica" {
				ui.warn("Replicas are read-only; create the table on the primary.")
				pause(reader, ui)
				continue
			}
			if err := createTableForDatabase(path, c, index, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
			}
		case "2":
			if c.Database.Role == "replica" {
				ui.warn("Replicas are read-only; remove the table definition on the primary.")
				pause(reader, ui)
				continue
			}
			if err := removeTableDefinition(path, c, index, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
			}
		case "3":
			if c.Database.Role == "replica" {
				ui.warn("Replicas are read-only; set permissions on the primary.")
				pause(reader, ui)
				continue
			}
			if err := setDatabasePermission(path, c, index, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
			}
		case "4":
			showDatabaseACL(ui, *c.Database, database.Name)
			pause(reader, ui)
		case "5":
			if c.Database.Role == "replica" {
				ui.warn("Replicas are read-only; remove the ACL rule on the primary.")
				pause(reader, ui)
				continue
			}
			if err := removeDatabasePermission(path, c, database.Name, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
			}
		case "6":
			if c.Database.Role == "replica" {
				ui.warn("Replicas are read-only; delete the database on the primary.")
				pause(reader, ui)
				continue
			}
			deleted, err := deleteDatabase(path, c, index, reader, ui)
			if err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
				continue
			}
			if deleted {
				return nil
			}
		case "0":
			return nil
		}
	}
}

func deleteDatabase(path string, c config.Config, index int, reader *bufio.Reader, ui *terminalUI) (bool, error) {
	d := c.Database
	if d == nil || index < 0 || index >= len(d.Databases) {
		return false, fmt.Errorf("database is not configured")
	}
	name := d.Databases[index].Name
	if !yesNo(selectOption(reader, ui, "Delete database "+name+" and all its tables and ACLs?", "n", "y", "n")) {
		return false, nil
	}
	d.Databases = append(d.Databases[:index], d.Databases[index+1:]...)
	if d.Name == name {
		// The legacy shorthand is materialized into Databases by
		// ensureDeclarativeDatabase. Clear it too, or the deleted database
		// would reappear on the next reload.
		d.Name = ""
		d.User = ""
		d.PasswordEnv = ""
	}
	d.Permissions = filterDatabasePermissionsByDatabase(d.Permissions, name)
	d.ACL = filterDatabasePermissionsByDatabase(d.ACL, name)
	if err := config.Write(path, c); err != nil {
		return false, err
	}
	ui.success("Database removed from configuration; its users and live data were kept")
	return true, nil
}

func filterDatabasePermissionsByDatabase(permissions []config.DatabasePermission, database string) []config.DatabasePermission {
	filtered := permissions[:0]
	for _, permission := range permissions {
		if permission.Database != database {
			filtered = append(filtered, permission)
		}
	}
	return filtered
}

func databaseIndex(d config.Database, name string) int {
	for i, database := range d.ManagedDatabases() {
		if database.Name == name {
			return i
		}
	}
	return -1
}

func createTableForDatabase(path string, c config.Config, index int, reader *bufio.Reader, ui *terminalUI) error {
	d := c.Database
	if d == nil || index < 0 || index >= len(d.Databases) {
		return fmt.Errorf("database is not configured")
	}
	table := config.DatabaseTable{Name: prompt(reader, ui, "New table name", "items")}
	if d.Provider == "postgresql" {
		table.Schema = prompt(reader, ui, "Schema", "public")
	}
	columns, err := parseDatabaseColumns(prompt(reader, ui, "Columns name:TYPE (comma-separated)", "id:BIGINT"))
	if err != nil {
		return err
	}
	table.Columns = columns
	table.PrimaryKey = parseAliases(prompt(reader, ui, "Primary key columns (comma-separated)", ""))
	d.Databases[index].Tables = append(d.Databases[index].Tables, table)
	if err := config.Write(path, c); err != nil {
		return err
	}
	ui.success("Table created in " + d.Databases[index].Name)
	return nil
}

func removeTableDefinition(path string, c config.Config, index int, reader *bufio.Reader, ui *terminalUI) error {
	d := c.Database
	if d == nil || index < 0 || index >= len(d.Databases) {
		return fmt.Errorf("database is not configured")
	}
	database := &d.Databases[index]
	if len(database.Tables) == 0 {
		return fmt.Errorf("database %q has no managed tables", database.Name)
	}
	options := make([]string, 0, len(database.Tables)+1)
	for _, table := range database.Tables {
		name := table.Name
		if d.Provider == "postgresql" {
			name = defaultValue(table.Schema, "public") + "." + table.Name
		}
		options = append(options, name)
	}
	options = append(options, "0")
	choice := selectOption(reader, ui, "Table definition", options[0], options...)
	if choice == "0" {
		return nil
	}
	tableIndex := -1
	for i, table := range database.Tables {
		name := table.Name
		if d.Provider == "postgresql" {
			name = defaultValue(table.Schema, "public") + "." + table.Name
		}
		if name == choice {
			tableIndex = i
			break
		}
	}
	if tableIndex < 0 {
		return fmt.Errorf("table definition %q was not found", choice)
	}
	table := database.Tables[tableIndex]
	if !yesNo(selectOption(reader, ui, "Remove "+choice+" from the configuration? The live table will be kept.", "n", "y", "n")) {
		return nil
	}
	database.Tables = append(database.Tables[:tableIndex], database.Tables[tableIndex+1:]...)
	d.Permissions = filterDatabasePermissionsByTable(d.Permissions, database.Name, table)
	d.ACL = filterDatabasePermissionsByTable(d.ACL, database.Name, table)
	if err := config.Write(path, c); err != nil {
		return err
	}
	ui.success("Table definition and its ACL rules removed; the live table was kept")
	return nil
}

func filterDatabasePermissionsByTable(permissions []config.DatabasePermission, database string, table config.DatabaseTable) []config.DatabasePermission {
	filtered := permissions[:0]
	schema := defaultValue(table.Schema, "public")
	for _, permission := range permissions {
		permissionSchema := defaultValue(permission.Schema, "public")
		if permission.Database != database || permission.Table != table.Name || permissionSchema != schema {
			filtered = append(filtered, permission)
		}
	}
	return filtered
}

func setDatabasePermission(path string, c config.Config, index int, reader *bufio.Reader, ui *terminalUI) error {
	d := c.Database
	if d == nil || index < 0 || index >= len(d.Databases) {
		return fmt.Errorf("database is not configured")
	}
	users := d.ManagedUsers()
	if len(users) == 0 {
		return fmt.Errorf("no database users exist; create one before setting ACLs")
	}
	userOptions := make([]string, 0, len(users)+1)
	for _, user := range users {
		userOptions = append(userOptions, user.Name)
	}
	userOptions = append(userOptions, "0")
	user := selectOption(reader, ui, "Existing database user", userOptions[0], userOptions...)
	if user == "0" {
		return nil
	}
	database := d.Databases[index]
	scopeOptions := []string{"database-wide", "schema-wide"}
	for _, table := range database.Tables {
		scopeOptions = append(scopeOptions, "table: "+table.Name)
	}
	scopeOptions = append(scopeOptions, "0")
	scope := selectOption(reader, ui, "Permission scope", scopeOptions[0], scopeOptions...)
	if scope == "0" {
		return nil
	}
	permission := config.DatabasePermission{User: user, Database: database.Name}
	if scope == "schema-wide" {
		if d.Provider == "postgresql" {
			permission.Schema = prompt(reader, ui, "Schema", "public")
		}
	} else if strings.HasPrefix(scope, "table: ") {
		permission.Table = strings.TrimPrefix(scope, "table: ")
		if d.Provider == "postgresql" {
			for _, table := range database.Tables {
				if table.Name == permission.Table {
					permission.Schema = defaultValue(table.Schema, "public")
					break
				}
			}
		}
	}
	privileges := selectOption(reader, ui, "Privileges", "SELECT", "SELECT", "SELECT,INSERT,UPDATE,DELETE", "ALL", "0")
	if privileges == "0" {
		return nil
	}
	permission.Privileges = parseAliases(privileges)
	permission.GrantOption = yesNo(selectOption(reader, ui, "Allow this user to grant onward?", "n", "y", "n"))
	upsertDatabasePermission(d, permission)
	if err := config.Write(path, c); err != nil {
		return err
	}
	ui.success("Permissions updated for " + user)
	return nil
}

func upsertDatabasePermission(d *config.Database, permission config.DatabasePermission) {
	for i, existing := range d.Permissions {
		if existing.User == permission.User && existing.Database == permission.Database && existing.Schema == permission.Schema && existing.Table == permission.Table {
			d.Permissions[i] = permission
			return
		}
	}
	for i, existing := range d.ACL {
		if existing.User == permission.User && existing.Database == permission.Database && existing.Schema == permission.Schema && existing.Table == permission.Table {
			d.ACL[i] = permission
			return
		}
	}
	d.Permissions = append(d.Permissions, permission)
}

type databasePermissionRef struct {
	acl        bool
	index      int
	permission config.DatabasePermission
}

func removeDatabasePermission(path string, c config.Config, database string, reader *bufio.Reader, ui *terminalUI) error {
	d := c.Database
	if d == nil {
		return fmt.Errorf("database is not configured")
	}
	var refs []databasePermissionRef
	for i, permission := range d.Permissions {
		if permission.Database == database {
			refs = append(refs, databasePermissionRef{index: i, permission: permission})
		}
	}
	for i, permission := range d.ACL {
		if permission.Database == database {
			refs = append(refs, databasePermissionRef{acl: true, index: i, permission: permission})
		}
	}
	if len(refs) == 0 {
		return fmt.Errorf("database %q has no ACL rules", database)
	}
	options := make([]string, 0, len(refs)+1)
	for i, ref := range refs {
		options = append(options, fmt.Sprintf("%d  %s  %s  %s", i+1, ref.permission.User, databasePermissionScope(ref.permission), strings.Join(ref.permission.Privileges, ",")))
	}
	options = append(options, "0")
	choice := selectOption(reader, ui, "ACL rule", options[0], options...)
	if choice == "0" {
		return nil
	}
	selected := -1
	for i, option := range options[:len(options)-1] {
		if option == choice {
			selected = i
			break
		}
	}
	if selected < 0 {
		return fmt.Errorf("ACL rule was not found")
	}
	if !yesNo(selectOption(reader, ui, "Remove this ACL rule from the configuration? Existing live grants will be kept.", "n", "y", "n")) {
		return nil
	}
	ref := refs[selected]
	if ref.acl {
		d.ACL = append(d.ACL[:ref.index], d.ACL[ref.index+1:]...)
	} else {
		d.Permissions = append(d.Permissions[:ref.index], d.Permissions[ref.index+1:]...)
	}
	if err := config.Write(path, c); err != nil {
		return err
	}
	ui.success("ACL rule removed from configuration; existing live grants were kept")
	return nil
}

func databasePermissionScope(permission config.DatabasePermission) string {
	if permission.Table != "" {
		if permission.Schema != "" {
			return permission.Schema + "." + permission.Table
		}
		return permission.Table
	}
	if permission.Schema != "" {
		return permission.Schema + ".*"
	}
	return "database-wide"
}

func countDatabasePermissions(d config.Database, database string) int {
	count := 0
	for _, permission := range d.ManagedPermissions() {
		if permission.Database == database {
			count++
		}
	}
	return count
}

func showDatabaseACL(ui *terminalUI, d config.Database, database string) {
	ui.clear()
	ui.brand("ACL / "+database, "Current declarative permissions for this database")
	found := false
	for _, permission := range d.ManagedPermissions() {
		if permission.Database != database {
			continue
		}
		found = true
		scope := "database-wide"
		if permission.Table != "" {
			scope = "table " + permission.Table
		} else if permission.Schema != "" {
			scope = "schema " + permission.Schema
		}
		grant := ""
		if permission.GrantOption {
			grant = "  grant-option"
		}
		fmt.Fprintf(ui, "%-20s %-24s %s%s\n", permission.User, scope, strings.Join(permission.Privileges, ", "), grant)
	}
	if !found {
		ui.muted("No permissions configured for this database.")
	}
}

func ensureDeclarativeDatabase(d *config.Database) {
	if len(d.Databases) == 0 && d.Name != "" {
		d.Databases = append(d.Databases, config.DatabaseSpec{Name: d.Name, Owner: d.User})
	}
	if len(d.Users) == 0 && d.User != "" {
		d.Users = append(d.Users, config.DatabaseUser{Name: d.User, PasswordEnv: d.PasswordEnv, Host: "localhost"})
	}
}

func parseDatabaseColumns(value string) ([]config.DatabaseColumn, error) {
	var columns []config.DatabaseColumn
	for _, item := range strings.Split(value, ",") {
		parts := strings.SplitN(strings.TrimSpace(item), ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("columns must use name:TYPE format")
		}
		columns = append(columns, config.DatabaseColumn{Name: strings.TrimSpace(parts[0]), Type: strings.TrimSpace(parts[1])})
	}
	return columns, nil
}
