package provider

import (
	"strings"
	"testing"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

func TestApachePackageNames(t *testing.T) {
	for _, tt := range []struct{ family, want string }{{"debian", "apache2"}, {"rhel", "httpd"}} {
		c := config.Default()
		c.WebServer.Provider = "apache"
		p, err := Build(c, platform.Platform{Distro: tt.family, Family: tt.family})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, step := range p.Steps {
			if strings.Contains(strings.Join(step.Args, " "), tt.want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s plan does not install %s", tt.family, tt.want)
		}
	}
}

func TestPlanDoesNotPrintSecretTemplates(t *testing.T) {
	c := config.Default()
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	p.Print(&out)
	if strings.Contains(out.String(), "${POORMAN_DB_PASSWORD}") {
		t.Fatal("plan exposed secret template")
	}
}

func TestWordPressPlanHasCompleteWorkflow(t *testing.T) {
	c := config.Default()
	c.Sites[0].WordPress = &config.WordPress{AdminEmail: "admin@example.com", AdminPassEnv: "WP_ADMIN_PASSWORD"}
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	descriptions := ""
	for _, step := range p.Steps {
		descriptions += step.Description + "\n"
	}
	for _, want := range []string{"Install wp-cli", "Download WordPress", "Create WordPress configuration", "Install WordPress", "Obtain and attach TLS certificate", "Install backup script"} {
		if !strings.Contains(descriptions, want) {
			t.Errorf("plan missing %q", want)
		}
	}
}

func TestReplicaPromotionIsGuardedByRole(t *testing.T) {
	c := config.Default()
	if _, err := PromoteReplica(c, platform.Platform{Family: "debian"}); err == nil {
		t.Fatal("expected standalone promotion to fail")
	}
}

func TestOpenLiteSpeedUsesOfficialRepositoryAndInclude(t *testing.T) {
	c := config.Default()
	c.WebServer.Provider = "openlitespeed"
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	descriptions := ""
	for _, step := range p.Steps {
		descriptions += step.Description + "\n"
	}
	for _, want := range []string{"official LiteSpeed repository", "OpenLiteSpeed includes", "Register OpenLiteSpeed virtual hosts"} {
		if !strings.Contains(descriptions, want) {
			t.Errorf("OpenLiteSpeed plan missing %q", want)
		}
	}
}

func TestFirewallPlansAreAvailableIndependently(t *testing.T) {
	c := config.Default()
	for _, tt := range []struct {
		family string
		want   string
	}{
		{family: "debian", want: "ufw"},
		{family: "rhel", want: "firewall-cmd"},
	} {
		p, err := Firewall(c, platform.Platform{Distro: tt.family, Family: tt.family})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, step := range p.Steps {
			if step.Command == tt.want || strings.Contains(strings.Join(step.Args, " "), tt.want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s firewall plan does not contain %s", tt.family, tt.want)
		}
	}
}

func TestFirewallDisableRequiresSupportedPlatform(t *testing.T) {
	if _, err := DisableFirewall(platform.Platform{Distro: "alpine", Family: "alpine"}); err == nil {
		t.Fatal("expected unsupported firewall disable to fail")
	}
}

func TestReplicaPlanSkipsWriteSideApplicationInitialization(t *testing.T) {
	c := config.Default()
	c.Database.Provider = "postgresql"
	c.Database.Role = "replica"
	c.Database.DataDir = "/var/lib/postgresql/replica"
	c.Database.Port = 5433
	c.Database.Replication = config.Replication{PrimaryHost: "127.0.0.1", PrimaryPort: 5432, User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", Slot: "replica_1"}
	c.Sites[0].WordPress = &config.WordPress{AdminEmail: "admin@example.com", AdminPassEnv: "WP_ADMIN_PASSWORD"}

	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	var descriptions string
	for _, step := range p.Steps {
		descriptions += step.Description + "\n"
	}
	for _, forbidden := range []string{"Create PostgreSQL application database", "Download wp-cli", "Download WordPress", "Create WordPress configuration", "Install WordPress"} {
		if strings.Contains(descriptions, forbidden) {
			t.Errorf("replica plan contains write-side initialization %q", forbidden)
		}
	}
	if !strings.Contains(descriptions, "Configure virtual host") {
		t.Fatal("replica plan did not preserve virtual-host configuration")
	}
}

func TestSameMachinePostgresReplicaKeepsPrimaryRunning(t *testing.T) {
	c := config.Default()
	c.Database.Provider = "postgresql"
	c.Database.Role = "replica"
	c.Database.DataDir = "/var/lib/postgresql/replica"
	c.Database.Port = 5433
	c.Database.Replication = config.Replication{PrimaryHost: "127.0.0.1", PrimaryPort: 5432, User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", Slot: "replica_1"}
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	foundBootstrap, foundStart := false, false
	for _, step := range p.Steps {
		if step.Description == "Stop postgresql" {
			t.Fatal("same-machine bootstrap stops the primary PostgreSQL service")
		}
		if step.Description == "Verify PostgreSQL replica data directory is uninitialized" {
			t.Fatal("repeat apply contains a mandatory uninitialized-directory failure")
		}
		if step.Description == "Bootstrap PostgreSQL replica from primary" {
			foundBootstrap = true
			if step.UnlessCommand != "test" || !strings.Contains(strings.Join(step.UnlessArgs, " "), "PG_VERSION") {
				t.Fatalf("base backup is not conditional on PG_VERSION: %#v", step)
			}
		}
		if step.Description == "Start PostgreSQL replica instance" {
			foundStart = true
			if step.UnlessCommand != "pg_isready" || !strings.Contains(strings.Join(step.UnlessArgs, " "), "-p 5433") {
				t.Fatalf("replica start is not conditional on readiness: %#v", step)
			}
		}
	}
	if !foundBootstrap || !foundStart {
		t.Fatalf("replica plan missing bootstrap/start: bootstrap=%t start=%t", foundBootstrap, foundStart)
	}
}

func TestMariaDBReplicationUsesPlatformPathAndRestartsBeforeSQL(t *testing.T) {
	for _, tt := range []struct {
		family string
		path   string
	}{{"debian", "/etc/mysql/mariadb.conf.d/90-poorman-replication.cnf"}, {"rhel", "/etc/my.cnf.d/90-poorman-replication.cnf"}, {"alpine", "/etc/my.cnf.d/90-poorman-replication.cnf"}} {
		c := config.Default()
		c.Database.Role = "primary"
		c.Database.Port = 3310
		c.Database.Replication = config.Replication{User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", AllowedCIDR: "10.20.0.0/24", NodeID: 1}
		p, err := Build(c, platform.Platform{Distro: tt.family, Family: tt.family})
		if err != nil {
			t.Fatal(err)
		}
		configIndex, restartIndex, sqlIndex := -1, -1, -1
		for i, step := range p.Steps {
			switch step.Description {
			case "Configure MariaDB primary":
				configIndex = i
				if step.Path != tt.path {
					t.Errorf("%s config path = %q, want %q", tt.family, step.Path, tt.path)
				}
			case "Reload or restart mariadb", "Restart mariadb":
				restartIndex = i
			case "Create MariaDB replication user":
				sqlIndex = i
			}
		}
		if configIndex < 0 || restartIndex <= configIndex || sqlIndex <= restartIndex {
			t.Errorf("%s replication ordering config=%d restart=%d sql=%d", tt.family, configIndex, restartIndex, sqlIndex)
		}
	}
}

func TestStandaloneFirewallActionsRejectUnsupportedPlatform(t *testing.T) {
	c := config.Default()
	p := platform.Platform{Distro: "alpine", Family: "alpine"}
	if _, err := Firewall(c, p); err == nil {
		t.Fatal("expected Alpine firewall apply to report unsupported")
	}
	if _, err := FirewallStatus(p); err == nil {
		t.Fatal("expected Alpine firewall status to report unsupported")
	}
}

func TestStandaloneFirewallApplyRejectsDisabledPolicy(t *testing.T) {
	c := config.Default()
	c.Firewall.Enabled = false
	if _, err := Firewall(c, platform.Platform{Distro: "ubuntu", Family: "debian"}); err == nil {
		t.Fatal("expected disabled firewall policy to reject apply")
	}
}
