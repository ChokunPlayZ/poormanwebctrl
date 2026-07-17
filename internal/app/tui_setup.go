package app

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
)

var errSetupCanceled = errors.New("setup canceled")

func tuiCommand(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("f", "poorman.json", "configuration file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	ui := newTerminalUI(out)
	attachTerminalInput(ui, in)
	if _, err := os.Stat(*path); err == nil {
		return tuiDashboard(ctx, *path, in, ui)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ui.confirmSetupCancel = true

	reader := bufio.NewReader(in)
	ui.clear()
	ui.brand("guided setup", "Build a safe, auditable server configuration")
	ui.panel("WEB SERVER", "1  nginx                 recommended\n2  apache\n3  openlitespeed")
	choice := selectOption(reader, ui, "Web server", "1", "nginx", "apache", "openlitespeed")
	providerName := "nginx"
	if choice == "apache" || choice == "2" {
		providerName = "apache"
	} else if choice == "openlitespeed" || choice == "3" {
		providerName = "openlitespeed"
	}

	domain := prompt(reader, ui, "First site domain", "example.com")
	root := prompt(reader, ui, "Document root", "/var/www/"+domain)
	owner := prompt(reader, ui, "System/SFTP user", "webadmin")
	runtimeName := selectOption(reader, ui, "Runtime", "php", "php", "static")
	dbChoice := selectOption(reader, ui, "Database", "mariadb", "mariadb", "postgresql", "none")
	var database *config.Database
	if dbChoice != "none" {
		database = &config.Database{Provider: dbChoice, Role: strings.ToLower(selectOption(reader, ui, "Database role", "standalone", "standalone", "primary", "replica")), Name: prompt(reader, ui, "Database name", "website"), User: prompt(reader, ui, "Database user", "website"), PasswordEnv: "POORMAN_DB_PASSWORD"}
		if err := configureReplication(reader, ui, database); err != nil {
			return err
		}
	}
	var wordpress *config.WordPress
	if runtimeName == "php" && database != nil && database.Role != "replica" && yesNo(selectOption(reader, ui, "Install WordPress?", "n", "y", "n")) {
		wordpress = &config.WordPress{Title: domain, AdminUser: "admin", AdminEmail: prompt(reader, ui, "WordPress admin email", "admin@"+domain), AdminPassEnv: "POORMAN_WP_ADMIN_PASSWORD"}
	}
	tlsEnabled := yesNo(selectOption(reader, ui, "Enable HTTPS with Let's Encrypt?", "y", "y", "n"))
	backupEnabled := yesNo(selectOption(reader, ui, "Enable nightly backups?", "y", "y", "n"))
	if ui.setupCanceled {
		ui.muted("Cancelled.")
		return nil
	}

	c := config.Config{
		Version:   1,
		WebServer: config.WebServer{Provider: providerName},
		Database:  database,
		Access:    config.Access{Users: []config.User{{Name: owner, Home: "/home/" + owner, SFTPOnly: true}}},
		Sites:     []config.Site{{Domain: domain, Root: root, Owner: owner, Runtime: runtimeName, TLS: &tlsEnabled, WordPress: wordpress}},
		TLS:       config.TLS{Email: "admin@" + domain},
		Firewall:  config.Firewall{Enabled: true},
		Backups:   config.Backup{Enabled: backupEnabled, Destination: "/var/backups/poorman", Schedule: "0 3 * * *", RetentionDays: 14},
	}
	if err := config.Write(*path, c); err != nil {
		return err
	}
	ui.success(fmt.Sprintf("Created %s", *path))
	ui.muted(fmt.Sprintf("Preview it on the target Linux server with: poorman plan -f %s", *path))
	return nil
}

func configureReplication(reader *bufio.Reader, ui *terminalUI, database *config.Database) error {
	if database.Role == "standalone" {
		return nil
	}
	database.Replication.User = prompt(reader, ui, "Replication user", defaultValue(database.Replication.User, "replicator"))
	database.Replication.PasswordEnv = prompt(reader, ui, "Replication password environment variable", defaultValue(database.Replication.PasswordEnv, "POORMAN_REPLICATION_PASSWORD"))
	if database.Role == "primary" {
		database.Replication.AllowedCIDR = prompt(reader, ui, "Replica allowed CIDR", defaultValue(database.Replication.AllowedCIDR, "10.20.0.0/24"))
		if database.Provider == "postgresql" {
			database.Replication.Slot = prompt(reader, ui, "PostgreSQL replication slot", defaultValue(database.Replication.Slot, "poorman_replica_1"))
		} else {
			nodeDefault := "1"
			if database.Replication.NodeID > 0 {
				nodeDefault = strconv.Itoa(database.Replication.NodeID)
			}
			nodeID := prompt(reader, ui, "MariaDB primary node ID", nodeDefault)
			database.Replication.NodeID, _ = strconv.Atoi(nodeID)
		}
		return nil
	}
	if database.Role == "replica" {
		local := yesNo(selectOption(reader, ui, "Is the primary on this machine?", "n", "y", "n"))
		primaryHostDefault := "10.20.0.10"
		if local {
			primaryHostDefault = "127.0.0.1"
		}
		database.Replication.PrimaryHost = prompt(reader, ui, "Primary database host", defaultValue(database.Replication.PrimaryHost, primaryHostDefault))
		primaryPort := 5432
		if database.Provider == "mariadb" {
			primaryPort = 3306
		}
		primaryPort = promptPort(reader, ui, "Primary database port", database.Replication.PrimaryPort, primaryPort)
		database.Replication.PrimaryPort = primaryPort
		if local {
			replicaPort := primaryPort + 1
			database.Port = promptPort(reader, ui, "Replica database port", database.Port, replicaPort)
			ui.muted("Same-machine replicas use separate ports so the primary and replica can run together.")
		}
		if database.Provider == "postgresql" {
			database.Replication.Slot = prompt(reader, ui, "PostgreSQL replication slot", defaultValue(database.Replication.Slot, "poorman_replica_1"))
			dataDirDefault := "/var/lib/postgresql/18/main"
			if local {
				dataDirDefault = "/var/lib/postgresql/poorman-replica"
			}
			database.DataDir = prompt(reader, ui, "PostgreSQL data directory", defaultValue(database.DataDir, dataDirDefault))
		} else {
			if local {
				dataDirDefault := fmt.Sprintf("/var/lib/mysql/poorman-replica-%d", database.Port)
				database.DataDir = prompt(reader, ui, "MariaDB replica data directory", defaultValue(database.DataDir, dataDirDefault))
				ui.muted("The replica gets its own MariaDB service, data directory, socket, PID, log, and port.")
			}
			nodeDefault := "2"
			if database.Replication.NodeID > 0 {
				nodeDefault = strconv.Itoa(database.Replication.NodeID)
			}
			nodeID := prompt(reader, ui, "MariaDB replica node ID", nodeDefault)
			database.Replication.NodeID, _ = strconv.Atoi(nodeID)
		}
	}
	return nil
}

func promptPort(reader *bufio.Reader, ui *terminalUI, label string, current, fallback int) int {
	if current == 0 {
		current = fallback
	}
	value, err := strconv.Atoi(prompt(reader, ui, label, strconv.Itoa(current)))
	if err != nil {
		return fallback
	}
	return value
}

func guidedLocalReplicaSetup(path string, in io.Reader, out io.Writer) error {
	c, err := config.Load(path)
	if err != nil {
		return err
	}
	if c.Database == nil {
		return fmt.Errorf("local replica setup requires a configured database")
	}
	if c.Database.Role == "replica" {
		return fmt.Errorf("this configuration already describes a replica; use the primary configuration for local replica setup")
	}
	ui, usingParentUI := out.(*terminalUI)
	if !usingParentUI {
		ui = newTerminalUI(out)
		attachTerminalInput(ui, in)
	}
	previousConfirm := ui.confirmSetupCancel
	ui.confirmSetupCancel = true
	defer func() { ui.confirmSetupCancel = previousConfirm }()
	reader := inputReader(in)
	ui.brand("guided local replica setup", "Add an independent replica process to this configuration")

	d := c.Database
	d.Role = "primary"
	d.Replication.User = prompt(reader, ui, "Replication user", defaultValue(d.Replication.User, "replicator"))
	d.Replication.PasswordEnv = prompt(reader, ui, "Replication password environment variable", defaultValue(d.Replication.PasswordEnv, "POORMAN_REPLICATION_PASSWORD"))
	d.Replication.AllowedCIDR = "127.0.0.1/32"
	primaryPort := d.Port
	if primaryPort == 0 {
		if d.Provider == "postgresql" {
			primaryPort = 5432
		} else {
			primaryPort = 3306
		}
	}
	local := d.LocalReplica
	if local == nil {
		local = &config.LocalReplica{}
	}
	if d.Provider == "mariadb" {
		primaryNodeDefault := d.Replication.NodeID
		if primaryNodeDefault < 1 {
			primaryNodeDefault = 1
		}
		d.Replication.NodeID, _ = strconv.Atoi(prompt(reader, ui, "MariaDB primary node ID", strconv.Itoa(primaryNodeDefault)))
		local.Port = promptPort(reader, ui, "Local replica database port", local.Port, primaryPort+1)
		local.DataDir = prompt(reader, ui, "Local replica data directory", defaultValue(local.DataDir, fmt.Sprintf("/var/lib/mysql/poorman-replica-%d", local.Port)))
		nodeDefault := local.NodeID
		if nodeDefault < 1 || nodeDefault == d.Replication.NodeID {
			nodeDefault = d.Replication.NodeID + 1
		}
		local.NodeID, _ = strconv.Atoi(prompt(reader, ui, "MariaDB replica node ID", strconv.Itoa(nodeDefault)))
		local.Slot = ""
	} else {
		local.Port = promptPort(reader, ui, "Local replica database port", local.Port, primaryPort+1)
		local.DataDir = prompt(reader, ui, "Local replica data directory", defaultValue(local.DataDir, "/var/lib/postgresql/poorman-replica"))
		local.Slot = prompt(reader, ui, "PostgreSQL replication slot", defaultValue(local.Slot, "poorman_replica_1"))
		local.NodeID = 0
		d.Replication.Slot = local.Slot
	}
	if ui.setupCanceled {
		ui.muted("Cancelled.")
		return errSetupCanceled
	}
	d.LocalReplica = local
	if err := config.Write(path, c); err != nil {
		return fmt.Errorf("save configuration: %w", err)
	}
	ui.success(fmt.Sprintf("Local replica added to %s", path))
	ui.muted(fmt.Sprintf("Preview and apply both database instances with: poorman plan -f %s", path))
	return nil
}

func guidedReplicaSetup(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("replica setup", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("f", "poorman.json", "configuration file")
	from := fs.String("from", "", "existing primary or stack configuration to copy")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	if *from == "" {
		if existing, loadErr := config.Load(*path); loadErr == nil && existing.Database != nil && existing.Database.Role != "replica" {
			return guidedLocalReplicaSetup(*path, in, out)
		}
	}
	c := config.Default()
	var sourceConfig *config.Config
	var err error
	loadedTarget := false
	if *from != "" && sameConfigPath(*path, *from) {
		return fmt.Errorf("replica configuration must be different from the primary configuration")
	}
	if *from != "" {
		source, loadErr := config.Load(*from)
		if loadErr != nil {
			return loadErr
		}
		if source.Database != nil && source.Database.Role == "replica" {
			return fmt.Errorf("--from must reference a standalone or primary database configuration")
		}
		sourceConfig = &source
		c = source
	}
	if _, statErr := os.Stat(*path); statErr == nil {
		loadedTarget = true
		target, loadErr := config.Load(*path)
		err = loadErr
		if err != nil {
			return err
		}
		if sourceConfig == nil {
			c = target
		} else if target.Database != nil && target.Database.Role == "replica" {
			// --from defines the stack the replica belongs to. Keep only the
			// existing replica's node-local database layout; otherwise stale
			// copies of web, site, access, and backup settings can silently
			// replace the source stack (for example, Apache replacing
			// OpenLiteSpeed on a rerun).
			c.Database = target.Database
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	ui, usingParentUI := out.(*terminalUI)
	if !usingParentUI {
		ui = newTerminalUI(out)
		attachTerminalInput(ui, in)
	}
	previousConfirm := ui.confirmSetupCancel
	ui.confirmSetupCancel = true
	defer func() { ui.confirmSetupCancel = previousConfirm }()
	reader := inputReader(in)
	ui.brand("guided replica setup", "Attach a database replica with safe same-machine defaults")
	providerName := "mariadb"
	if c.Database != nil {
		providerName = c.Database.Provider
	}
	providerName = strings.ToLower(selectOption(reader, ui, "Database", providerName, "mariadb", "postgresql"))
	if sourceConfig != nil && sourceConfig.Database != nil && sourceConfig.Database.Provider != providerName {
		return fmt.Errorf("replica database provider %q must match source provider %q", providerName, sourceConfig.Database.Provider)
	}
	database := &config.Database{Provider: providerName, Role: "replica", PasswordEnv: "POORMAN_DB_PASSWORD"}
	if c.Database != nil {
		copy := *c.Database
		database = &copy
	}
	normalizeReplicaDatabase(database, providerName)
	if err := configureReplication(reader, ui, database); err != nil {
		return err
	}
	if ui.setupCanceled {
		ui.muted("Cancelled.")
		return errSetupCanceled
	}
	c.Database = database
	if err := c.Validate(); err != nil {
		return fmt.Errorf("replica configuration: %w", err)
	}
	if sourceConfig != nil {
		updatedSource, err := configureReplicaSource(*sourceConfig, database, reader, ui)
		if err != nil {
			return err
		}
		if ui.setupCanceled {
			ui.muted("Cancelled.")
			return errSetupCanceled
		}
		if err := config.Write(*from, updatedSource); err != nil {
			return fmt.Errorf("save primary configuration: %w", err)
		}
	}
	if err := config.Write(*path, c); err != nil {
		return fmt.Errorf("save replica configuration: %w", err)
	}
	if !loadedTarget && *from != "" {
		if err := copyConfigSecrets(*from, *path, c); err != nil {
			return err
		}
	}
	if sourceConfig != nil {
		ui.success(fmt.Sprintf("Primary configuration saved to %s", *from))
		ui.success(fmt.Sprintf("Replica configuration saved to %s", *path))
		ui.muted("Apply the configurations in this order:")
		ui.muted(fmt.Sprintf("1. poorman apply -f %s", *from))
		ui.muted(fmt.Sprintf("2. poorman apply -f %s", *path))
	} else {
		ui.success(fmt.Sprintf("Replica configuration saved to %s", *path))
		ui.muted(fmt.Sprintf("Preview it with: poorman plan -f %s", *path))
	}
	return nil
}

func sameConfigPath(left, right string) bool {
	leftPath, leftErr := filepath.Abs(left)
	rightPath, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && filepath.Clean(leftPath) == filepath.Clean(rightPath)
}

// configureReplicaSource persists the other half of a generated topology. A
// replica cloned from a standalone stack is unusable until that source is also
// configured as a primary with matching credentials.
func configureReplicaSource(source config.Config, replica *config.Database, reader *bufio.Reader, ui *terminalUI) (config.Config, error) {
	primary := config.Database{Provider: replica.Provider, PasswordEnv: replica.PasswordEnv}
	if source.Database != nil {
		primary = *source.Database
	}
	if primary.Role == "replica" {
		return config.Config{}, fmt.Errorf("--from must reference a standalone or primary database configuration")
	}
	wasPrimary := primary.Role == "primary"
	primary.Provider = replica.Provider
	primary.Role = "primary"
	primary.Replication.User = replica.Replication.User
	primary.Replication.PasswordEnv = replica.Replication.PasswordEnv
	primary.Replication.PrimaryHost = ""
	primary.Replication.PrimaryPort = 0

	if !wasPrimary {
		if isLoopbackAddress(replica.Replication.PrimaryHost) {
			primary.Replication.AllowedCIDR = "127.0.0.1/32"
		} else {
			primary.Replication.AllowedCIDR = prompt(reader, ui, "Replica network CIDR allowed by primary", defaultValue(primary.Replication.AllowedCIDR, "10.20.0.0/24"))
		}
	}
	if replica.Provider == "mariadb" {
		primary.Replication.Slot = ""
		if primary.Replication.NodeID < 1 {
			primary.Replication.NodeID = 1
		}
		if primary.Replication.NodeID == replica.Replication.NodeID {
			return config.Config{}, fmt.Errorf("MariaDB primary and replica must use different node IDs")
		}
	} else {
		primary.Replication.NodeID = 0
		primary.Replication.Slot = replica.Replication.Slot
	}
	source.Database = &primary
	if err := source.Validate(); err != nil {
		return config.Config{}, fmt.Errorf("primary configuration: %w", err)
	}
	return source, nil
}

// normalizeReplicaDatabase keeps shared credentials while removing topology
// that belongs to the source node. Existing replica files keep their local
// port, data directory, and node ID when edited.
func normalizeReplicaDatabase(database *config.Database, providerName string) {
	sourceProvider, sourceRole := database.Provider, database.Role
	database.Provider = providerName
	if sourceRole != "replica" || sourceProvider != providerName {
		sourcePort := database.Port
		sourceNodeID := database.Replication.NodeID
		database.Port = 0
		database.DataDir = ""
		database.Replication.PrimaryHost = ""
		database.Replication.PrimaryPort = 0
		if sourceProvider == providerName {
			database.Replication.PrimaryPort = sourcePort
		}
		if providerName == "mariadb" {
			database.Replication.NodeID = 2
			if sourceProvider == providerName && sourceNodeID > 0 {
				database.Replication.NodeID = sourceNodeID + 1
			}
		} else {
			database.Replication.NodeID = 0
		}
	}
	database.Role = "replica"
	database.Replication.AllowedCIDR = ""
	if providerName == "mariadb" {
		database.Replication.Slot = ""
	}
}

func tuiDashboard(ctx context.Context, path string, in io.Reader, ui *terminalUI) error {
	c, err := config.Load(path)
	if err != nil {
		return err
	}
	reader := bufio.NewReader(in)
	for {
		c, err = config.Load(path)
		if err != nil {
			return err
		}
		ui.clear()
		choice := dashboardChoice(ctx, in, reader, ui, c, path)
		switch choice {
		case "1":
			if err := deploymentTUI(ctx, path, reader, ui); err != nil {
				ui.warn("Deployment unavailable: " + err.Error())
				pause(reader, ui)
			}
		case "2":
			if err := operationsTUI(ctx, c, path, reader, ui); err != nil {
				ui.warn("Monitoring unavailable: " + err.Error())
				pause(reader, ui)
			}
		case "3":
			if err := websitesTUI(path, reader, ui); err != nil {
				ui.warn("Website management unavailable: " + err.Error())
				pause(reader, ui)
			}
		case "4":
			if err := databaseTUI(ctx, path, reader, ui); err != nil {
				ui.warn("Database management unavailable: " + err.Error())
				pause(reader, ui)
			}
		case "5":
			if err := securityTUI(ctx, path, reader, ui); err != nil {
				ui.warn("Security and backups unavailable: " + err.Error())
				pause(reader, ui)
			}
		case "6":
			if err := updateManagerTUI(ctx, reader, ui); err != nil {
				ui.warn("Update manager unavailable: " + err.Error())
				pause(reader, ui)
			}
		case "0", "q", "Q":
			return nil
		default:
			ui.warn("Unknown selection.")
			pause(reader, ui)
		}
	}
}

func deploymentTUI(ctx context.Context, path string, reader *bufio.Reader, ui *terminalUI) error {
	for {
		ui.clear()
		ui.brand("Deploy configuration", "Review the desired changes before applying them")
		ui.panel("ACTIONS", "1  preview plan\n2  apply configuration\n0  back")
		switch selectMenu(reader, ui, "Deploy configuration", "1",
			selectorChoice{Value: "1", Label: "preview plan"},
			selectorChoice{Value: "2", Label: "apply configuration"},
			selectorChoice{Value: "0", Label: "back"},
		) {
		case "1":
			if err := planCommand([]string{"-f", path}, ui); err != nil {
				ui.warn("Plan unavailable: " + err.Error())
			}
			pause(reader, ui)
		case "2":
			if err := applyCommand(ctx, []string{"-f", path}, reader, ui, ui); err != nil {
				ui.warn("Apply failed: " + err.Error())
			}
			pause(reader, ui)
		case "0", "q", "Q":
			return nil
		}
	}
}

func websitesTUI(path string, reader *bufio.Reader, ui *terminalUI) error {
	for {
		c, err := config.Load(path)
		if err != nil {
			return err
		}
		ui.clear()
		ui.brand("Websites & stack", "Manage sites and the web server that serves them")
		ui.panel("CURRENT", fmt.Sprintf("web server  %s\nsites       %d", c.WebServer.Provider, len(c.Sites)))
		ui.panel("ACTIONS", "1  virtual hosts\n2  web server\n0  back")
		switch selectMenu(reader, ui, "Websites & stack", "1",
			selectorChoice{Value: "1", Label: "virtual hosts"},
			selectorChoice{Value: "2", Label: "web server"},
			selectorChoice{Value: "0", Label: "back"},
		) {
		case "1":
			if err := vhostsTUI(path, reader, ui); err != nil {
				return err
			}
		case "2":
			c.WebServer.Provider = selectOption(reader, ui, "Web server", c.WebServer.Provider, "nginx", "apache", "openlitespeed")
			if err := config.Write(path, c); err != nil {
				return err
			}
			ui.success("Web server updated")
		case "0", "q", "Q":
			return nil
		}
	}
}

func databaseTUI(ctx context.Context, path string, reader *bufio.Reader, ui *terminalUI) error {
	for {
		c, err := config.Load(path)
		if err != nil {
			return err
		}
		replicationAction := "replication status"
		if c.Database == nil || c.Database.Role == "standalone" || c.Database.Role == "" {
			replicationAction += " (not configured)"
		}
		ui.clear()
		ui.brand("Database & replication", "Configure data services, schemas, and replicas")
		ui.panel("CURRENT", "database  "+databaseLabel(c))
		ui.panel("ACTIONS", "1  database engine & replication settings\n2  databases, users & permissions\n3  "+replicationAction+"\n4  guided local replica setup\n0  back")
		switch selectMenu(reader, ui, "Database & replication", "1",
			selectorChoice{Value: "1", Label: "database engine and replication settings"},
			selectorChoice{Value: "2", Label: "databases, users and permissions"},
			selectorChoice{Value: "3", Label: replicationAction},
			selectorChoice{Value: "4", Label: "guided local replica setup"},
			selectorChoice{Value: "0", Label: "back"},
		) {
		case "1":
			if err := adjustDatabase(&c, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
				continue
			}
			if err := config.Write(path, c); err != nil {
				return err
			}
			ui.success("Database settings updated")
		case "2":
			if err := databaseManagementTUI(path, reader, ui); err != nil {
				return err
			}
		case "3":
			if c.Database == nil || c.Database.Role == "standalone" || c.Database.Role == "" {
				ui.warn("Replication is not configured. Configure it in this menu first.")
				pause(reader, ui)
				continue
			}
			if err := replicaCommand(ctx, []string{"status", "-f", path}, reader, ui, ui); err != nil {
				ui.warn("Replication status unavailable: " + err.Error())
			}
			pause(reader, ui)
		case "4":
			if err := guidedReplicaSetupTUI(ctx, path, reader, ui); err != nil {
				ui.warn("Replica setup unavailable: " + err.Error())
				pause(reader, ui)
			}
		case "0", "q", "Q":
			return nil
		}
	}
}

func securityTUI(ctx context.Context, path string, reader *bufio.Reader, ui *terminalUI) error {
	for {
		ui.clear()
		ui.brand("Security & backups", "Configure protections and operate recovery controls")
		ui.panel("ACTIONS", "1  HTTPS, firewall & backups\n2  firewall status & policy\n0  back")
		switch selectMenu(reader, ui, "Security & backups", "1",
			selectorChoice{Value: "1", Label: "HTTPS, firewall and backups"},
			selectorChoice{Value: "2", Label: "firewall status and policy"},
			selectorChoice{Value: "0", Label: "back"},
		) {
		case "1":
			if err := protectionTUI(ctx, path, reader, ui); err != nil {
				return err
			}
		case "2":
			if err := firewallTUI(ctx, path, reader, ui); err != nil {
				return err
			}
		case "0", "q", "Q":
			return nil
		}
	}
}

func guidedReplicaSetupTUI(ctx context.Context, primaryPath string, reader *bufio.Reader, ui *terminalUI) error {
	previousConfirm := ui.confirmSetupCancel
	ui.confirmSetupCancel = true
	defer func() { ui.confirmSetupCancel = previousConfirm }()
	ui.clear()
	ui.brand("guided local replica setup", "Manage the primary and local replica from this stack")
	if err := guidedLocalReplicaSetup(primaryPath, reader, ui); err != nil {
		if errors.Is(err, errSetupCanceled) {
			ui.setupCanceled = false
			return nil
		}
		return err
	}
	// The setup itself is now saved. The remaining preview/apply questions use
	// the normal Ctrl+C behavior for operations.
	ui.confirmSetupCancel = false
	if yesNo(selectOption(reader, ui, "Preview the combined primary and replica plan now?", "y", "y", "n")) {
		if err := planCommand([]string{"-f", primaryPath}, ui); err != nil {
			return err
		}
	}
	if yesNo(selectOption(reader, ui, "Apply the combined primary and replica plan now?", "n", "y", "n")) {
		// The guided prompt is already the user's confirmation. Passing --yes
		// avoids asking for a second confirmation and consuming the next TUI input.
		if err := applyCommand(ctx, []string{"-f", primaryPath, "--yes"}, reader, ui, ui); err != nil {
			return err
		}
		return nil
	}
	ui.muted(fmt.Sprintf("Primary and local replica are ready: poorman apply -f %s", primaryPath))
	return nil
}

func stackSettingsTUI(path string, reader *bufio.Reader, ui *terminalUI) error {
	for {
		c, err := config.Load(path)
		if err != nil {
			return err
		}
		ui.clear()
		ui.brand("Stack settings", "Adjust the platform after initial setup")
		ui.panel("CURRENT", fmt.Sprintf("web         %s\ndatabase    %s\nhttps       %s\ncert email  %s\nfirewall    %s\nbackups     %s", c.WebServer.Provider, databaseLabel(c), siteTLSLabel(c), defaultValue(c.TLS.Email, "not configured"), enabledLabel(c.Firewall.Enabled), enabledLabel(c.Backups.Enabled)))
		ui.panel("ACTIONS", "1  web server\n2  database and replication\n3  certificate email\n4  firewall\n5  backups\n0  back")
		switch selectMenu(reader, ui, "Stack settings", "1",
			selectorChoice{Value: "1", Label: "web server"},
			selectorChoice{Value: "2", Label: "database and replication"},
			selectorChoice{Value: "3", Label: "certificate email"},
			selectorChoice{Value: "4", Label: "firewall"},
			selectorChoice{Value: "5", Label: "backups"},
			selectorChoice{Value: "0", Label: "back"},
		) {
		case "1":
			c.WebServer.Provider = selectOption(reader, ui, "Web server", c.WebServer.Provider, "nginx", "apache", "openlitespeed")
		case "2":
			if err := adjustDatabase(&c, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
				continue
			}
		case "3":
			c.TLS.Email = prompt(reader, ui, "Certificate email", defaultValue(c.TLS.Email, defaultSiteEmail(c)))
		case "4":
			c.Firewall.Enabled = yesNo(selectOption(reader, ui, "Enable firewall?", enabledDefault(c.Firewall.Enabled), "y", "n"))
		case "5":
			c.Backups.Enabled = yesNo(selectOption(reader, ui, "Enable nightly backups?", enabledDefault(c.Backups.Enabled), "y", "n"))
			if c.Backups.Enabled {
				c.Backups.Destination = prompt(reader, ui, "Backup destination", c.Backups.Destination)
				c.Backups.Schedule = prompt(reader, ui, "Backup schedule", c.Backups.Schedule)
			}
		case "0", "q", "Q":
			return nil
		default:
			ui.warn("Unknown selection.")
			continue
		}
		if err := config.Write(path, c); err != nil {
			ui.warn(err.Error())
			pause(reader, ui)
			continue
		}
		ui.success("Stack settings updated")
	}
}
