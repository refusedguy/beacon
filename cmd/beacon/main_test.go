package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/refusedguy/beacon/harnesses/acp"
	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/pkg/core"
)

type bareHarness struct{}

func (bareHarness) Spawn(context.Context, core.SpawnRequest) (core.Session, error) {
	return core.Session{}, nil
}
func (bareHarness) Wake(context.Context, string, core.WakeContext) error { return nil }
func (bareHarness) Stop(context.Context, string) error                   { return nil }
func (bareHarness) IsAlive(context.Context, string) (bool, error)        { return false, nil }
func (bareHarness) NotifyParent(context.Context, string, string) error   { return nil }
func (bareHarness) Events() <-chan core.HarnessEvent                     { return nil }

type portHarness struct {
	bareHarness
	repo   string
	models map[string]string
}

func (h *portHarness) SetRepo(root string) { h.repo = root }

func (h *portHarness) ResolveModel(parent string) map[string]string {
	return map[string]string{"parent": parent, "id": h.models["id"]}
}

func (h *portHarness) WatchIdle(context.Context) {}

func TestSetHarnessRepoUsesPort(t *testing.T) {
	h := &portHarness{}
	setHarnessRepo(h, "/workspace/repo")
	if h.repo != "/workspace/repo" {
		t.Fatalf("repo = %q, want /workspace/repo", h.repo)
	}
	setHarnessRepo(bareHarness{}, "/workspace/repo")
}

func TestProjectRootPrefersEnvironment(t *testing.T) {
	want := t.TempDir()
	t.Setenv("BEACON_PROJECT_ROOT", want)
	got, err := projectRoot()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("projectRoot() = %q, want %q", got, want)
	}
}

func TestProjectRootFallsBackToWorkingDirectory(t *testing.T) {
	t.Setenv("BEACON_PROJECT_ROOT", "")
	want, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	got, err := projectRoot()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("projectRoot() = %q, want %q", got, want)
	}
}

func TestLoadConfigPinsRelativePathsToProjectRoot(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, ".beacon")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "state_db: .beacon/state.db\nworkspace_dir: .beacon/worktrees\n" +
		"task_source:\n  provider: markdown\n  dir: docs/tasks\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEACON_PROJECT_ROOT", root)
	t.Chdir(t.TempDir())

	cfg, gotRoot, err := loadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != root {
		t.Fatalf("root = %q, want %q", gotRoot, root)
	}
	for name, got := range map[string]string{
		"state_db": cfg.StateDB, "workspace_dir": cfg.WorkspaceDir, "task dir": cfg.TaskSource.Dir,
	} {
		want := filepath.Join(root, map[string]string{
			"state_db": ".beacon/state.db", "workspace_dir": ".beacon/worktrees", "task dir": "docs/tasks",
		}[name])
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestNewHarnessBuildsACPProvider(t *testing.T) {
	cfg := config.Config{Harness: config.Harness{
		Provider: "ACP", Command: "fake-agent --acp", Model: "sonnet",
	}}
	h, err := newHarness(cfg, "", "ses_parent")
	if err != nil {
		t.Fatal(err)
	}
	ah, ok := h.(*acp.Harness)
	if !ok {
		t.Fatalf("harness for acp provider = %T, want *acp.Harness", h)
	}
	if ah.Command != "fake-agent --acp" || ah.Model != "sonnet" || ah.ParentSessionID != "ses_parent" {
		t.Fatalf("acp harness = %+v, want command, model and parent from config", ah)
	}
	if _, ok := h.(core.ModelResolver); ok {
		t.Error("acp harness must not claim the model resolver port")
	}
}

func TestNewHarnessACPRequiresCommand(t *testing.T) {
	cfg := config.Config{Harness: config.Harness{Provider: "acp"}}
	if _, err := newHarness(cfg, "", ""); err == nil {
		t.Fatal("acp harness without a command should fail")
	}
}

func TestResolveModelUsesConfiguredHarness(t *testing.T) {
	t.Setenv("BEACON_MODEL", "")
	acpCfg := config.Config{Harness: config.Harness{Provider: "acp", Command: "fake-agent"}}
	if got := resolveModel(acpCfg, "", "ses_parent"); !got.IsZero() {
		t.Fatalf("model for acp = %+v, want zero value", got)
	}
	if got := resolveModel(config.Config{Harness: config.Harness{Provider: "acp"}}, "", ""); !got.IsZero() {
		t.Fatalf("model for invalid config = %+v, want zero value", got)
	}
}

func TestResolveModelPrefersEnvironmentOverConfig(t *testing.T) {
	t.Setenv("BEACON_MODEL", "env-model")
	cfg := config.Config{}
	cfg.Harness.Model = "config-model"
	got := resolveModel(cfg, "", "")
	want := core.ModelRef{ProviderID: "opencode", ID: "env-model"}
	if got != want {
		t.Fatalf("resolveModel() = %+v, want %+v", got, want)
	}
}

func TestResolveModelUsesConfiguredModel(t *testing.T) {
	t.Setenv("BEACON_MODEL", "")
	cfg := config.Config{}
	cfg.Harness.Model = "config-model"
	got := resolveModel(cfg, "", "")
	want := core.ModelRef{ProviderID: "opencode", ID: "config-model"}
	if got != want {
		t.Fatalf("resolveModel() = %+v, want %+v", got, want)
	}
}

func TestResolveModelIsZeroWithoutSource(t *testing.T) {
	t.Setenv("BEACON_MODEL", "")
	if got := resolveModel(config.Config{}, "", ""); !got.IsZero() {
		t.Fatalf("resolveModel() = %+v, want zero value", got)
	}
}

func TestResolveModelReadsParentSession(t *testing.T) {
	t.Setenv("BEACON_MODEL", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/session/ses_parent" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"model":{"id":"hy4-flash-free","providerID":"opencode","variant":"default"}}}`)
	}))
	defer srv.Close()
	cfg := config.Config{}
	cfg.Harness.Endpoint = srv.URL
	got := resolveModel(cfg, "token", "ses_parent")
	want := core.ModelRef{ProviderID: "opencode", ID: "hy4-flash-free", Variant: "default"}
	if got != want {
		t.Fatalf("resolveModel() = %+v, want %+v", got, want)
	}
}

