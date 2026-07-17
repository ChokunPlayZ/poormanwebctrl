package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestRunWithCommandsAddsCommandWithoutChangingRouter(t *testing.T) {
	commands := BuiltInCommands()
	commands = append(commands, Command{
		Name:        "doctor",
		Usage:       "poorman doctor",
		Description: "inspect extension wiring",
		Run: func(_ context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
			if len(args) > 0 {
				return fmt.Errorf("unexpected argument %q", args[0])
			}
			fmt.Fprintln(out, "ready")
			return nil
		},
	})

	var out bytes.Buffer
	if err := RunWithCommands(context.Background(), []string{"doctor"}, strings.NewReader(""), &out, &out, commands); err != nil {
		t.Fatal(err)
	}
	if out.String() != "ready\n" {
		t.Fatalf("doctor output = %q", out.String())
	}
	out.Reset()
	if err := RunWithCommands(context.Background(), []string{"help"}, strings.NewReader(""), &out, &out, commands); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "poorman doctor") {
		t.Fatalf("extended help omitted doctor command:\n%s", out.String())
	}
}

func TestRunWithCommandsRejectsDuplicateAliases(t *testing.T) {
	commands := BuiltInCommands()
	commands = append(commands, Command{Name: "release", Aliases: []string{"-v"}, Run: func(context.Context, []string, io.Reader, io.Writer, io.Writer) error { return nil }})
	err := RunWithCommands(context.Background(), nil, strings.NewReader(""), io.Discard, io.Discard, commands)
	if err == nil || !strings.Contains(err.Error(), `duplicate command or alias "-v"`) {
		t.Fatalf("duplicate alias error = %v", err)
	}
}
