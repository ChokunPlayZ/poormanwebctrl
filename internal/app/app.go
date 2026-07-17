package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/executor"
	"github.com/chokunplayz/poormanwebctrl/internal/health"
	"github.com/chokunplayz/poormanwebctrl/internal/managed"
	"github.com/chokunplayz/poormanwebctrl/internal/ops"
	"github.com/chokunplayz/poormanwebctrl/internal/plan"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
	"github.com/chokunplayz/poormanwebctrl/internal/provider"
)

var version = "1.0.0-dev"

func Run(args []string, in io.Reader, out, errOut io.Writer) error {
	return RunContext(context.Background(), args, in, out, errOut)
}

func RunContext(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		usage(out)
		return nil
	}
	switch args[0] {
	case "init":
		return initCommand(args[1:], out)
	case "plan":
		return planCommand(args[1:], out)
	case "apply":
		return applyCommand(ctx, args[1:], in, out, errOut)
	case "tui":
		return tuiCommand(ctx, args[1:], in, out)
	case "status":
		return statusCommand(ctx, args[1:], out)
	case "replica":
		return replicaCommand(ctx, args[1:], in, out, errOut)
	case "backup":
		return backupCommand(ctx, args[1:], in, out, errOut)
	case "version", "--version", "-v":
		if len(args) > 1 {
			return fmt.Errorf("unexpected argument %q", args[1])
		}
		fmt.Fprintln(out, version)
		return nil
	case "help", "--help", "-h":
		if len(args) > 1 {
			return fmt.Errorf("unexpected argument %q", args[1])
		}
		usage(out)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `poorman makes a server match a small, auditable configuration.

Usage:
  poorman init [-f poorman.json]
  poorman plan [-f poorman.json]
  poorman apply [-f poorman.json] [--yes]
  poorman tui [-f poorman.json]
  poorman status [-f poorman.json]
  poorman replica setup [-f replica.json] [--from primary.json]
  poorman replica status [-f poorman.json]
  poorman replica promote [-f poorman.json] [--yes]
  poorman backup [-f poorman.json] [--yes]
  poorman version

Start with "poorman init", edit the file, then preview with "poorman plan".`)
}

func statusCommand(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
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
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	p, err := platform.Detect()
	if err != nil {
		return err
	}
	return health.Report(ctx, c, p, out)
}

func replicaCommand(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("replica requires setup, status, or promote")
	}
	action := args[0]
	if action == "setup" {
		return guidedReplicaSetup(args[1:], in, out)
	}
	if action != "status" && action != "promote" {
		return fmt.Errorf("unknown replica action %q", action)
	}
	fs := flag.NewFlagSet("replica "+action, flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("f", "poorman.json", "configuration file")
	yes := false
	if action == "promote" {
		fs.BoolVar(&yes, "yes", false, "confirm replica promotion")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	p, err := platform.Detect()
	if err != nil {
		return err
	}
	var operation plan.Plan
	if action == "status" {
		operation, err = provider.ReplicaStatus(c, p)
	} else if action == "promote" {
		operation, err = provider.PromoteReplica(c, p)
	}
	if err != nil {
		return err
	}
	reader := inputReader(in)
	operation.Print(out)
	if action == "promote" && !yes {
		fmt.Fprint(out, "Type PROMOTE to confirm the old primary is fenced: ")
		answer, _ := reader.ReadString('\n')
		if strings.TrimSpace(answer) != "PROMOTE" {
			fmt.Fprintln(out, "Cancelled.")
			return nil
		}
	}
	applyErr := executor.Apply(ctx, operation, reader, out, errOut)
	discardBlankInput(reader)
	return applyErr
}

func backupCommand(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("f", "poorman.json", "configuration file")
	yes := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if !c.Backups.Enabled {
		return fmt.Errorf("backups are disabled in %s", *path)
	}
	operation := plan.Plan{Platform: "local", Steps: []plan.Step{plan.Cmd("Run configured backup", backupScriptPath(c), true)}}
	reader := inputReader(in)
	operation.Print(out)
	if !*yes {
		fmt.Fprint(out, "Run backup now? [y/N] ")
		answer, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Fprintln(out, "Cancelled.")
			return nil
		}
	}
	applyErr := executor.Apply(ctx, operation, reader, out, errOut)
	discardBlankInput(reader)
	return applyErr
}

func backupScriptPath(c config.Config) string {
	if service := managedMariaDBService(c); service != "" {
		return "/usr/local/sbin/poorman-backup-" + service
	}
	return "/usr/local/sbin/poorman-backup"
}

func managedMariaDBService(c config.Config) string {
	if c.Database != nil && c.Database.Provider == "mariadb" && c.Database.Port > 0 && c.Database.DataDir != "" && isLoopbackAddress(c.Database.Replication.PrimaryHost) {
		return fmt.Sprintf("poorman-mariadb-replica-%d", c.Database.Port)
	}
	return ""
}

