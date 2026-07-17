package provider

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/plan"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

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
	if !c.AnySiteTLSEnabled() {
		return
	}
	for _, s := range c.Sites {
		if !c.SiteTLSEnabled(s) {
			continue
		}
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
	offsitePrefix := ""
	if c.Backups.Offsite != nil {
		offsitePrefix = strings.Trim(c.Backups.Offsite.Prefix, "/")
	}
	managedMariaDBInstance := c.Database != nil && isManagedMariaDBInstance(*c.Database)
	if managedMariaDBInstance {
		layout := mariaDBReplicaLayout(*c.Database)
		destination = filepath.Join(destination, layout.Service)
		scriptPath += "-" + layout.Service
		cronPath += "-" + layout.Service
		// The primary configuration owns same-host website backups. This
		// instance-specific job protects only the independent replica data.
		sites = nil
		offsitePrefix = strings.Trim(strings.Join([]string{offsitePrefix, layout.Service}, "/"), "/")
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
	script := fmt.Sprintf("#!/bin/sh\nset -eu\nBASE=%q\nRUN=$(date -u +%%F-%%H%%M%%S)\nDEST=\"$BASE/$RUN\"\ninstall -d -m 700 \"$DEST\"\n%s\n", destination, database)
	for _, s := range sites {
		script += fmt.Sprintf("tar -C %q -czf \"$DEST/%s-files.tar.gz\" .\n", s.Root, s.Domain)
	}
	if offsite := c.Backups.Offsite; offsite != nil && offsite.Provider == "s3" {
		prefix := offsitePrefix
		if prefix != "" {
			prefix += "/"
		}
		aws := awsCLICommand(*offsite)
		remoteDays := offsite.EffectiveRetentionDays(c.Backups.EffectiveRetentionDays())
		script += fmt.Sprintf("S3_ROOT=%s\nS3_PREFIX=%s\n%s s3 cp \"$DEST\" \"${S3_ROOT}${S3_PREFIX}${RUN}/\" --recursive --only-show-errors\n", shellQuote("s3://"+offsite.Bucket+"/"), shellQuote(prefix), aws)
		script += fmt.Sprintf("CUTOFF=$(date -u -d '-%d days' +%%F-%%H%%M%%S)\n", remoteDays)
		script += fmt.Sprintf("for REMOTE_RUN in $(%s s3api list-objects-v2 --bucket %s --prefix \"$S3_PREFIX\" --delimiter / --query 'CommonPrefixes[].Prefix' --output text); do\n", aws, shellQuote(offsite.Bucket))
		script += "  RUN_NAME=${REMOTE_RUN#\"$S3_PREFIX\"}\n  RUN_NAME=${RUN_NAME%/}\n  case \"$RUN_NAME\" in\n    [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]-[0-9][0-9][0-9][0-9][0-9][0-9])\n      if [ \"$RUN_NAME\" \\< \"$CUTOFF\" ]; then\n"
		script += fmt.Sprintf("        %s s3 rm \"${S3_ROOT}${REMOTE_RUN}\" --recursive --only-show-errors\n", aws)
		script += "      fi\n      ;;\n  esac\ndone\n"
		pn.Warn("S3 backup access must allow PutObject, ListBucket, and DeleteObject; credentials are read from the AWS CLI credential chain")
	}
	localMinutes := c.Backups.EffectiveRetentionDays() * 24 * 60
	script += fmt.Sprintf("find %s -mindepth 1 -maxdepth 1 -type d -mmin +%d -exec rm -rf -- {} +\n", shellQuote(destination), localMinutes)
	pn.Add(plan.ManagedFile("Install backup script", scriptPath, script, "root", 0o700))
	cron := c.Backups.Schedule + " root " + scriptPath + "\n"
	pn.Add(plan.ManagedFile("Schedule backups", cronPath, cron, "root", 0o600))
}

func awsCLICommand(offsite config.OffsiteBackup) string {
	command := "aws"
	if offsite.Profile != "" {
		command += " --profile " + shellQuote(offsite.Profile)
	}
	if offsite.Region != "" {
		command += " --region " + shellQuote(offsite.Region)
	}
	if offsite.Endpoint != "" {
		command += " --endpoint-url " + shellQuote(offsite.Endpoint)
	}
	return command
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
