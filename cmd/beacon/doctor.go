package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/refusedguy/beacon/internal/infra/config"
	beaconmcp "github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/shared"
	"github.com/refusedguy/beacon/pkg/core/redact"
)

// checkLevel ranks a doctor check as OK, warning or failure.
type checkLevel int

// checkOK, checkWarn and checkFail order doctor results by severity.
const (
	checkOK checkLevel = iota
	checkWarn
	checkFail
)

// label renders the check level as a bracketed marker.
func (l checkLevel) label() string {
	switch l {
	case checkOK:
		return "[OK]"
	case checkWarn:
		return "[WARN]"
	default:
		return "[FAIL]"
	}
}

// doctorCheck is one named result line printed by the doctor command.
type doctorCheck struct {
	name   string
	level  checkLevel
	detail string
}

// doctorExitCode returns a nonzero exit code when any check failed.
func doctorExitCode(checks []doctorCheck) int {
	for _, c := range checks {
		if c.level == checkFail {
			return 1
		}
	}
	return 0
}

// printDoctorChecks writes each check's level, name and detail to out.
func printDoctorChecks(out io.Writer, checks []doctorCheck) {
	for _, c := range checks {
		_, _ = fmt.Fprintf(out, "%s %s: %s\n", c.level.label(), c.name, redact.Redact(c.detail))
	}
}

// doctorHTTPTimeout bounds each doctor network probe.
const doctorHTTPTimeout = 5 * time.Second

// doctorHTTPClient builds an HTTP client with the doctor timeout.
func doctorHTTPClient() *http.Client {
	return bearerClient(doctorHTTPTimeout)
}

// exitCoder is implemented by errors that carry a process exit status.
type exitCoder interface {
	ExitCode() int
}

// doctorExitError reports the exit status chosen by the doctor checks.
type doctorExitError struct{ code int }

func (e *doctorExitError) Error() string { return fmt.Sprintf("doctor exit status %d", e.code) }

func (e *doctorExitError) ExitCode() int { return e.code }

// newDoctorCommand builds the doctor Cobra command.
func newDoctorCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:           "doctor",
		Short:         "Check a Beacon installation and report problems",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client := doctorHTTPClient()
			code := runDoctor(cmd.Context(), cmd.OutOrStdout(), *configPath, client)
			if code == 0 {
				return nil
			}
			return &doctorExitError{code: code}
		},
	}
}

// runDoctor runs all checks and returns the process exit code.
func runDoctor(ctx context.Context, out io.Writer, configPath string, client *http.Client) int {
	root, err := projectRoot()
	if err != nil {
		printDoctorChecks(out, []doctorCheck{{"project root", checkFail, err.Error()}})
		return 1
	}
	checks := doctorGitChecks(root)
	cfgChecks, cfg := doctorConfigChecks(root, doctorConfigPath(root, configPath))
	checks = append(checks, cfgChecks...)
	checks = append(checks, doctorToolChecks(root, cfg)...)
	checks = append(checks, doctorNetworkChecks(ctx, cfg, client)...)
	printDoctorChecks(out, checks)
	return doctorExitCode(checks)
}

// doctorConfigPath resolves the config path from a flag or the project root.
func doctorConfigPath(root, flag string) string {
	if flag != "" {
		return flag
	}
	return filepath.Join(root, ".beacon", "config.yaml")
}

