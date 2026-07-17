package provider

import (
	"slices"
	"strings"
	"testing"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/plan"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

func TestBuildWithFeaturesAddsFeaturePackagesAndSteps(t *testing.T) {
	features := BuiltInFeatures()
	features = append(features, FeatureFunc{
		ID: "metrics-agent",
		RequiredPackages: func(config.Config, platform.Platform) []string {
			return []string{"metrics-agent"}
		},
		ContributePlan: func(result *plan.Plan, _ config.Config, _ platform.Platform) error {
			result.Add(plan.Cmd("Configure metrics agent", "metrics-agent", true, "configure"))
			return nil
		},
	})

	result, err := BuildWithFeatures(config.Default(), platform.Platform{Distro: "ubuntu", Family: "debian"}, features)
	if err != nil {
		t.Fatal(err)
	}
	installed, contributed := false, false
	for _, step := range result.Steps {
		installed = installed || slices.Contains(step.Args, "metrics-agent")
		contributed = contributed || step.Description == "Configure metrics agent"
	}
	if !installed || !contributed {
		t.Fatalf("extension package installed=%v, plan contributed=%v", installed, contributed)
	}
}

func TestBuildWithFeaturesCanRemoveFeature(t *testing.T) {
	features := BuiltInFeatures()
	filtered := features[:0]
	for _, feature := range features {
		if feature.Name() != "backups" {
			filtered = append(filtered, feature)
		}
	}
	result, err := BuildWithFeatures(config.Default(), platform.Platform{Distro: "ubuntu", Family: "debian"}, filtered)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range result.Steps {
		if slices.Contains(step.Args, "rsync") || strings.Contains(strings.ToLower(step.Description), "backup") {
			t.Fatalf("removed backup feature contributed step %#v", step)
		}
	}
}

func TestBuildWithFeaturesRejectsDuplicateNames(t *testing.T) {
	features := BuiltInFeatures()
	features = append(features, FeatureFunc{ID: features[0].Name()})
	_, err := BuildWithFeatures(config.Default(), platform.Platform{Distro: "ubuntu", Family: "debian"}, features)
	if err == nil || !strings.Contains(err.Error(), "duplicate feature") {
		t.Fatalf("duplicate feature error = %v", err)
	}
}
