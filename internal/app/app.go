package app

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/executor"
	"github.com/chokunplayz/poormanwebctrl/internal/health"
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
		fmt.Fprintln(out, version)
		return nil
	case "help", "--help", "-h":
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
	poorman replica status|promote|setup [-f poorman.json] [--from primary.json]
  poorman backup [--yes]
  poorman version

Start with "poorman init", edit the file, then preview with "poorman plan".`)
}

func statusCommand(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("f", "poorman.json", "configuration file")
	if err := fs.Parse(args); err != nil {
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
		return fmt.Errorf("replica requires status or promote")
	}
	action := args[0]
	if action == "setup" {
		return guidedReplicaSetup(args[1:], in, out)
	}
	fs := flag.NewFlagSet("replica "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("f", "poorman.json", "configuration file")
	yes := fs.Bool("yes", false, "confirm replica promotion")
	if err := fs.Parse(args[1:]); err != nil {
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
	} else {
		return fmt.Errorf("unknown replica action %q", action)
	}
	if err != nil {
		return err
	}
	reader := inputReader(in)
	operation.Print(out)
	if action == "promote" && !*yes {
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
	fs.SetOutput(io.Discard)
	yes := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	operation := plan.Plan{Platform: "local", Steps: []plan.Step{plan.Cmd("Run configured backup", "/usr/local/sbin/poorman-backup", true)}}
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

func tuiCommand(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("f", "poorman.json", "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ui := newTerminalUI(out)
	if _, err := os.Stat(*path); err == nil {
		return tuiDashboard(ctx, *path, in, ui)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	reader := bufio.NewReader(in)
	ui.clear()
	ui.brand("guided setup", "Build a safe, auditable server configuration")
	ui.panel("WEB SERVER", "1  nginx                 recommended\n2  apache\n3  openlitespeed")
	choice := prompt(reader, ui, "Selection", "1")
	providerName := "nginx"
	if choice == "2" {
		providerName = "apache"
	} else if choice == "3" {
		providerName = "openlitespeed"
	}

	domain := prompt(reader, ui, "First site domain", "example.com")
	root := prompt(reader, ui, "Document root", "/var/www/"+domain)
	owner := prompt(reader, ui, "System/SFTP user", "webadmin")
	runtimeName := prompt(reader, ui, "Runtime (php/static)", "php")
	dbChoice := prompt(reader, ui, "Database (mariadb/postgresql/none)", "mariadb")
	var database *config.Database
	if dbChoice != "none" {
		database = &config.Database{Provider: dbChoice, Role: strings.ToLower(prompt(reader, ui, "Database role (standalone/primary/replica)", "standalone")), Name: prompt(reader, ui, "Database name", "website"), User: prompt(reader, ui, "Database user", "website"), PasswordEnv: "POORMAN_DB_PASSWORD"}
		configureReplication(reader, ui, database)
	}
	var wordpress *config.WordPress
	if runtimeName == "php" && database != nil && database.Role != "replica" && strings.EqualFold(prompt(reader, ui, "Install WordPress? (y/N)", "n"), "y") {
		wordpress = &config.WordPress{Title: domain, AdminUser: "admin", AdminEmail: prompt(reader, ui, "WordPress admin email", "admin@"+domain), AdminPassEnv: "POORMAN_WP_ADMIN_PASSWORD"}
	}
	tlsEnabled := strings.EqualFold(prompt(reader, ui, "Enable HTTPS with Let's Encrypt? (Y/n)", "y"), "y")
	backupEnabled := strings.EqualFold(prompt(reader, ui, "Enable nightly backups? (Y/n)", "y"), "y")

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

func configureReplication(reader *bufio.Reader, ui *terminalUI, database *config.Database) {
	if database.Role == "standalone" {
		return
	}
	database.Replication.User = prompt(reader, ui, "Replication user", defaultValue(database.Replication.User, "replicator"))
	database.Replication.PasswordEnv = prompt(reader, ui, "Replication password environment variable", defaultValue(database.Replication.PasswordEnv, "POORMAN_REPLICATION_PASSWORD"))
	if database.Role == "primary" {
		database.Replication.AllowedCIDR = prompt(reader, ui, "Replica allowed CIDR", defaultValue(database.Replication.AllowedCIDR, "10.20.0.0/24"))
		if database.Provider == "postgresql" {
			database.Replication.Slot = prompt(reader, ui, "PostgreSQL replication slot", defaultValue(database.Replication.Slot, "poorman_replica_1"))
		}
		return
	}
	if database.Role == "replica" {
		local := yesNo(prompt(reader, ui, "Is the primary on this machine? (y/N)", "n"))
		primaryHostDefault := "10.20.0.10"
		if local {
			primaryHostDefault = "127.0.0.1"
		}
		database.Replication.PrimaryHost = prompt(reader, ui, "Primary database host", defaultValue(database.Replication.PrimaryHost, primaryHostDefault))
		primaryPort := 5432
		if database.Provider == "mariadb" {
			primaryPort = 3306
		}
		database.Replication.PrimaryPort = promptPort(reader, ui, "Primary database port", database.Replication.PrimaryPort, primaryPort)
		if local {
			replicaPort := primaryPort + 1
			database.Port = promptPort(reader, ui, "Replica database port", database.Port, replicaPort)
			ui.muted("Same-machine replicas use separate ports so the primary and replica can run together.")
		}
		if database.Provider == "postgresql" {
			database.Replication.Slot = prompt(reader, ui, "PostgreSQL replication slot", defaultValue(database.Replication.Slot, "poorman_replica_1"))
			database.DataDir = prompt(reader, ui, "PostgreSQL data directory", defaultValue(database.DataDir, "/var/lib/postgresql/18/main"))
		} else {
			nodeDefault := "2"
			if database.Replication.NodeID > 0 {
				nodeDefault = strconv.Itoa(database.Replication.NodeID)
			}
			nodeID := prompt(reader, ui, "MariaDB replica node ID", nodeDefault)
			database.Replication.NodeID, _ = strconv.Atoi(nodeID)
		}
	}
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
	fs.SetOutput(io.Discard)
	path := fs.String("f", "poorman.json", "configuration file")
	from := fs.String("from", "", "existing primary or stack configuration to copy")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := config.Default()
	var err error
	if _, statErr := os.Stat(*path); statErr == nil {
		c, err = config.Load(*path)
		if err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	} else if *from != "" {
		c, err = config.Load(*from)
		if err != nil {
			return err
		}
	}
	ui := newTerminalUI(out)
	reader := bufio.NewReader(in)
	ui.brand("guided replica setup", "Attach a database replica with safe same-machine defaults")
	providerName := "mariadb"
	if c.Database != nil {
		providerName = c.Database.Provider
	}
	providerName = strings.ToLower(prompt(reader, ui, "Database (mariadb/postgresql)", providerName))
	database := &config.Database{Provider: providerName, Role: "replica", PasswordEnv: "POORMAN_DB_PASSWORD"}
	if c.Database != nil {
		copy := *c.Database
		database = &copy
		database.Provider = providerName
		database.Role = "replica"
	}
	configureReplication(reader, ui, database)
	c.Database = database
	if err := config.Write(*path, c); err != nil {
		return err
	}
	ui.success(fmt.Sprintf("Replica configuration saved to %s", *path))
	ui.muted(fmt.Sprintf("Preview it with: poorman plan -f %s", *path))
	return nil
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
		ui.dashboard(c, path)
		switch prompt(reader, ui, "Select action", "1") {
		case "1":
			if err := planCommand([]string{"-f", path}, ui); err != nil {
				return err
			}
		case "2":
			if err := applyCommand(ctx, []string{"-f", path}, reader, ui, ui); err != nil {
				return err
			}
		case "3":
			if err := statusCommand(ctx, []string{"-f", path}, ui); err != nil {
				ui.warn("Health warning: " + err.Error())
			}
		case "4":
			if err := backupCommand(ctx, nil, reader, ui, ui); err != nil {
				return err
			}
		case "5":
			if err := replicaCommand(ctx, []string{"status", "-f", path}, reader, ui, ui); err != nil {
				ui.warn("Replication status unavailable: " + err.Error())
			}
		case "6":
			if err := firewallTUI(ctx, path, reader, ui); err != nil {
				ui.warn("Firewall operation unavailable: " + err.Error())
			}
		case "7":
			if err := operationsTUI(ctx, c, reader, ui); err != nil {
				ui.warn("Operations unavailable: " + err.Error())
			}
		case "8":
			if err := vhostsTUI(path, reader, ui); err != nil {
				ui.warn("Virtual host management unavailable: " + err.Error())
			}
		case "9":
			if err := stackSettingsTUI(path, reader, ui); err != nil {
				ui.warn("Stack settings unavailable: " + err.Error())
			}
		case "10":
			if err := guidedReplicaSetup([]string{"-f", path}, reader, ui); err != nil {
				ui.warn("Replica setup unavailable: " + err.Error())
			}
		case "0", "q", "Q":
			return nil
		default:
			ui.warn("Unknown selection.")
		}
	}
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
		switch prompt(reader, ui, "Select action", "1") {
		case "1":
			c.WebServer.Provider = strings.ToLower(prompt(reader, ui, "Web server (nginx/apache/openlitespeed)", c.WebServer.Provider))
		case "2":
			if err := adjustDatabase(&c, reader, ui); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
				continue
			}
		case "3":
			c.TLS.Enabled = yesNo(prompt(reader, ui, "Enable HTTPS with Let's Encrypt? (Y/n)", enabledDefault(c.TLS.Enabled)))
			if c.TLS.Enabled {
				c.TLS.Email = prompt(reader, ui, "Certificate email", c.TLS.Email)
			}
		case "4":
			c.Firewall.Enabled = yesNo(prompt(reader, ui, "Enable firewall? (Y/n)", enabledDefault(c.Firewall.Enabled)))
		case "5":
			c.Backups.Enabled = yesNo(prompt(reader, ui, "Enable nightly backups? (Y/n)", enabledDefault(c.Backups.Enabled)))
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

func adjustDatabase(c *config.Config, reader *bufio.Reader, ui *terminalUI) error {
	currentProvider, currentRole := "mariadb", "standalone"
	name, user, passwordEnv := "website", "website", "POORMAN_DB_PASSWORD"
	if c.Database != nil {
		currentProvider, currentRole = c.Database.Provider, c.Database.Role
		name, user, passwordEnv = c.Database.Name, c.Database.User, c.Database.PasswordEnv
	}
	providerName := strings.ToLower(prompt(reader, ui, "Database (mariadb/postgresql/none)", currentProvider))
	if providerName == "none" {
		c.Database = nil
		return nil
	}
	database := &config.Database{Provider: providerName, Role: strings.ToLower(prompt(reader, ui, "Database role (standalone/primary/replica)", currentRole)), Name: prompt(reader, ui, "Database name", name), User: prompt(reader, ui, "Database user", user), PasswordEnv: prompt(reader, ui, "Database password environment variable", passwordEnv)}
	configureReplication(reader, ui, database)
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
	return strings.EqualFold(strings.TrimSpace(value), "y")
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
		switch prompt(reader, ui, "Select action", "1") {
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
		Runtime: prompt(reader, ui, "Runtime (static/php)", "static"),
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
	site := c.Sites[i]
	site.Domain = prompt(reader, ui, "Domain", site.Domain)
	site.Root = prompt(reader, ui, "Document root", site.Root)
	site.Owner = prompt(reader, ui, "System/SFTP user", site.Owner)
	site.Runtime = prompt(reader, ui, "Runtime (static/php)", defaultValue(site.Runtime, "static"))
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
	site := c.Sites[i]
	if strings.ToLower(prompt(reader, ui, "Remove "+site.Domain+"? (y/N)", "n")) != "y" {
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
	choice := prompt(reader, ui, "Virtual host number", "1")
	n, err := parseChoice(choice, len(c.Sites))
	if err != nil {
		return 0, fmt.Errorf("invalid virtual host number")
	}
	return n - 1, nil
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

func operationsTUI(ctx context.Context, c config.Config, reader *bufio.Reader, ui *terminalUI) error {
	services := configuredServices(c)
	for {
		ui.clear()
		ui.brand("Long-term operations", "Inspect the host and keep services healthy")
		ui.panel("READ-ONLY", "These views do not change server state")
		ui.panel("ACTIONS", "1  host resource stats\n2  recent service logs\n3  backup inventory\n0  back")
		switch prompt(reader, ui, "Select action", "1") {
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
			choice := prompt(reader, ui, "Service", "1")
			if choice == "0" {
				continue
			}
			service := ""
			if choice == "s" || choice == "S" {
				service = "system"
			} else if n, err := parseChoice(choice, len(services)); err == nil {
				service = services[n-1]
			} else {
				ui.warn("Unknown service.")
				pause(reader, ui)
				continue
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
	services := []string{webServiceName(c.WebServer.Provider)}
	if c.Database != nil {
		services = append(services, c.Database.Provider)
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
		ui.panel("ACTIONS", "1  show firewall status\n2  preview configured policy\n3  apply configured policy\n4  disable firewall\n0  back")
		switch prompt(reader, ui, "Select action", "1") {
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
			operation, err := provider.Firewall(c, p)
			if err != nil {
				return err
			}
			operation.Print(ui)
		case "3":
			operation, err := provider.Firewall(c, p)
			if err != nil {
				return err
			}
			operation.Print(ui)
			fmt.Fprint(ui, "Apply firewall policy? [y/N] ")
			answer, _ := reader.ReadString('\n')
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				ui.muted("Cancelled.")
				break
			}
			if err := executor.Apply(ctx, operation, reader, ui, ui); err != nil {
				return err
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
	ansi bool
}

func newTerminalUI(w io.Writer) *terminalUI {
	ansi := false
	if f, ok := w.(*os.File); ok {
		if info, err := f.Stat(); err == nil {
			ansi = info.Mode()&os.ModeCharDevice != 0 && os.Getenv("TERM") != "dumb"
		}
	}
	return &terminalUI{Writer: w, ansi: ansi}
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
	fmt.Fprintln(ui, ui.paint("38;5;238", "╭─ ")+ui.paint("38;5;45;1", title)+ui.paint("38;5;238", " "+strings.Repeat("─", 65-len(title))+"╮"))
	for _, line := range strings.Split(body, "\n") {
		fmt.Fprintf(ui, "%s %s %s\n", ui.paint("38;5;238", "│"), line, ui.paint("38;5;238", "│"))
	}
	fmt.Fprintln(ui, ui.paint("38;5;238", "╰"+strings.Repeat("─", 72)+"╯"))
}

func (ui *terminalUI) dashboard(c config.Config, path string) {
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
	ui.panel("STACK", fmt.Sprintf("web       %s\ndatabase  %s (%s)\nsite      %s\nconfig    %s", c.WebServer.Provider, db, role, site, path))
	ui.panel("GUARDRAILS", fmt.Sprintf("https   %s     firewall  %s     backups  %s", ui.status(enabledLabel(c.TLS.Enabled), c.TLS.Enabled), ui.status(enabledLabel(c.Firewall.Enabled), c.Firewall.Enabled), ui.status(enabledLabel(c.Backups.Enabled), c.Backups.Enabled)))
	ui.panel("ACTIONS", "1  preview plan          5  replication status\n2  apply configuration    6  Firewall management\n3  health status           7  long-term operations\n4  run backup              8  Virtual hosts\n9  Stack settings         10 guided replica setup\n0  exit")
	fmt.Fprintln(ui, ui.paint("38;5;244", "  ↑/↓ choose  ·  enter confirm  ·  q exit"))
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

func initCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("f", "poorman.json", "configuration file")
	if err := fs.Parse(args); err != nil {
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
	return provider.WebServer(c, p)
}

func planCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("f", "poorman.json", "configuration file")
	if err := fs.Parse(args); err != nil {
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
	fs.SetOutput(io.Discard)
	path := fs.String("f", "poorman.json", "configuration file")
	yes := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(args); err != nil {
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
	p, err := provider.WebServer(c, plat)
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
	err = executor.Apply(ctx, p, reader, out, errOut)
	discardBlankInput(reader)
	return err
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
