package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/executor"
	"github.com/chokunplayz/poormanwebctrl/internal/health"
	"github.com/chokunplayz/poormanwebctrl/internal/plan"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
	"github.com/chokunplayz/poormanwebctrl/internal/provider"
)

var version = "1.0.0-dev"

func Run(args []string, in io.Reader, out, errOut io.Writer) error {
	return RunContext(context.Background(), args, in, out, errOut)
}

func RunContext(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	return RunWithCommands(ctx, args, in, out, errOut, BuiltInCommands())
}

func statusCommand(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("f", "poorman.json", "configuration file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	p, err := platform.Detect()
	if err != nil {
		return err
	}
	return health.Report(ctx, c, p, out)
}

func replicaCommand(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("replica requires setup, status, or promote")
	}
	action := args[0]
	if action == "setup" {
		err := guidedReplicaSetup(args[1:], in, out)
		if errors.Is(err, errSetupCanceled) {
			return nil
		}
		return err
	}
	if action != "status" && action != "promote" {
		return fmt.Errorf("unknown replica action %q", action)
	}
	fs := flag.NewFlagSet("replica "+action, flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("f", "poorman.json", "configuration file")
	yes := false
	if action == "promote" {
		fs.BoolVar(&yes, "yes", false, "confirm replica promotion")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	p, err := platform.Detect()
	if err != nil {
		return err
	}
	var operation plan.Plan
	if action == "status" {
		operation, err = provider.ReplicaStatus(c, p)
	} else if action == "promote" {
		operation, err = provider.PromoteReplica(c, p)
	}
	if err != nil {
		return err
	}
	reader := inputReader(in)
	operation.Print(out)
	if action == "promote" && !yes {
		fmt.Fprint(out, "Type PROMOTE to confirm the old primary is fenced: ")
		answer, _ := reader.ReadString('\n')
		if strings.TrimSpace(answer) != "PROMOTE" {
			fmt.Fprintln(out, "Cancelled.")
			return nil
		}
	}
	applyErr := executor.Apply(ctx, operation, reader, out, errOut)
	discardBlankInput(reader)
	return applyErr
}

func backupCommand(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("f", "poorman.json", "configuration file")
	yes := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if !c.Backups.Enabled {
		return fmt.Errorf("backups are disabled in %s", *path)
	}
	operation := plan.Plan{Platform: "local", Steps: []plan.Step{plan.Cmd("Run configured backup", backupScriptPath(c), true)}}
	reader := inputReader(in)
	operation.Print(out)
	if !*yes {
		fmt.Fprint(out, "Run backup now? [y/N] ")
		answer, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Fprintln(out, "Cancelled.")
			return nil
		}
	}
	applyErr := executor.Apply(ctx, operation, reader, out, errOut)
	discardBlankInput(reader)
	return applyErr
}

func backupScriptPath(c config.Config) string {
	if service := managedMariaDBService(c); service != "" {
		return "/usr/local/sbin/poorman-backup-" + service
	}
	return "/usr/local/sbin/poorman-backup"
}

func managedMariaDBService(c config.Config) string {
	if c.Database != nil && c.Database.Provider == "mariadb" && c.Database.Port > 0 && c.Database.DataDir != "" && isLoopbackAddress(c.Database.Replication.PrimaryHost) {
		return fmt.Sprintf("poorman-mariadb-replica-%d", c.Database.Port)
	}
	return ""
}

func isLoopbackAddress(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func initCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("f", "poorman.json", "configuration file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	if _, err := os.Stat(*path); err == nil {
		return fmt.Errorf("%s already exists", *path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := config.WriteDefault(*path); err != nil {
		return err
	}
	fmt.Fprintf(out, "Created %s\n", *path)
	return nil
}

func buildPlan(path string) (interface{ Print(io.Writer) }, error) {
	c, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	p, err := platform.Detect()
	if err != nil {
		return nil, err
	}
	return provider.BuildForConfig(c, p, path)
}

func planCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("f", "poorman.json", "configuration file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	p, err := buildPlan(*path)
	if err != nil {
		return err
	}
	p.Print(out)
	return nil
}

func applyCommand(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("f", "poorman.json", "configuration file")
	yes := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	plat, err := platform.Detect()
	if err != nil {
		return err
	}
	p, err := provider.BuildForConfig(c, plat, *path)
	if err != nil {
		return err
	}
	reader := inputReader(in)
	p.Print(out)
	if !*yes {
		fmt.Fprint(out, "Apply this plan? [y/N] ")
		answer, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Fprintln(out, "Cancelled.")
			return nil
		}
	}
	if err := ensureConfigSecrets(c, *path, out); err != nil {
		return err
	}
	err = executor.Apply(ctx, p, reader, out, errOut)
	discardBlankInput(reader)
	return err
}

func requireNoArgs(fs *flag.FlagSet) error {
	if fs.NArg() == 0 {
		return nil
	}
	return fmt.Errorf("unexpected argument %q", fs.Arg(0))
}

func ensureDatabasePassword(c config.Config, configPath string, out io.Writer) error {
	return ensureConfigSecrets(c, configPath, out)
}

// requiredConfigSecrets maps environment-variable names to whether poorman may
// safely generate a value. Replica credentials must come from the primary;
// inventing a different value would produce a valid-looking config that can
// never authenticate.
func requiredConfigSecrets(c config.Config) map[string]bool {
	required := map[string]bool{}
	add := func(name string, mayGenerate bool) {
		if name == "" {
			return
		}
		if existing, ok := required[name]; ok {
			// If any consumer requires a shared value from the primary,
			// generating it would silently break replica authentication.
			required[name] = existing && mayGenerate
			return
		}
		required[name] = mayGenerate
	}
	replica := c.Database != nil && c.Database.Role == "replica"
	if c.Database != nil {
		if !replica {
			add(c.Database.PasswordEnv, true)
		}
		if c.Database.Role != "" && c.Database.Role != "standalone" && c.Database.Replication.PasswordEnv != "" {
			add(c.Database.Replication.PasswordEnv, c.Database.Role == "primary")
		}
	}
	if !replica {
		for _, site := range c.Sites {
			if site.WordPress != nil {
				add(site.WordPress.AdminPassEnv, true)
			}
		}
	}
	return required
}

func configSecretNames(c config.Config) map[string]bool {
	names := map[string]bool{}
	if c.Database != nil {
		if c.Database.PasswordEnv != "" {
			names[c.Database.PasswordEnv] = true
		}
		if c.Database.Replication.PasswordEnv != "" {
			names[c.Database.Replication.PasswordEnv] = true
		}
	}
	for _, site := range c.Sites {
		if site.WordPress != nil && site.WordPress.AdminPassEnv != "" {
			names[site.WordPress.AdminPassEnv] = true
		}
	}
	return names
}

func ensureConfigSecrets(c config.Config, configPath string, out io.Writer) error {
	required := requiredConfigSecrets(c)
	if len(required) == 0 {
		return nil
	}
	secretPath := configPath + ".secrets"
	values, err := readSecretValues(secretPath)
	if err != nil {
		return err
	}
	// Load persisted values first, then reject missing shared replica secrets
	// before generating anything else. A failed apply must not leave behind a
	// partially initialized set of credentials.
	for name := range required {
		if os.Getenv(name) == "" && values[name] != "" {
			if err := os.Setenv(name, values[name]); err != nil {
				return err
			}
		}
	}
	for name, mayGenerate := range required {
		if os.Getenv(name) == "" && !mayGenerate {
			return fmt.Errorf("replica requires %s from the primary; export it or copy it into %s", name, secretPath)
		}
	}
	generated := false
	for name, mayGenerate := range required {
		if value := os.Getenv(name); value != "" {
			continue
		}
		if !mayGenerate {
			continue
		}
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return fmt.Errorf("generate password for %s: %w", name, err)
		}
		value := base64.RawURLEncoding.EncodeToString(raw)
		values[name] = value
		if err := os.Setenv(name, value); err != nil {
			return err
		}
		generated = true
	}
	if generated {
		if err := writeSecretValues(secretPath, values); err != nil {
			return err
		}
		fmt.Fprintf(out, "Generated and saved required passwords to %s\n", secretPath)
	}
	return nil
}

func copyConfigSecrets(sourceConfigPath, destinationConfigPath string, c config.Config) error {
	source, err := readSecretValues(sourceConfigPath + ".secrets")
	if err != nil {
		return err
	}
	destinationPath := destinationConfigPath + ".secrets"
	destination, err := readSecretValues(destinationPath)
	if err != nil {
		return err
	}
	changed := false
	for name := range configSecretNames(c) {
		value := source[name]
		if value == "" {
			value = os.Getenv(name)
		}
		if value != "" && destination[name] == "" {
			destination[name] = value
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return writeSecretValues(destinationPath, destination)
}

func readSecretValues(path string) (map[string]string, error) {
	values := map[string]string{}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read secrets file %s: %w", path, err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(name) != "" && value != "" {
			values[strings.TrimSpace(name)] = value
		}
	}
	return values, nil
}

func writeSecretValues(path string, values map[string]string) error {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	var content strings.Builder
	for _, name := range names {
		fmt.Fprintf(&content, "%s=%s\n", name, values[name])
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		return fmt.Errorf("write secrets file %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure secrets file %s: %w", path, err)
	}
	return nil
}

// inputReader preserves an existing buffer. Wrapping a *bufio.Reader in a
// second reader can read ahead past a confirmation newline, consuming an
// Enter intended for the dashboard after the operation finishes.
func inputReader(in io.Reader) *bufio.Reader {
	if reader, ok := in.(*bufio.Reader); ok {
		return reader
	}
	return bufio.NewReader(in)
}

// An Enter pressed during a command is intentionally not sent to the
// command. Remove the resulting blank line before the next dashboard prompt
// so it cannot be interpreted as that prompt's default action.
func discardBlankInput(reader *bufio.Reader) {
	for {
		// Peek blocks when the terminal has no queued input. Only inspect
		// bytes that the reader already buffered while the command was running.
		if reader.Buffered() == 0 {
			return
		}
		line, err := reader.Peek(1)
		if err != nil || line[0] != '\n' {
			return
		}
		_, _ = reader.ReadString('\n')
	}
}