func isLoopbackAddress(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

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

	c := config.Config{
		Version:   1,
		WebServer: config.WebServer{Provider: providerName},
		Database:  database,
		Access:    config.Access{Users: []config.User{{Name: owner, Home: "/home/" + owner, SFTPOnly: true}}},
		Sites:     []config.Site{{Domain: domain, Root: root, Owner: owner, Runtime: runtimeName, WordPress: wordpress}},
		TLS:       config.TLS{Enabled: tlsEnabled, Email: "admin@" + domain},
		Firewall:  config.Firewall{Enabled: true},
		Backups:   config.Backup{Enabled: backupEnabled, Destination: "/var/backups/poorman", Schedule: "0 3 * * *"},
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
	ui := newTerminalUI(out)
	attachTerminalInput(ui, in)
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
	c.Database = database
	if err := c.Validate(); err != nil {
		return fmt.Errorf("replica configuration: %w", err)
	}
	if sourceConfig != nil {
		updatedSource, err := configureReplicaSource(*sourceConfig, database, reader, ui)
		if err != nil {
			return err
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
		choice := dashboardChoice(in, reader, ui, c, path)
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
	ui.clear()
	ui.brand("guided replica setup", "Create a separate replica configuration from this stack")
	defaultPath := filepath.Join(filepath.Dir(primaryPath), "replica.json")
	replicaPath := filepath.Clean(prompt(reader, ui, "Replica configuration file", defaultPath))
	if replicaPath == filepath.Clean(primaryPath) {
		return "", fmt.Errorf("replica configuration must be different from the primary configuration")
	}
	if err := guidedReplicaSetup([]string{"-f", replicaPath, "--from", primaryPath}, reader, ui); err != nil {
		return "", err
	}
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
		ui.brand("Database management", "Select a database to manage its tables and permissions")
		if d.Role == "replica" {
			ui.panel("REPLICA", "This database is read-only. Schema, users, and grants are sourced from the primary through replication.")
		}
		databases := d.ManagedDatabases()
		options := []string{"Create new database"}
		for _, database := range databases {
			options = append(options, database.Name)
		}
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
		ui.brand("Database / "+database.Name, "Manage tables and access control for the selected database")
		ui.panel("DATABASE", fmt.Sprintf("provider  %s (%s)\nowner     %s\ntables    %d\nacl rules %d", c.Database.Provider, defaultValue(c.Database.Role, "standalone"), defaultValue(database.Owner, "none"), len(database.Tables), countDatabasePermissions(*c.Database, database.Name)))
		if c.Database.Role == "replica" {
			ui.panel("REPLICA", "This database is read-only. Manage tables and permissions on the primary.")
		}
		ui.panel("ACTIONS", "1  create table\n2  set user permissions\n3  view current ACLs\n0  back")
		choice := selectMenu(reader, ui, "Database / "+database.Name, "1",
			selectorChoice{Value: "1", Label: "create table"},
			selectorChoice{Value: "2", Label: "set user permissions"},
			selectorChoice{Value: "3", Label: "view current ACLs"},
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
				ui.warn("Replicas are read-only; set permissions on the primary.")
				pause(reader, ui)
				continue
			}
			if err := setDatabasePermission(path, c, index, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
			}
		case "3":
			showDatabaseACL(ui, *c.Database, database.Name)
			pause(reader, ui)
		case "0":
			return nil
		}
	}
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

func protectionTUI(ctx context.Context, path string, reader *bufio.Reader, ui *terminalUI) error {
	for {
		c, err := config.Load(path)
		if err != nil {
			return err
		}
		ui.clear()
		ui.brand("Guardrails & backups", "Turn on the protections that keep a live server recoverable")
		ui.panel("CURRENT", fmt.Sprintf("https       %s\nfirewall    %s\nbackups     %s\nbackup path %s\nschedule    %s", enabledLabel(c.TLS.Enabled), enabledLabel(c.Firewall.Enabled), enabledLabel(c.Backups.Enabled), defaultValue(c.Backups.Destination, "not configured"), defaultValue(c.Backups.Schedule, "not configured")))
		ui.panel("ACTIONS", "1  HTTPS and certificate email\n2  firewall\n3  backups and schedule\n4  run backup now\n5  backup inventory\n0  back")
		switch selectMenu(reader, ui, "Guardrails & backups", "1",
			selectorChoice{Value: "1", Label: "HTTPS and certificate email"},
			selectorChoice{Value: "2", Label: "firewall"},
			selectorChoice{Value: "3", Label: "backups and schedule"},
			selectorChoice{Value: "4", Label: "run backup now"},
			selectorChoice{Value: "5", Label: "backup inventory"},
			selectorChoice{Value: "0", Label: "back"},
		) {
		case "1":
			c.TLS.Enabled = yesNo(selectOption(reader, ui, "Enable HTTPS with Let's Encrypt?", enabledDefault(c.TLS.Enabled), "y", "n"))
			if c.TLS.Enabled {
				c.TLS.Email = prompt(reader, ui, "Certificate email", defaultValue(c.TLS.Email, defaultSiteEmail(c)))
			}
			if err := config.Write(path, c); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
				continue
			}
			ui.success("HTTPS settings updated")
		case "2":
			c.Firewall.Enabled = yesNo(selectOption(reader, ui, "Enable firewall?", enabledDefault(c.Firewall.Enabled), "y", "n"))
			if err := config.Write(path, c); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
				continue
			}
			ui.success("Firewall settings updated")
		case "3":
			c.Backups.Enabled = yesNo(selectOption(reader, ui, "Enable nightly backups?", enabledDefault(c.Backups.Enabled), "y", "n"))
			if c.Backups.Enabled {
				c.Backups.Destination = prompt(reader, ui, "Backup destination", defaultValue(c.Backups.Destination, "/var/backups/poorman"))
				c.Backups.Schedule = prompt(reader, ui, "Backup schedule", defaultValue(c.Backups.Schedule, "0 3 * * *"))
			}
			if err := config.Write(path, c); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
				continue
			}
			ui.success("Backup settings updated")
		case "4":
			if !c.Backups.Enabled {
				ui.warn("Backups are disabled. Enable them in this menu first.")
				pause(reader, ui)
				continue
			}
			if err := backupCommand(ctx, []string{"-f", path}, reader, ui, ui); err != nil {
				ui.warn("Backup failed: " + err.Error())
			}
			pause(reader, ui)
		case "5":
			ui.clear()
			ui.brand("Backup inventory", "Review artifacts produced by the configured backup job")
			if !c.Backups.Enabled {
				ui.warn("Backups are disabled. Enable them in this menu first.")
				pause(reader, ui)
				continue
			}
			ui.muted("Destination: " + c.Backups.Destination)
			if err := ops.BackupFiles(ctx, c.Backups.Destination, ui); err != nil {
				ui.warn(err.Error())
			}
			pause(reader, ui)
		case "0", "q", "Q":
			return nil
		default:
			ui.warn("Unknown selection.")
		}
	}
}

func defaultSiteEmail(c config.Config) string {
	if len(c.Sites) > 0 && strings.Contains(c.Sites[0].Domain, ".") {
		return "admin@" + c.Sites[0].Domain
	}
	return "admin@example.com"
}

