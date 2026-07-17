package managed

import (
	"path/filepath"
	"testing"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
)

func TestApplyReplacesOnlyOneConfiguration(t *testing.T) {
	primaryPath := filepath.Join(t.TempDir(), "primary.json")
	otherPath := filepath.Join(t.TempDir(), "other.json")
	inventory := Inventory{Version: 1, Services: []Service{
		{Key: ServiceKey(primaryPath, "database"), ConfigPath: ConfigKey(primaryPath), Kind: "database", Name: "poorman-mariadb-replica-3307"},
		{Key: ServiceKey(otherPath, "database"), ConfigPath: ConfigKey(otherPath), Kind: "database", Name: "mariadb"},
	}}
	desired := []Service{{Key: ServiceKey(primaryPath, "database"), ConfigPath: ConfigKey(primaryPath), Kind: "database", Name: "poorman-mariadb-replica-3308"}}
	got := Apply(inventory, primaryPath, desired)
	if len(got.Services) != 2 {
		t.Fatalf("services = %#v, want old primary replaced and other retained", got.Services)
	}
	for _, service := range got.Services {
		if service.ConfigPath == ConfigKey(primaryPath) && service.Name != "poorman-mariadb-replica-3308" {
			t.Fatalf("primary service = %#v, want new replica service", service)
		}
	}
}

func TestDesiredServicesKeepsSeparateDatabaseInstancesVisible(t *testing.T) {
	primary := config.Default()
	primary.Database.Port = 3306
	replica := config.Default()
	replica.Database.Role = "replica"
	replica.Database.Port = 3307
	replica.Database.DataDir = "/var/lib/mysql/poorman-replica-3307"
	replica.Database.Replication.PrimaryHost = "127.0.0.1"
	primaryServices := DesiredServices(primary, "/etc/poorman/primary.json")
	replicaServices := DesiredServices(replica, "/etc/poorman/replica.json")
	if len(primaryServices) != 2 || len(replicaServices) != 2 {
		t.Fatalf("desired service sets = %#v / %#v", primaryServices, replicaServices)
	}
	if primaryServices[1].Name != "mariadb" || replicaServices[1].Name != "poorman-mariadb-replica-3307" {
		t.Fatalf("database services = %#v / %#v", primaryServices[1], replicaServices[1])
	}
}

func TestDesiredServicesIncludesNestedLocalReplica(t *testing.T) {
	c := config.Default()
	c.Database.Role = "primary"
	c.Database.Replication = config.Replication{User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", AllowedCIDR: "127.0.0.1/32", NodeID: 1}
	c.Database.LocalReplica = &config.LocalReplica{Port: 3307, DataDir: "/var/lib/mysql/poorman-replica-3307", NodeID: 2}
	services := DesiredServices(c, "/etc/poorman/poorman.json")
	if len(services) != 3 {
		t.Fatalf("services = %#v, want web, primary database, and local replica", services)
	}
	if services[1].Name != "mariadb" || services[2].Name != "poorman-mariadb-replica-3307" || services[1].ConfigPath != services[2].ConfigPath {
		t.Fatalf("database services = %#v", services[1:])
	}
}
