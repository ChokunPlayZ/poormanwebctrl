package executor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

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
