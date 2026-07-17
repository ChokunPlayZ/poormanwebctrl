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

func TestServiceStateMapsSystemdStates(t *testing.T) {
	tests := []struct {
		output string
		want   ServiceState
	}{
		{output: "active\n", want: ServiceUp},
		{output: "inactive\n", want: ServiceDown},
		{output: "failed\n", want: ServiceDown},
		{output: "activating\n", want: ServiceChanging},
		{output: "unknown\n", want: ServiceUnknown},
	}
	for _, tt := range tests {
		if got := serviceState(tt.output, tt.want == ServiceUp, "debian"); got != tt.want {
			t.Errorf("serviceState(%q) = %q, want %q", tt.output, got, tt.want)
		}
	}
}

func TestServiceStateMapsOpenRCStates(t *testing.T) {
	if got := serviceState(" * status: started", true, "alpine"); got != ServiceUp {
		t.Fatalf("started state = %q, want up", got)
	}
	if got := serviceState(" * status: stopped", false, "alpine"); got != ServiceDown {
		t.Fatalf("stopped state = %q, want down", got)
	}
}
