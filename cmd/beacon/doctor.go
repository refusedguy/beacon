package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/refusedguy/beacon/internal/infra/config"
	beaconmcp "github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/shared"
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
		_, _ = fmt.Fprintf(out, "%s %s: %s\n", c.level.label(), c.name, c.detail)
	}
}

// doctorHTTPTimeout bounds each doctor network probe.
const doctorHTTPTimeout = 5 * time.Second

// doctorHTTPClient builds an HTTP client with the doctor timeout.
func doctorHTTPClient() *http.Client {
	return &http.Client{Timeout: doctorHTTPTimeout}
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
	if config.LiteralOrEnv(cfg.SCM.Token, cfg.SCM.TokenEnv) != "" {
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
	path := filepath.Join(root, "opencode.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return append(checks, doctorCheck{"opencode.json", checkFail, "missing or unreadable"})
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return append(checks, doctorCheck{"opencode.json", checkFail, "invalid JSON"})
	}
	checks = append(checks, doctorFileShapeCheck(doc, root))
	checks = append(checks, doctorPermissionCheck(doc))
	checks = append(checks, doctorBinaryCheck(root), doctorDaemonCheck(cfg))
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

// doctorBinaryCheck locates the beacon binary on PATH or in the project.
func doctorBinaryCheck(root string) doctorCheck {
	if path, err := exec.LookPath("beacon"); err == nil {
		return doctorCheck{"beacon binary", checkOK, path}
	}
	if existsIn(root, filepath.Join(".opencode", "bin", "beacon")) {
		return doctorCheck{"beacon binary", checkOK, ".opencode/bin/beacon"}
	}
	return doctorCheck{"beacon binary", checkWarn, "not in PATH or .opencode/bin"}
}

// doctorDaemonCheck reports daemon liveness from its status record.
func doctorDaemonCheck(cfg config.Config) doctorCheck {
	status := readDaemonStatus(stateDir(cfg))
	switch status.State {
	case "alive":
		detail := fmt.Sprintf("alive (pid %d, heartbeat %.1fs ago)", status.PID, status.HeartbeatAgeSecond)
		return doctorCheck{"daemon", checkOK, detail}
	case "absent":
		return doctorCheck{"daemon", checkWarn, "not running"}
	default:
		return doctorCheck{"daemon", checkWarn, status.State}
	}
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
			token:    config.LiteralOrEnv(cfg.Harness.Token, cfg.Harness.TokenEnv),
			auth:     shared.ApplyAuth,
		},
		{
			name:     "scm base branch",
			endpoint: scmBranchURL(cfg),
			token:    config.LiteralOrEnv(cfg.SCM.Token, cfg.SCM.TokenEnv),
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

// bearerAuth sets a Bearer Authorization header on req.
func bearerAuth(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
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
