package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// initFlags holds the values parsed from the init command flags.
type initFlags struct {
	maxInFlight     int
	maxActiveAgents int
	taskSource      string
	scm             string
	harness         string
	harnessEndpoint string
	pluginDir       string
	force           bool
	nonInteractive  bool
	yes             bool
	weakPC          bool
	weakPCSet       bool
	skipPlugin      bool
}

// initOptions is the resolved configuration used by the init flow.
type initOptions struct {
	taskSource      string
	scm             string
	harness         string
	endpoint        string
	maxInFlight     int
	maxActiveAgents int
	weakPC          bool
	skipPlugin      bool
	pluginDir       string
	nonInteractive  bool
	force           bool
}

// newInitCommand builds the init Cobra command and registers its flags.
func newInitCommand() *cobra.Command {
	f := &initFlags{}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up Beacon in any git project",
		RunE: func(cmd *cobra.Command, _ []string) error {
			f.weakPCSet = cmd.Flags().Changed("weak-pc")
			return runInit(f)
		},
	}
	cmd.Flags().IntVar(&f.maxInFlight, "max-in-flight", 10, "maximum unfinished runs")
	cmd.Flags().IntVar(&f.maxActiveAgents, "max-active-agents", 2, "maximum active agent sessions")
	cmd.Flags().StringVar(&f.taskSource, "task-source", "markdown", "markdown, github_issues or gitlab_issues")
	cmd.Flags().StringVar(&f.scm, "scm", "", "github or gitlab")
	cmd.Flags().StringVar(&f.harness, "harness", "opencode", "opencode or cli")
	cmd.Flags().StringVar(&f.harnessEndpoint, "harness-endpoint", "", "OpenCode HTTP endpoint")
	cmd.Flags().StringVar(&f.pluginDir, "plugin-dir", "", "source directory for the project plugin")
	cmd.Flags().BoolVar(&f.force, "force", false, "overwrite existing config")
	cmd.Flags().BoolVar(&f.nonInteractive, "non-interactive", false, "use defaults without prompting")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "accept defaults without prompting")
	cmd.Flags().BoolVar(&f.weakPC, "weak-pc", true, "use targeted test instructions for a weak PC")
	cmd.Flags().BoolVar(&f.skipPlugin, "skip-plugin", false, "do not deploy the project plugin")
	return cmd
}

// resolveInitOptions turns parsed flags into init options with defaults.
// The OpenCode endpoint keeps both spellings: OPENCODE_ENDPOINT from OpenCode
// and BEACON_OPENCODE_ENDPOINT from older Beacon configurations.
func resolveInitOptions(f *initFlags) initOptions {
	o := initOptions{
		taskSource:      coalesce(f.taskSource, "markdown"),
		scm:             f.scm,
		harness:         coalesce(f.harness, "opencode"),
		endpoint:        coalesce(f.harnessEndpoint, envOr("OPENCODE_ENDPOINT", ""), envOr("BEACON_OPENCODE_ENDPOINT", ""), "http://127.0.0.1:4096"),
		maxInFlight:     f.maxInFlight,
		maxActiveAgents: f.maxActiveAgents,
		weakPC:          true,
		skipPlugin:      f.skipPlugin,
		pluginDir:       f.pluginDir,
		nonInteractive:  f.nonInteractive || f.yes,
		force:           f.force,
	}
	if o.maxInFlight <= 0 {
		o.maxInFlight = 10
	}
	if o.maxActiveAgents <= 0 {
		o.maxActiveAgents = 2
	}
	if f.weakPCSet {
		o.weakPC = f.weakPC
	}
	return o
}

// runInit detects the project, writes artifacts and prints the handoff.
func runInit(f *initFlags) error {
	return runInitWithInput(f, os.Stdout, os.Stdin, stdinIsTTY())
}

// runInitWithInput runs init against explicit streams. interactive reports
// whether the operator can answer the confirmation prompt.
func runInitWithInput(f *initFlags, out io.Writer, in io.Reader, interactive bool) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	opts := resolveInitOptions(f)
	r, base, err := detectRemoteAndBase()
	if err != nil {
		return err
	}
	if interactive && !opts.nonInteractive {
		ok, err := confirmInit(out, in, r, base)
		if err != nil {
			return err
		}
		if !ok {
			return errInitDeclined
		}
	}
	cfg := buildInitConfig(root, opts, r, base)
	fmt.Fprintf(out, "Detected required checks: %v (review merge.required_checks)\n", cfg.Merge.RequiredChecks)
	if err := writeInitArtifacts(root, cfg, opts); err != nil {
		return err
	}
	printHandoff(out, r)
	return nil
}

// printHandoff writes the next steps for the operator.
func printHandoff(out io.Writer, r remote) {
	_, _ = fmt.Fprintln(out, "\nBeacon setup complete. Next steps:")
	_, _ = fmt.Fprintln(out, "  1. Export GITHUB_TOKEN (or GITLAB_TOKEN) and BEACON_OPENCODE_TOKEN if needed.")
	_, _ = fmt.Fprintln(out, "  2. Run `beacon doctor` and fix any FAIL lines.")
	_, _ = fmt.Fprintln(out, "  3. Restart OpenCode so it loads the new opencode.json and plugin.")
	_, _ = fmt.Fprintf(out, "  4. Dispatch the first task with beacon_dispatch (repo %s/%s).\n", r.owner, r.repo)
}
