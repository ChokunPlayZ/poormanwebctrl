package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Config struct {
	Version   int       `json:"version"`
	WebServer WebServer `json:"web_server"`
	Database  *Database `json:"database,omitempty"`
	Access    Access    `json:"access,omitempty"`
	Sites     []Site    `json:"sites,omitempty"`
	TLS       TLS       `json:"tls,omitempty"`
	Firewall  Firewall  `json:"firewall,omitempty"`
	Backups   Backup    `json:"backups,omitempty"`
}

type WebServer struct {
	Provider string `json:"provider"`
}

type Database struct {
	Provider    string      `json:"provider"`
	Role        string      `json:"role,omitempty"`
	Name        string      `json:"name,omitempty"`
	User        string      `json:"user,omitempty"`
	PasswordEnv string      `json:"password_env,omitempty"`
	DataDir     string      `json:"data_dir,omitempty"`
	Port        int         `json:"port,omitempty"`
	Replication Replication `json:"replication,omitempty"`
}

type Replication struct {
	PrimaryHost string `json:"primary_host,omitempty"`
	PrimaryPort int    `json:"primary_port,omitempty"`
	User        string `json:"user,omitempty"`
	PasswordEnv string `json:"password_env,omitempty"`
	Slot        string `json:"slot,omitempty"`
	AllowedCIDR string `json:"allowed_cidr,omitempty"`
	NodeID      int    `json:"node_id,omitempty"`
}

type Access struct {
	Users []User `json:"users,omitempty"`
	FTP   FTP    `json:"ftp,omitempty"`
}

type User struct {
	Name       string   `json:"name"`
	Home       string   `json:"home,omitempty"`
	SFTPOnly   bool     `json:"sftp_only,omitempty"`
	PublicKeys []string `json:"public_keys,omitempty"`
}

type FTP struct {
	Enabled        bool `json:"enabled,omitempty"`
	AllowPlaintext bool `json:"allow_plaintext,omitempty"`
}

type Site struct {
	Domain    string     `json:"domain"`
	Aliases   []string   `json:"aliases,omitempty"`
	Root      string     `json:"root"`
	Owner     string     `json:"owner,omitempty"`
	Runtime   string     `json:"runtime,omitempty"`
	WordPress *WordPress `json:"wordpress,omitempty"`
}

type WordPress struct {
	Title        string `json:"title,omitempty"`
	AdminUser    string `json:"admin_user,omitempty"`
	AdminEmail   string `json:"admin_email,omitempty"`
	AdminPassEnv string `json:"admin_password_env,omitempty"`
}

type TLS struct {
	Enabled bool   `json:"enabled,omitempty"`
	Email   string `json:"email,omitempty"`
}

type Firewall struct {
	Enabled bool `json:"enabled,omitempty"`
}

type Backup struct {
	Enabled     bool   `json:"enabled,omitempty"`
	Destination string `json:"destination,omitempty"`
	Schedule    string `json:"schedule,omitempty"`
}