func adjustDatabase(c *config.Config, reader *bufio.Reader, ui *terminalUI) error {
	currentProvider, currentRole := "mariadb", "standalone"
	name, user, passwordEnv := "website", "website", "POORMAN_DB_PASSWORD"
	if c.Database != nil {
		currentProvider, currentRole = c.Database.Provider, c.Database.Role
		name, user, passwordEnv = c.Database.Name, c.Database.User, c.Database.PasswordEnv
	}
	providerName := selectOption(reader, ui, "Database", currentProvider, "mariadb", "postgresql", "none")
	if providerName == "none" {
		c.Database = nil
		return nil
	}
	database := &config.Database{}
	if c.Database != nil {
		// Stack settings should not silently discard advanced values such as a
		// custom port, data directory, slot, or replication node ID.
		copy := *c.Database
		database = &copy
	}
	database.Provider = providerName
	database.Role = selectOption(reader, ui, "Database role", currentRole, "standalone", "primary", "replica")
	database.Name = prompt(reader, ui, "Database name", name)
	database.User = prompt(reader, ui, "Database user", user)
	database.PasswordEnv = prompt(reader, ui, "Database password environment variable", passwordEnv)
	if err := configureReplication(reader, ui, database); err != nil {
		return err
	}
	c.Database = database
	return nil
}

func databaseLabel(c config.Config) string {
	if c.Database == nil {
		return "none"
	}
	return c.Database.Provider + " (" + c.Database.Role + ")"
}

func enabledDefault(enabled bool) string {
	if enabled {
		return "y"
	}
	return "n"
}

func yesNo(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "y", "yes", "true", "1":
		return true
	default:
		return false
	}
}

func vhostsTUI(path string, reader *bufio.Reader, ui *terminalUI) error {
	for {
		c, err := config.Load(path)
		if err != nil {
			return err
		}
		ui.clear()
		ui.brand("Virtual hosts", "Manage multiple domains served by this machine")
		if len(c.Sites) == 0 {
			ui.muted("No virtual hosts configured.")
		} else {
			for i, site := range c.Sites {
				aliases := ""
				if len(site.Aliases) > 0 {
					aliases = "  aliases: " + strings.Join(site.Aliases, ", ")
				}
				fmt.Fprintf(ui, "%d  %-30s %-6s %s%s\n", i+1, site.Domain, defaultValue(site.Runtime, "static"), site.Root, aliases)
			}
		}
		ui.panel("ACTIONS", "1  add virtual host\n2  edit virtual host\n3  remove virtual host\n0  back")
		switch selectMenu(reader, ui, "Virtual hosts", "1",
			selectorChoice{Value: "1", Label: "add virtual host"},
			selectorChoice{Value: "2", Label: "edit virtual host"},
			selectorChoice{Value: "3", Label: "remove virtual host"},
			selectorChoice{Value: "0", Label: "back"},
		) {
		case "1":
			if err := addVHost(path, c, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
			}
		case "2":
			if err := editVHost(path, c, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
			}
		case "3":
			if err := removeVHost(path, c, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
			}
		case "0", "q", "Q":
			return nil
		default:
			ui.warn("Unknown selection.")
		}
	}
}

func addVHost(path string, c config.Config, reader *bufio.Reader, ui *terminalUI) error {
	domain := prompt(reader, ui, "Domain", "example.com")
	site := config.Site{
		Domain:  domain,
		Root:    prompt(reader, ui, "Document root", "/var/www/"+domain),
		Owner:   prompt(reader, ui, "System/SFTP user", firstUser(c)),
		Runtime: selectOption(reader, ui, "Runtime", "static", "static", "php"),
	}
	site.Aliases = parseAliases(prompt(reader, ui, "Aliases (comma-separated)", ""))
	c.Sites = append(c.Sites, site)
	if err := config.Write(path, c); err != nil {
		return err
	}
	ui.success("Added virtual host " + site.Domain)
	return nil
}

func editVHost(path string, c config.Config, reader *bufio.Reader, ui *terminalUI) error {
	i, err := chooseVHost(c, reader, ui)
	if err != nil {
		return err
	}
	if i < 0 {
		return nil
	}
	site := c.Sites[i]
	site.Domain = prompt(reader, ui, "Domain", site.Domain)
	site.Root = prompt(reader, ui, "Document root", site.Root)
	site.Owner = prompt(reader, ui, "System/SFTP user", site.Owner)
	site.Runtime = selectOption(reader, ui, "Runtime", defaultValue(site.Runtime, "static"), "static", "php")
	site.Aliases = parseAliases(prompt(reader, ui, "Aliases (comma-separated)", strings.Join(site.Aliases, ",")))
	c.Sites[i] = site
	if err := config.Write(path, c); err != nil {
		return err
	}
	ui.success("Updated virtual host " + site.Domain)
	return nil
}

func removeVHost(path string, c config.Config, reader *bufio.Reader, ui *terminalUI) error {
	i, err := chooseVHost(c, reader, ui)
	if err != nil {
		return err
	}
	if i < 0 {
		return nil
	}
	site := c.Sites[i]
	if !yesNo(selectOption(reader, ui, "Remove "+site.Domain+"?", "n", "y", "n")) {
		ui.muted("Cancelled.")
		return nil
	}
	c.Sites = append(c.Sites[:i], c.Sites[i+1:]...)
	if err := config.Write(path, c); err != nil {
		return err
	}
	ui.success("Removed virtual host " + site.Domain)
	return nil
}

func chooseVHost(c config.Config, reader *bufio.Reader, ui *terminalUI) (int, error) {
	if len(c.Sites) == 0 {
		return 0, fmt.Errorf("no virtual hosts configured")
	}
	options := make([]string, 0, len(c.Sites))
	for i, site := range c.Sites {
		options = append(options, fmt.Sprintf("%d  %s", i+1, site.Domain))
	}
	options = append(options, "0")
	choice := selectOption(reader, ui, "Virtual host", options[0], options...)
	if choice == "0" {
		return -1, nil
	}
	for i, option := range options {
		if choice == option {
			return i, nil
		}
	}
	return 0, fmt.Errorf("invalid virtual host selection")
}

func firstUser(c config.Config) string {
	if len(c.Access.Users) > 0 {
		return c.Access.Users[0].Name
	}
	return ""
}

func defaultValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func parseAliases(value string) []string {
	var aliases []string
	for _, alias := range strings.Split(value, ",") {
		alias = strings.TrimSpace(alias)
		if alias != "" {
			aliases = append(aliases, alias)
		}
	}
	return aliases
}

func operationsTUI(ctx context.Context, c config.Config, path string, reader *bufio.Reader, ui *terminalUI) error {
	services := configuredServicesFor(c, path)
	for {
		ui.clear()
		ui.brand("Long-term operations", "Inspect the host and keep services healthy")
		ui.panel("READ-ONLY", "These views do not change server state")
		backupAction := "backup inventory"
		if !c.Backups.Enabled {
			backupAction += " (disabled)"
		}
		ui.panel("ACTIONS", "1  host resource stats\n2  recent service logs\n3  "+backupAction+"\n0  back")
		switch selectMenu(reader, ui, "Long-term operations", "1",
			selectorChoice{Value: "1", Label: "host resource stats"},
			selectorChoice{Value: "2", Label: "recent service logs"},
			selectorChoice{Value: "3", Label: backupAction},
			selectorChoice{Value: "0", Label: "back"},
		) {
		case "1":
			ui.clear()
			ui.brand("Host resource stats", "A point-in-time view of capacity and service failures")
			if err := ops.Stats(ctx, ui); err != nil {
				ui.warn(err.Error())
			}
			pause(reader, ui)
		case "2":
			ui.clear()
			ui.brand("Service logs", "Recent entries from the system journal")
			for i, service := range services {
				fmt.Fprintf(ui, "%d  %s\n", i+1, service)
			}
			fmt.Fprintln(ui, "s  system boot log\n0  back")
			serviceOptions := append([]string(nil), services...)
			serviceOptions = append(serviceOptions, "system", "0")
			choice := selectOption(reader, ui, "Service", services[0], serviceOptions...)
			if choice == "0" {
				continue
			}
			service := ""
			if choice == "system" {
				service = "system"
			} else {
				service = choice
			}
			lineCount := prompt(reader, ui, "Lines", "50")
			lines := 50
			if n, err := parsePositive(lineCount); err == nil {
				lines = n
			}
			if err := ops.Logs(ctx, service, lines, ui); err != nil {
				ui.warn(err.Error())
			}
			pause(reader, ui)
		case "3":
			ui.clear()
			ui.brand("Backup inventory", "Review artifacts produced by the configured backup job")
			if !c.Backups.Enabled {
				ui.warn("Backups are disabled in Stack settings.")
				pause(reader, ui)
				continue
			}
			ui.muted("Destination: " + c.Backups.Destination)
			if err := ops.BackupFiles(ctx, c.Backups.Destination, ui); err != nil {
				ui.warn(err.Error())
			}
			pause(reader, ui)
		case "0", "q", "Q":
			return nil
		default:
			ui.warn("Unknown selection.")
		}
	}
}

func configuredServices(c config.Config) []string {
	return configuredServicesFor(c, "")
}

func databaseInstances(c config.Config, path string) []managed.Service {
	inventory := managed.Inventory{}
	if inventory, err := managed.Load(managed.StatePath); err == nil {
		return databaseInstancesFrom(inventory, c, path)
	}
	return databaseInstancesFrom(inventory, c, path)
}

func databaseInstancesFrom(inventory managed.Inventory, c config.Config, path string) []managed.Service {
	instances := make([]managed.Service, 0)
	seen := map[string]bool{}
	for _, service := range inventory.Services {
		if service.Kind != "database" || seen[service.Key] {
			continue
		}
		instances = append(instances, service)
		seen[service.Key] = true
	}
	for _, service := range managed.DesiredServices(c, path) {
		if service.Kind != "database" || seen[service.Key] {
			continue
		}
		instances = append(instances, service)
		seen[service.Key] = true
	}
	sort.Slice(instances, func(i, j int) bool {
		if instances[i].Name == instances[j].Name {
			return instances[i].Key < instances[j].Key
		}
		return instances[i].Name < instances[j].Name
	})
	return instances
}

func configuredServicesFor(c config.Config, path string) []string {
	services := []string{webServiceName(c.WebServer.Provider)}
	seen := map[string]bool{}
	for _, service := range databaseInstances(c, path) {
		if seen[service.Name] {
			continue
		}
		seen[service.Name] = true
		services = append(services, service.Name)
	}
	if c.Access.FTP.Enabled {
		services = append(services, "vsftpd")
	}
	return services
}

func webServiceName(providerName string) string {
	switch providerName {
	case "apache":
		return "apache2"
	case "openlitespeed":
		return "lsws"
	default:
		return "nginx"
	}
}

func parseChoice(value string, max int) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 1 || n > max {
		return 0, fmt.Errorf("invalid choice")
	}
	return n, nil
}

func parsePositive(value string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 1 || n > 500 {
		return 0, fmt.Errorf("invalid line count")
	}
	return n, nil
}

func pause(reader *bufio.Reader, ui *terminalUI) {
	prompt(reader, ui, "Press enter to continue", "")
}

