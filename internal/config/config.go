package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Config struct {
	Version    int                        `json:"version"`
	WebServer  WebServer                  `json:"web_server"`
	Database   *Database                  `json:"database,omitempty"`
	Access     Access                     `json:"access,omitempty"`
	Sites      []Site                     `json:"sites,omitempty"`
	TLS        TLS                        `json:"tls,omitempty"`
	Firewall   Firewall                   `json:"firewall,omitempty"`
	Backups    Backup                     `json:"backups,omitempty"`
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

type WebServer struct {
	Provider string `json:"provider"`
}

type Database struct {
	Provider     string               `json:"provider"`
	Role         string               `json:"role,omitempty"`
	Name         string               `json:"name,omitempty"`
	User         string               `json:"user,omitempty"`
	PasswordEnv  string               `json:"password_env,omitempty"`
	DataDir      string               `json:"data_dir,omitempty"`
	Port         int                  `json:"port,omitempty"`
	Databases    []DatabaseSpec       `json:"databases,omitempty"`
	Users        []DatabaseUser       `json:"users,omitempty"`
	Permissions  []DatabasePermission `json:"permissions,omitempty"`
	ACL          []DatabasePermission `json:"acl,omitempty"`
	Replication  Replication          `json:"replication,omitempty"`
	LocalReplica *LocalReplica        `json:"local_replica,omitempty"`
}

// LocalReplica describes a second database process managed on the same host
// as the primary. Shared replication credentials stay on Database.Replication
// so the web stack and both database instances have one source of truth.
type LocalReplica struct {
	Port    int    `json:"port"`
	DataDir string `json:"data_dir"`
	NodeID  int    `json:"node_id,omitempty"`
	Slot    string `json:"slot,omitempty"`
}

// DatabaseSpec describes one logical database managed inside an instance.
// Name/Owner remain as legacy shorthand for a single database and application
// user; new configurations should use Databases, Users, and Permissions.
type DatabaseSpec struct {
	Name      string          `json:"name"`
	Owner     string          `json:"owner,omitempty"`
	Charset   string          `json:"charset,omitempty"`
	Collation string          `json:"collation,omitempty"`
	Tables    []DatabaseTable `json:"tables,omitempty"`
}

type DatabaseUser struct {
	Name        string `json:"name"`
	PasswordEnv string `json:"password_env,omitempty"`
	Host        string `json:"host,omitempty"`  // MariaDB account host; ignored by PostgreSQL.
	Local       bool   `json:"local,omitempty"` // Explicit account created only on a MariaDB replica.
}

type DatabaseTable struct {
	Name       string           `json:"name"`
	Schema     string           `json:"schema,omitempty"`
	Columns    []DatabaseColumn `json:"columns,omitempty"`
	PrimaryKey []string         `json:"primary_key,omitempty"`
}

type DatabaseColumn struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Nullable    bool   `json:"nullable,omitempty"`
	DefaultExpr string `json:"default,omitempty"`
}

type DatabasePermission struct {
	User        string   `json:"user"`
	Database    string   `json:"database"`
	Schema      string   `json:"schema,omitempty"`
	Table       string   `json:"table,omitempty"`
	Privileges  []string `json:"privileges"`
	GrantOption bool     `json:"grant_option,omitempty"`
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
	TLS       *bool      `json:"tls,omitempty"`
	WordPress *WordPress `json:"wordpress,omitempty"`
}

type WordPress struct {
	Title        string `json:"title,omitempty"`
	AdminUser    string `json:"admin_user,omitempty"`
	AdminEmail   string `json:"admin_email,omitempty"`
	AdminPassEnv string `json:"admin_password_env,omitempty"`
}

type TLS struct {
	// Enabled is retained as the default for configurations written before TLS
	// became a per-site setting. New configurations set Site.TLS explicitly.
	Enabled bool   `json:"enabled,omitempty"`
	Email   string `json:"email,omitempty"`
}

type Firewall struct {
	Enabled bool `json:"enabled,omitempty"`
}

type Backup struct {
	Enabled       bool           `json:"enabled,omitempty"`
	Destination   string         `json:"destination,omitempty"`
	Schedule      string         `json:"schedule,omitempty"`
	RetentionDays int            `json:"retention_days,omitempty"`
	Offsite       *OffsiteBackup `json:"offsite,omitempty"`
}

