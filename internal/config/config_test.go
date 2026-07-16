package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRejectsUnknownWebServer(t *testing.T) {
	c := Default()
	c.WebServer.Provider = "mystery"
	if err := c.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPlainFTPRequiresExplicitRiskAcceptance(t *testing.T) {
	c := Default()
	c.Access.FTP.Enabled = true
	if err := c.Validate(); err == nil {
		t.Fatal("expected plaintext FTP validation error")
	}
	c.Access.FTP.AllowPlaintext = true
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestReplicationValidation(t *testing.T) {
	c := Default()
	c.Database = &Database{Provider: "mariadb", Role: "primary", Replication: Replication{User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", AllowedCIDR: "10.0.0.0/24", NodeID: 1}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Database.Replication.NodeID = 0
	if err := c.Validate(); err == nil {
		t.Fatal("expected missing MariaDB node ID error")
	}
}

func TestSameMachineReplicaPortsAreValid(t *testing.T) {
	c := Default()
	c.Database = &Database{Provider: "postgresql", Role: "replica", Port: 5433, DataDir: "/var/lib/postgresql/replica", Replication: Replication{PrimaryHost: "127.0.0.1", PrimaryPort: 5432, User: "replicator", PasswordEnv: "REPLICATION_PASSWORD"}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSameMachineReplicaRejectsPrimaryPortReuse(t *testing.T) {
	c := Default()
	c.Database = &Database{Provider: "postgresql", Role: "replica", Port: 5432, DataDir: "/var/lib/postgresql/replica", Replication: Replication{PrimaryHost: "127.0.0.1", PrimaryPort: 5432, User: "replicator", PasswordEnv: "REPLICATION_PASSWORD"}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected same-machine port collision validation error")
	}
}

func TestSameMachinePostgresReplicaRequiresExplicitPort(t *testing.T) {
	c := Default()
	c.Database = &Database{Provider: "postgresql", Role: "replica", DataDir: "/var/lib/postgresql/replica", Replication: Replication{PrimaryHost: "127.0.0.1", User: "replicator", PasswordEnv: "REPLICATION_PASSWORD"}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected missing same-machine replica port validation error")
	}
}

func TestRejectsSameMachineMariaDBReplica(t *testing.T) {
	c := Default()
	c.Database = &Database{Provider: "mariadb", Role: "replica", Port: 3307, Replication: Replication{PrimaryHost: "127.0.0.1", PrimaryPort: 3306, User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", NodeID: 2}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected same-machine MariaDB replica validation error")
	}
}

func TestRejectsDangerousManagedRoots(t *testing.T) {
	c := Default()
	c.Sites[0].Root = "/"
	if err := c.Validate(); err == nil {
		t.Fatal("expected dangerous root validation error")
	}
}

func TestAllExamplesAreValid(t *testing.T) {
	paths, err := filepath.Glob("../../examples/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no examples found")
	}
	for _, path := range paths {
		if _, err := Load(path); err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
		}
	}
}

func TestWriteReplacesPermissiveConfigPrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poorman.json")
	if err := os.WriteFile(path, []byte("old contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, Default()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config permissions = %04o, want 0600", got)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("replacement config is not complete and valid: %v", err)
	}
}

func TestWriteValidationFailurePreservesExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poorman.json")
	if err := Write(path, Default()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	invalid := Default()
	invalid.WebServer.Provider = "invalid"
	if err := Write(path, invalid); err == nil {
		t.Fatal("expected invalid replacement to fail")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed replacement changed the active config")
	}
}
