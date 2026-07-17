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
		Sites:     []config.Site{{Domain: domain, Root: root, Owner: owner, Runtime: runtimeName, WordPress: wordpress}},
		TLS:       config.TLS{Enabled: tlsEnabled, Email: "admin@" + domain},
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
		c, err = config.Load(*path)
		if err != nil {
			return err
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
			if err := planCommand([]string{"-f", path}, ui); err != nil {
				ui.warn("Plan unavailable: " + err.Error())
			}
			pause(reader, ui)
		case "2":
			if err := applyCommand(ctx, []string{"-f", path}, reader, ui, ui); err != nil {
				ui.warn("Apply failed: " + err.Error())
			}
			pause(reader, ui)
		case "3":
			if err := statusCommand(ctx, []string{"-f", path}, ui); err != nil {
				ui.warn("Health warning: " + err.Error())
			}
			pause(reader, ui)
		case "4":
			if !c.Backups.Enabled {
				ui.warn("Backups are disabled in Stack settings.")
				pause(reader, ui)
				continue
			}
			if err := backupCommand(ctx, []string{"-f", path}, reader, ui, ui); err != nil {
				ui.warn("Backup failed: " + err.Error())
			}
			pause(reader, ui)
		case "5":
			if c.Database == nil || c.Database.Role == "standalone" || c.Database.Role == "" {
				ui.warn("Replication is not configured. Use guided replica setup or Stack settings first.")
				pause(reader, ui)
				continue
			}
			if err := replicaCommand(ctx, []string{"status", "-f", path}, reader, ui, ui); err != nil {
				ui.warn("Replication status unavailable: " + err.Error())
			}
			pause(reader, ui)
		case "6":
			if err := firewallTUI(ctx, path, reader, ui); err != nil {
				ui.warn("Firewall operation unavailable: " + err.Error())
				pause(reader, ui)
			}
		case "7":
			if err := operationsTUI(ctx, c, path, reader, ui); err != nil {
				ui.warn("Operations unavailable: " + err.Error())
				pause(reader, ui)
			}
		case "8":
			if err := vhostsTUI(path, reader, ui); err != nil {
				ui.warn("Virtual host management unavailable: " + err.Error())
				pause(reader, ui)
			}
		case "9":
			if err := stackSettingsTUI(path, reader, ui); err != nil {
				ui.warn("Stack settings unavailable: " + err.Error())
				pause(reader, ui)
			}
		case "10":
			replicaPath, err := guidedReplicaSetupTUI(ctx, path, reader, ui)
			if replicaPath != "" {
				path = replicaPath
			}
			if err != nil {
				ui.warn("Replica setup unavailable: " + err.Error())
				pause(reader, ui)
			}
		case "11":
			if err := protectionTUI(ctx, path, reader, ui); err != nil {
				ui.warn("Protection settings unavailable: " + err.Error())
				pause(reader, ui)
			}
		case "12":
			if err := databaseManagementTUI(path, reader, ui); err != nil {
				ui.warn("Database management unavailable: " + err.Error())
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

func guidedReplicaSetupTUI(ctx context.Context, primaryPath string, reader *bufio.Reader, ui *terminalUI) (string, error) {
	previousConfirm := ui.confirmSetupCancel
	ui.confirmSetupCancel = true
	defer func() { ui.confirmSetupCancel = previousConfirm }()
	ui.clear()
	ui.brand("guided replica setup", "Create a separate replica configuration from this stack")
	defaultPath := filepath.Join(filepath.Dir(primaryPath), "replica.json")
	replicaPath := filepath.Clean(prompt(reader, ui, "Replica configuration file", defaultPath))
	if ui.setupCanceled {
		ui.muted("Cancelled.")
		ui.setupCanceled = false
		return "", nil
	}
	if replicaPath == filepath.Clean(primaryPath) {
		return "", fmt.Errorf("replica configuration must be different from the primary configuration")
	}
	if err := guidedReplicaSetup([]string{"-f", replicaPath, "--from", primaryPath}, reader, ui); err != nil {
		if errors.Is(err, errSetupCanceled) {
			ui.setupCanceled = false
			return "", nil
		}
		return "", err
	}
	// The setup itself is now saved. The remaining preview/apply questions use
	// the normal Ctrl+C behavior for operations.
	ui.confirmSetupCancel = false
	if yesNo(selectOption(reader, ui, "Preview the primary and replica plans now?", "y", "y", "n")) {
		if err := planCommand([]string{"-f", primaryPath}, ui); err != nil {
			return replicaPath, err
		}
		if err := planCommand([]string{"-f", replicaPath}, ui); err != nil {
			return replicaPath, err
		}
	}
	if yesNo(selectOption(reader, ui, "Apply the primary, then the replica now?", "n", "y", "n")) {
		// The guided prompt is already the user's confirmation. Passing --yes
		// avoids asking for a second confirmation and consuming the next TUI input.
		if err := applyCommand(ctx, []string{"-f", primaryPath, "--yes"}, reader, ui, ui); err != nil {
			return replicaPath, err
		}
		return replicaPath, applyCommand(ctx, []string{"-f", replicaPath, "--yes"}, reader, ui, ui)
	}
	ui.muted(fmt.Sprintf("Replica configuration is ready; apply the primary first: poorman apply -f %s", primaryPath))
	ui.muted(fmt.Sprintf("Then apply the replica: poorman apply -f %s", replicaPath))
	return replicaPath, nil
}

func stackSettingsTUI(path string, reader *bufio.Reader, ui *terminalUI) error {
	for {
		c, err := config.Load(path)
		if err != nil {
			return err
		}
		ui.clear()
		ui.brand("Stack settings", "Adjust the platform after initial setup")
		ui.panel("CURRENT", fmt.Sprintf("web       %s\ndatabase  %s\ntls       %s\nfirewall  %s\nbackups   %s", c.WebServer.Provider, databaseLabel(c), enabledLabel(c.TLS.Enabled), enabledLabel(c.Firewall.Enabled), enabledLabel(c.Backups.Enabled)))
		ui.panel("ACTIONS", "1  web server\n2  database and replication\n3  TLS and certificate email\n4  firewall\n5  backups\n0  back")
		switch selectMenu(reader, ui, "Stack settings", "1",
			selectorChoice{Value: "1", Label: "web server"},
			selectorChoice{Value: "2", Label: "database and replication"},
			selectorChoice{Value: "3", Label: "TLS and certificate email"},
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
			c.TLS.Enabled = yesNo(selectOption(reader, ui, "Enable HTTPS with Let's Encrypt?", enabledDefault(c.TLS.Enabled), "y", "n"))
			if c.TLS.Enabled {
				c.TLS.Email = prompt(reader, ui, "Certificate email", c.TLS.Email)
			}
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
