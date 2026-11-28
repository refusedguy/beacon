package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/config"
)

func TestParseRemote(t *testing.T) {
	cases := []struct {
		raw      string
		provider string
		owner    string
		repo     string
	}{
		{"git@github.com:acme/demo.git", "github", "acme", "demo"},
		{"https://github.com/acme/demo.git", "github", "acme", "demo"},
		{"ssh://git@gitlab.com/acme/demo.git", "gitlab", "acme", "demo"},
		{"ssh://git@gitlab.example.com:2222/acme/demo.git", "gitlab", "acme", "demo"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			r, err := parseRemote(tc.raw)
			if err != nil {
				t.Fatalf("parseRemote(tc.raw): %v", err)
			}
			if r.provider != tc.provider || r.owner != tc.owner || r.repo != tc.repo {
				t.Fatalf("got %+v", r)
			}
		})
	}
}

func TestRunInitUsesNewRebaseLimit(t *testing.T) {
	dir := initProject(t, "go.mod")
	t.Chdir(dir)

	if err := runInit(&initFlags{nonInteractive: true, skipPlugin: true}); err != nil {
		t.Fatalf("runInit(&initFlags{nonInteractive: true, skipPlugin: true}): %v", err)
	}

	cfg, err := config.Load(".beacon/config.yaml")
	if err != nil {
		t.Fatalf("config.Load(\".beacon/config.yaml\"): %v", err)
	}
	if cfg.Merge.MaxAttempts != 12 {
		t.Fatalf("generated merge.max_attempts = %d, want 12", cfg.Merge.MaxAttempts)
	}
}

func TestRunInitDeclineFailsWithoutWritingConfig(t *testing.T) {
	dir := initProject(t, "go.mod")
	t.Chdir(dir)

	var out bytes.Buffer
	err := runInitWithInput(&initFlags{skipPlugin: true}, &out, strings.NewReader("n\n"), true)
	if !errors.Is(err, errInitDeclined) {
		t.Fatalf("runInitWithInput error = %v, want errInitDeclined", err)
	}
	if existsIn(dir, filepath.Join(".beacon", "config.yaml")) {
		t.Error("declined init wrote a config")
	}
}

func TestRunInitAcceptWritesConfig(t *testing.T) {
	dir := initProject(t, "go.mod")
	t.Chdir(dir)

	var out bytes.Buffer
	if err := runInitWithInput(&initFlags{skipPlugin: true}, &out, strings.NewReader("y\n"), true); err != nil {
		t.Fatalf("runInitWithInput: %v", err)
	}
	if _, err := config.Load(filepath.Join(dir, ".beacon", "config.yaml")); err != nil {
		t.Fatalf("accepted init did not write a config: %v", err)
	}
}

func TestRunInitNonInteractive(t *testing.T) {
	dir := initProject(t, "go.mod")
	writeWorkflow(t, dir)
	t.Chdir(dir)

	f := &initFlags{
		nonInteractive:  true,
		harnessEndpoint: "http://127.0.0.1:9999",
		maxActiveAgents: 3,
		skipPlugin:      true,
		weakPC:          true,
	}
	if err := runInit(f); err != nil {
		t.Fatalf("runInit(f): %v", err)
	}

	cfg, err := config.Load(".beacon/config.yaml")
	if err != nil {
		t.Fatalf("config.Load(\".beacon/config.yaml\"): %v", err)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"task source", cfg.TaskSource.Provider, "markdown"},
		{"harness command", cfg.Harness.Command, "worker"},
		{"endpoint", cfg.Harness.Endpoint, "http://127.0.0.1:9999"},
		{"max in flight", cfg.Concurrency.MaxInFlight, 10},
		{"max active agents", cfg.Concurrency.MaxActiveAgents, 3},
		{"max attempts", cfg.Merge.MaxAttempts, 12},
		{"base red mode", cfg.OnBaseRed.Mode, "spawn_fixer"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if !strings.Contains(cfg.Harness.WorkerInstructions, "go test") {
		t.Errorf("Go stack instructions lack targeted go test: %q", cfg.Harness.WorkerInstructions)
	}
	if len(cfg.Merge.RequiredChecks) != 2 || cfg.Merge.RequiredChecks[0] != "lint" || cfg.Merge.RequiredChecks[1] != "test" {
		t.Errorf("required_checks = %v, want [lint test]", cfg.Merge.RequiredChecks)
	}
	if !existsIn(dir, filepath.Join("docs", "tasks", ".gitkeep")) {
		t.Error("docs/tasks/.gitkeep was not created")
	}
	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("os.ReadFile(filepath.Join(dir, \".gitignore\")): %v", err)
	}
	for _, entry := range []string{".beacon/state.db*", ".beacon/worktrees/", ".beacon/connection.json", ".beacon/*.lock", ".beacon/daemon*", ".beacon/config.yaml"} {
		if !strings.Contains(string(gitignore), entry) {
			t.Errorf(".gitignore lacks %q", entry)
		}
	}
	doc := readOpenCode(t, dir)
	agents, _ := doc["agents"].(map[string]any)
	for _, role := range []string{"orchestrator", "worker", "build"} {
		if _, ok := agents[role].(map[string]any); !ok {
			t.Errorf("opencode.json lacks agents.%s", role)
		}
	}
	mcpMap, _ := doc["mcp"].(map[string]any)
	if _, ok := mcpMap["beacon"].(map[string]any); !ok {
		t.Error("opencode.json lacks mcp.beacon")
	}
}

