package provider

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/plan"
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

func TestDatabaseChainPlanCreatesObjectsAndGrants(t *testing.T) {
	c := config.Default()
	c.Database = &config.Database{
		Provider: "postgresql",
		Role:     "primary",
		Users: []config.DatabaseUser{
			{Name: "owner", PasswordEnv: "OWNER_PASSWORD"},
			{Name: "reader", PasswordEnv: "READER_PASSWORD"},
		},
		Databases: []config.DatabaseSpec{{
			Name: "catalog", Owner: "owner",
			Tables: []config.DatabaseTable{{Name: "products", Schema: "public", Columns: []config.DatabaseColumn{{Name: "id", Type: "BIGINT"}}, PrimaryKey: []string{"id"}}},
		}},
		Permissions: []config.DatabasePermission{{User: "reader", Database: "catalog", Schema: "public", Table: "products", Privileges: []string{"SELECT"}}},
		Replication: config.Replication{User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", AllowedCIDR: "10.0.0.0/24"},
	}
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	descriptions := ""
	for _, step := range p.Steps {
		descriptions += step.Description + "\n"
	}
	for _, want := range []string{"Create PostgreSQL database users", "Create PostgreSQL database catalog", "Create PostgreSQL table public.products", "Grant PostgreSQL permissions to reader"} {
		if !strings.Contains(descriptions, want) {
			t.Errorf("database chain plan missing %q", want)
		}
	}
}

func TestReplicaPlanSkipsDatabaseChainWrites(t *testing.T) {
	c := config.Default()
	c.Database = &config.Database{
		Provider:    "postgresql",
		Role:        "replica",
		Port:        5433,
		DataDir:     "/var/lib/postgresql/replica",
		Databases:   []config.DatabaseSpec{{Name: "catalog", Tables: []config.DatabaseTable{{Name: "products", Columns: []config.DatabaseColumn{{Name: "id", Type: "BIGINT"}}}}}},
		Permissions: []config.DatabasePermission{{User: "reader", Database: "catalog", Privileges: []string{"SELECT"}}},
		Replication: config.Replication{PrimaryHost: "10.0.0.10", PrimaryPort: 5432, User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", Slot: "catalog_replica"},
	}
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range p.Steps {
		if strings.Contains(step.Description, "database user") || strings.Contains(step.Description, "database catalog") || strings.Contains(step.Description, "table") || strings.Contains(step.Description, "permissions") {
			t.Fatalf("replica plan contains write-side database step %q", step.Description)
		}
	}
}

func TestMariaDBReplicaPlanCreatesExplicitLocalDatabaseUserWithoutBinaryLogging(t *testing.T) {
	c := config.Default()
	c.Database = &config.Database{
		Provider: "mariadb",
		Role:     "replica",
		Users:    []config.DatabaseUser{{Name: "local_reader", PasswordEnv: "LOCAL_READER_PASSWORD", Local: true}},
		Replication: config.Replication{
			PrimaryHost: "10.0.0.10",
			PrimaryPort: 3306,
			User:        "replicator",
			PasswordEnv: "REPLICATION_PASSWORD",
			NodeID:      2,
		},
	}
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, step := range p.Steps {
		if step.Description == "Create MariaDB database user local_reader" {
			found = true
			if !strings.Contains(step.Input, "local_reader") || !strings.Contains(step.Input, "SET SESSION sql_log_bin=0") {
				t.Fatalf("local replica user SQL = %q", step.Input)
			}
		}
	}
	if !found {
		t.Fatal("MariaDB replica plan did not create the explicit local database user")
	}
}

func TestReplicaPlanSkipsNonLocalDatabaseUsers(t *testing.T) {
	c := config.Default()
	c.Database = &config.Database{
		Provider: "mariadb",
		Role:     "replica",
		Users:    []config.DatabaseUser{{Name: "replicated_reader", PasswordEnv: "READER_PASSWORD"}},
		Replication: config.Replication{
			PrimaryHost: "10.0.0.10", PrimaryPort: 3306, User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", NodeID: 2,
		},
	}
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range p.Steps {
		if strings.Contains(step.Description, "database user replicated_reader") {
			t.Fatalf("replica plan recreated replicated user: %q", step.Description)
		}
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

func TestTLSIsPlannedOnlyForEnabledSites(t *testing.T) {
	c := config.Default()
	disabled := false
	c.Sites = append(c.Sites, config.Site{
		Domain: "plain.example.com",
		Root:   "/var/www/plain.example.com",
		TLS:    &disabled,
	})
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	foundEnabled := false
	for _, step := range p.Steps {
		if strings.Contains(step.Description, "TLS certificate for plain.example.com") {
			t.Fatalf("disabled site received a TLS step: %q", step.Description)
		}
		if step.Description == "Obtain and attach TLS certificate for example.com" {
			foundEnabled = true
		}
	}
	if !foundEnabled {
		t.Fatal("enabled site did not receive a TLS step")
	}
}

func TestS3BackupPlanUploadsAndPrunesIndependentCopies(t *testing.T) {
	c := config.Default()
	c.Backups.RetentionDays = 30
	c.Backups.Offsite = &config.OffsiteBackup{
		Provider:      "s3",
		Bucket:        "company-server-backups",
		Prefix:        "production/web-01",
		Region:        "ap-southeast-1",
		Profile:       "backup-writer",
		RetentionDays: 90,
	}
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	var packages, script string
	for _, step := range p.Steps {
		if step.Description == "Install required packages" {
			packages = strings.Join(step.Args, " ")
		}
		if step.Description == "Install backup script" {
			script = step.Content
		}
	}
	for _, want := range []string{"awscli", "rsync"} {
		if !strings.Contains(packages, want) {
			t.Errorf("backup packages %q do not contain %q", packages, want)
		}
	}
	for _, want := range []string{
		"aws --profile 'backup-writer' --region 'ap-southeast-1' s3 cp",
		"S3_ROOT='s3://company-server-backups/'",
		"S3_PREFIX='production/web-01/'",
		"list-objects-v2",
		"s3 rm",
		"date -u -d '-90 days'",
		"-mmin +43200",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("backup script does not contain %q:\n%s", want, script)
		}
	}
	if len(p.Warnings) == 0 || !strings.Contains(strings.Join(p.Warnings, " "), "PutObject") {
		t.Fatalf("S3 permissions warning missing: %v", p.Warnings)
	}
	command := exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated backup script is not valid shell: %v: %s\n%s", err, output, script)
	}
}

func TestS3BackupUsesReplicaSpecificPrefix(t *testing.T) {
	c := config.Default()
	c.Database = &config.Database{
		Provider: "mariadb", Role: "replica", Port: 3307, DataDir: "/var/lib/mysql/poorman-replica-3307",
		Replication: config.Replication{PrimaryHost: "127.0.0.1", PrimaryPort: 3306, User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", NodeID: 2},
	}
	c.Backups.Offsite = &config.OffsiteBackup{Provider: "s3", Bucket: "company-server-backups", Prefix: "production"}
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range p.Steps {
		if step.Description == "Install backup script" && strings.Contains(step.Content, "S3_PREFIX='production/poorman-mariadb-replica-3307/'") {
			return
		}
	}
	t.Fatal("replica backup did not receive an isolated S3 prefix")
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

func TestOpenLiteSpeedFixesExampleOwnershipBeforeValidation(t *testing.T) {
	c := config.Default()
	c.WebServer.Provider = "openlitespeed"
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	ownershipIndex, validationIndex := -1, -1
	for i, step := range p.Steps {
		switch step.Description {
		case "Restore OpenLiteSpeed example root ownership":
			ownershipIndex = i
			if step.Kind != plan.Directory || step.Path != "/usr/local/lsws/Example/html" || step.Owner != "nobody" || step.Group != "nogroup" || step.Mode != 0o755 {
				t.Fatalf("example ownership step = %#v", step)
			}
		case "Validate openlitespeed configuration":
			validationIndex = i
		}
	}
	if ownershipIndex < 0 || validationIndex < 0 || ownershipIndex >= validationIndex {
		t.Fatalf("ownership step index %d, validation step index %d; ownership must run first", ownershipIndex, validationIndex)
	}
}

func TestManagedServerMOTDIsInstalledWithPlatformOwnership(t *testing.T) {
	for _, tt := range []struct {
		family string
		path   string
		mode   uint32
	}{
		{family: "debian", path: "/etc/update-motd.d/99-poorman", mode: 0o755},
		{family: "rhel", path: "/etc/motd.d/99-poorman", mode: 0o644},
		{family: "alpine", path: "/etc/motd", mode: 0o644},
	} {
		c := config.Default()
		p, err := Build(c, platform.Platform{Distro: tt.family, Family: tt.family})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, step := range p.Steps {
			if step.Description != "Install poorman managed-server MOTD" {
				continue
			}
			found = true
			if step.Path != tt.path || step.Mode != tt.mode || step.Owner != "root" || step.Group != "root" || !strings.Contains(step.Content, managedServerMessage) {
				t.Errorf("%s MOTD step = %#v", tt.family, step)
			}
		}
		if !found {
			t.Errorf("%s plan has no managed-server MOTD", tt.family)
		}
	}
}

func TestEveryManagedFileAndDirectoryDeclaresOwnership(t *testing.T) {
	for _, tt := range []struct{ web, family string }{
		{web: "nginx", family: "debian"},
		{web: "apache", family: "rhel"},
		{web: "openlitespeed", family: "debian"},
	} {
		c := config.Default()
		c.WebServer.Provider = tt.web
		p, err := Build(c, platform.Platform{Distro: tt.family, Family: tt.family})
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range p.Steps {
			if step.Kind != plan.Directory && step.Kind != plan.File && step.Kind != plan.Line {
				continue
			}
			if step.Owner == "" || step.Group == "" {
				t.Errorf("%s/%s step lacks explicit ownership: %#v", tt.family, tt.web, step)
			}
		}
	}
}

func TestWebRootOwnershipUsesDeploymentOwnerAndRuntimeGroup(t *testing.T) {
	for _, tt := range []struct{ web, family, group string }{
		{web: "nginx", family: "debian", group: "www-data"},
		{web: "nginx", family: "rhel", group: "nginx"},
		{web: "apache", family: "debian", group: "www-data"},
		{web: "apache", family: "rhel", group: "apache"},
		{web: "openlitespeed", family: "debian", group: "nogroup"},
		{web: "openlitespeed", family: "rhel", group: "nobody"},
	} {
		c := config.Default()
		c.WebServer.Provider = tt.web
		p, err := Build(c, platform.Platform{Distro: tt.family, Family: tt.family})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, step := range p.Steps {
			if step.Description != "Create document root for example.com" {
				continue
			}
			found = true
			if step.Owner != "webadmin" || step.Group != tt.group || step.Mode != 0o750 {
				t.Errorf("%s/%s document root ownership = %s:%s mode %04o", tt.family, tt.web, step.Owner, step.Group, step.Mode)
			}
		}
		if !found {
			t.Errorf("%s/%s document root step missing", tt.family, tt.web)
		}
	}
}

func TestSitePlanVerifiesUnmanagedOwnerOnce(t *testing.T) {
	c := config.Default()
	c.Sites[0].Owner = "existing-user"
	c.Sites = append(c.Sites, config.Site{Domain: "second.example.com", Root: "/var/www/second.example.com", Owner: "existing-user", Runtime: "static"})
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	verificationCount, verificationIndex, firstRootIndex := 0, -1, -1
	for i, step := range p.Steps {
		if step.Description == "Verify existing system user existing-user" {
			verificationCount++
			verificationIndex = i
			if step.Command != "id" || !slices.Equal(step.Args, []string{"-u", "existing-user"}) {
				t.Fatalf("owner verification step = %#v", step)
			}
		}
		if firstRootIndex < 0 && step.Description == "Create document root for example.com" {
			firstRootIndex = i
		}
	}
	if verificationCount != 1 || verificationIndex < 0 || firstRootIndex < 0 || verificationIndex >= firstRootIndex {
		t.Fatalf("verification count/index = %d/%d, first root index = %d", verificationCount, verificationIndex, firstRootIndex)
	}
}

func TestSitePlanCreatesReplaceableWelcomePage(t *testing.T) {
	for _, tt := range []struct {
		web, runtime, stack string
	}{
		{web: "nginx", runtime: "static", stack: "nginx + static HTML"},
		{web: "apache", runtime: "php", stack: "apache + PHP"},
	} {
		c := config.Default()
		c.WebServer.Provider = tt.web
		c.Sites[0].Runtime = tt.runtime
		p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, step := range p.Steps {
			if step.Description != "Create welcome page for example.com" {
				continue
			}
			found = true
			if step.Path != "/var/www/example.com/index.html" || step.Owner != "webadmin" || step.Group != "www-data" || step.Mode != 0o640 {
				t.Errorf("welcome page metadata = %#v", step)
			}
			if step.UnlessCommand != "test" || !slices.Equal(step.UnlessArgs, []string{"-e", step.Path}) || step.SkipIfNotEmpty != "/var/www/example.com" {
				t.Errorf("welcome page is not create-only: %#v", step)
			}
			for _, want := range []string{"Welcome to example.com", "Poorman's Panel", tt.stack, step.Path} {
				if !strings.Contains(step.Content, want) {
					t.Errorf("welcome page missing %q:\n%s", want, step.Content)
				}
			}
		}
		if !found {
			t.Errorf("%s/%s welcome page step missing", tt.web, tt.runtime)
		}
	}
}

func TestWordPressSiteDoesNotCreateWelcomePage(t *testing.T) {
	c := config.Default()
	c.Database = &config.Database{Provider: "mariadb", Name: "website", User: "website", PasswordEnv: "POORMAN_DB_PASSWORD"}
	c.Sites[0].Runtime = "php"
	c.Sites[0].WordPress = &config.WordPress{AdminEmail: "admin@example.com", AdminPassEnv: "POORMAN_WP_ADMIN_PASSWORD"}
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range p.Steps {
		if step.Description == "Create welcome page for example.com" {
			t.Fatal("WordPress site received a welcome page that could mask index.php")
		}
	}
}

func TestManagedWebFilesCoverEveryVhostAndProviderSwitch(t *testing.T) {
	c := config.Default()
	c.Sites = append(c.Sites, config.Site{Domain: "shop.example.com", Root: "/var/www/shop.example.com", Owner: "webadmin", Runtime: "php"})
	for _, tt := range []struct {
		web     string
		family  string
		service string
		files   []string
	}{
		{web: "nginx", family: "debian", service: "nginx", files: []string{"/etc/nginx/conf.d/poorman-example.com.conf", "/etc/nginx/conf.d/poorman-shop.example.com.conf"}},
		{web: "apache", family: "rhel", service: "httpd", files: []string{"/etc/httpd/conf.d/poorman-example.com.conf", "/etc/httpd/conf.d/poorman-shop.example.com.conf"}},
		{web: "openlitespeed", family: "debian", service: "lsws", files: []string{"/usr/local/lsws/conf/poorman.conf", "/usr/local/lsws/conf/vhosts/example.com/vhconf.conf", "/usr/local/lsws/conf/vhosts/shop.example.com/vhconf.conf"}},
	} {
		c.WebServer.Provider = tt.web
		services := desiredManagedServices(c, platform.Platform{Distro: tt.family, Family: tt.family}, "/etc/poorman.json")
		if len(services) == 0 || services[0].Name != tt.service || !slices.Equal(services[0].Files, tt.files) {
			t.Errorf("%s/%s managed web service = %#v, want service %s files %#v", tt.family, tt.web, services[0], tt.service, tt.files)
		}
	}
}

func TestOpenLiteSpeedVhostUsesSiteOwnerAndManagedHeader(t *testing.T) {
	c := config.Default()
	c.WebServer.Provider = "openlitespeed"
	_, content := siteConfig("openlitespeed", c.Sites[0], platform.Platform{Distro: "ubuntu", Family: "debian"})
	for _, want := range []string{managedConfigHeader, "extUser                 webadmin", "extGroup                webadmin"} {
		if !strings.Contains(content, want) {
			t.Errorf("OpenLiteSpeed vhost config missing %q:\n%s", want, content)
		}
	}
}

func TestPostgresReplicaLineEditPreservesDatabaseOwnership(t *testing.T) {
	c := config.Default()
	c.Database = &config.Database{
		Provider: "postgresql", Role: "replica", Port: 5433, DataDir: "/var/lib/postgresql/poorman-replica",
		Replication: config.Replication{PrimaryHost: "10.0.0.10", PrimaryPort: 5432, User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", Slot: "poorman_replica_1"},
	}
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range p.Steps {
		if step.Description == "Set PostgreSQL replica port" {
			if step.Kind != plan.Line || step.Owner != "postgres" || step.Group != "postgres" || step.Mode != 0o600 {
				t.Fatalf("PostgreSQL line edit ownership = %#v", step)
			}
			return
		}
	}
	t.Fatal("PostgreSQL replica port step not found")
}

func TestOpenLiteSpeedDebianUsesPublishedLSPHPPackages(t *testing.T) {
	c := config.Default()
	c.WebServer.Provider = "openlitespeed"
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	var installArgs string
	for _, step := range p.Steps {
		if step.Description == "Install required packages" && containsArg(step.Args, "openlitespeed") {
			installArgs = strings.Join(step.Args, " ")
			break
		}
	}
	if installArgs == "" {
		t.Fatal("OpenLiteSpeed package installation step not found")
	}
	for _, want := range []string{"openlitespeed", "lsphp84", "lsphp84-common", "lsphp84-curl", "lsphp84-mysql"} {
		if !strings.Contains(installArgs, want) {
			t.Errorf("OpenLiteSpeed install packages missing %q: %s", want, installArgs)
		}
	}
	for _, unavailable := range []string{"lsphp84-gd", "lsphp84-mbstring", "lsphp84-xml", "lsphp84-zip"} {
		if strings.Contains(installArgs, unavailable) {
			t.Errorf("OpenLiteSpeed Debian install includes unavailable package %q: %s", unavailable, installArgs)
		}
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
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

func TestSameMachineMariaDBReplicaUsesIndependentService(t *testing.T) {
	c := config.Default()
	c.Database.Role = "replica"
	c.Database.Port = 3307
	c.Database.DataDir = "/var/lib/mysql/poorman-replica-3307"
	c.Database.Replication = config.Replication{PrimaryHost: "127.0.0.1", PrimaryPort: 3306, User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", NodeID: 2}
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string]bool{
		"Create MariaDB replica data directory":               false,
		"Create MariaDB replica runtime directory":            false,
		"Initialize MariaDB replica data directory":           false,
		"Install independent MariaDB replica service":         false,
		"Restart independent MariaDB replica service":         false,
		"Wait for MariaDB replica socket":                     false,
		"Seed MariaDB replica from local primary":             false,
		"Load primary snapshot into MariaDB replica":          false,
		"Attach independent MariaDB replica to local primary": false,
	}
	for _, step := range p.Steps {
		if _, ok := wants[step.Description]; ok {
			wants[step.Description] = true
		}
		if step.Description == "Configure MariaDB replica" {
			t.Fatal("local replica overwrites the primary's global MariaDB configuration")
		}
		if step.Description == "Enable and start mariadb" || step.Description == "Reload or restart mariadb" {
			t.Fatal("local replica plan manipulates the primary MariaDB service")
		}
		if step.Description == "Install independent MariaDB replica service" {
			if step.Path != "/etc/systemd/system/poorman-mariadb-replica-3307.service" || !strings.Contains(step.Content, "Restart=on-failure") || strings.Contains(step.Content, "Requires=mariadb.service") {
				t.Fatalf("independent service is not isolated from primary failure: %#v", step)
			}
		}
		if step.Description == "Wait for MariaDB replica socket" {
			if step.Command != "mariadb-admin" || step.TimeoutSeconds != 60 || !strings.Contains(strings.Join(step.Args, " "), "--socket=/run/poorman-mariadb-replica-3307/mariadb.sock") {
				t.Fatalf("replica readiness check is incomplete: %#v", step)
			}
		}
		if step.Description == "Load primary snapshot into MariaDB replica" && step.TimeoutSeconds != 60 {
			t.Fatalf("snapshot load has no 60-second timeout: %#v", step)
		}
		if step.Description == "Attach independent MariaDB replica to local primary" && !strings.Contains(strings.Join(step.Args, " "), "--socket=/run/poorman-mariadb-replica-3307/mariadb.sock") {
			t.Fatalf("replication SQL does not target the replica socket: %v", step.Args)
		}
		if step.Description == "Install backup script" {
			if step.Path != "/usr/local/sbin/poorman-backup-poorman-mariadb-replica-3307" || !strings.Contains(step.Content, "--socket='/run/poorman-mariadb-replica-3307/mariadb.sock'") {
				t.Fatalf("replica backup is not isolated from the primary: %#v", step)
			}
		}
	}
	for description, found := range wants {
		if !found {
			t.Errorf("plan missing %q", description)
		}
	}
}

func TestOneConfigPlansPrimaryAndLocalMariaDBReplicaWithOpenLiteSpeed(t *testing.T) {
	c := config.Default()
	c.WebServer.Provider = "openlitespeed"
	c.Database.Role = "primary"
	c.Database.Replication = config.Replication{User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", AllowedCIDR: "127.0.0.1/32", NodeID: 1}
	c.Database.LocalReplica = &config.LocalReplica{Port: 3307, DataDir: "/var/lib/mysql/poorman-replica-3307", NodeID: 2}
	p, err := Build(c, platform.Platform{Distro: "debian", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	primaryUser, localService, attach, apache := -1, -1, -1, false
	for i, step := range p.Steps {
		switch step.Description {
		case "Create MariaDB replication user":
			primaryUser = i
		case "Install independent MariaDB replica service":
			localService = i
		case "Attach independent MariaDB replica to local primary":
			attach = i
		}
		if step.Command == "apachectl" || strings.Contains(step.Path, "/apache2/") {
			apache = true
		}
	}
	if primaryUser < 0 || localService <= primaryUser || attach <= localService {
		t.Fatalf("combined plan ordering primary=%d local-service=%d attach=%d", primaryUser, localService, attach)
	}
	if apache {
		t.Fatal("OpenLiteSpeed combined plan contains Apache configuration")
	}
	status, err := ReplicaStatus(c, platform.Platform{Distro: "debian", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	promotion, err := PromoteReplica(c, platform.Platform{Distro: "debian", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	for name, operation := range map[string]plan.Plan{"status": status, "promotion": promotion} {
		foundSocket := false
		for _, step := range operation.Steps {
			if strings.Contains(strings.Join(step.Args, " "), "--socket=/run/poorman-mariadb-replica-3307/mariadb.sock") {
				foundSocket = true
			}
		}
		if !foundSocket {
			t.Errorf("%s does not target the nested local replica socket: %#v", name, operation.Steps)
		}
	}
}

func TestBuildForConfigAddsManagedServiceReconciliation(t *testing.T) {
	c := config.Default()
	c.Database.Role = "replica"
	c.Database.Port = 3307
	c.Database.DataDir = "/var/lib/mysql/poorman-replica-3307"
	c.Database.Replication.PrimaryHost = "127.0.0.1"
	p, err := BuildForConfig(c, platform.Platform{Distro: "debian", Family: "debian"}, "/etc/poorman/replica.json")
	if err != nil {
		t.Fatal(err)
	}
	var reconcile, state bool
	for _, step := range p.Steps {
		if step.Kind == plan.Reconcile {
			reconcile = true
		}
		if step.Kind == plan.State {
			state = true
		}
	}
	if !reconcile || !state {
		t.Fatalf("managed service steps missing: reconcile=%t state=%t", reconcile, state)
	}
}

func TestSameMachineMariaDBStatusAndPromotionTargetReplicaSocket(t *testing.T) {
	c := config.Default()
	c.Database.Role = "replica"
	c.Database.Port = 3307
	c.Database.DataDir = "/var/lib/mysql/poorman-replica-3307"
	c.Database.Replication = config.Replication{PrimaryHost: "127.0.0.1", PrimaryPort: 3306, User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", NodeID: 2}
	for name, build := range map[string]func() (plan.Plan, error){
		"status": func() (plan.Plan, error) {
			return ReplicaStatus(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
		},
		"promotion": func() (plan.Plan, error) {
			return PromoteReplica(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
		},
	} {
		operation, err := build()
		if err != nil {
			t.Fatal(err)
		}
		step := operation.Steps[len(operation.Steps)-1]
		if !strings.Contains(strings.Join(step.Args, " "), "--socket=/run/poorman-mariadb-replica-3307/mariadb.sock") {
			t.Errorf("%s does not target replica socket: %#v", name, operation.Steps)
		}
		if name == "promotion" && (len(operation.Steps) != 2 || operation.Steps[0].Description != "Persist promoted MariaDB instance as writable") {
			t.Errorf("promotion does not persist writable state: %#v", operation.Steps)
		}
		if name == "promotion" && (!strings.Contains(operation.Steps[0].Content, "read_only=OFF") || operation.Steps[0].Mode != 0o644) {
			t.Errorf("promoted service config is not readable and writable: %#v", operation.Steps[0])
		}
	}
}

func TestPromotedSameMachineMariaDBKeepsIndependentService(t *testing.T) {
	c := config.Default()
	c.Database.Role = "primary"
	c.Database.Port = 3307
	c.Database.DataDir = "/var/lib/mysql/poorman-replica-3307"
	c.Database.Replication = config.Replication{PrimaryHost: "127.0.0.1", PrimaryPort: 3306, User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", AllowedCIDR: "127.0.0.1/32", NodeID: 2}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	p, err := Build(c, platform.Platform{Distro: "ubuntu", Family: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	foundWritableConfig, foundCustomSQL := false, false
	for _, step := range p.Steps {
		if step.Description == "Enable and start mariadb" || step.Description == "Configure MariaDB primary" {
			t.Fatal("promoted instance plan manipulates the old primary service")
		}
		if step.Description == "Configure independent MariaDB instance" && strings.Contains(step.Content, "read_only=OFF") {
			foundWritableConfig = true
		}
		if step.Description == "Update application database on promoted MariaDB instance" && strings.Contains(strings.Join(step.Args, " "), "--socket=/run/poorman-mariadb-replica-3307/mariadb.sock") {
			foundCustomSQL = true
		}
		if step.Description == "Seed MariaDB replica from local primary" || step.Description == "Attach independent MariaDB replica to local primary" {
			t.Fatal("promoted instance plan tries to reseed or reattach as a replica")
		}
	}
	if !foundWritableConfig || !foundCustomSQL {
		t.Fatalf("promoted plan does not retain independent writable service: config=%t sql=%t", foundWritableConfig, foundCustomSQL)
	}
}

func TestSameMachineMariaDBReplicaRejectsUnsupportedOpenRC(t *testing.T) {
	c := config.Default()
	c.Database.Role = "replica"
	c.Database.Port = 3307
	c.Database.DataDir = "/var/lib/mysql/poorman-replica-3307"
	c.Database.Replication = config.Replication{PrimaryHost: "127.0.0.1", PrimaryPort: 3306, User: "replicator", PasswordEnv: "REPLICATION_PASSWORD", NodeID: 2}
	if _, err := Build(c, platform.Platform{Distro: "alpine", Family: "alpine"}); err == nil {
		t.Fatal("expected a clear unsupported OpenRC error")
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