var (
	nameRE   = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	domainRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	envRE    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

func Default() Config {
	return Config{
		Version:   1,
		WebServer: WebServer{Provider: "nginx"},
		Database:  &Database{Provider: "mariadb", Role: "standalone", Name: "example", User: "example", PasswordEnv: "POORMAN_DB_PASSWORD"},
		Access:    Access{Users: []User{{Name: "webadmin", Home: "/home/webadmin", SFTPOnly: true}}},
		Sites:     []Site{{Domain: "example.com", Root: "/var/www/example.com", Owner: "webadmin", Runtime: "php"}},
		TLS:       TLS{Enabled: true, Email: "admin@example.com"},
		Firewall:  Firewall{Enabled: true},
		Backups:   Backup{Enabled: true, Destination: "/var/backups/poorman", Schedule: "0 3 * * *"},
	}
}

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	switch c.WebServer.Provider {
	case "nginx", "apache", "openlitespeed":
	default:
		return fmt.Errorf("unsupported web server %q", c.WebServer.Provider)
	}
	users := map[string]bool{}
	for i, u := range c.Access.Users {
		if !nameRE.MatchString(u.Name) {
			return fmt.Errorf("access user %d has invalid name %q", i+1, u.Name)
		}
		if u.Home != "" && !filepath.IsAbs(u.Home) {
			return fmt.Errorf("access user %q home must be absolute", u.Name)
		}
		for _, key := range u.PublicKeys {
			if !strings.HasPrefix(key, "ssh-") && !strings.HasPrefix(key, "ecdsa-") {
				return fmt.Errorf("access user %q has an invalid SSH public key", u.Name)
			}
		}
		users[u.Name] = true
	}
	if c.Access.FTP.Enabled && !c.Access.FTP.AllowPlaintext {
		return fmt.Errorf("FTP is plaintext; set access.ftp.allow_plaintext=true to explicitly accept the risk, or use SFTP")
	}
	seenSites := map[string]string{}
	for i, s := range c.Sites {
		if !validDomain(s.Domain) || !safeManagedPath(s.Root) {
			return fmt.Errorf("site %d requires a valid domain and absolute root", i+1)
		}
		domainKey := strings.ToLower(s.Domain)
		if owner, ok := seenSites[domainKey]; ok {
			return fmt.Errorf("site domain %q conflicts with %q", s.Domain, owner)
		}
		seenSites[domainKey] = s.Domain
		for _, alias := range s.Aliases {
			if !validDomain(alias) {
				return fmt.Errorf("site %q has invalid alias %q", s.Domain, alias)
			}
			if strings.EqualFold(alias, s.Domain) {
				return fmt.Errorf("site %q cannot use its domain as an alias", s.Domain)
			}
			aliasKey := strings.ToLower(alias)
			if owner, ok := seenSites[aliasKey]; ok {
				return fmt.Errorf("site alias %q conflicts with %q", alias, owner)
			}
			seenSites[aliasKey] = s.Domain
		}
		if s.Owner != "" && !users[s.Owner] {
			return fmt.Errorf("site %q references unknown owner %q", s.Domain, s.Owner)
		}
		if s.Runtime != "" && s.Runtime != "static" && s.Runtime != "php" {
			return fmt.Errorf("site %q runtime must be static or php", s.Domain)
		}
		if s.WordPress != nil {
			if s.Runtime != "php" {
				return fmt.Errorf("WordPress site %q requires runtime php", s.Domain)
			}
			if c.Database == nil || c.Database.Name == "" || c.Database.User == "" || c.Database.PasswordEnv == "" {
				return fmt.Errorf("WordPress site %q requires an application database, user, and password_env", s.Domain)
			}
			if s.WordPress.AdminPassEnv == "" || !envRE.MatchString(s.WordPress.AdminPassEnv) {
				return fmt.Errorf("WordPress site %q requires a valid admin_password_env", s.Domain)
			}
		}
	}
	if c.Database != nil {
		d := c.Database
		switch d.Provider {
		case "postgresql", "mariadb":
		default:
			return fmt.Errorf("unsupported database %q", d.Provider)
		}
		if d.Role == "" {
			d.Role = "standalone"
		}
		if d.Role != "standalone" && d.Role != "primary" && d.Role != "replica" {
			return fmt.Errorf("database role must be standalone, primary, or replica")
		}
		if d.Name != "" && !nameRE.MatchString(d.Name) {
			return fmt.Errorf("invalid database name %q", d.Name)
		}
		if d.User != "" && !nameRE.MatchString(d.User) {
			return fmt.Errorf("invalid database user %q", d.User)
		}
		if d.PasswordEnv != "" && !envRE.MatchString(d.PasswordEnv) {
			return fmt.Errorf("invalid database password environment variable")
		}
		if d.Port < 0 || d.Port > 65535 || (d.Port > 0 && d.Port < 1024) {
			return fmt.Errorf("database port must be between 1024 and 65535")
		}
		if (d.Name != "" || d.User != "") && (d.Name == "" || d.User == "" || d.PasswordEnv == "") {
			return fmt.Errorf("application database requires name, user, and password_env together")
		}
		if d.Role != "standalone" {
			r := d.Replication
			if r.PrimaryPort < 0 || r.PrimaryPort > 65535 || (r.PrimaryPort > 0 && r.PrimaryPort < 1024) {
				return fmt.Errorf("database primary_port must be between 1024 and 65535")
			}
			if !nameRE.MatchString(r.User) || !envRE.MatchString(r.PasswordEnv) {
				return fmt.Errorf("database replication requires a valid user and password_env")
			}
			if d.Role == "replica" && net.ParseIP(r.PrimaryHost) == nil && !validDomain(r.PrimaryHost) {
				return fmt.Errorf("database replica requires a valid primary_host")
			}
			if d.Role == "primary" {
				if _, _, err := net.ParseCIDR(r.AllowedCIDR); err != nil {
					return fmt.Errorf("database primary requires a valid replication allowed_cidr")
				}
			}
			if d.Provider == "mariadb" && r.NodeID < 1 {
				return fmt.Errorf("MariaDB replication requires a unique positive node_id")
			}
			if d.Provider == "postgresql" && d.Role == "replica" && !safeManagedPath(d.DataDir) {
				return fmt.Errorf("PostgreSQL replica requires its actual absolute data_dir")
			}
		}
	}
	if c.TLS.Enabled && !strings.Contains(c.TLS.Email, "@") {
		return fmt.Errorf("TLS requires a contact email")
	}
	if c.Backups.Enabled {
		if !safeManagedPath(c.Backups.Destination) {
			return fmt.Errorf("backup destination must be an absolute path")
		}
		if len(strings.Fields(c.Backups.Schedule)) != 5 {
			return fmt.Errorf("backup schedule must be a five-field cron expression")
		}
	}
	return nil
}

func validDomain(s string) bool {
	return len(s) <= 253 && domainRE.MatchString(s) && strings.Contains(s, ".")
}

func safeManagedPath(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	switch clean {
	case "/", "/var", "/home", "/srv", "/tmp", "/etc", "/usr":
		return false
	}
	return true
}

func WriteDefault(path string) error { return Write(path, Default()) }

func Write(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