func TestWireInfraDoesNotRefreshWorktreesAtStartup(t *testing.T) {
	t.Setenv("BEACON_MODEL", "")
	root := t.TempDir()
	cmd := exec.Command("git", "-C", root, "init", "--initial-branch=main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	var mu sync.Mutex
	refreshes, reloads := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/project":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "prj_startup", "canonical": root}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/worktree/refresh":
			mu.Lock()
			refreshes++
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/location/reload":
			mu.Lock()
			reloads++
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tmp := t.TempDir()
	cfg := config.Config{
		StateDB:      filepath.Join(tmp, "state.db"),
		WorkspaceDir: filepath.Join(tmp, "worktrees"),
		BaseBranch:   "main",
		TaskSource:   config.TaskSource{Provider: "markdown", Dir: tmp},
		SCM:          config.SCM{Provider: "github", Owner: "owner", Repo: "repo"},
		Harness:      config.Harness{Provider: "opencode", Endpoint: srv.URL},
	}
	infra, err := wireInfra(cfg, root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer infra.store.Close()

	mu.Lock()
	defer mu.Unlock()
	if refreshes != 0 {
		t.Fatalf("startup worktree refresh calls = %d, want 0", refreshes)
	}
	if reloads != 0 {
		t.Fatalf("startup location reloads = %d, want 0", reloads)
	}
}

func TestWireServicesInjectsDecisionSink(t *testing.T) {
	cfg := config.Config{BaseBranch: "main"}
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	infra := &wiredInfra{
		store: store, changes: make(chan int, 1),
		decisions: app.DecisionLogger{Sink: store},
	}
	w := wireServices(cfg, t.TempDir(), "", infra)
	if w.dispatcher.Decisions.Sink != store {
		t.Error("dispatcher is missing the decision sink")
	}
	if w.recon.Decisions.Sink != store {
		t.Error("reconciler is missing the decision sink")
	}
}

func TestWireServicesConfiguresTaskWriters(t *testing.T) {
	cfg := config.Config{BaseBranch: "main"}
	infra := &wiredInfra{changes: make(chan int, 1)}
	w := wireServices(cfg, t.TempDir(), "", infra)
	if w.taskCreator == nil || w.taskDeleter == nil || w.taskUpdater == nil {
		t.Fatalf("task writers: creator=%v deleter=%v updater=%v",
			w.taskCreator, w.taskDeleter, w.taskUpdater)
	}
	if w.taskCreator.Store != infra.store {
		t.Error("task creator is missing the store")
	}
	if w.taskDeleter.Root.Branch != "main" || w.taskUpdater.Root.Branch != "main" {
		t.Error("task writers did not inherit the base branch")
	}
}

func TestWireServicesReconcilerUsesConfigParentWhenEnvironmentEmpty(t *testing.T) {
	cfg := config.Config{
		BaseBranch: "main",
		Harness:    config.Harness{ParentSessionID: "ses_from_config"},
	}
	infra := &wiredInfra{changes: make(chan int, 1)}
	w := wireServices(cfg, t.TempDir(), "", infra)
	if w.recon.ParentSessionID != "ses_from_config" {
		t.Fatalf("reconciler parent = %q, want ses_from_config", w.recon.ParentSessionID)
	}
	if w.dispatcher.ParentSessionID != "ses_from_config" {
		t.Fatalf("dispatcher parent = %q, want ses_from_config", w.dispatcher.ParentSessionID)
	}
}

func TestWireServicesReconcilerPrefersEnvironmentParent(t *testing.T) {
	cfg := config.Config{
		BaseBranch: "main",
		Harness:    config.Harness{ParentSessionID: "ses_from_config"},
	}
	infra := &wiredInfra{changes: make(chan int, 1)}
	w := wireServices(cfg, t.TempDir(), "ses_from_env", infra)
	if w.recon.ParentSessionID != "ses_from_env" {
		t.Fatalf("reconciler parent = %q, want ses_from_env", w.recon.ParentSessionID)
	}
}
