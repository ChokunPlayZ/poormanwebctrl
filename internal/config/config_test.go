package config

import (
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