func TestBuildInitConfigStackInstructions(t *testing.T) {
	cases := []struct {
		name   string
		marker string
		want   string
	}{
		{"go", "go.mod", "go test"},
		{"node", "package.json", "npm run build"},
		{"python-pyproject", "pyproject.toml", "pytest"},
		{"python-requirements", "requirements.txt", "pytest"},
		{"rust", "Cargo.toml", "cargo test"},
		{"dotnet-sln", "app.sln", "dotnet test"},
		{"dotnet-csproj", "app.csproj", "dotnet test"},
		{"unknown", "", "targeted tests"},
	}
	remote := remote{provider: "github", owner: "acme", repo: "demo"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.marker != "" {
				if err := os.WriteFile(filepath.Join(dir, tc.marker), nil, 0o644); err != nil {
					t.Fatalf("os.WriteFile(filepath.Join(dir, tc.marker), nil, 0o644): %v", err)
				}
			}
			cfg := buildInitConfig(dir, initOptions{taskSource: "markdown", endpoint: "http://127.0.0.1:4096", weakPC: true, maxInFlight: 10, maxActiveAgents: 2}, remote, "main")
			if !strings.Contains(cfg.Harness.WorkerInstructions, tc.want) {
				t.Fatalf("instructions lack %q: %q", tc.want, cfg.Harness.WorkerInstructions)
			}
			if !strings.Contains(cfg.Harness.WorkerInstructions, "weak PC") {
				t.Fatalf("instructions lack weak-PC rule: %q", cfg.Harness.WorkerInstructions)
			}
		})
	}
}

func TestBuildInitConfigFullVerificationWhenWeakPCDisabled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), nil, 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(dir, \"go.mod\"), nil, 0o644): %v", err)
	}
	cfg := buildInitConfig(dir, initOptions{taskSource: "markdown", weakPC: false}, remote{provider: "github"}, "main")
	if !strings.Contains(cfg.Harness.WorkerInstructions, "full test suite") {
		t.Fatalf("instructions = %q, want full verification", cfg.Harness.WorkerInstructions)
	}
}

func TestDetectRequiredChecks(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir)
	got := detectRequiredChecks(dir)
	if len(got) != 2 || got[0] != "lint" || got[1] != "test" {
		t.Fatalf("required checks = %v, want [lint test]", got)
	}
}

func TestMergeOpenCodeConfigPreservesUserKeys(t *testing.T) {
	dir := t.TempDir()
	original := `{"theme":"dark","customRoot":42,"agents":{"worker":{"description":"Custom worker","mode":"primary","permissions":[{"action":"custom","effect":"allow","resource":"*"}]}},"mcp":{"beacon":{"command":["old"],"custom":true}}}`
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(original), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(dir, \"opencode.json\"), []byte(original), 0o644): %v", err)
	}
	if err := mergeOpenCodeConfig(dir); err != nil {
		t.Fatalf("mergeOpenCodeConfig(dir): %v", err)
	}
	doc := readOpenCode(t, dir)
	if doc["theme"] != "dark" || doc["customRoot"] != float64(42) {
		t.Errorf("unrelated root keys changed: %v", doc)
	}
	agents, _ := doc["agents"].(map[string]any)
	worker, _ := agents["worker"].(map[string]any)
	if worker["description"] != "Custom worker" || worker["mode"] != "primary" {
		t.Errorf("worker agent keys changed: %v", worker)
	}
	perms, _ := worker["permissions"].([]any)
	foundCustom, foundDeny := false, false
	for _, entry := range perms {
		obj, _ := entry.(map[string]any)
		switch obj["action"] {
		case "custom":
			foundCustom = obj["effect"] == "allow"
		case "beacon_beacon_task_create":
			foundDeny = obj["effect"] == "deny"
		}
	}
	if !foundCustom || !foundDeny {
		t.Errorf("worker permissions lost user entries or generated rules: %v", perms)
	}
	mcpMap, _ := doc["mcp"].(map[string]any)
	beacon, _ := mcpMap["beacon"].(map[string]any)
	if beacon["custom"] != true {
		t.Errorf("mcp.beacon user key lost: %v", beacon)
	}
	command, _ := beacon["command"].([]any)
	if len(command) != 2 || command[0] != "beacon" || command[1] != "mcp" {
		t.Errorf("mcp.beacon.command = %v, want [beacon mcp]", command)
	}
	env, _ := beacon["environment"].(map[string]any)
	if env["BEACON_PROJECT_ROOT"] != dir {
		t.Errorf("BEACON_PROJECT_ROOT = %v, want %v", env["BEACON_PROJECT_ROOT"], dir)
	}
}

func TestAppendGitignoreIsSafeAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")
	if err := os.WriteFile(path, []byte("node_modules/\ncustom.log\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(path, []byte(\"node_modules/\\ncustom.log\\n\"), 0o644): %v", err)
	}
	if err := appendGitignore(path); err != nil {
		t.Fatalf("appendGitignore(path): %v", err)
	}
	if err := appendGitignore(path); err != nil {
		t.Fatalf("appendGitignore(path): %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(path): %v", err)
	}
	text := string(data)
	for _, want := range []string{"node_modules/", "custom.log", ".beacon/state.db*", ".beacon/worktrees/", ".beacon/connection.json", ".beacon/*.lock", ".beacon/daemon*", ".beacon/config.yaml"} {
		if !strings.Contains(text, want) {
			t.Errorf(".gitignore lacks %q", want)
		}
	}
	if strings.Count(text, ".beacon/config.yaml") != 1 {
		t.Error(".gitignore duplicated an entry on the second run")
	}
}

func TestDeployPluginCopiesSourceAndHonoursSkip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "plugin")
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(filepath.Join(src, \"nested\"), 0o755): %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "index.js"), []byte("// plugin"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(src, \"index.js\"), []byte(\"// plugin\"), 0o644): %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "ui.js"), []byte("// ui"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(src, \"nested\", \"ui.js\"), []byte(\"// ui\"), 0o644): %v", err)
	}
	root := filepath.Join(dir, "target")
	if err := deployPlugin(root, initOptions{pluginDir: src}); err != nil {
		t.Fatalf("deployPlugin(root, initOptions{pluginDir: src}): %v", err)
	}
	dst := filepath.Join(root, ".opencode", "plugins", "beacon-project")
	for _, name := range []string{"index.js", filepath.Join("nested", "ui.js")} {
		want, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatalf("os.ReadFile(filepath.Join(src, name)): %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil {
			t.Fatalf("os.ReadFile(filepath.Join(dst, name)): %v", err)
		}
		if string(got) != string(want) {
			t.Errorf("plugin file %s changed", name)
		}
	}
	skipRoot := filepath.Join(dir, "skip")
	if err := deployPlugin(skipRoot, initOptions{skipPlugin: true, pluginDir: src}); err != nil {
		t.Fatalf("deployPlugin(skipRoot, initOptions{skipPlugin: true, pluginDir: src}): %v", err)
	}
	if existsIn(skipRoot, filepath.Join(".opencode", "plugins", "beacon-project")) {
		t.Error("--skip-plugin still deployed a plugin")
	}
}

func initProject(t *testing.T, marker string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/acme/demo.git")
	if marker != "" {
		if err := os.WriteFile(filepath.Join(dir, marker), nil, 0o644); err != nil {
			t.Fatalf("os.WriteFile(filepath.Join(dir, marker), nil, 0o644): %v", err)
		}
	}
	return dir
}

func writeWorkflow(t *testing.T, dir string) {
	t.Helper()
	workflows := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(workflows, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(workflows, 0o755): %v", err)
	}
	content := "name: CI\non: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo test\n  lint:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo lint\n"
	if err := os.WriteFile(filepath.Join(workflows, "ci.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(workflows, \"ci.yml\"), []byte(content), 0o644): %v", err)
	}
}

func readOpenCode(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "opencode.json"))
	if err != nil {
		t.Fatalf("os.ReadFile(filepath.Join(dir, \"opencode.json\")): %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("json.Unmarshal(data, &doc): %v", err)
	}
	return doc
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