// doctorGitChecks verifies the work tree and the origin remote.
func doctorGitChecks(root string) []doctorCheck {
	inside, err := git(root, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(inside) != "true" {
		return []doctorCheck{{"git repo", checkFail, root + " is not a git work tree"}}
	}
	checks := []doctorCheck{{"git repo", checkOK, root}}
	if gitTrim(root, "remote", "get-url", "origin") == "" {
		checks = append(checks, doctorCheck{"origin remote", checkWarn, "no origin remote configured"})
	} else {
		checks = append(checks, doctorCheck{"origin remote", checkOK, "configured"})
	}
	return checks
}

// doctorConfigChecks validates the config and returns the parsed config.
func doctorConfigChecks(root, path string) ([]doctorCheck, config.Config) {
	var checks []doctorCheck
	if _, err := os.Stat(path); err != nil {
		return append(checks, doctorCheck{"config", checkFail, "missing: " + path}), config.Config{}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return append(checks, doctorCheck{"config", checkFail, err.Error()}), config.Config{}
	}
	checks = append(checks, doctorCheck{"config", checkOK, "parsed"})
	checks = append(checks, doctorBaseBranch(root, cfg))
	if cfg.Harness.Command != "worker" {
		checks = append(checks, doctorCheck{"harness.command", checkFail, fmt.Sprintf("want worker, got %q", cfg.Harness.Command)})
	} else {
		checks = append(checks, doctorCheck{"harness.command", checkOK, "worker"})
	}
	checks = append(checks, doctorTokenCheck(cfg), doctorWebhookCheck(cfg))
	if len(cfg.Merge.RequiredChecks) == 0 {
		checks = append(checks, doctorCheck{"required_checks", checkWarn, "none configured"})
	} else {
		checks = append(checks, doctorCheck{"required_checks", checkOK, fmt.Sprintf("%d configured", len(cfg.Merge.RequiredChecks))})
	}
	return append(checks, doctorTasksCheck(root)), cfg
}

// doctorBaseBranch verifies that the configured base branch exists.
func doctorBaseBranch(root string, cfg config.Config) doctorCheck {
	if cfg.BaseBranch == "" {
		return doctorCheck{"base branch", checkFail, "base_branch is empty"}
	}
	for _, ref := range []string{cfg.BaseBranch, "origin/" + cfg.BaseBranch} {
		if gitTrim(root, "rev-parse", "--verify", "--quiet", ref) != "" {
			return doctorCheck{"base branch", checkOK, cfg.BaseBranch}
		}
	}
	return doctorCheck{"base branch", checkWarn, cfg.BaseBranch + " not found locally or on origin"}
}

// doctorTokenCheck reports whether the SCM token is available. A missing token
// stays advisory: doctor warns instead of failing startup.
func doctorTokenCheck(cfg config.Config) doctorCheck {
	if envOr(cfg.SCM.TokenEnv, cfg.SCM.Token) != "" {
		return doctorCheck{"scm token", checkOK, "present via env or config"}
	}
	return doctorCheck{"scm token", checkWarn, "not set; export " + cfg.SCM.TokenEnv}
}

// doctorWebhookCheck verifies the webhook configuration. The rule comes from
// config; doctor keeps its advisory wording and level.
func doctorWebhookCheck(cfg config.Config) doctorCheck {
	if !cfg.Webhook.Enabled {
		return doctorCheck{"webhook", checkOK, "disabled"}
	}
	if err := config.ValidateWebhook(cfg.Webhook); err != nil {
		return doctorCheck{"webhook", checkFail, "enabled without a secret; set webhook.secret or webhook.secret_env"}
	}
	return doctorCheck{"webhook", checkOK, "enabled with a secret"}
}

// doctorTasksCheck verifies that the markdown task directory exists.
func doctorTasksCheck(root string) doctorCheck {
	info, err := os.Stat(filepath.Join(root, "docs", "tasks"))
	if err == nil && info.IsDir() {
		return doctorCheck{"docs/tasks", checkOK, "exists"}
	}
	return doctorCheck{"docs/tasks", checkWarn, "missing; markdown task source needs it"}
}

// doctorToolChecks validates opencode.json, permissions, the binary and the daemon.
func doctorToolChecks(root string, cfg config.Config) []doctorCheck {
	var checks []doctorCheck
	var doc map[string]any
	path := filepath.Join(root, "opencode.json")
	data, err := os.ReadFile(path)
	if err != nil {
		checks = append(checks, doctorCheck{"opencode.json", checkFail, "missing or unreadable"})
	} else if err := json.Unmarshal(data, &doc); err != nil {
		checks = append(checks, doctorCheck{"opencode.json", checkFail, "invalid JSON"})
		doc = nil
	} else {
		checks = append(checks, doctorFileShapeCheck(doc, root))
		checks = append(checks, doctorPermissionCheck(doc))
	}
	checks = append(checks, doctorBinaryCheck(root, doc), doctorDaemonCheck(cfg))
	checks = append(checks, doctorPreflightChecks(root, doc)...)
	return checks
}

// doctorFileShapeCheck verifies that the agents and the beacon MCP server are configured.
func doctorFileShapeCheck(doc map[string]any, root string) doctorCheck {
	agents, _ := doc["agents"].(map[string]any)
	var missing []string
	for _, role := range []string{"orchestrator", "worker"} {
		if _, ok := agents[role].(map[string]any); !ok {
			missing = append(missing, role)
		}
	}
	source := beaconMCPSource(doc)
	location := ""
	if source == "" {
		if parent := readParentOpenCode(root); parent != nil {
			source = beaconMCPSource(parent)
			if source != "" {
				location = " (workspace parent)"
			}
		}
	}
	if source == "" {
		missing = append(missing, "mcp.beacon")
	}
	if len(missing) > 0 {
		return doctorCheck{"opencode.json", checkFail, "missing " + strings.Join(missing, ", ")}
	}
	return doctorCheck{"opencode.json", checkOK, "agents and " + source + " present" + location}
}

// beaconMCPSource reports the key path of a beacon MCP server in one config
// document, or "" when the document has none.
func beaconMCPSource(doc map[string]any) string {
	mcpMap, _ := doc["mcp"].(map[string]any)
	if _, ok := mcpMap["beacon"].(map[string]any); ok {
		return "mcp.beacon"
	}
	servers, _ := mcpMap["servers"].(map[string]any)
	if _, ok := servers["beacon"].(map[string]any); ok {
		return "mcp.servers.beacon"
	}
	return ""
}

// readParentOpenCode loads the parent workspace opencode.json, or nil.
func readParentOpenCode(root string) map[string]any {
	path := filepath.Join(filepath.Dir(root), "opencode.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	return doc
}

// doctorPermissionCheck compares configured permissions with the generated set.
func doctorPermissionCheck(doc map[string]any) doctorCheck {
	gen := beaconmcp.GeneratedPermissions(beaconmcp.ToolCatalog())
	if !beaconmcp.PermissionsInSync(doc["permissions"], gen.Global) {
		return doctorCheck{"permissions", checkWarn, "global permissions out of sync; run go generate"}
	}
	agents, _ := doc["agents"].(map[string]any)
	want := map[string][]beaconmcp.Permission{
		"orchestrator": gen.Allow, "build": gen.Allow, "worker": gen.Deny,
	}
	for role, perms := range want {
		agent, ok := agents[role].(map[string]any)
		if !ok {
			continue
		}
		if !beaconmcp.PermissionsInSync(agent["permissions"], perms) {
			return doctorCheck{"permissions", checkWarn, role + " permissions out of sync; run go generate"}
		}
	}
	return doctorCheck{"permissions", checkOK, "in sync with ToolCatalog"}
}

// doctorBinaryCheck reports the executable named by the active beacon MCP
// entry. A PATH-only name stays advisory because doctor cannot judge the
// freshness of a binary it does not own.
func doctorBinaryCheck(root string, doc map[string]any) doctorCheck {
	entry := doctorMCPEntry(root, doc)
	command, direct := doctorConfiguredCommand(root, entry)
	if command == "" {
		return doctorCheck{"beacon command", checkFail,
			"no command in the beacon MCP entry; fix: run beacon init to write the pooled server command"}
	}
	if !direct {
		return doctorCheck{"beacon command", checkFail, command + "; fix: build with bash scripts/build-beacon.sh and set command to " +
			filepath.Join(root, ".opencode", "bin", "beacon") + " mcp"}
	}
	if !strings.ContainsAny(command, `/\`) {
		if resolved, err := exec.LookPath(command); err == nil {
			return doctorCheck{"beacon command", checkOK,
				resolved + " via PATH; fix: pin the command path in the MCP entry to avoid a stale binary"}
		}
		return doctorCheck{"beacon command", checkWarn,
			command + " not found on PATH; fix: run bash scripts/build-beacon.sh and set the command path in " + entry.path}
	}
	if _, err := os.Stat(command); err != nil {
		return doctorCheck{"beacon command", checkFail,
			command + " does not exist; fix: run bash scripts/build-beacon.sh, then restart the MCP daemon and the TUI"}
	}
	return doctorCheck{"beacon command", checkOK, command + " from " + entry.source + " in " + entry.path}
}

// doctorDaemonCheck reports the lock owner and the last daemon error. A fatal
// error or a broken owner fails the check because no reconciler can advance
// runs while that record stands.
func doctorDaemonCheck(cfg config.Config) doctorCheck {
	dir := stateDir(cfg)
	status := readDaemonStatus(dir)
	if status.Error != nil {
		return doctorCheck{"daemon", checkFail, fmt.Sprintf(
			"lock owner %s, state %s, error %s: %s; fix: inspect %s, then restart beacon serve",
			doctorOwnerLabel(status.PID), status.State, status.Error.Kind, status.Error.Message,
			filepath.Join(dir, daemonErrorFile))}
	}
	switch status.State {
	case "alive":
		detail := fmt.Sprintf("lock owner pid %d, heartbeat %.1fs ago, no daemon error", status.PID, status.HeartbeatAgeSecond)
		return doctorCheck{"daemon", checkOK, detail}
	case "absent":
		return doctorCheck{"daemon", checkWarn,
			"no lock owner and no daemon error; fix: start beacon serve when the panel needs live runs"}
	default:
		return doctorCheck{"daemon", checkFail, fmt.Sprintf(
			"lock owner %s, state %s; fix: remove the stale daemon record, then restart beacon serve",
			doctorOwnerLabel(status.PID), status.State)}
	}
}

// doctorOwnerLabel names the process recorded as the daemon owner.
func doctorOwnerLabel(pid int) string {
	if pid <= 0 {
		return "none"
	}
	return fmt.Sprintf("pid %d", pid)
}

// doctorProbe describes one doctor network probe.
type doctorProbe struct {
	name     string
	endpoint string
	token    string
	auth     func(*http.Request, string)
}

// doctorNetworkChecks probes the OpenCode endpoint and the SCM base branch concurrently.
// Both probes share one deadline, so two hung endpoints cost one timeout rather than two.
// Results are returned in probe order regardless of which finishes first.
func doctorNetworkChecks(ctx context.Context, cfg config.Config, client *http.Client) []doctorCheck {
	probes := []doctorProbe{
		{
			name:     "opencode endpoint",
			endpoint: strings.TrimRight(cfg.Harness.Endpoint, "/") + "/api/project",
			token:    envOr(cfg.Harness.TokenEnv, cfg.Harness.Token),
			auth:     shared.ApplyAuth,
		},
		{
			name:     "scm base branch",
			endpoint: scmBranchURL(cfg),
			token:    envOr(cfg.SCM.TokenEnv, cfg.SCM.Token),
			auth:     bearerAuth,
		},
	}

	ctx, cancel := context.WithTimeout(ctx, doctorHTTPTimeout)
	defer cancel()

	checks := make([]doctorCheck, len(probes))
	var wg sync.WaitGroup
	for i, probe := range probes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			checks[i] = doctorHTTPCheck(ctx, probe.name, probe.endpoint, probe.token, probe.auth, client)
		}()
	}
	wg.Wait()
	return checks
}

// scmBranchURL builds the branch API URL for the configured SCM provider.
func scmBranchURL(cfg config.Config) string {
	if cfg.BaseBranch == "" {
		return ""
	}
	base := url.PathEscape(cfg.BaseBranch)
	if cfg.SCM.Provider == "gitlab" {
		root := cfg.SCM.BaseURL
		if root == "" {
			root = "https://gitlab.com/api/v4"
		}
		project := url.PathEscape(cfg.SCM.Owner + "/" + cfg.SCM.Repo)
		return strings.TrimRight(root, "/") + "/projects/" + project + "/repository/branches/" + base
	}
	return "https://api.github.com/repos/" + url.PathEscape(cfg.SCM.Owner) + "/" +
		url.PathEscape(cfg.SCM.Repo) + "/branches/" + base
}

// doctorHTTPCheck performs one network probe and formats its result.
func doctorHTTPCheck(ctx context.Context, name, endpoint, token string, auth func(*http.Request, string), client *http.Client) doctorCheck {
	if endpoint == "" || endpoint == "/api/project" {
		return doctorCheck{name, checkWarn, "not configured"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return doctorCheck{name, checkWarn, "invalid endpoint"}
	}
	if token != "" {
		auth(req, token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return doctorCheck{name, checkWarn, "unreachable"}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return doctorCheck{name, checkWarn, fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	return doctorCheck{name, checkOK, fmt.Sprintf("HTTP %d", resp.StatusCode)}
}

// doctorMCPConfig locates the active beacon MCP entry and the file that holds it.
type doctorMCPConfig struct {
	server map[string]any
	source string
	path   string
}

// doctorMCPEntry checks the root config first, then the workspace parent. The
// plugin never registers MCP tools, so one of these files must hold the server.
func doctorMCPEntry(root string, doc map[string]any) doctorMCPConfig {
	if found := doctorMCPIn(doc, filepath.Join(root, "opencode.json")); found.server != nil {
		return found
	}
	return doctorMCPIn(readParentOpenCode(root), filepath.Join(filepath.Dir(root), "opencode.json"))
}

// doctorMCPIn returns the beacon server from one config document.
func doctorMCPIn(doc map[string]any, path string) doctorMCPConfig {
	empty := doctorMCPConfig{path: path}
	if doc == nil {
		return empty
	}
	mcpMap, _ := doc["mcp"].(map[string]any)
	if server, ok := mcpMap["beacon"].(map[string]any); ok {
		return doctorMCPConfig{server: server, source: "mcp.beacon", path: path}
	}
	servers, _ := mcpMap["servers"].(map[string]any)
	if server, ok := servers["beacon"].(map[string]any); ok {
		return doctorMCPConfig{server: server, source: "mcp.servers.beacon", path: path}
	}
	return empty
}

// doctorPreflightChecks names each prerequisite for tools and the status panel.
// A failed line carries the manual fix in the same line.
func doctorPreflightChecks(root string, doc map[string]any) []doctorCheck {
	entry := doctorMCPEntry(root, doc)
	return []doctorCheck{
		doctorMCPSourceCheck(entry),
		doctorBinaryFreshnessCheck(root),
		doctorTUIPeersCheck(root),
		doctorOperatorCheck(doc),
	}
}

// doctorMCPSourceCheck names the file that provides the ten tools.
func doctorMCPSourceCheck(entry doctorMCPConfig) doctorCheck {
	if entry.server == nil {
		return doctorCheck{"preflight MCP config", checkFail,
			"no beacon MCP entry in " + entry.path + " or its parent; fix: run beacon init or add a pooled beacon server entry, the plugin does not register tools"}
	}
	return doctorCheck{"preflight MCP config", checkOK, entry.source + " in " + entry.path}
}

// doctorCommandArgs accepts the string and list forms of an MCP command.
func doctorCommandArgs(value any) []string {
	switch args := value.(type) {
	case []any:
		out := make([]string, 0, len(args))
		for _, arg := range args {
			if s, ok := arg.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return args
	case string:
		return strings.Fields(args)
	default:
		return nil
	}
}

// doctorConfiguredCommand resolves the executable from the active MCP entry.
// It follows the cwd field and the exec token of a shell wrapper, so doctor
// checks the command the pool actually starts instead of a PATH binary.
// The second result is false for the go run fallback, which names no binary.
func doctorConfiguredCommand(root string, entry doctorMCPConfig) (string, bool) {
	if entry.server == nil {
		return "", false
	}
	args := doctorCommandArgs(entry.server["command"])
	if len(args) == 0 {
		return "", false
	}
	if args[0] == "go" && len(args) > 1 && args[1] == "run" {
		return "go run ./cmd/beacon mcp", false
	}
	base := root
	if cwd, _ := entry.server["cwd"].(string); cwd != "" {
		if filepath.IsAbs(cwd) {
			base = cwd
		} else {
			base = filepath.Join(root, cwd)
		}
	}
	name := args[0]
	if target := doctorShellExecTarget(args); target != "" {
		name = target
	}
	if filepath.IsAbs(name) {
		return filepath.Clean(name), true
	}
	if strings.Contains(name, "/") {
		return filepath.Clean(filepath.Join(base, name)), true
	}
	return name, true
}

// doctorShellExecTarget reads the executable after exec in a shell command.
// The pool config wraps its command in sh -c, so the shell string holds the
// path doctor must check.
func doctorShellExecTarget(args []string) string {
	if len(args) < 3 {
		return ""
	}
	switch args[0] {
	case "sh", "/bin/sh", "bash", "/bin/bash":
	default:
		return ""
	}
	for _, part := range args[2:] {
		fields := strings.Fields(part)
		for i, arg := range fields {
			if arg == "exec" && i+1 < len(fields) {
				return fields[i+1]
			}
		}
	}
	return ""
}

// doctorBinaryFreshnessCheck compares the local binary with the source hash
// recorded by scripts/build-beacon.sh. A stale binary keeps serving until the
// operator rebuilds and restarts the MCP daemon.
func doctorBinaryFreshnessCheck(root string) doctorCheck {
	binary := filepath.Join(root, ".opencode", "bin", "beacon")
	if _, err := os.Stat(binary); err != nil {
		return doctorCheck{"preflight binary freshness", checkWarn,
			"no local binary at " + binary + "; fix: run bash scripts/build-beacon.sh if the MCP command uses this path"}
	}
	recordedPath := binary + ".build-sha"
	recorded, err := os.ReadFile(recordedPath)
	if err != nil {
		return doctorCheck{"preflight binary freshness", checkFail,
			"no recorded source hash at " + recordedPath + "; fix: run bash scripts/build-beacon.sh, then restart the MCP daemon and the TUI"}
	}
	current, err := doctorBuildHash(root)
	if err != nil {
		return doctorCheck{"preflight binary freshness", checkWarn,
			"cannot compute the source hash: " + err.Error() + "; fix: run bash scripts/build-beacon.sh from the repository root"}
	}
	if strings.TrimSpace(string(recorded)) != current {
		return doctorCheck{"preflight binary freshness", checkFail, fmt.Sprintf(
			"binary built from %s but sources hash to %s; fix: run bash scripts/build-beacon.sh, then restart the MCP daemon and the TUI",
			doctorShortHash(string(recorded)), doctorShortHash(current))}
	}
	return doctorCheck{"preflight binary freshness", checkOK, "binary matches recorded source hash " + doctorShortHash(current)}
}

// doctorShortHash keeps a hash readable in one doctor line.
func doctorShortHash(hash string) string {
	value := strings.TrimSpace(hash)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

// doctorBuildHash reproduces scripts/build-beacon.sh --hash in Go. Each build
// input contributes its path and its own sha256 line before the final digest.
func doctorBuildHash(root string) (string, error) {
	var sources []string
	for _, dir := range []string{"cmd", "internal", "pkg", "harnesses", "ui/tui"} {
		base := filepath.Join(root, filepath.FromSlash(dir))
		info, err := os.Stat(base)
		if err != nil || !info.IsDir() {
			continue
		}
		if err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			sources = append(sources, filepath.ToSlash(rel))
			return nil
		}); err != nil {
			return "", err
		}
	}
	sort.Strings(sources)
	for _, name := range []string{"go.mod", "go.sum", "go.work"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			sources = append(sources, name)
		}
	}
	var input bytes.Buffer
	for _, rel := range sources {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(&input, "%s\n%x  %s\n", rel, sum, rel)
	}
	digest := sha256.Sum256(input.Bytes())
	return hex.EncodeToString(digest[:]), nil
}

// doctorTUIPeersCheck verifies that the plugin pins the peers supplied by the
// supported OpenCode CLI and that no local copy creates a second renderer.
func doctorTUIPeersCheck(root string) doctorCheck {
	plugin := filepath.Join(root, ".opencode", "plugins", "beacon-project")
	manifestPath := filepath.Join(plugin, "supported-cli.json")
	packagePath := filepath.Join(plugin, "package.json")
	manifestData, errManifest := os.ReadFile(manifestPath)
	packageData, errPackage := os.ReadFile(packagePath)
	if errManifest != nil || errPackage != nil {
		return doctorCheck{"preflight TUI peers", checkWarn,
			"plugin files missing at " + plugin + "; fix: run beacon init to deploy .opencode/plugins/beacon-project"}
	}
	var manifest struct {
		CliVersion string            `json:"cliVersion"`
		Peers      map[string]string `json:"peers"`
	}
	var pkg struct {
		PeerDependencies map[string]string `json:"peerDependencies"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return doctorCheck{"preflight TUI peers", checkFail, "invalid " + manifestPath + ": " + err.Error() +
			"; fix: restore supported-cli.json from the repository"}
	}
	if err := json.Unmarshal(packageData, &pkg); err != nil {
		return doctorCheck{"preflight TUI peers", checkFail, "invalid " + packagePath + ": " + err.Error() +
			"; fix: restore package.json from the repository"}
	}
	for _, peer := range doctorSortedKeys(manifest.Peers) {
		if pkg.PeerDependencies[peer] != manifest.Peers[peer] {
			return doctorCheck{"preflight TUI peers", checkFail, fmt.Sprintf(
				"peer %s is %q but %s pins %q; fix: copy peerDependencies from %s",
				peer, pkg.PeerDependencies[peer], packagePath, manifest.Peers[peer], manifestPath)}
		}
	}
	if copies := doctorLocalPeerCopies(plugin, manifest.Peers); len(copies) > 0 {
		return doctorCheck{"preflight TUI peers", checkFail,
			"local peer copy " + copies[0] + "; fix: remove plugin node_modules peers so the host supplies one renderer"}
	}
	version, err := doctorInstalledCLIVersion()
	if err != nil {
		return doctorCheck{"preflight TUI peers", checkWarn, fmt.Sprintf(
			"cannot read opencode --version: %v; fix: install OpenCode CLI %s, which supplies the pinned TUI peers", err, manifest.CliVersion)}
	}
	if !strings.Contains(version, manifest.CliVersion) {
		return doctorCheck{"preflight TUI peers", checkWarn, fmt.Sprintf(
			"installed CLI %s does not supply the pinned peers; fix: install OpenCode CLI %s", version, manifest.CliVersion)}
	}
	return doctorCheck{"preflight TUI peers", checkOK,
		"OpenCode " + manifest.CliVersion + " supplies " + strings.Join(doctorSortedKeys(manifest.Peers), ", ")}
}

// doctorSortedKeys returns map keys in a stable order for doctor lines.
func doctorSortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// doctorLocalPeerCopies finds peer packages installed under the plugin. A
// second Solid or OpenTUI copy breaks the renderer context.
func doctorLocalPeerCopies(plugin string, peers map[string]string) []string {
	root := filepath.Join(plugin, "node_modules")
	var found []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		slash := filepath.ToSlash(rel)
		for peer := range peers {
			if slash == peer || strings.HasSuffix(slash, "/"+peer) {
				found = append(found, rel)
				return filepath.SkipDir
			}
		}
		return nil
	})
	sort.Strings(found)
	return found
}

