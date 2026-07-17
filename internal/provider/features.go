package provider

import (
	"fmt"
	"sort"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/plan"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

// Feature is one independently plannable part of a server configuration.
// Keeping package discovery and plan generation together makes a feature easy
// to add, remove, or replace without changing the build orchestrator.
type Feature interface {
	Name() string
	Validate(config.Config, platform.Platform) error
	Packages(config.Config, platform.Platform) []string
	Plan(*plan.Plan, config.Config, platform.Platform) error
}

// FeatureFunc is a convenient Feature implementation for built-in and
// downstream features that do not need their own stateful type.
type FeatureFunc struct {
	ID               string
	ValidateConfig   func(config.Config, platform.Platform) error
	RequiredPackages func(config.Config, platform.Platform) []string
	ContributePlan   func(*plan.Plan, config.Config, platform.Platform) error
}

func (f FeatureFunc) Name() string { return f.ID }

func (f FeatureFunc) Validate(c config.Config, p platform.Platform) error {
	if f.ValidateConfig == nil {
		return nil
	}
	return f.ValidateConfig(c, p)
}

func (f FeatureFunc) Packages(c config.Config, p platform.Platform) []string {
	if f.RequiredPackages == nil {
		return nil
	}
	return f.RequiredPackages(c, p)
}

func (f FeatureFunc) Plan(result *plan.Plan, c config.Config, p platform.Platform) error {
	if f.ContributePlan == nil {
		return nil
	}
	return f.ContributePlan(result, c, p)
}

// BuiltInFeatures returns the default dependency-ordered feature pipeline.
// Callers may copy this slice, insert a feature at the required dependency
// boundary, and pass it to BuildWithFeatures or BuildForConfigWithFeatures.
func BuiltInFeatures() []Feature {
	return []Feature{
		FeatureFunc{
			ID: "web-server",
			ValidateConfig: func(c config.Config, p platform.Platform) error {
				if c.WebServer.Provider == "openlitespeed" && p.Family == "alpine" {
					return fmt.Errorf("OpenLiteSpeed supports Debian/Ubuntu and RHEL-family packages, not Alpine")
				}
				return nil
			},
			RequiredPackages: webServerPackages,
			ContributePlan: func(result *plan.Plan, c config.Config, p platform.Platform) error {
				if c.WebServer.Provider == "openlitespeed" {
					addOpenLiteSpeedInstall(result, c, p)
				}
				return nil
			},
		},
		FeatureFunc{
			ID: "access-users",
			ContributePlan: func(result *plan.Plan, c config.Config, p platform.Platform) error {
				addUsers(result, c, p)
				return nil
			},
		},
		FeatureFunc{
			ID: "database",
			ValidateConfig: func(c config.Config, p platform.Platform) error {
				if c.Database != nil && isManagedMariaDBInstance(*c.Database) && p.Family == "alpine" {
					return fmt.Errorf("same-machine MariaDB replicas require systemd; Alpine/OpenRC is not supported yet")
				}
				return nil
			},
			RequiredPackages: func(c config.Config, p platform.Platform) []string {
				if c.Database == nil {
					return nil
				}
				return databasePackages(*c.Database, p)
			},
			ContributePlan: func(result *plan.Plan, c config.Config, p platform.Platform) error {
				addDatabase(result, c, p)
				return nil
			},
		},
		FeatureFunc{
			ID:               "sites",
			RequiredPackages: sitePackages,
			ContributePlan: func(result *plan.Plan, c config.Config, p platform.Platform) error {
				addSites(result, c, p)
				return nil
			},
		},
		FeatureFunc{
			ID: "ftp",
			RequiredPackages: func(c config.Config, _ platform.Platform) []string {
				if c.Access.FTP.Enabled {
					return []string{"vsftpd"}
				}
				return nil
			},
			ContributePlan: func(result *plan.Plan, c config.Config, p platform.Platform) error {
				addFTP(result, c, p)
				return nil
			},
		},
		FeatureFunc{
			ID:               "firewall",
			RequiredPackages: firewallPackages,
			ContributePlan: func(result *plan.Plan, c config.Config, p platform.Platform) error {
				addFirewall(result, c, p)
				return nil
			},
		},
		FeatureFunc{
			ID:               "tls",
			RequiredPackages: tlsPackages,
			ContributePlan: func(result *plan.Plan, c config.Config, p platform.Platform) error {
				addTLS(result, c, p)
				return nil
			},
		},
		FeatureFunc{
			ID:               "backups",
			RequiredPackages: backupPackages,
			ContributePlan: func(result *plan.Plan, c config.Config, p platform.Platform) error {
				addBackups(result, c, p)
				return nil
			},
		},
	}
}

func validateFeatures(c config.Config, p platform.Platform, features []Feature) error {
	seen := make(map[string]bool, len(features))
	for index, feature := range features {
		if feature == nil {
			return fmt.Errorf("feature %d is nil", index+1)
		}
		name := feature.Name()
		if name == "" {
			return fmt.Errorf("feature %d has an empty name", index+1)
		}
		if seen[name] {
			return fmt.Errorf("duplicate feature %q", name)
		}
		seen[name] = true
		if err := feature.Validate(c, p); err != nil {
			return fmt.Errorf("feature %s: %w", name, err)
		}
	}
	return nil
}

func packageSetForFeatures(c config.Config, p platform.Platform, features []Feature) []string {
	set := map[string]bool{}
	for _, feature := range features {
		for _, packageName := range feature.Packages(c, p) {
			if packageName != "" {
				set[packageName] = true
			}
		}
	}
	items := make([]string, 0, len(set))
	for item := range set {
		items = append(items, item)
	}
	sort.Strings(items)
	return items
}

func webServerPackages(c config.Config, p platform.Platform) []string {
	web := c.WebServer.Provider
	if web == "apache" {
		if p.Family == "debian" {
			return []string{"apache2"}
		}
		return []string{"httpd"}
	}
	if web == "openlitespeed" {
		return []string{"wget"}
	}
	return []string{web}
}

func sitePackages(c config.Config, p platform.Platform) []string {
	packages := []string{}
	for _, site := range c.Sites {
		if (site.Runtime == "php" || site.WordPress != nil) && c.WebServer.Provider != "openlitespeed" {
			packages = append(packages, phpPackages(p, c.WebServer.Provider, c.Database)...)
		}
	}
	if anyWordPress(c) && wordpressInitializationAllowed(c) {
		packages = append(packages, "curl", "tar")
	}
	return packages
}

func firewallPackages(c config.Config, p platform.Platform) []string {
	if !c.Firewall.Enabled {
		return nil
	}
	switch p.Family {
	case "debian":
		return []string{"ufw"}
	case "rhel":
		return []string{"firewalld"}
	default:
		return nil
	}
}

func tlsPackages(c config.Config, p platform.Platform) []string {
	if !c.TLS.Enabled {
		return nil
	}
	packages := []string{"certbot"}
	switch c.WebServer.Provider {
	case "nginx":
		if p.Family == "alpine" {
			packages = append(packages, "certbot-nginx")
		} else {
			packages = append(packages, "python3-certbot-nginx")
		}
	case "apache":
		if p.Family == "alpine" {
			packages = append(packages, "certbot-apache")
		} else {
			packages = append(packages, "python3-certbot-apache")
		}
	}
	return packages
}

func backupPackages(c config.Config, p platform.Platform) []string {
	if !c.Backups.Enabled {
		return nil
	}
	packages := []string{"rsync"}
	if c.Backups.Offsite == nil || c.Backups.Offsite.Provider != "s3" {
		return packages
	}
	switch p.Family {
	case "alpine":
		return append(packages, "aws-cli")
	case "rhel":
		return append(packages, "awscli2")
	default:
		return append(packages, "awscli")
	}
}
