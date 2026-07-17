package app

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/ops"
)

func protectionTUI(ctx context.Context, path string, reader *bufio.Reader, ui *terminalUI) error {
	for {
		c, err := config.Load(path)
		if err != nil {
			return err
		}
		ui.clear()
		ui.brand("Guardrails & backups", "Turn on the protections that keep a live server recoverable")
		offsite := "disabled"
		if c.Backups.Offsite != nil {
			offsite = c.Backups.Offsite.Provider + "://" + c.Backups.Offsite.Bucket
		}
		ui.panel("CURRENT", fmt.Sprintf("https       %s\nfirewall    %s\nbackups     %s\nbackup path %s\nschedule    %s\nkeep local  %d days\noffsite     %s", enabledLabel(c.TLS.Enabled), enabledLabel(c.Firewall.Enabled), enabledLabel(c.Backups.Enabled), defaultValue(c.Backups.Destination, "not configured"), defaultValue(c.Backups.Schedule, "not configured"), c.Backups.EffectiveRetentionDays(), offsite))
		ui.panel("ACTIONS", "1  HTTPS and certificate email\n2  firewall\n3  backups and schedule\n4  run backup now\n5  backup inventory\n6  retention and offsite storage\n0  back")
		switch selectMenu(reader, ui, "Guardrails & backups", "1",
			selectorChoice{Value: "1", Label: "HTTPS and certificate email"},
			selectorChoice{Value: "2", Label: "firewall"},
			selectorChoice{Value: "3", Label: "backups and schedule"},
			selectorChoice{Value: "4", Label: "run backup now"},
			selectorChoice{Value: "5", Label: "backup inventory"},
			selectorChoice{Value: "6", Label: "retention and offsite storage"},
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
		case "6":
			if !c.Backups.Enabled {
				ui.warn("Backups are disabled. Enable them in this menu first.")
				pause(reader, ui)
				continue
			}
			configureBackupRetention(&c, reader, ui)
			if err := config.Write(path, c); err != nil {
				ui.warn(err.Error())
				pause(reader, ui)
				continue
			}
			ui.success("Backup retention and offsite settings updated")
		case "0", "q", "Q":
			return nil
		default:
			ui.warn("Unknown selection.")
		}
	}
}

func configureBackupRetention(c *config.Config, reader *bufio.Reader, ui *terminalUI) {
	c.Backups.RetentionDays = promptRetentionDays(reader, ui, "Keep local backups for days", c.Backups.EffectiveRetentionDays())
	enabled := c.Backups.Offsite != nil
	if !yesNo(selectOption(reader, ui, "Copy backups to S3?", enabledDefault(enabled), "y", "n")) {
		c.Backups.Offsite = nil
		return
	}
	offsite := config.OffsiteBackup{Provider: "s3"}
	if c.Backups.Offsite != nil {
		offsite = *c.Backups.Offsite
		offsite.Provider = "s3"
	}
	offsite.Bucket = prompt(reader, ui, "S3 bucket", offsite.Bucket)
	offsite.Prefix = prompt(reader, ui, "S3 key prefix", defaultValue(offsite.Prefix, "poorman"))
	offsite.Region = prompt(reader, ui, "AWS region (blank uses credential defaults)", offsite.Region)
	offsite.Profile = prompt(reader, ui, "AWS profile (blank uses server role/environment)", offsite.Profile)
	offsite.Endpoint = prompt(reader, ui, "S3 endpoint URL (blank uses AWS)", offsite.Endpoint)
	offsite.RetentionDays = promptRetentionDays(reader, ui, "Keep offsite backups for days", offsite.EffectiveRetentionDays(c.Backups.RetentionDays))
	c.Backups.Offsite = &offsite
}

func promptRetentionDays(reader *bufio.Reader, ui *terminalUI, label string, fallback int) int {
	value, err := strconv.Atoi(prompt(reader, ui, label, strconv.Itoa(fallback)))
	if err != nil || value < 1 || value > 36500 {
		ui.warn("Retention must be between 1 and 36500 days; keeping the previous value.")
		return fallback
	}
	return value
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