func firewallTUI(ctx context.Context, path string, in io.Reader, ui *terminalUI) error {
	c, err := config.Load(path)
	if err != nil {
		return err
	}
	p, err := platform.Detect()
	if err != nil {
		return err
	}
	reader := bufio.NewReader(in)
	for {
		ui.clear()
		ui.brand("Firewall management", "Review and apply the host access policy")
		ui.panel("POLICY", "Configured policy  "+ui.status(enabledLabel(c.Firewall.Enabled), c.Firewall.Enabled))
		policySuffix := ""
		if !c.Firewall.Enabled {
			policySuffix = " (disabled)"
		}
		ui.panel("ACTIONS", "1  show firewall status\n2  preview configured policy"+policySuffix+"\n3  apply configured policy"+policySuffix+"\n4  disable firewall\n0  back")
		switch selectMenu(reader, ui, "Firewall management", "1",
			selectorChoice{Value: "1", Label: "show firewall status"},
			selectorChoice{Value: "2", Label: "preview configured policy" + policySuffix},
			selectorChoice{Value: "3", Label: "apply configured policy" + policySuffix},
			selectorChoice{Value: "4", Label: "disable firewall"},
			selectorChoice{Value: "0", Label: "back"},
		) {
		case "1":
			operation, err := provider.FirewallStatus(p)
			if err != nil {
				return err
			}
			operation.Print(ui)
			if err := executor.Apply(ctx, operation, reader, ui, ui); err != nil {
				ui.warn("Status check failed: " + err.Error())
			}
		case "2":
			if !c.Firewall.Enabled {
				ui.warn("Firewall policy is disabled in Stack settings.")
				continue
			}
			operation, err := provider.Firewall(c, p)
			if err != nil {
				return err
			}
			operation.Print(ui)
		case "3":
			if !c.Firewall.Enabled {
				ui.warn("Firewall policy is disabled in Stack settings.")
				continue
			}
			operation, err := provider.Firewall(c, p)
			if err != nil {
				return err
			}
			operation.Print(ui)
			if yesNo(selectOption(reader, ui, "Apply firewall policy?", "n", "y", "n")) {
				if err := executor.Apply(ctx, operation, reader, ui, ui); err != nil {
					return err
				}
			} else {
				ui.muted("Cancelled.")
			}
		case "4":
			operation, err := provider.DisableFirewall(p)
			if err != nil {
				return err
			}
			operation.Print(ui)
			fmt.Fprint(ui, "Type DISABLE to turn off the system firewall: ")
			answer, _ := reader.ReadString('\n')
			if strings.TrimSpace(answer) != "DISABLE" {
				ui.muted("Cancelled.")
				break
			}
			if err := executor.Apply(ctx, operation, reader, ui, ui); err != nil {
				return err
			}
		case "0", "q", "Q":
			return nil
		default:
			ui.warn("Unknown selection.")
		}
	}
}

// terminalUI is intentionally small: the TUI remains dependency-light, but has
// one place for its visual language and can gracefully fall back to plain text.
type terminalUI struct {
	io.Writer
	ansi  bool
	input *os.File
}

const (
	panelInnerWidth       = 70
	dashboardLabelWidth   = 26
	maxDashboardSelection = 12
)

func newTerminalUI(w io.Writer) *terminalUI {
	ansi := false
	if f, ok := w.(*os.File); ok {
		if info, err := f.Stat(); err == nil {
			ansi = info.Mode()&os.ModeCharDevice != 0 && os.Getenv("TERM") != "dumb"
		}
	}
	return &terminalUI{Writer: w, ansi: ansi}
}

func attachTerminalInput(ui *terminalUI, in io.Reader) {
	if file, ok := in.(*os.File); ok && isTerminal(file) {
		ui.input = file
	}
}

func (ui *terminalUI) paint(code, value string) string {
	if !ui.ansi {
		return value
	}
	return "\033[" + code + "m" + value + "\033[0m"
}

func (ui *terminalUI) clear() {
	if ui.ansi {
		fmt.Fprint(ui, "\033[2J\033[H")
	}
}

func (ui *terminalUI) brand(section, subtitle string) {
	if !ui.ansi {
		fmt.Fprintln(ui, "poorman "+section)
		fmt.Fprintln(ui, subtitle)
		fmt.Fprintln(ui, strings.Repeat("-", 72))
		return
	}
	fmt.Fprintln(ui, ui.paint("38;5;45;1", "◆ POORMAN")+ui.paint("38;5;244", "  /  ")+ui.paint("38;5;255;1", section))
	fmt.Fprintln(ui, ui.paint("38;5;244", "  "+subtitle))
	fmt.Fprintln(ui, ui.paint("38;5;238", "  "+strings.Repeat("─", 72)))
}

func (ui *terminalUI) panel(title, body string) {
	lines := strings.Split(body, "\n")
	innerWidth := panelInnerWidth
	for _, line := range lines {
		if width := displayWidth(line); width > innerWidth {
			innerWidth = width
		}
	}
	lineWidth := innerWidth - displayWidth(title) - 1
	if lineWidth < 0 {
		lineWidth = 0
	}
	fmt.Fprintln(ui, ui.paint("38;5;238", "╭─ ")+ui.paint("38;5;45;1", title)+ui.paint("38;5;238", " "+strings.Repeat("─", lineWidth)+"╮"))
	for _, line := range lines {
		fmt.Fprintf(ui, "%s %s %s\n", ui.paint("38;5;238", "│"), padPanelLine(line, innerWidth), ui.paint("38;5;238", "│"))
	}
	fmt.Fprintln(ui, ui.paint("38;5;238", "╰"+strings.Repeat("─", innerWidth+2)+"╯"))
}

func displayWidth(value string) int {
	return utf8.RuneCountInString(stripANSI(value))
}

func stripANSI(value string) string {
	var clean strings.Builder
	for i := 0; i < len(value); {
		if value[i] == 0x1b && i+1 < len(value) && value[i+1] == '[' {
			i += 2
			for i < len(value) {
				b := value[i]
				i++
				if b >= '@' && b <= '~' {
					break
				}
			}
			continue
		}
		_, size := utf8.DecodeRuneInString(value[i:])
		clean.WriteString(value[i : i+size])
		i += size
	}
	return clean.String()
}

func padPanelLine(value string, width int) string {
	return value + strings.Repeat(" ", width-displayWidth(value))
}

func (ui *terminalUI) dashboard(c config.Config, path string) {
	ui.dashboardSelected(c, path, 1)
}

