package cmd

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/artbegolli/anvilmcp/internal/policy"
)

// printer wraps an io.Writer so the callers below can format output without
// the errcheck noise of `_, _ = fmt.Fprintf(...)` at every line. Writes to
// stdout/stderr don't have a meaningful recovery path from an I/O error.
type printer struct{ w io.Writer }

func (p printer) printf(format string, a ...any) { _, _ = fmt.Fprintf(p.w, format, a...) }
func (p printer) println(a ...any)               { _, _ = fmt.Fprintln(p.w, a...) }

// defaultConfigYAML is the canonical starter policy shipped with `policy init`.
// Kept in sync with config.yaml.example at the repo root — that file is the
// human-facing reference; this one is what the binary actually writes.
//
//go:embed embed/default-config.yaml
var defaultConfigYAML []byte

// policyPathFlags carries the two file-path flags that every policy subcommand
// needs. Cobra doesn't inherit flag values between sibling commands cleanly, so
// we thread them through explicitly rather than duplicating strings across
// each RunE closure.
type policyPathFlags struct {
	userPath    string
	clusterPath string
}

func (f *policyPathFlags) register(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&f.userPath, "policy", os.ExpandEnv("$HOME/.config/anvilmcp/config.yaml"), "Path to user policy file")
	cmd.PersistentFlags().StringVar(&f.clusterPath, "cluster-policy", "/etc/anvilmcp/config.yaml", "Path to cluster policy file")
}

func NewPolicyCommand() *cobra.Command {
	paths := &policyPathFlags{}

	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Inspect, test, and initialize the session policy",
	}
	paths.register(cmd)

	cmd.AddCommand(newPolicyInitCmd(paths))
	cmd.AddCommand(newPolicyShowCmd(paths))
	cmd.AddCommand(newPolicyTestCmd(paths))

	return cmd
}

// ---------------------------------------------------------------------------
// policy init

func newPolicyInitCmd(paths *policyPathFlags) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter policy file to --policy",
		Long: `Write a documented default policy to --policy (default
~/.config/anvilmcp/config.yaml). Refuses if the target exists unless --force
is passed. Parent directories are created with mode 0700 and the file itself
is written with mode 0600 to match what the policy loader will accept at
server startup.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyInit(cmd.OutOrStdout(), paths.userPath, force)
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite the file if it already exists")
	return cmd
}

func runPolicyInit(out io.Writer, path string, force bool) error {
	if _, err := os.Stat(path); err == nil {
		if !force {
			return fmt.Errorf("refusing to overwrite existing file %s (use --force)", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	// 0700 on the directory + 0600 on the file matches the loader's ownership
	// check (see internal/policy/loader.go): the file must not be group- or
	// world-writable, or the server will refuse to load it.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	if err := os.WriteFile(path, defaultConfigYAML, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	p := printer{out}
	p.printf("Wrote default policy to %s\n", path)
	p.println("Edit that file to narrow the policy, then run `anvilmcp policy show` to inspect the effective result.")
	return nil
}

// ---------------------------------------------------------------------------
// policy show

func newPolicyShowCmd(paths *policyPathFlags) *cobra.Command {
	var (
		asJSON      bool
		showUser    bool
		showCluster bool
	)

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the effective policy (or one of its source layers)",
		Long: `Print the effective policy — the intersection of the cluster and
user policies — as YAML. Use --json for machine-readable output. Use --user
or --cluster to inspect one of the two source layers instead of the
effective merge; these two flags are mutually exclusive.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if showUser && showCluster {
				return fmt.Errorf("--user and --cluster are mutually exclusive")
			}
			return runPolicyShow(cmd.OutOrStdout(), paths, showUser, showCluster, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of YAML")
	cmd.Flags().BoolVar(&showUser, "user", false, "Show the user-layer policy instead of the effective merge")
	cmd.Flags().BoolVar(&showCluster, "cluster", false, "Show the cluster-layer policy instead of the effective merge")
	return cmd
}

func runPolicyShow(out io.Writer, paths *policyPathFlags, showUser, showCluster, asJSON bool) error {
	loader, err := policy.NewLoader(paths.userPath, paths.clusterPath)
	if err != nil {
		return fmt.Errorf("load policy: %w", err)
	}

	var chosen *policy.Policy
	switch {
	case showUser:
		chosen = loader.UserPolicy()
	case showCluster:
		chosen = loader.ClusterPolicy()
	default:
		chosen = loader.Effective()
	}

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(chosen)
	}

	data, err := yaml.Marshal(chosen)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	_, err = out.Write(data)
	return err
}

