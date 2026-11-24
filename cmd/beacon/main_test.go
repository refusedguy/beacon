package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/pkg/core"
)

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
