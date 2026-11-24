package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/refusedguy/beacon/harnesses/opencode"
	"github.com/refusedguy/beacon/internal/infra/config"
	beaconmcp "github.com/refusedguy/beacon/internal/infra/mcp"
)

type checkLevel int

const (
	checkOK checkLevel = iota
	checkWarn
	checkFail
)

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

type doctorCheck struct {
	name   string
	level  checkLevel
	detail string
}

func doctorExitCode(checks []doctorCheck) int {
	for _, c := range checks {
		if c.level == checkFail {
			return 1
		}
	}
	return 0
}

func printDoctorChecks(out io.Writer, checks []doctorCheck) {
	for _, c := range checks {
		_, _ = fmt.Fprintf(out, "%s %s: %s\n", c.level.label(), c.name, c.detail)
	}
}

func newDoctorCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check a Beacon installation and report problems",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client := &http.Client{Timeout: 2 * time.Second}
			if code := runDoctor(cmd.OutOrStdout(), *configPath, client); code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
}

func runDoctor(out io.Writer, configPath string, client *http.Client) int {
	root, err := projectRoot()
	if err != nil {
		printDoctorChecks(out, []doctorCheck{{"project root", checkFail, err.Error()}})
		return 1
	}
	checks := doctorGitChecks(root)
	cfgChecks, cfg := doctorConfigChecks(root, doctorConfigPath(root, configPath))
	checks = append(checks, cfgChecks...)
	checks = append(checks, doctorToolChecks(root, cfg)...)
	checks = append(checks, doctorNetworkChecks(cfg, client)...)
	printDoctorChecks(out, checks)
	return doctorExitCode(checks)
}

func doctorConfigPath(root, flag string) string {
	if flag != "" {
		return flag
	}
	return filepath.Join(root, ".beacon", "config.yaml")
}

func gitOutputAt(root string, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func doctorGitChecks(root string) []doctorCheck {
	inside, err := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil || strings.TrimSpace(string(inside)) != "true" {
		return []doctorCheck{{"git repo", checkFail, root + " is not a git work tree"}}
	}
	checks := []doctorCheck{{"git repo", checkOK, root}}
	if gitOutputAt(root, "remote", "get-url", "origin") == "" {
		checks = append(checks, doctorCheck{"origin remote", checkWarn, "no origin remote configured"})
	} else {
		checks = append(checks, doctorCheck{"origin remote", checkOK, "configured"})
	}
	return checks
}

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
	checks = append(checks, doctorTokenCheck(cfg))
	if len(cfg.Merge.RequiredChecks) == 0 {
		checks = append(checks, doctorCheck{"required_checks", checkWarn, "none configured"})
	} else {
		checks = append(checks, doctorCheck{"required_checks", checkOK, fmt.Sprintf("%d configured", len(cfg.Merge.RequiredChecks))})
	}
	return append(checks, doctorTasksCheck(root)), cfg
}

func doctorBaseBranch(root string, cfg config.Config) doctorCheck {
	if cfg.BaseBranch == "" {
		return doctorCheck{"base branch", checkFail, "base_branch is empty"}
	}
	for _, ref := range []string{cfg.BaseBranch, "origin/" + cfg.BaseBranch} {
		if gitOutputAt(root, "rev-parse", "--verify", "--quiet", ref) != "" {
			return doctorCheck{"base branch", checkOK, cfg.BaseBranch}
		}
	}
	return doctorCheck{"base branch", checkWarn, cfg.BaseBranch + " not found locally or on origin"}
}

func doctorTokenCheck(cfg config.Config) doctorCheck {
	if cfg.SCM.Token != "" || os.Getenv(cfg.SCM.TokenEnv) != "" {
		return doctorCheck{"scm token", checkOK, "present via env or config"}
	}
	return doctorCheck{"scm token", checkWarn, "not set; export " + cfg.SCM.TokenEnv}
}

func doctorTasksCheck(root string) doctorCheck {
	info, err := os.Stat(filepath.Join(root, "docs", "tasks"))
	if err == nil && info.IsDir() {
		return doctorCheck{"docs/tasks", checkOK, "exists"}
	}
	return doctorCheck{"docs/tasks", checkWarn, "missing; markdown task source needs it"}
}

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

func doctorBinaryCheck(root string) doctorCheck {
	if path, err := exec.LookPath("beacon"); err == nil {
		return doctorCheck{"beacon binary", checkOK, path}
	}
	if existsIn(root, filepath.Join(".opencode", "bin", "beacon")) {
		return doctorCheck{"beacon binary", checkOK, ".opencode/bin/beacon"}
	}
	return doctorCheck{"beacon binary", checkWarn, "not in PATH or .opencode/bin"}
}

func doctorDaemonCheck(cfg config.Config) doctorCheck {
	status := readDaemonStatus(filepath.Dir(cfg.StateDB))
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

func doctorNetworkChecks(cfg config.Config, client *http.Client) []doctorCheck {
	return []doctorCheck{
		doctorHTTPCheck("opencode endpoint", strings.TrimRight(cfg.Harness.Endpoint, "/")+"/api/project",
			firstNonEmpty(os.Getenv(cfg.Harness.TokenEnv), cfg.Harness.Token), opencode.ApplyAuth, client),
		doctorHTTPCheck("scm base branch", scmBranchURL(cfg),
			firstNonEmpty(os.Getenv(cfg.SCM.TokenEnv), cfg.SCM.Token), bearerAuth, client),
	}
}

func bearerAuth(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
}

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

func doctorHTTPCheck(name, endpoint, token string, auth func(*http.Request, string), client *http.Client) doctorCheck {
	if endpoint == "" || endpoint == "/api/project" {
		return doctorCheck{name, checkWarn, "not configured"}
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
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
