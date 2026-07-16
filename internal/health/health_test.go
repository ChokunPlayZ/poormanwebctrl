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