func (ui *terminalUI) dashboardSelected(c config.Config, path string, selected int) {
	ui.brand("operations", "A calm control surface for your self-hosted stack")
	db := "none"
	role := "—"
	if c.Database != nil {
		db, role = c.Database.Provider, c.Database.Role
	}
	site := "no sites"
	if len(c.Sites) > 0 {
		site = c.Sites[0].Domain
		if len(c.Sites) > 1 {
			site += fmt.Sprintf(" + %d more", len(c.Sites)-1)
		}
	}
	databaseLine := fmt.Sprintf("%s (%s)", db, role)
	instances := databaseInstances(c, path)
	if len(instances) > 1 {
		labels := make([]string, 0, len(instances))
		for _, instance := range instances {
			labels = append(labels, managed.InstanceLabel(instance))
		}
		databaseLine += "\ninstances " + strings.Join(labels, ", ")
	}
	ui.panel("STACK", fmt.Sprintf("web       %s\ndatabase  %s\nsite      %s\nconfig    %s", c.WebServer.Provider, databaseLine, site, path))
	ui.panel("GUARDRAILS", fmt.Sprintf("https   %s     firewall  %s     backups  %s", ui.status(enabledLabel(c.TLS.Enabled), c.TLS.Enabled), ui.status(enabledLabel(c.Firewall.Enabled), c.Firewall.Enabled), ui.status(enabledLabel(c.Backups.Enabled), c.Backups.Enabled)))
	replicationAction := "replication status"
	if c.Database == nil || c.Database.Role == "standalone" || c.Database.Role == "" {
		replicationAction += " (not configured)"
	}
	backupAction := "run backup"
	if !c.Backups.Enabled {
		backupAction += " (disabled)"
	}
	actions := []string{
		dashboardActionLine(1, -1, selected, "preview plan", ""),
		dashboardActionLine(2, -1, selected, "apply configuration", ""),
		dashboardActionLine(3, -1, selected, "health status", ""),
		dashboardActionLine(4, -1, selected, backupAction, ""),
		dashboardActionLine(5, -1, selected, replicationAction, ""),
		dashboardActionLine(6, -1, selected, "Firewall management", ""),
		dashboardActionLine(7, -1, selected, "long-term operations", ""),
		dashboardActionLine(8, -1, selected, "Virtual hosts", ""),
		dashboardActionLine(9, -1, selected, "Stack settings", ""),
		dashboardActionLine(10, -1, selected, "guided replica setup", ""),
		dashboardActionLine(11, -1, selected, "guardrails & backups", ""),
		dashboardActionLine(12, -1, selected, "Database management", ""),
		dashboardActionLine(0, -1, selected, "exit", ""),
	}
	ui.panel("ACTIONS", strings.Join(actions, "\n"))
	fmt.Fprintln(ui, ui.paint("38;5;244", "  ↑/↓ choose  ·  enter confirm  ·  q exit"))
}

func dashboardActionLine(left, right, selected int, leftLabel, rightLabel string) string {
	leftMarker, rightMarker := "  ", "  "
	if selected == left {
		leftMarker = "> "
	}
	if selected == right {
		rightMarker = "> "
	}
	if right < 0 {
		return fmt.Sprintf("%s%-2d  %s", leftMarker, left, leftLabel)
	}
	return fmt.Sprintf("%s%-2d  %-*s%s%-2d  %s", leftMarker, left, dashboardLabelWidth, leftLabel, rightMarker, right, rightLabel)
}

func dashboardChoice(in io.Reader, reader *bufio.Reader, ui *terminalUI, c config.Config, path string) string {
	file, ok := in.(*os.File)
	if ok && isTerminal(file) && rawTerminalAvailable(file) {
		if choice, rawOK := rawDashboardChoice(file, ui, c, path); rawOK {
			return choice
		}
	}
	ui.dashboard(c, path)
	return selectMenu(reader, ui, "Select action", "1",
		selectorChoice{Value: "1", Label: "preview plan"},
		selectorChoice{Value: "2", Label: "apply changes"},
		selectorChoice{Value: "3", Label: "health check"},
		selectorChoice{Value: "4", Label: "backup and restore"},
		selectorChoice{Value: "5", Label: "replication"},
		selectorChoice{Value: "6", Label: "firewall"},
		selectorChoice{Value: "7", Label: "long-term operations"},
		selectorChoice{Value: "8", Label: "virtual hosts"},
		selectorChoice{Value: "9", Label: "stack settings"},
		selectorChoice{Value: "10", Label: "guided replica setup"},
		selectorChoice{Value: "11", Label: "guardrails and backups"},
		selectorChoice{Value: "12", Label: "database management"},
		selectorChoice{Value: "0", Label: "exit"},
	)
}

func rawTerminalAvailable(file *os.File) bool {
	check := exec.Command("stty", "-g")
	check.Stdin = file
	return check.Run() == nil
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func rawDashboardChoice(file *os.File, ui *terminalUI, c config.Config, path string) (string, bool) {
	getState := exec.Command("stty", "-g")
	getState.Stdin = file
	state, err := getState.Output()
	if err != nil {
		return "", false
	}
	defer func() {
		restore := exec.Command("stty", strings.TrimSpace(string(state)))
		restore.Stdin = file
		_ = restore.Run()
	}()
	raw := exec.Command("stty", "-icanon", "-echo", "min", "1", "time", "0")
	raw.Stdin = file
	if err := raw.Run(); err != nil {
		return "", false
	}

	selected := 1
	typed := ""
	ui.clear()
	ui.dashboardSelected(c, path, selected)
	for {
		b, ok := readRawByte(file)
		if !ok {
			return "", false
		}
		switch b {
		case '\r', '\n':
			if typed != "" {
				return typed, true
			}
			return strconv.Itoa(selected), true
		case 'q', 'Q':
			return "q", true
		case 0x1b:
			prefix, ok := readRawMaybeByte(file)
			if !ok {
				return "0", true
			}
			if prefix != '[' {
				return "0", true
			}
			code, ok := readRawMaybeByte(file)
			if !ok {
				return "0", true
			}
			switch code {
			case 'A':
				selected--
				if selected < 0 {
					selected = maxDashboardSelection
				}
			case 'B':
				selected++
				if selected > maxDashboardSelection {
					selected = 0
				}
			default:
				continue
			}
			ui.clear()
			ui.dashboardSelected(c, path, selected)
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			typed += string(b)
		case 8, 127:
			if len(typed) > 0 {
				typed = typed[:len(typed)-1]
			}
		}
	}
}

func (ui *terminalUI) status(label string, good bool) string {
	if good {
		return ui.paint("38;5;42;1", "● "+label)
	}
	return ui.paint("38;5;214;1", "● "+label)
}

func (ui *terminalUI) success(message string) {
	fmt.Fprintln(ui, ui.paint("38;5;42;1", "✓ ")+message)
}
func (ui *terminalUI) warn(message string)  { fmt.Fprintln(ui, ui.paint("38;5;214;1", "! ")+message) }
func (ui *terminalUI) muted(message string) { fmt.Fprintln(ui, ui.paint("38;5;244", message)) }

func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func prompt(reader *bufio.Reader, out io.Writer, label, fallback string) string {
	fmt.Fprintf(out, "%s [%s]: ", label, fallback)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return fallback
	}
	return answer
}

