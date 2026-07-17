package app

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/managed"
)

func TestInputReaderPreservesExistingBuffer(t *testing.T) {
	reader := bufio.NewReader(bytes.NewBufferString("y\n\nnext\n"))
	if got, _ := inputReader(reader).ReadString('\n'); got != "y\n" {
		t.Fatalf("confirmation = %q, want %q", got, "y\n")
	}
	if got, _ := reader.ReadString('\n'); got != "\n" {
		t.Fatalf("queued Enter = %q, want a blank line", got)
	}
}

func TestEnsureDatabasePasswordPersistsGeneratedValue(t *testing.T) {
	name := "POORMAN_TEST_GENERATED_DB_PASSWORD"
	t.Setenv(name, "")
	path := filepath.Join(t.TempDir(), "server.json")
	c := config.Config{Database: &config.Database{PasswordEnv: name}}
	var out bytes.Buffer
	if err := ensureDatabasePassword(c, path, &out); err != nil {
		t.Fatal(err)
	}
	first := os.Getenv(name)
	if len(first) < 40 {
		t.Fatalf("generated password is unexpectedly short: %d", len(first))
	}
	if info, err := os.Stat(path + ".secrets"); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file permissions = %v, err = %v", info, err)
	}
	os.Unsetenv(name)
	if err := ensureDatabasePassword(c, path, &out); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(name); got != first {
		t.Fatalf("reloaded password = %q, want original", got)
	}
}

func TestTUIWritesSelectedConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.json")
	in := bytes.NewBufferString("2\nblog.example.com\n\n")
	var out bytes.Buffer
	if err := Run([]string{"tui", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.WebServer.Provider != "apache" {
		t.Fatalf("provider = %q, want apache", c.WebServer.Provider)
	}
	if got := c.Sites[0].Root; got != "/var/www/blog.example.com" {
		t.Fatalf("root = %q", got)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions are not private: info=%v err=%v", info, err)
	}
}

func TestTUIShowsOperationsForExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.json")
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	in := bytes.NewBufferString("0\n")
	var out bytes.Buffer
	if err := Run([]string{"tui", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("poorman operations")) {
		t.Fatal("operations screen not shown")
	}
	if !bytes.Contains(out.Bytes(), []byte("Firewall management")) {
		t.Fatal("firewall management option not shown")
	}
	if !bytes.Contains(out.Bytes(), []byte("long-term operations")) {
		t.Fatal("long-term operations option not shown")
	}
}

func TestUIPanelsKeepTheirRightBorderAligned(t *testing.T) {
	var out bytes.Buffer
	newTerminalUI(&out).panel("TEST", "short\nthis line is deliberately longer than the default panel width so the panel must grow as one unit")
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("panel lines = %d, want 4", len(lines))
	}
	want := utf8.RuneCountInString(lines[0])
	for i, line := range lines {
		if got := utf8.RuneCountInString(line); got != want {
			t.Fatalf("panel line %d width = %d, want %d:\n%s", i, got, want, out.String())
		}
	}
}

func TestDashboardActionColumnsStayAligned(t *testing.T) {
	first := dashboardActionLine(1, 5, 0, "preview plan", "replication status")
	second := dashboardActionLine(2, 6, 0, "apply configuration", "Firewall management")
	if got, want := strings.Index(first, "  5"), strings.Index(second, "  6"); got != want {
		t.Fatalf("right-column starts = %d and %d, want equal", got, want)
	}
}

