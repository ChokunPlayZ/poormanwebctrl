package health

import (
	"testing"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

func TestChecksCoverServiceConfigAndSite(t *testing.T) {
	c := config.Default()
	checks := Checks(c, platform.Platform{Family: "debian"})
	if len(checks) < 4 {
		t.Fatalf("got %d checks, want service, database, config, and site", len(checks))
	}
}

func TestChecksUseIndependentMariaDBReplicaService(t *testing.T) {
	c := config.Default()
	c.Database.Role = "replica"
	c.Database.Port = 3307
	c.Database.DataDir = "/var/lib/mysql/poorman-replica-3307"
	c.Database.Replication = config.Replication{PrimaryHost: "127.0.0.1"}
	checks := Checks(c, platform.Platform{Family: "debian"})
	for _, check := range checks {
		if check.Name == "poorman-mariadb-replica-3307 service" {
			return
		}
	}
	t.Fatalf("health checks do not target the independent replica service: %#v", checks)
}