// selectOption is the finite-choice counterpart to prompt. Interactive TUI
// sessions get a real arrow-key selector; pipes, tests, and scripted setup
// retain a deterministic numbered/value input mode.
func selectOption(reader *bufio.Reader, ui *terminalUI, label, fallback string, options ...string) string {
	choices := make([]selectorChoice, 0, len(options))
	for _, option := range options {
		choices = append(choices, selectorChoice{Value: option, Label: option})
	}
	return selectChoices(reader, ui, label, fallback, choices...)
}

type selectorChoice struct {
	Value string
	Label string
}

func selectMenu(reader *bufio.Reader, ui *terminalUI, label, fallback string, options ...selectorChoice) string {
	return selectChoices(reader, ui, label, fallback, options...)
}

func selectChoices(reader *bufio.Reader, ui *terminalUI, label, fallback string, options ...selectorChoice) string {
	if len(options) == 0 {
		return prompt(reader, ui, label, fallback)
	}
	if ui.input != nil && isTerminal(ui.input) && reader.Buffered() == 0 {
		if value, ok := rawSelectOption(ui, label, fallback, options); ok {
			return value
		}
	}
	fmt.Fprintf(ui, "%s [%s]\n", label, fallback)
	for i, option := range options {
		fmt.Fprintf(ui, "  %d  %s\n", i+1, option.Label)
	}
	answer, err := reader.ReadString('\n')
	if err == io.EOF {
		return escapeOption(fallback, options)
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return fallback
	}
	return selectedOptionValue(answer, fallback, options)
}

func selectedOptionValue(answer, fallback string, options []selectorChoice) string {
	for i, option := range options {
		if answer == strconv.Itoa(i+1) || strings.EqualFold(answer, option.Value) || strings.EqualFold(answer, option.Label) {
			return option.Value
		}
	}
	lower := strings.ToLower(answer)
	for _, option := range options {
		if (lower == "yes" && strings.EqualFold(option.Value, "y")) || (lower == "no" && strings.EqualFold(option.Value, "n")) {
			return option.Value
		}
	}
	return fallback
}

func rawSelectOption(ui *terminalUI, label, fallback string, options []selectorChoice) (string, bool) {
	file := ui.input
	getState := exec.Command("stty", "-g")
	getState.Stdin = file
	state, err := getState.Output()
	if err != nil {
		return "", false
	}
	defer func() {
		restore := exec.Command("stty", strings.TrimSpace(string(state)))
		restore.Stdin = file
		_ = restore.Run()
	}()
	raw := exec.Command("stty", "-icanon", "-echo", "min", "1", "time", "0")
	raw.Stdin = file
	if err := raw.Run(); err != nil {
		return "", false
	}

	selected := 0
	for i, option := range options {
		if strings.EqualFold(option.Value, fallback) {
			selected = i
			break
		}
	}
	render := func() {
		fmt.Fprint(ui, "\033[2J\033[H")
		fmt.Fprintln(ui, ui.paint("38;5;45;1", label))
		for i, option := range options {
			marker := "  "
			if i == selected {
				marker = "> "
			}
			fmt.Fprintf(ui, "%s%s\n", marker, option.Label)
		}
		fmt.Fprintln(ui, ui.paint("38;5;244", "Use ↑/↓ and Enter"))
	}
	render()
	for {
		b, ok := readRawByte(file)
		if !ok {
			return "", false
		}
		switch b {
		case '\r', '\n':
			fmt.Fprintf(ui, "Selected: %s\n", options[selected].Label)
			return options[selected].Value, true
		case 'q', 'Q':
			return escapeOption(fallback, options), true
		case 'k':
			selected--
			if selected < 0 {
				selected = len(options) - 1
			}
			render()
		case 'j':
			selected = (selected + 1) % len(options)
			render()
		case 0x1b:
			prefix, ok := readRawMaybeByte(file)
			if !ok {
				return escapeOption(fallback, options), true
			}
			if prefix != '[' {
				return escapeOption(fallback, options), true
			}
			code, ok := readRawMaybeByte(file)
			if !ok {
				return escapeOption(fallback, options), true
			}
			switch code {
			case 'A':
				selected--
				if selected < 0 {
					selected = len(options) - 1
				}
				render()
			case 'B':
				selected = (selected + 1) % len(options)
				render()
			}
		}
	}
}

func readRawByte(file *os.File) (byte, bool) {
	for {
		var b [1]byte
		n, err := file.Read(b[:])
		if n == 1 {
			return b[0], true
		}
		if err != nil {
			return 0, false
		}
	}
}

func readRawMaybeByte(file *os.File) (byte, bool) {
	fd := int(file.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		return 0, false
	}
	defer func() { _ = syscall.SetNonblock(fd, false) }()
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		var b [1]byte
		n, err := syscall.Read(fd, b[:])
		if n == 1 {
			return b[0], true
		}
		if err != syscall.EAGAIN && err != syscall.EWOULDBLOCK && err != nil {
			return 0, false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return 0, false
}

func escapeOption(fallback string, options []selectorChoice) string {
	for _, option := range options {
		if option.Value == "0" {
			return option.Value
		}
	}
	return fallback
}

func initCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
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
	if _, err := os.Stat(*path); err == nil {
		return fmt.Errorf("%s already exists", *path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := config.WriteDefault(*path); err != nil {
		return err
	}
	fmt.Fprintf(out, "Created %s\n", *path)
	return nil
}

func buildPlan(path string) (interface{ Print(io.Writer) }, error) {
	c, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	p, err := platform.Detect()
	if err != nil {
		return nil, err
	}
	return provider.BuildForConfig(c, p, path)
}

func planCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
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
	p, err := buildPlan(*path)
	if err != nil {
		return err
	}
	p.Print(out)
	return nil
}

func applyCommand(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("f", "poorman.json", "configuration file")
	yes := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	plat, err := platform.Detect()
	if err != nil {
		return err
	}
	p, err := provider.BuildForConfig(c, plat, *path)
	if err != nil {
		return err
	}
	reader := inputReader(in)
	p.Print(out)
	if !*yes {
		fmt.Fprint(out, "Apply this plan? [y/N] ")
		answer, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Fprintln(out, "Cancelled.")
			return nil
		}
	}
	if err := ensureConfigSecrets(c, *path, out); err != nil {
		return err
	}
	err = executor.Apply(ctx, p, reader, out, errOut)
	discardBlankInput(reader)
	return err
}

