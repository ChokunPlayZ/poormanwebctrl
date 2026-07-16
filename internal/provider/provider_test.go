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