func TestTUIEnablesBackupsFromGuardrailsMenu(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	c := config.Default()
	c.Backups.Enabled = false
	c.Backups.Destination = ""
	c.Backups.Schedule = ""
	if err := config.Write(path, c); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	in := bytes.NewBufferString("11\n3\ny\n\n\n0\n0\n")
	if err := Run([]string{"tui", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Backups.Enabled || got.Backups.Destination != "/var/backups/poorman" || got.Backups.Schedule != "0 3 * * *" {
		t.Fatalf("backups = %#v, want enabled defaults", got.Backups)
	}
}

func TestTUIManagesMultipleVirtualHosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.json")
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	in := bytes.NewBufferString("8\n1\nshop.example.com\n\n\nstatic\n\n0\n0\n")
	var out bytes.Buffer
	if err := Run([]string{"tui", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Sites) != 2 || c.Sites[1].Domain != "shop.example.com" {
		t.Fatalf("sites = %#v, want second host shop.example.com", c.Sites)
	}
}

func TestTUICreatesDatabaseChainObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.json")
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	in := bytes.NewBufferString("12\n1\nanalytics\n\n0\n0\n")
	if err := Run([]string{"tui", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Database == nil || len(c.Database.Databases) != 2 || c.Database.Databases[1].Name != "analytics" {
		t.Fatalf("databases = %#v, want legacy database plus analytics", c.Database)
	}
}

func TestTUIDatabaseManagerCreatesTableForSelectedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database-table.json")
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	in := bytes.NewBufferString("12\n2\n1\nitems\nid:BIGINT\nid\n0\n0\n0\n")
	if err := Run([]string{"tui", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Database == nil || len(c.Database.Databases) != 1 || len(c.Database.Databases[0].Tables) != 1 || c.Database.Databases[0].Tables[0].Name != "items" {
		t.Fatalf("database tables = %#v, want items on selected database", c.Database)
	}
}

func TestTUIDatabaseManagerSetsACLForExistingUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database-acl.json")
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	in := bytes.NewBufferString("12\n2\n2\n1\n1\n1\nn\n0\n0\n0\n")
	if err := Run([]string{"tui", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Database == nil || len(c.Database.Permissions) != 1 || c.Database.Permissions[0].User != "example" || c.Database.Permissions[0].Database != "example" {
		t.Fatalf("database permissions = %#v, want example on example", c.Database)
	}
}

func TestTUIConfiguresPostgresReplica(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replica.json")
	in := bytes.NewBufferString("1\nreplica.example.com\n\n\nphp\npostgresql\nreplica\n")
	var out bytes.Buffer
	if err := Run([]string{"tui", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Database == nil || c.Database.Role != "replica" {
		t.Fatalf("database = %#v, want replica", c.Database)
	}
	if got := c.Database.Replication.PrimaryHost; got != "10.20.0.10" {
		t.Fatalf("primary host = %q", got)
	}
}

func TestGuidedReplicaSetupSupportsSameMachine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.json")
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	in := bytes.NewBufferString("postgresql\n\n\ny\n\n\n\n\n\n\n")
	var out bytes.Buffer
	if err := Run([]string{"replica", "setup", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Database == nil || c.Database.Role != "replica" || c.Database.Port != 5433 || c.Database.Replication.PrimaryHost != "127.0.0.1" || c.Database.Replication.PrimaryPort != 5432 {
		t.Fatalf("database = %#v, want same-machine PostgreSQL replica ports", c.Database)
	}
	if c.Database.DataDir == "/var/lib/postgresql/18/main" {
		t.Fatalf("data_dir = %q, must not reuse the primary default", c.Database.DataDir)
	}
}

func TestGuidedReplicaSetupNormalizesClonedPostgresTopology(t *testing.T) {
	dir := t.TempDir()
	primaryPath := filepath.Join(dir, "primary.json")
	replicaPath := filepath.Join(dir, "replica.json")
	c := config.Default()
	c.Database = &config.Database{
		Provider: "postgresql", Role: "primary", Port: 5544, DataDir: "/var/lib/postgresql/primary",
		Replication: config.Replication{User: "replicator", PasswordEnv: "POORMAN_TEST_REPLICATION_SECRET", AllowedCIDR: "10.20.0.0/24", Slot: "poorman_replica_1"},
	}
	if err := config.Write(primaryPath, c); err != nil {
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
	if got := replica.Database.Replication.PrimaryPort; got != 5544 {
		t.Fatalf("primary port = %d, want cloned source port 5544", got)
	}
	if got := replica.Database.Port; got != 5545 {
		t.Fatalf("replica port = %d, want 5545", got)
	}
	if replica.Database.DataDir == c.Database.DataDir || replica.Database.DataDir == "" {
		t.Fatalf("replica data_dir = %q, must be distinct from primary", replica.Database.DataDir)
	}
}

func TestGuidedReplicaSetupGivesMariaDBReplicaUniqueNodeID(t *testing.T) {
	dir := t.TempDir()
	primaryPath := filepath.Join(dir, "primary.json")
	replicaPath := filepath.Join(dir, "replica.json")
	c := config.Default()
	c.Database.Role = "primary"
	c.Database.Port = 3310
	c.Database.Replication = config.Replication{User: "replicator", PasswordEnv: "POORMAN_TEST_REPLICATION_SECRET", AllowedCIDR: "10.20.0.0/24", NodeID: 7}
	if err := config.Write(primaryPath, c); err != nil {
		t.Fatal(err)
	}
	in := bytes.NewBufferString("\n\n\nn\n\n\n\n")
	var out bytes.Buffer
	if err := Run([]string{"replica", "setup", "-f", replicaPath, "--from", primaryPath}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	replica, err := config.Load(replicaPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := replica.Database.Replication.PrimaryPort; got != 3310 {
		t.Fatalf("primary port = %d, want cloned source port 3310", got)
	}
	if got := replica.Database.Replication.NodeID; got != 8 {
		t.Fatalf("replica node ID = %d, want 8", got)
	}
}

func TestConfigureReplicationSetsMariaDBPrimaryNodeID(t *testing.T) {
	database := &config.Database{Provider: "mariadb", Role: "primary"}
	var out bytes.Buffer
	if err := configureReplication(bufio.NewReader(bytes.NewBufferString("\n\n\n\n")), newTerminalUI(&out), database); err != nil {
		t.Fatal(err)
	}
	if database.Replication.NodeID != 1 {
		t.Fatalf("primary node ID = %d, want 1", database.Replication.NodeID)
	}
}

func TestConfigureReplicationCreatesIndependentSameMachineMariaDB(t *testing.T) {
	database := &config.Database{Provider: "mariadb", Role: "replica"}
	var out bytes.Buffer
	if err := configureReplication(bufio.NewReader(bytes.NewBufferString("\n\ny\n\n\n\n\n\n")), newTerminalUI(&out), database); err != nil {
		t.Fatal(err)
	}
	if database.Port != 3307 || database.Replication.PrimaryPort != 3306 || database.DataDir != "/var/lib/mysql/poorman-replica-3307" {
		t.Fatalf("database = %#v, want independent local MariaDB instance defaults", database)
	}
}

func TestReplicaSecretsAreCopiedFromPrimaryConfig(t *testing.T) {
	const databaseEnv = "POORMAN_TEST_REPLICA_DB_SECRET"
	const replicationEnv = "POORMAN_TEST_REPLICA_LINK_SECRET"
	t.Setenv(databaseEnv, "")
	t.Setenv(replicationEnv, "")
	dir := t.TempDir()
	primaryPath := filepath.Join(dir, "primary.json")
	replicaPath := filepath.Join(dir, "replica.json")
	c := config.Config{Database: &config.Database{Role: "primary", PasswordEnv: databaseEnv, Replication: config.Replication{PasswordEnv: replicationEnv}}}
	var out bytes.Buffer
	if err := ensureConfigSecrets(c, primaryPath, &out); err != nil {
		t.Fatal(err)
	}
	primaryReplicationSecret := os.Getenv(replicationEnv)
	if primaryReplicationSecret == "" {
		t.Fatal("primary replication secret was not generated")
	}
	c.Database.Role = "replica"
	if err := copyConfigSecrets(primaryPath, replicaPath, c); err != nil {
		t.Fatal(err)
	}
	replicaSecrets, err := readSecretValues(replicaPath + ".secrets")
	if err != nil {
		t.Fatal(err)
	}
	if replicaSecrets[databaseEnv] == "" {
		t.Fatal("replica handoff did not preserve the application database secret needed after promotion")
	}
	os.Unsetenv(databaseEnv)
	os.Unsetenv(replicationEnv)
	if err := ensureConfigSecrets(c, replicaPath, &out); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(replicationEnv); got != primaryReplicationSecret {
		t.Fatalf("replica replication secret did not match primary")
	}
}

func TestReplicaSecretIsNeverInvented(t *testing.T) {
	const replicationEnv = "POORMAN_TEST_MISSING_REPLICATION_SECRET"
	t.Setenv(replicationEnv, "")
	c := config.Config{Database: &config.Database{Role: "replica", Replication: config.Replication{PasswordEnv: replicationEnv}}}
	var out bytes.Buffer
	err := ensureConfigSecrets(c, filepath.Join(t.TempDir(), "replica.json"), &out)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("from the primary")) {
		t.Fatalf("error = %v, want missing primary secret guidance", err)
	}
}

func TestSharedReplicaSecretIsNeverInvented(t *testing.T) {
	const sharedEnv = "POORMAN_TEST_SHARED_REPLICA_SECRET"
	t.Setenv(sharedEnv, "")
	c := config.Config{
		Database: &config.Database{Role: "replica", PasswordEnv: sharedEnv, Replication: config.Replication{PasswordEnv: sharedEnv}},
		Sites:    []config.Site{{WordPress: &config.WordPress{AdminPassEnv: sharedEnv}}},
	}
	var out bytes.Buffer
	err := ensureConfigSecrets(c, filepath.Join(t.TempDir(), "replica.json"), &out)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("from the primary")) {
		t.Fatalf("error = %v, want missing shared primary secret guidance", err)
	}
	if got := os.Getenv(sharedEnv); got != "" {
		t.Fatalf("shared replica secret was invented: %q", got)
	}
}

func TestNormalizeReplicaDatabaseResetsTopologyWhenProviderChanges(t *testing.T) {
	database := &config.Database{
		Provider: "mariadb",
		Role:     "replica",
		Port:     3307,
		DataDir:  "/var/lib/mysql/replica",
		Replication: config.Replication{
			PrimaryHost: "db.example.com",
			PrimaryPort: 3306,
			NodeID:      2,
		},
	}
	normalizeReplicaDatabase(database, "postgresql")
	if database.Port != 0 || database.DataDir != "" || database.Replication.PrimaryHost != "" || database.Replication.PrimaryPort != 0 || database.Replication.NodeID != 0 {
		t.Fatalf("provider change retained incompatible topology: %#v", database)
	}
}

func TestTUIReplicaSetupKeepsDashboardInputFlow(t *testing.T) {
	primaryPath := filepath.Join(t.TempDir(), "primary.json")
	replicaPath := filepath.Join(t.TempDir(), "replica.json")
	primary := config.Default()
	primary.Database.Provider = "postgresql"
	if err := config.Write(primaryPath, primary); err != nil {
		t.Fatal(err)
	}
	input := "10\n" + replicaPath + "\n\n\n\nn\n\n\n\n\n\nn\nn\n0\n"
	var out bytes.Buffer
	if err := Run([]string{"tui", "-f", primaryPath}, bytes.NewBufferString(input), &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(replicaPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.Database == nil || c.Database.Role != "replica" {
		t.Fatalf("database = %#v, want replica configuration", c.Database)
	}
	if !bytes.Contains(out.Bytes(), []byte("Replica configuration is ready")) {
		t.Fatal("TUI did not return to the replica setup handoff")
	}
	if !bytes.Contains(out.Bytes(), []byte("Apply the primary, then the replica now?")) {
		t.Fatal("TUI offered to apply the replica without applying the primary first")
	}
	if !bytes.Contains(out.Bytes(), []byte("config    "+replicaPath)) {
		t.Fatal("dashboard did not switch to the saved replica configuration")
	}
}

func TestTUIAdjustsStackSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	in := bytes.NewBufferString("9\n3\nn\n0\n0\n")
	var out bytes.Buffer
	if err := Run([]string{"tui", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.TLS.Enabled {
		t.Fatal("TLS remained enabled after stack settings update")
	}
}

func TestYesNoAcceptsFullAffirmativeAnswers(t *testing.T) {
	for _, value := range []string{"y", "Y", "yes", "YES", "true", "1"} {
		if !yesNo(value) {
			t.Errorf("yesNo(%q) = false", value)
		}
	}
	for _, value := range []string{"", "n", "no", "false", "0"} {
		if yesNo(value) {
			t.Errorf("yesNo(%q) = true", value)
		}
	}
}

func TestCommandsRejectIgnoredPositionalArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ignored.json")
	var out bytes.Buffer
	err := Run([]string{"init", "ignored", "-f", path}, bytes.NewReader(nil), &out, &out)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("unexpected argument")) {
		t.Fatalf("init error = %v, want unexpected argument", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("init wrote a config despite ignored argument: %v", statErr)
	}
}

func TestReplicaStatusRejectsPromotionOnlyFlag(t *testing.T) {
	var out bytes.Buffer
	err := Run([]string{"replica", "status", "--yes"}, bytes.NewReader(nil), &out, &out)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("flag provided but not defined")) {
		t.Fatalf("replica status --yes error = %v", err)
	}
}

func TestUnknownReplicaActionFailsBeforeLoadingConfig(t *testing.T) {
	var out bytes.Buffer
	err := Run([]string{"replica", "typo", "-f", filepath.Join(t.TempDir(), "missing.json")}, bytes.NewReader(nil), &out, &out)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte(`unknown replica action "typo"`)) {
		t.Fatalf("unknown replica action error = %v", err)
	}
}

func TestSubcommandHelpIsVisibleAndSuccessful(t *testing.T) {
	var out bytes.Buffer
	if err := Run([]string{"apply", "--help"}, bytes.NewReader(nil), &out, &out); err != nil {
		t.Fatalf("apply --help returned error: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Usage of apply")) || !bytes.Contains(out.Bytes(), []byte("-f")) {
		t.Fatalf("apply --help output is incomplete:\n%s", out.String())
	}
}

func TestStackSettingsPreservesAdvancedDatabaseValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	c := config.Default()
	c.Database.Port = 3310
	c.Database.DataDir = "/var/lib/mysql/custom"
	c.Database.Replication.NodeID = 7
	if err := config.Write(path, c); err != nil {
		t.Fatal(err)
	}
	in := bytes.NewBufferString("9\n2\n\n\n\n\n\n0\n0\n")
	var out bytes.Buffer
	if err := Run([]string{"tui", "-f", path}, in, &out, &out); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Database.Port != 3310 || got.Database.DataDir != "/var/lib/mysql/custom" || got.Database.Replication.NodeID != 7 {
		t.Fatalf("database advanced settings were discarded: %#v", got.Database)
	}
}

func TestDashboardDoesNotRunDisabledBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	c := config.Default()
	c.Backups.Enabled = false
	if err := config.Write(path, c); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run([]string{"tui", "-f", path}, bytes.NewBufferString("4\n\n0\n"), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Backups are disabled")) {
		t.Fatalf("disabled backup action did not explain itself:\n%s", out.String())
	}
}

func TestBackupScriptPathTargetsIndependentMariaDBReplica(t *testing.T) {
	c := config.Default()
	c.Database.Role = "replica"
	c.Database.Port = 3307
	c.Database.DataDir = "/var/lib/mysql/poorman-replica-3307"
	c.Database.Replication.PrimaryHost = "127.0.0.1"
	if got := backupScriptPath(c); got != "/usr/local/sbin/poorman-backup-poorman-mariadb-replica-3307" {
		t.Fatalf("backup script = %q", got)
	}
}

func TestDatabaseInstancesIncludesPrimaryAndReplica(t *testing.T) {
	primaryPath := filepath.Join(t.TempDir(), "primary.json")
	replicaPath := filepath.Join(t.TempDir(), "replica.json")
	primary := config.Default()
	replica := config.Default()
	replica.Database.Role = "replica"
	replica.Database.Port = 3307
	replica.Database.DataDir = "/var/lib/mysql/poorman-replica-3307"
	replica.Database.Replication.PrimaryHost = "127.0.0.1"
	inventory := managed.Inventory{Version: 1, Services: append(
		managed.DesiredServices(primary, primaryPath),
		managed.DesiredServices(replica, replicaPath)...,
	)}
	instances := databaseInstancesFrom(inventory, primary, primaryPath)
	if len(instances) != 2 {
		t.Fatalf("database instances = %#v, want primary and replica", instances)
	}
	if managed.InstanceLabel(instances[0]) == managed.InstanceLabel(instances[1]) {
		t.Fatalf("database instance labels are not distinct: %#v", instances)
	}
}

func TestDashboardKeepsReplicaSetupErrorVisible(t *testing.T) {
	dir := t.TempDir()
	primaryPath := filepath.Join(dir, "primary.json")
	if err := config.WriteDefault(primaryPath); err != nil {
		t.Fatal(err)
	}
	input := "10\n" + primaryPath + "\n\n0\n"
	var out bytes.Buffer
	if err := Run([]string{"tui", "-f", primaryPath}, bytes.NewBufferString(input), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Replica setup unavailable: replica configuration must be different")) {
		t.Fatalf("dashboard did not show the replica setup error:\n%s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("Press enter to continue")) {
		t.Fatalf("dashboard did not pause on the replica setup error:\n%s", out.String())
	}
}

func TestGuidedSetupCreatesSameMachineMariaDBReplica(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replica.json")
	var out bytes.Buffer
	if err := Run([]string{"replica", "setup", "-f", path}, bytes.NewBufferString("\n\n\nyes\n\n\n\n\n\n"), &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Database.Port != 3307 || c.Database.DataDir != "/var/lib/mysql/poorman-replica-3307" || c.Database.Replication.PrimaryHost != "127.0.0.1" {
		t.Fatalf("database = %#v, want independent local MariaDB replica", c.Database)
	}
}