func requireNoArgs(fs *flag.FlagSet) error {
	if fs.NArg() == 0 {
		return nil
	}
	return fmt.Errorf("unexpected argument %q", fs.Arg(0))
}

func ensureDatabasePassword(c config.Config, configPath string, out io.Writer) error {
	return ensureConfigSecrets(c, configPath, out)
}

// requiredConfigSecrets maps environment-variable names to whether poorman may
// safely generate a value. Replica credentials must come from the primary;
// inventing a different value would produce a valid-looking config that can
// never authenticate.
func requiredConfigSecrets(c config.Config) map[string]bool {
	required := map[string]bool{}
	add := func(name string, mayGenerate bool) {
		if name == "" {
			return
		}
		if existing, ok := required[name]; ok {
			// If any consumer requires a shared value from the primary,
			// generating it would silently break replica authentication.
			required[name] = existing && mayGenerate
			return
		}
		required[name] = mayGenerate
	}
	replica := c.Database != nil && c.Database.Role == "replica"
	if c.Database != nil {
		if !replica {
			add(c.Database.PasswordEnv, true)
		}
		if c.Database.Role != "" && c.Database.Role != "standalone" && c.Database.Replication.PasswordEnv != "" {
			add(c.Database.Replication.PasswordEnv, c.Database.Role == "primary")
		}
	}
	if !replica {
		for _, site := range c.Sites {
			if site.WordPress != nil {
				add(site.WordPress.AdminPassEnv, true)
			}
		}
	}
	return required
}

func configSecretNames(c config.Config) map[string]bool {
	names := map[string]bool{}
	if c.Database != nil {
		if c.Database.PasswordEnv != "" {
			names[c.Database.PasswordEnv] = true
		}
		if c.Database.Replication.PasswordEnv != "" {
			names[c.Database.Replication.PasswordEnv] = true
		}
	}
	for _, site := range c.Sites {
		if site.WordPress != nil && site.WordPress.AdminPassEnv != "" {
			names[site.WordPress.AdminPassEnv] = true
		}
	}
	return names
}

func ensureConfigSecrets(c config.Config, configPath string, out io.Writer) error {
	required := requiredConfigSecrets(c)
	if len(required) == 0 {
		return nil
	}
	secretPath := configPath + ".secrets"
	values, err := readSecretValues(secretPath)
	if err != nil {
		return err
	}
	// Load persisted values first, then reject missing shared replica secrets
	// before generating anything else. A failed apply must not leave behind a
	// partially initialized set of credentials.
	for name := range required {
		if os.Getenv(name) == "" && values[name] != "" {
			if err := os.Setenv(name, values[name]); err != nil {
				return err
			}
		}
	}
	for name, mayGenerate := range required {
		if os.Getenv(name) == "" && !mayGenerate {
			return fmt.Errorf("replica requires %s from the primary; export it or copy it into %s", name, secretPath)
		}
	}
	generated := false
	for name, mayGenerate := range required {
		if value := os.Getenv(name); value != "" {
			continue
		}
		if !mayGenerate {
			continue
		}
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return fmt.Errorf("generate password for %s: %w", name, err)
		}
		value := base64.RawURLEncoding.EncodeToString(raw)
		values[name] = value
		if err := os.Setenv(name, value); err != nil {
			return err
		}
		generated = true
	}
	if generated {
		if err := writeSecretValues(secretPath, values); err != nil {
			return err
		}
		fmt.Fprintf(out, "Generated and saved required passwords to %s\n", secretPath)
	}
	return nil
}

func copyConfigSecrets(sourceConfigPath, destinationConfigPath string, c config.Config) error {
	source, err := readSecretValues(sourceConfigPath + ".secrets")
	if err != nil {
		return err
	}
	destinationPath := destinationConfigPath + ".secrets"
	destination, err := readSecretValues(destinationPath)
	if err != nil {
		return err
	}
	changed := false
	for name := range configSecretNames(c) {
		value := source[name]
		if value == "" {
			value = os.Getenv(name)
		}
		if value != "" && destination[name] == "" {
			destination[name] = value
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return writeSecretValues(destinationPath, destination)
}

func readSecretValues(path string) (map[string]string, error) {
	values := map[string]string{}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read secrets file %s: %w", path, err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(name) != "" && value != "" {
			values[strings.TrimSpace(name)] = value
		}
	}
	return values, nil
}

func writeSecretValues(path string, values map[string]string) error {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	var content strings.Builder
	for _, name := range names {
		fmt.Fprintf(&content, "%s=%s\n", name, values[name])
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		return fmt.Errorf("write secrets file %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure secrets file %s: %w", path, err)
	}
	return nil
}

// inputReader preserves an existing buffer. Wrapping a *bufio.Reader in a
// second reader can read ahead past a confirmation newline, consuming an
// Enter intended for the dashboard after the operation finishes.
func inputReader(in io.Reader) *bufio.Reader {
	if reader, ok := in.(*bufio.Reader); ok {
		return reader
	}
	return bufio.NewReader(in)
}

// An Enter pressed during a command is intentionally not sent to the
// command. Remove the resulting blank line before the next dashboard prompt
// so it cannot be interpreted as that prompt's default action.
func discardBlankInput(reader *bufio.Reader) {
	for {
		// Peek blocks when the terminal has no queued input. Only inspect
		// bytes that the reader already buffered while the command was running.
		if reader.Buffered() == 0 {
			return
		}
		line, err := reader.Peek(1)
		if err != nil || line[0] != '\n' {
			return
		}
		_, _ = reader.ReadString('\n')
	}
}
