package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/chokunplayz/poormanwebctrl/internal/managed"
	"github.com/chokunplayz/poormanwebctrl/internal/plan"
)

func TestResolveEnv(t *testing.T) {
	t.Setenv("POORMAN_TEST_SECRET", "a'b")
	got, err := resolveEnv("value='${POORMAN_TEST_SECRET}'", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "value='a''b'" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveEnvRejectsMissing(t *testing.T) {
	os.Unsetenv("POORMAN_MISSING_SECRET")
	if _, err := resolveEnv("${POORMAN_MISSING_SECRET}", false); err == nil {
		t.Fatal("expected missing environment variable error")
	}
}

func TestRunDoesNotConsumeInteractiveInput(t *testing.T) {
	var out bytes.Buffer
	operation := plan.Plan{Steps: []plan.Step{{
		Kind:    plan.Command,
		Command: "cat",
	}}}

	if err := Apply(t.Context(), operation, bytes.NewBufferString("unexpected input\n"), &out, &out); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("unexpected input")) {
		t.Fatal("command consumed interactive input")
	}
}

func TestApplyStopsBeforeNextStepWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	operation := plan.Plan{Steps: []plan.Step{{Description: "must not run", Kind: plan.Command, Command: "false"}}}

	if err := Apply(ctx, operation, bytes.NewReader(nil), &out, &out); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if out.Len() != 0 {
		t.Fatalf("canceled operation produced output: %q", out.String())
	}
}

func TestAppendOwnershipArgsSupportsDifferentOwnerAndGroup(t *testing.T) {
	got := appendOwnershipArgs([]string{"-d"}, plan.Step{Owner: "nobody", Group: "nogroup"})
	want := []string{"-d", "-o", "nobody", "-g", "nogroup"}
	if !slices.Equal(got, want) {
		t.Fatalf("ownership args = %#v, want %#v", got, want)
	}
}

func TestReconcileManagedStateRemovesObsoleteOwnedFiles(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "managed.json")
	configPath := filepath.Join(dir, "poorman.json")
	obsolete := filepath.Join(dir, "poorman-old.conf")
	if err := os.WriteFile(obsolete, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := managed.Service{
		Key: managed.ServiceKey(configPath, "web"), ConfigPath: managed.ConfigKey(configPath), Kind: "web", Name: "nginx", Files: []string{obsolete},
	}
	inventory, err := managed.Marshal(managed.Inventory{Services: []managed.Service{previous}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, inventory, 0o600); err != nil {
		t.Fatal(err)
	}
	current := previous
	current.Files = nil
	desired, err := json.Marshal([]managed.Service{current})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	step := plan.ReconcileManagedState("reconcile", statePath, configPath, string(desired))
	if err := reconcileManagedState(t.Context(), step, &out, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(obsolete); !os.IsNotExist(err) {
		t.Fatalf("obsolete managed file still exists: %v", err)
	}
}

func TestSafeManagedConfigPathRejectsBroadOrUnownedTargets(t *testing.T) {
	for _, path := range []string{"/", "/etc/passwd", "/etc/nginx/nginx.conf", "/etc/nginx/conf.d/customer.conf", "/usr/local/lsws/conf/httpd_config.conf"} {
		if safeManagedConfigPath(path) {
			t.Errorf("unsafe path accepted: %s", path)
		}
	}
	for _, path := range []string{"/etc/nginx/conf.d/poorman-example.conf", "/etc/apache2/sites-enabled/poorman-example.conf", "/etc/httpd/conf.d/poorman-example.conf", "/usr/local/lsws/conf/poorman.conf", "/usr/local/lsws/conf/vhosts/example.com/vhconf.conf"} {
		if !safeManagedConfigPath(path) {
			t.Errorf("managed config path rejected: %s", path)
		}
	}
}
