package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/refusedguy/beacon/internal/infra/config"
	beaconmcp "github.com/refusedguy/beacon/internal/infra/mcp"
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
func resolveInitOptions(f *initFlags) initOptions {
	o := initOptions{
		taskSource:      firstNonEmpty(f.taskSource, "markdown"),
		scm:             f.scm,
		harness:         firstNonEmpty(f.harness, "opencode"),
		endpoint:        firstNonEmpty(f.harnessEndpoint, os.Getenv("OPENCODE_ENDPOINT"), os.Getenv("BEACON_OPENCODE_ENDPOINT"), "http://127.0.0.1:4096"),
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

// remote identifies the project's SCM host, owner and repository.
type remote struct {
	provider string
	owner    string
	repo     string
	host     string
}

// parseRemote extracts provider, owner and repo from a git remote URL.
func parseRemote(raw string) (remote, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, ".git")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 && !strings.Contains(s[:i], "/") {
		rest := s[i+1:]
		if j := strings.Index(rest, "/"); j >= 0 && allDigits(rest[:j]) {
			rest = rest[j+1:]
		}
		s = s[:i] + "/" + rest
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 3 {
		return remote{}, fmt.Errorf("cannot parse git remote %q", raw)
	}
	host := parts[0]
	owner := parts[len(parts)-2]
	repo := parts[len(parts)-1]
	provider := "github"
	if strings.Contains(host, "gitlab") {
		provider = "gitlab"
	}
	return remote{provider: provider, owner: owner, repo: repo, host: host}, nil
}

// allDigits reports whether s is a non-empty string of decimal digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// gitOutput runs a git command and returns trimmed output, or an empty string.
func gitOutput(args ...string) string {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// detectRemoteAndBase reads the origin URL and the default base branch.
func detectRemoteAndBase() (remote, string, error) {
	r, err := parseRemote(gitOutput("remote", "get-url", "origin"))
	if err != nil {
		return remote{}, "", err
	}
	base := strings.TrimPrefix(gitOutput("rev-parse", "--abbrev-ref", "origin/HEAD"), "origin/")
	if base == "" || base == "origin/HEAD" {
		base = "main"
	}
	return r, base, nil
}

// stdinIsTTY reports whether stdin is a terminal.
func stdinIsTTY() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// confirmInit asks the operator to accept the detected configuration.
func confirmInit(out io.Writer, in io.Reader, r remote, base string) (bool, error) {
	_, _ = fmt.Fprintf(out, "Configure Beacon for %s/%s (base %s)? [Y/n] ", r.owner, r.repo, base)
	var answer string
	if _, err := fmt.Fscanln(in, &answer); err != nil {
		if err == io.EOF {
			return true, nil
		}
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "" || answer == "y" || answer == "yes", nil
}

// runInit detects the project, writes artifacts and prints the handoff.
func runInit(f *initFlags) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	opts := resolveInitOptions(f)
	r, base, err := detectRemoteAndBase()
	if err != nil {
		return err
	}
	if !opts.nonInteractive && stdinIsTTY() {
		ok, err := confirmInit(os.Stdout, os.Stdin, r, base)
		if err != nil || !ok {
			return err
		}
	}
	cfg := buildInitConfig(root, opts, r, base)
	fmt.Printf("Detected required checks: %v (review merge.required_checks)\n", cfg.Merge.RequiredChecks)
	if err := writeInitArtifacts(root, cfg, opts); err != nil {
		return err
	}
	printHandoff(os.Stdout, r)
	return nil
}

// buildInitConfig constructs the Beacon config from resolved options and the remote.
func buildInitConfig(root string, o initOptions, r remote, base string) config.Config {
	provider := firstNonEmpty(o.scm, r.provider)
	tokenEnv := "GITHUB_TOKEN"
	baseURL := ""
	if provider == "gitlab" {
		tokenEnv = "GITLAB_TOKEN"
		baseURL = gitlabAPI(r.host)
	}
	stack := detectStack(root)
	return config.Config{
		StateDB:      ".beacon/state.db",
		WorkspaceDir: ".beacon/worktrees",
		BaseBranch:   base,
		PollInterval: "30s",
		Concurrency: config.Concurrency{
			MaxInFlight: o.maxInFlight, MaxActiveAgents: o.maxActiveAgents,
		},
		TaskSource: config.TaskSource{
			Provider: o.taskSource, Dir: "docs/tasks", Owner: r.owner, Repo: r.repo, TokenEnv: tokenEnv,
		},
		SCM: config.SCM{
			Provider: provider, Owner: r.owner, Repo: r.repo, TokenEnv: tokenEnv, BaseURL: baseURL,
		},
		Harness: config.Harness{
			Provider: o.harness, Endpoint: o.endpoint, Command: "worker",
			WorkerInstructions: stack.workerInstructions(o.weakPC),
		},
		Merge:     config.Merge{RequiredChecks: detectRequiredChecks(root), MaxAttempts: 12, AutoRebase: true, Labels: []string{}},
		OnBaseRed: config.OnBaseRed{Mode: "spawn_fixer", MaxFixAttempts: 2},
	}
}

// gitlabAPI returns the GitLab API base URL for host.
func gitlabAPI(host string) string {
	if host == "" || host == "gitlab.com" {
		return "https://gitlab.com/api/v4"
	}
	return "https://" + host + "/api/v4"
}

// writeInitArtifacts writes the config, tasks directory, gitignore, opencode config and plugin.
func writeInitArtifacts(root string, cfg config.Config, o initOptions) error {
	path := filepath.Join(root, ".beacon", "config.yaml")
	if err := config.WriteDefault(path, cfg, o.force); err != nil {
		return err
	}
	if err := ensureTasksDir(root); err != nil {
		return err
	}
	if err := appendGitignore(filepath.Join(root, ".gitignore")); err != nil {
		return err
	}
	if err := mergeOpenCodeConfig(root); err != nil {
		return err
	}
	return deployPlugin(root, o)
}

// ensureTasksDir creates docs/tasks and its .gitkeep marker.
func ensureTasksDir(root string) error {
	dir := filepath.Join(root, "docs", "tasks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, ".gitkeep")
	if existsIn(dir, ".gitkeep") {
		return nil
	}
	return os.WriteFile(path, nil, 0o644)
}

// stackInfo identifies the project stack and its targeted test instruction.
type stackInfo struct {
	kind     string
	targeted string
}

// workerInstructions returns the test instruction for the given weak-PC setting.
func (s stackInfo) workerInstructions(weakPC bool) string {
	if !weakPC {
		return "Run the full test suite, the full build and the repository linters before reporting done."
	}
	return s.targeted
}

// detectStack identifies the project stack from marker files.
func detectStack(root string) stackInfo {
	if existsIn(root, "go.mod") {
		return stackInfo{"go", "Worker environment: weak PC. Run targeted tests only for the packages you changed (for example, go test ./internal/app). Leave full go test ./..., go build ./... and repo linters to CI."}
	}
	if existsIn(root, "package.json") {
		return stackInfo{"node", "Worker environment: weak PC. Run targeted tests only for the files you changed; do not run a full npm run build. Leave full builds and repo linters to CI."}
	}
	if existsIn(root, "pyproject.toml") || existsIn(root, "requirements.txt") {
		return stackInfo{"python", "Worker environment: weak PC. Run targeted tests only for the files you changed (for example, pytest path/to/test.py). Leave the full suite and repo linters to CI."}
	}
	if existsIn(root, "Cargo.toml") {
		return stackInfo{"rust", "Worker environment: weak PC. Run targeted tests only for the module you changed (for example, cargo test <module>). Leave the full suite and repo linters to CI."}
	}
	if hasMarker(root, "*.sln") || hasMarker(root, "*.csproj") {
		return stackInfo{"dotnet", "Worker environment: weak PC. Run targeted tests only for the project you changed (for example, dotnet test path/to/project.csproj). Leave the full suite and repo linters to CI."}
	}
	return stackInfo{"generic", config.DefaultWorkerInstructions}
}

// existsIn reports whether name exists under root.
func existsIn(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}

// hasMarker reports whether any file under root matches pattern.
func hasMarker(root, pattern string) bool {
	matches, err := filepath.Glob(filepath.Join(root, pattern))
	return err == nil && len(matches) > 0
}

// detectRequiredChecks collects job names from CI workflow files.
func detectRequiredChecks(root string) []string {
	files, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	if err != nil {
		return []string{}
	}
	seen := make(map[string]bool)
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping workflow %s: %v\n", file, err)
			continue
		}
		for _, name := range parseJobNames(string(data)) {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// parseJobNames extracts job names from a workflow document.
func parseJobNames(content string) []string {
	var names []string
	inJobs := false
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "jobs:") {
			inJobs = true
			continue
		}
		if !inJobs {
			continue
		}
		if line != "" && !strings.HasPrefix(line, " ") {
			break
		}
		if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "    ") {
			continue
		}
		name, _, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && name != "" && !strings.HasPrefix(name, "#") {
			names = append(names, name)
		}
	}
	return names
}

// mergeOpenCodeConfig adds agents, permissions and the beacon MCP server to opencode.json.
func mergeOpenCodeConfig(root string) error {
	path := filepath.Join(root, "opencode.json")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if os.IsNotExist(err) {
		data = []byte("{}")
	}
	var doc map[string]any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return fmt.Errorf("opencode.json: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	gen := beaconmcp.GeneratedPermissions(beaconmcp.ToolCatalog())
	agents, _ := doc["agents"].(map[string]any)
	if agents == nil {
		agents = map[string]any{}
		doc["agents"] = agents
	}
	for name, role := range roleDefinitions(gen) {
		agent, ok := agents[name].(map[string]any)
		if !ok {
			agent = map[string]any{"description": role.description}
			if role.mode != "" {
				agent["mode"] = role.mode
			}
		}
		agent["permissions"] = beaconmcp.MergePermissions(agent["permissions"], role.perms)
		agents[name] = agent
	}
	doc["permissions"] = beaconmcp.MergePermissions(doc["permissions"], gen.Global)
	mergeMCPServer(doc, root)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// roleDefinition describes one generated operator role.
type roleDefinition struct {
	description string
	mode        string
	perms       []beaconmcp.Permission
}

// roleDefinitions maps role names to their description, mode and permissions.
func roleDefinitions(gen beaconmcp.PermissionSet) map[string]roleDefinition {
	return map[string]roleDefinition{
		"orchestrator": {"Creates and dispatches Beacon tasks.", "all", gen.Allow},
		"build":        {"Default coding agent allowed to manage Beacon tasks.", "", gen.Allow},
		"worker":       {"Executes one Beacon task in its own worktree.", "subagent", gen.Deny},
	}
}

// mergeMCPServer installs the pooled beacon MCP server entry in doc.
func mergeMCPServer(doc map[string]any, root string) {
	mcpMap, _ := doc["mcp"].(map[string]any)
	if mcpMap == nil {
		mcpMap = map[string]any{}
		doc["mcp"] = mcpMap
	}
	server, _ := mcpMap["beacon"].(map[string]any)
	if server == nil {
		server = map[string]any{}
	}
	server["type"] = "local"
	server["command"] = []any{"beacon", "mcp"}
	server["environment"] = map[string]any{"BEACON_PROJECT_ROOT": root}
	server["pool"] = true
	mcpMap["beacon"] = server
}

// pluginSource resolves the project plugin source directory.
func pluginSource(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if fromEnv := os.Getenv("BEACON_PLUGIN_DIR"); fromEnv != "" {
		return fromEnv
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	candidate := filepath.Clean(filepath.Join(filepath.Dir(exe), "..", "..", ".opencode", "plugins", "beacon-project"))
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return candidate
	}
	return ""
}

// deployPlugin copies the project plugin into the repository.
func deployPlugin(root string, o initOptions) error {
	if o.skipPlugin {
		return nil
	}
	src := pluginSource(o.pluginDir)
	if src == "" {
		fmt.Fprintln(os.Stderr, "warning: no plugin source found; pass --plugin-dir to install the project plugin")
		return nil
	}
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		fmt.Fprintln(os.Stderr, "warning: plugin source is not a directory; pass --plugin-dir")
		return nil
	}
	dst := filepath.Join(root, ".opencode", "plugins", "beacon-project")
	if err := copyTree(src, dst); err != nil {
		return err
	}
	fmt.Printf("installed project plugin from %s\n", src)
	return nil
}

// copyTree recursively copies src into dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// appendGitignore adds Beacon artifacts to the gitignore file.
func appendGitignore(path string) error {
	entries := []string{
		".beacon/state.db*",
		".beacon/worktrees/",
		".beacon/connection.json",
		".beacon/*.lock",
		".beacon/daemon*",
		".beacon/config.yaml",
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(string(data), "\n")
	have := make(map[string]bool)
	for _, line := range lines {
		have[strings.TrimSpace(line)] = true
	}
	var add []string
	for _, e := range entries {
		if !have[e] {
			add = append(add, e)
		}
	}
	if len(add) == 0 {
		return nil
	}
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	lines = append(lines, add...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// printHandoff writes the next steps for the operator.
func printHandoff(out io.Writer, r remote) {
	_, _ = fmt.Fprintln(out, "\nBeacon setup complete. Next steps:")
	_, _ = fmt.Fprintln(out, "  1. Export GITHUB_TOKEN (or GITLAB_TOKEN) and BEACON_OPENCODE_TOKEN if needed.")
	_, _ = fmt.Fprintln(out, "  2. Run `beacon doctor` and fix any FAIL lines.")
	_, _ = fmt.Fprintln(out, "  3. Restart OpenCode so it loads the new opencode.json and plugin.")
	_, _ = fmt.Fprintf(out, "  4. Dispatch the first task with beacon_dispatch (repo %s/%s).\n", r.owner, r.repo)
}