// OffsiteBackup describes a second copy written after a local backup succeeds.
// Authentication deliberately uses the AWS CLI credential chain so secrets do
// not enter the poorman configuration or generated script.
type OffsiteBackup struct {
	Provider      string `json:"provider"`
	Bucket        string `json:"bucket"`
	Prefix        string `json:"prefix,omitempty"`
	Region        string `json:"region,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	Profile       string `json:"profile,omitempty"`
	RetentionDays int    `json:"retention_days,omitempty"`
}

var (
	nameRE         = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	sqlTypeRE      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(?:[[:space:]]*\([[:space:]]*[0-9]+(?:[[:space:]]*,[[:space:]]*[0-9]+)*[[:space:]]*\))?(?:[[:space:]]+[A-Za-z][A-Za-z0-9_]*(?:[[:space:]]*\([[:space:]]*[0-9]+(?:[[:space:]]*,[[:space:]]*[0-9]+)*[[:space:]]*\))?){0,3}$`)
	defaultExprRE  = regexp.MustCompile(`^[A-Za-z0-9_()' +*/:.,-]+$`)
	charsetRE      = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	domainRE       = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	envRE          = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	s3BucketRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	s3PrefixRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,511}$`)
	awsOptionRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	extensionKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
)

const defaultBackupRetentionDays = 14

// EffectiveRetentionDays keeps existing configurations compatible with the
// original fixed 14-day cleanup while allowing new configs to override it.
func (b Backup) EffectiveRetentionDays() int {
	if b.RetentionDays > 0 {
		return b.RetentionDays
	}
	return defaultBackupRetentionDays
}

// EffectiveRetentionDays inherits the local policy when the offsite policy is
// omitted, so adding S3 does not silently retain objects forever.
func (o OffsiteBackup) EffectiveRetentionDays(localDays int) int {
	if o.RetentionDays > 0 {
		return o.RetentionDays
	}
	return localDays
}

// ManagedDatabases returns the declarative database list while keeping old
// single-database configurations fully compatible.
func (d Database) ManagedDatabases() []DatabaseSpec {
	if len(d.Databases) > 0 {
		return append([]DatabaseSpec(nil), d.Databases...)
	}
	if d.Name == "" {
		return nil
	}
	return []DatabaseSpec{{Name: d.Name, Owner: d.User}}
}

// ManagedUsers returns explicit users plus the legacy application user when
// it is not already represented explicitly.
func (d Database) ManagedUsers() []DatabaseUser {
	users := append([]DatabaseUser(nil), d.Users...)
	if d.User != "" {
		for _, user := range users {
			if user.Name == d.User {
				return users
			}
		}
		users = append(users, DatabaseUser{Name: d.User, PasswordEnv: d.PasswordEnv, Host: "localhost"})
	}
	return users
}

// ApplicationCredentials resolves the database/user used by application
// installers such as WordPress. It prefers the legacy shorthand and then
// falls back to the first declarative database and its owner/user.
func (d Database) ApplicationCredentials() (name, user, passwordEnv string) {
	name, user, passwordEnv = d.Name, d.User, d.PasswordEnv
	databases := d.ManagedDatabases()
	if name == "" && len(databases) > 0 {
		name = databases[0].Name
	}
	if user == "" && len(databases) > 0 {
		user = databases[0].Owner
	}
	users := d.ManagedUsers()
	if user == "" && len(users) > 0 {
		user = users[0].Name
	}
	for _, candidate := range users {
		if candidate.Name == user {
			passwordEnv = candidate.PasswordEnv
			break
		}
	}
	return name, user, passwordEnv
}

// ManagedPermissions accepts both the descriptive permissions key and the
// shorter acl alias so hand-authored configurations can use either spelling.
func (d Database) ManagedPermissions() []DatabasePermission {
	permissions := append([]DatabasePermission(nil), d.Permissions...)
	return append(permissions, d.ACL...)
}

// LocalReplicaDatabase expands the compact same-host declaration into the
// legacy replica-shaped value used by provider-specific planning code.
func (d Database) LocalReplicaDatabase() (Database, bool) {
	if d.LocalReplica == nil {
		return Database{}, false
	}
	replica := d
	replica.Role = "replica"
	replica.Port = d.LocalReplica.Port
	replica.DataDir = d.LocalReplica.DataDir
	replica.LocalReplica = nil
	replica.Replication.PrimaryHost = "127.0.0.1"
	replica.Replication.PrimaryPort = d.Port
	if replica.Replication.PrimaryPort == 0 {
		if d.Provider == "postgresql" {
			replica.Replication.PrimaryPort = 5432
		} else {
			replica.Replication.PrimaryPort = 3306
		}
	}
	replica.Replication.AllowedCIDR = ""
	replica.Replication.NodeID = d.LocalReplica.NodeID
	replica.Replication.Slot = d.LocalReplica.Slot
	return replica, true
}

func Default() Config {
	tlsEnabled := true
	return Config{
		Version:   1,
		WebServer: WebServer{Provider: "nginx"},
		Database:  &Database{Provider: "mariadb", Role: "standalone", Name: "example", User: "example", PasswordEnv: "POORMAN_DB_PASSWORD"},
		Access:    Access{Users: []User{{Name: "webadmin", Home: "/home/webadmin", SFTPOnly: true}}},
		Sites:     []Site{{Domain: "example.com", Root: "/var/www/example.com", Owner: "webadmin", Runtime: "php", TLS: &tlsEnabled}},
		TLS:       TLS{Email: "admin@example.com"},
		Firewall:  Firewall{Enabled: true},
		Backups:   Backup{Enabled: true, Destination: "/var/backups/poorman", Schedule: "0 3 * * *", RetentionDays: defaultBackupRetentionDays},
	}
}

// SiteTLSEnabled resolves the site's explicit HTTPS choice. The top-level
// value is a compatibility default for configurations created by older
// versions, where one switch controlled every site.
func (c Config) SiteTLSEnabled(site Site) bool {
	if site.TLS != nil {
		return *site.TLS
	}
	return c.TLS.Enabled
}

func (c Config) AnySiteTLSEnabled() bool {
	for _, site := range c.Sites {
		if c.SiteTLSEnabled(site) {
			return true
		}
	}
	return false
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
	for name := range c.Extensions {
		if !extensionKeyRE.MatchString(name) {
			return fmt.Errorf("invalid extension name %q", name)
		}
	}
	switch c.WebServer.Provider {
	case "nginx", "apache", "openlitespeed":
	default:
		return fmt.Errorf("unsupported web server %q", c.WebServer.Provider)
	}
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
		if s.Owner != "" && !nameRE.MatchString(s.Owner) {
			return fmt.Errorf("site %q has invalid owner %q", s.Domain, s.Owner)
		}
		if s.Runtime != "" && s.Runtime != "static" && s.Runtime != "php" {
			return fmt.Errorf("site %q runtime must be static or php", s.Domain)
		}
		if s.WordPress != nil {
			if s.Runtime != "php" {
				return fmt.Errorf("WordPress site %q requires runtime php", s.Domain)
			}
			if c.Database == nil {
				return fmt.Errorf("WordPress site %q requires an application database, user, and password_env", s.Domain)
			}
			name, user, passwordEnv := c.Database.ApplicationCredentials()
			if name == "" || user == "" || passwordEnv == "" {
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
		managedDatabases := d.ManagedDatabases()
		managedUsers := d.ManagedUsers()
		databaseNames := map[string]bool{}
		for i, database := range managedDatabases {
			if !nameRE.MatchString(database.Name) {
				return fmt.Errorf("database definition %d has invalid name %q", i+1, database.Name)
			}
			if databaseNames[database.Name] {
				return fmt.Errorf("duplicate database definition %q", database.Name)
			}
			databaseNames[database.Name] = true
			if database.Owner != "" && !nameRE.MatchString(database.Owner) {
				return fmt.Errorf("database %q has invalid owner %q", database.Name, database.Owner)
			}
			if database.Charset != "" && !charsetRE.MatchString(database.Charset) {
				return fmt.Errorf("database %q has invalid charset", database.Name)
			}
			if database.Collation != "" && !charsetRE.MatchString(database.Collation) {
				return fmt.Errorf("database %q has invalid collation", database.Name)
			}
			tableNames := map[string]bool{}
			for j, table := range database.Tables {
				if !nameRE.MatchString(table.Name) {
					return fmt.Errorf("database %q table %d has invalid name %q", database.Name, j+1, table.Name)
				}
				schema := table.Schema
				if schema == "" && d.Provider == "postgresql" {
					schema = "public"
				}
				if schema != "" && !nameRE.MatchString(schema) {
					return fmt.Errorf("database %q table %q has invalid schema %q", database.Name, table.Name, schema)
				}
				tableKey := schema + "." + table.Name
				if tableNames[tableKey] {
					return fmt.Errorf("database %q has duplicate table %q", database.Name, tableKey)
				}
				tableNames[tableKey] = true
				if len(table.Columns) == 0 {
					return fmt.Errorf("database %q table %q requires at least one column", database.Name, table.Name)
				}
				columns := map[string]bool{}
				for k, column := range table.Columns {
					if !nameRE.MatchString(column.Name) {
						return fmt.Errorf("database %q table %q column %d has invalid name %q", database.Name, table.Name, k+1, column.Name)
					}
					if columns[column.Name] {
						return fmt.Errorf("database %q table %q has duplicate column %q", database.Name, table.Name, column.Name)
					}
					columns[column.Name] = true
					if !sqlTypeRE.MatchString(strings.TrimSpace(column.Type)) {
						return fmt.Errorf("database %q table %q column %q has unsafe or invalid type", database.Name, table.Name, column.Name)
					}
					if column.DefaultExpr != "" && (!defaultExprRE.MatchString(column.DefaultExpr) || strings.Contains(column.DefaultExpr, "--") || strings.Contains(column.DefaultExpr, "/*") || strings.Contains(column.DefaultExpr, "*/")) {
						return fmt.Errorf("database %q table %q column %q has an unsafe default expression", database.Name, table.Name, column.Name)
					}
				}
				for _, key := range table.PrimaryKey {
					if !columns[key] {
						return fmt.Errorf("database %q table %q primary key references unknown column %q", database.Name, table.Name, key)
					}
				}
			}
		}
		userNames := map[string]bool{}
		for i, user := range managedUsers {
			if !nameRE.MatchString(user.Name) {
				return fmt.Errorf("database user %d has invalid name %q", i+1, user.Name)
			}
			if userNames[user.Name] {
				return fmt.Errorf("duplicate database user %q", user.Name)
			}
			userNames[user.Name] = true
			if user.PasswordEnv == "" || !envRE.MatchString(user.PasswordEnv) {
				return fmt.Errorf("database user %q requires a valid password_env", user.Name)
			}
			if user.Host != "" && user.Host != "localhost" && user.Host != "%" {
				if net.ParseIP(user.Host) == nil && !validDomain(user.Host) {
					return fmt.Errorf("database user %q has invalid host %q", user.Name, user.Host)
				}
			}
			if user.Local && d.Role != "replica" {
				return fmt.Errorf("database user %q can be local only on a replica", user.Name)
			}
			if user.Local && d.Provider != "mariadb" {
				return fmt.Errorf("database user %q cannot be local on a PostgreSQL hot standby", user.Name)
			}
		}
		for _, database := range managedDatabases {
			if database.Owner != "" && !userNames[database.Owner] {
				return fmt.Errorf("database %q references unknown owner %q", database.Name, database.Owner)
			}
		}
		validPrivileges := map[string]bool{"ALL": true, "SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "REFERENCES": true, "TRIGGER": true, "USAGE": true, "CREATE": true, "CONNECT": true, "TEMPORARY": true, "EXECUTE": true}
		for i, permission := range d.ManagedPermissions() {
			if !userNames[permission.User] {
				return fmt.Errorf("permission %d references unknown database user %q", i+1, permission.User)
			}
			if !databaseNames[permission.Database] {
				return fmt.Errorf("permission %d references unknown database %q", i+1, permission.Database)
			}
			if permission.Schema != "" && !nameRE.MatchString(permission.Schema) {
				return fmt.Errorf("permission %d has invalid schema %q", i+1, permission.Schema)
			}
			if permission.Table != "" && !nameRE.MatchString(permission.Table) {
				return fmt.Errorf("permission %d has invalid table %q", i+1, permission.Table)
			}
			if len(permission.Privileges) == 0 {
				return fmt.Errorf("permission %d requires at least one privilege", i+1)
			}
			for _, privilege := range permission.Privileges {
				if !validPrivileges[strings.ToUpper(privilege)] {
					return fmt.Errorf("permission %d has unsupported privilege %q", i+1, privilege)
				}
			}
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
			if d.Provider == "mariadb" && isLoopbackHost(r.PrimaryHost) && (d.Role == "replica" || d.DataDir != "") {
				primaryPort := r.PrimaryPort
				if primaryPort == 0 {
					primaryPort = 3306
				}
				if d.Port == 0 || d.Port == primaryPort {
					return fmt.Errorf("same-machine MariaDB instance requires an explicit port different from primary_port")
				}
				if !safeManagedPath(d.DataDir) || filepath.Clean(d.DataDir) == "/var/lib/mysql" {
					return fmt.Errorf("same-machine MariaDB instance requires a separate absolute data_dir")
				}
			}
			if d.Provider == "postgresql" && d.Role == "replica" && isLoopbackHost(r.PrimaryHost) {
				primaryPort := r.PrimaryPort
				if primaryPort == 0 {
					primaryPort = 5432
				}
				if d.Port == 0 || d.Port == primaryPort {
					return fmt.Errorf("same-machine PostgreSQL replica requires an explicit port different from primary_port")
				}
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
		if local := d.LocalReplica; local != nil {
			if d.Role != "primary" {
				return fmt.Errorf("database local_replica requires role=primary")
			}
			primaryPort := d.Port
			if primaryPort == 0 {
				if d.Provider == "postgresql" {
					primaryPort = 5432
				} else {
					primaryPort = 3306
				}
			}
			if local.Port < 1024 || local.Port > 65535 || local.Port == primaryPort {
				return fmt.Errorf("database local_replica requires a port between 1024 and 65535 different from the primary port")
			}
			if !safeManagedPath(local.DataDir) {
				return fmt.Errorf("database local_replica requires a separate absolute data_dir")
			}
			if d.Provider == "mariadb" {
				if filepath.Clean(local.DataDir) == "/var/lib/mysql" {
					return fmt.Errorf("database local_replica cannot use the primary MariaDB data directory")
				}
				if local.NodeID < 1 || local.NodeID == d.Replication.NodeID {
					return fmt.Errorf("database local_replica requires a unique positive MariaDB node_id")
				}
				if local.Slot != "" {
					return fmt.Errorf("database local_replica slot is supported only by PostgreSQL")
				}
			} else {
				if local.NodeID != 0 {
					return fmt.Errorf("database local_replica node_id is supported only by MariaDB")
				}
				if local.Slot == "" || !nameRE.MatchString(local.Slot) {
					return fmt.Errorf("PostgreSQL local_replica requires a valid slot")
				}
			}
		}
	}
	if c.AnySiteTLSEnabled() && !strings.Contains(c.TLS.Email, "@") {
		return fmt.Errorf("TLS requires a contact email")
	}
	if c.Backups.Enabled {
		if !safeManagedPath(c.Backups.Destination) {
			return fmt.Errorf("backup destination must be an absolute path")
		}
		if len(strings.Fields(c.Backups.Schedule)) != 5 {
			return fmt.Errorf("backup schedule must be a five-field cron expression")
		}
		if c.Backups.RetentionDays < 0 || c.Backups.RetentionDays > 36500 {
			return fmt.Errorf("backup retention_days must be between 1 and 36500 when set")
		}
		if offsite := c.Backups.Offsite; offsite != nil {
			if offsite.Provider != "s3" {
				return fmt.Errorf("unsupported offsite backup provider %q", offsite.Provider)
			}
			if !validS3Bucket(offsite.Bucket) {
				return fmt.Errorf("S3 backup requires a valid bucket name")
			}
			if offsite.Prefix != "" && (!s3PrefixRE.MatchString(offsite.Prefix) || strings.HasPrefix(offsite.Prefix, "/") || strings.Contains(offsite.Prefix, "//")) {
				return fmt.Errorf("S3 backup prefix must be a relative path using letters, numbers, dot, underscore, dash, or slash")
			}
			for _, segment := range strings.Split(offsite.Prefix, "/") {
				if segment == "." || segment == ".." {
					return fmt.Errorf("S3 backup prefix cannot contain dot path segments")
				}
			}
			if offsite.Region != "" && !awsOptionRE.MatchString(offsite.Region) {
				return fmt.Errorf("S3 backup region is invalid")
			}
			if offsite.Profile != "" && !awsOptionRE.MatchString(offsite.Profile) {
				return fmt.Errorf("S3 backup profile is invalid")
			}
			if offsite.Endpoint != "" && !validS3Endpoint(offsite.Endpoint) {
				return fmt.Errorf("S3 backup endpoint must be an HTTP or HTTPS URL without credentials, query, or fragment")
			}
			if offsite.RetentionDays < 0 || offsite.RetentionDays > 36500 {
				return fmt.Errorf("S3 backup retention_days must be between 1 and 36500 when set")
			}
		}
	}
	return nil
}

func validS3Bucket(bucket string) bool {
	if !s3BucketRE.MatchString(bucket) || strings.Contains(bucket, "..") {
		return false
	}
	return net.ParseIP(bucket) == nil
}

func validS3Endpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return false
	}
	return u.RawQuery == "" && u.Fragment == ""
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validDomain(s string) bool {
	return len(s) <= 253 && domainRE.MatchString(s) && strings.Contains(s, ".")
}

func safeManagedPath(path string) bool {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
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
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set temporary config permissions: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	committed = true
	return nil
}