// doctorCLIVersionTimeout bounds the version probe so doctor stays local and quick.
const doctorCLIVersionTimeout = 3 * time.Second

// doctorInstalledCLIVersion reads the version of the OpenCode CLI in PATH.
func doctorInstalledCLIVersion() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), doctorCLIVersionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "opencode", "--version").CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// doctorOperatorCheck names the agent that runs the operator session and
// checks that it carries the generated orchestrator permissions.
func doctorOperatorCheck(doc map[string]any) doctorCheck {
	agents, _ := doc["agents"].(map[string]any)
	want := beaconmcp.GeneratedPermissions(beaconmcp.ToolCatalog()).Allow
	var broken []string
	for _, role := range []string{"build", "orchestrator"} {
		agent, ok := agents[role].(map[string]any)
		if !ok {
			broken = append(broken, role+" agent missing")
			continue
		}
		if !beaconmcp.PermissionsInSync(agent["permissions"], want) {
			broken = append(broken, role+" permissions out of sync")
		}
	}
	if len(broken) > 0 {
		return doctorCheck{"preflight operator agent", checkFail,
			strings.Join(broken, ", ") + "; fix: run beacon init to restore the operator agents, then go generate ./... to refresh permissions"}
	}
	return doctorCheck{"preflight operator agent", checkOK,
		"build and orchestrator carry the generated beacon_* permissions"}
}
