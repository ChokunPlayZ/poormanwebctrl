package executor

import (
	"os"
	"testing"
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