// ---------------------------------------------------------------------------
// policy test

func newPolicyTestCmd(paths *policyPathFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Ask the policy engine whether an action would be allowed",
		Long: `Ask the policy engine whether an action would be allowed. Prints
allow/deny plus the matched rule and the reason string, and exits non-zero
on deny so this composes with shell pipelines (e.g. anvilmcp policy test
read /workspace/foo && ...).`,
	}
	cmd.AddCommand(newPolicyTestReadCmd(paths))
	cmd.AddCommand(newPolicyTestWriteCmd(paths))
	cmd.AddCommand(newPolicyTestExecCmd(paths))
	return cmd
}

// silentOnError is what every `policy test *` subcommand sets so a policy
// deny doesn't spray cobra's usage text; the ALLOW/DENY line already told
// the user what happened.
func silentOnError(cmd *cobra.Command) *cobra.Command {
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd
}

func newPolicyTestReadCmd(paths *policyPathFlags) *cobra.Command {
	return silentOnError(&cobra.Command{
		Use:   "read <path>",
		Short: "Test whether the engine would allow reading a path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyTestPath(cmd.OutOrStdout(), paths, "read", args[0])
		},
	})
}

func newPolicyTestWriteCmd(paths *policyPathFlags) *cobra.Command {
	return silentOnError(&cobra.Command{
		Use:   "write <path>",
		Short: "Test whether the engine would allow writing a path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyTestPath(cmd.OutOrStdout(), paths, "write", args[0])
		},
	})
}

func newPolicyTestExecCmd(paths *policyPathFlags) *cobra.Command {
	return silentOnError(&cobra.Command{
		Use:   "exec <cmd> [args...]",
		Short: "Test whether the engine would allow executing a command",
		Long: `Test whether the engine would allow executing a command against
the exec policy. Because cobra parses flag-shaped tokens ("-rf", "--force")
as flags of this CLI, use "--" to separate the command's own flags:

    anvilmcp policy test exec -- rm -rf /workspace

Positional arguments before "--" are still fine as long as they don't look
like flags.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyTestExec(cmd.OutOrStdout(), paths, args[0], args[1:])
		},
	})
}

func runPolicyTestPath(out io.Writer, paths *policyPathFlags, mode, path string) error {
	engine, err := newTestEngine(paths)
	if err != nil {
		return err
	}
	d := engine.ResolvePath(path, mode)

	p := printer{out}
	p.printf("%s %s\n", verdict(d.Allowed), path)
	p.printf("  resolved: %s\n", d.AbsPath)
	p.printf("  rule:     %s\n", d.PolicyRule)
	if d.Reason != "" {
		p.printf("  reason:   %s\n", d.Reason)
	}
	if !d.Allowed {
		// Non-zero exit lets callers gate on this in shell pipelines. Cobra maps
		// a non-nil error to a non-zero exit code; SilenceUsage/Errors on the
		// parent command keep the output clean.
		return errDenied
	}
	return nil
}

func runPolicyTestExec(out io.Writer, paths *policyPathFlags, cmdName string, argv []string) error {
	engine, err := newTestEngine(paths)
	if err != nil {
		return err
	}
	d := engine.EvaluateExec(cmdName, argv)

	p := printer{out}
	p.printf("%s %s %v\n", verdict(d.Allowed), cmdName, argv)
	p.printf("  rule:     %s\n", d.PolicyRule)
	if d.RequiresConfirmation {
		p.println("  note:     requires confirmation")
	}
	if d.Reason != "" {
		p.printf("  reason:   %s\n", d.Reason)
	}
	if !d.Allowed {
		return errDenied
	}
	return nil
}

func newTestEngine(paths *policyPathFlags) (*policy.Engine, error) {
	loader, err := policy.NewLoader(paths.userPath, paths.clusterPath)
	if err != nil {
		return nil, fmt.Errorf("load policy: %w", err)
	}
	return policy.NewEngine(loader.Effective(), paths.userPath), nil
}

func verdict(allowed bool) string {
	if allowed {
		return "ALLOW"
	}
	return "DENY "
}

// errDenied signals a policy denial to the caller. Cobra will exit non-zero
// when RunE returns a non-nil error; SilenceUsage on the top-level cobra
// command keeps this from spraying help text on a valid deny outcome.
var errDenied = fmt.Errorf("policy denied")
