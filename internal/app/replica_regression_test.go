package app

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
)

func TestGuidedMariaDBReplicaDerivesDistinctRemoteTopology(t *testing.T) {
	dir := t.TempDir()
	primaryPath := filepath.Join(dir, "primary.json")
	replicaPath := filepath.Join(dir, "replica.json")
	primary := config.Default()
	primary.Database.Role = "primary"
	primary.Database.Port = 3310
	primary.Database.Replication = config.Replication{
		User:        "replicator",
		PasswordEnv: "POORMAN_REPLICATION_PASSWORD",
		AllowedCIDR: "127.0.0.1/32",
		NodeID:      7,
	}
	if err := config.Write(primaryPath, primary); err != nil {
		t.Fatal(err)
	}

	// Keep the inherited provider and credentials, use a separate host, and
	// accept all topology defaults derived from the source primary.
	in := bytes.NewBufferString("\n\n\nn\n\n\n\n")
	var out bytes.Buffer
	if err := Run([]string{"replica", "setup", "-f", replicaPath, "--from", primaryPath}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	replica, err := config.Load(replicaPath)
	if err != nil {
		t.Fatal(err)
	}
	if replica.Database.Replication.PrimaryPort != primary.Database.Port {
		t.Fatalf("primary port = %d, want source port %d", replica.Database.Replication.PrimaryPort, primary.Database.Port)
	}
	if replica.Database.Replication.NodeID == primary.Database.Replication.NodeID {
		t.Fatalf("replica reused primary node ID %d", primary.Database.Replication.NodeID)
	}
}

func TestGuidedMariaDBReplicaCreatesIndependentSameMachineLayout(t *testing.T) {
	dir := t.TempDir()
	primaryPath := filepath.Join(dir, "primary.json")
	replicaPath := filepath.Join(dir, "replica.json")
	primary := config.Default()
	primary.Database.Role = "primary"
	primary.Database.Replication = config.Replication{
		User:        "replicator",
		PasswordEnv: "POORMAN_REPLICATION_PASSWORD",
		AllowedCIDR: "127.0.0.1/32",
		NodeID:      1,
	}
	if err := config.Write(primaryPath, primary); err != nil {
		t.Fatal(err)
	}

	in := bytes.NewBufferString("\n\n\ny\n\n\n\n\n\n")
	var out bytes.Buffer
	if err := Run([]string{"replica", "setup", "-f", replicaPath, "--from", primaryPath}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	replica, err := config.Load(replicaPath)
	if err != nil {
		t.Fatal(err)
	}
	if replica.Database.Port != 3307 || replica.Database.DataDir != "/var/lib/mysql/poorman-replica-3307" {
		t.Fatalf("database = %#v, want independent same-machine MariaDB layout", replica.Database)
	}
}

func TestGuidedPostgreSQLReplicaDoesNotReuseLocalPrimaryStorage(t *testing.T) {
	dir := t.TempDir()
	primaryPath := filepath.Join(dir, "primary.json")
	replicaPath := filepath.Join(dir, "replica.json")
	primary := config.Default()
	primary.Database.Provider = "postgresql"
	primary.Database.Role = "primary"
	primary.Database.Port = 5440
	primary.Database.DataDir = "/var/lib/postgresql/primary-custom"
	primary.Database.Replication = config.Replication{
		User:        "replicator",
		PasswordEnv: "POORMAN_REPLICATION_PASSWORD",
		AllowedCIDR: "127.0.0.1/32",
		Slot:        "primary_slot",
	}
	if err := config.Write(primaryPath, primary); err != nil {
		t.Fatal(err)
	}

	in := bytes.NewBufferString("\n\n\ny\n\n\n\n\n\n")
	var out bytes.Buffer
	if err := Run([]string{"replica", "setup", "-f", replicaPath, "--from", primaryPath}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	replica, err := config.Load(replicaPath)
	if err != nil {
		t.Fatal(err)
	}
	if replica.Database.Replication.PrimaryPort != primary.Database.Port {
		t.Fatalf("primary port = %d, want source port %d", replica.Database.Replication.PrimaryPort, primary.Database.Port)
	}
	if replica.Database.Port == primary.Database.Port {
		t.Fatalf("replica reused primary port %d", primary.Database.Port)
	}
	if replica.Database.DataDir == primary.Database.DataDir {
		t.Fatalf("replica reused primary data directory %q", primary.Database.DataDir)
	}
}
