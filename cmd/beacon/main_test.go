package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/harnesses/acp"
	"github.com/refusedguy/beacon/harnesses/opencode"
	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/internal/infra/workspace"
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

func TestProjectRootPrefersEnvironment(t *testing.T) {
	want := t.TempDir()
	t.Setenv("BEACON_PROJECT_ROOT", want)
	got, err := projectRoot()
	if err != nil {
		t.Fatalf("projectRoot(): %v", err)
	}
	if got != want {
		t.Fatalf("projectRoot() = %q, want %q", got, want)
	}
}

func TestProjectRootFallsBackToWorkingDirectory(t *testing.T) {
	t.Setenv("BEACON_PROJECT_ROOT", "")
	want, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("filepath.Abs(\".\"): %v", err)
	}
	got, err := projectRoot()
	if err != nil {
		t.Fatalf("projectRoot(): %v", err)
	}
	if got != want {
		t.Fatalf("projectRoot() = %q, want %q", got, want)
	}
}

func TestLoadConfigPinsRelativePathsToProjectRoot(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, ".beacon")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(configDir, 0o755): %v", err)
	}
	body := "state_db: .beacon/state.db\nworkspace_dir: .beacon/worktrees\n" +
		"task_source:\n  provider: markdown\n  dir: docs/tasks\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(configDir, \"config.yaml\"), []byte(body), 0o644): %v", err)
	}
	t.Setenv("BEACON_PROJECT_ROOT", root)
	t.Chdir(t.TempDir())

	cfg, gotRoot, err := loadConfig("")
	if err != nil {
		t.Fatalf("loadConfig(\"\"): %v", err)
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
		Provider: "ACP", Command: "fake-agent --acp",
	}}
	h, err := newHarness(cfg, "", "ses_parent", "")
	if err != nil {
		t.Fatalf("newHarness(cfg, \"\", \"ses_parent\", \"\"): %v", err)
	}
	ah, ok := h.(*acp.Harness)
	if !ok {
		t.Fatalf("harness for acp provider = %T, want *acp.Harness", h)
	}
	if ah.Command != "fake-agent --acp" {
		t.Fatalf("acp harness command = %q, want fake-agent --acp", ah.Command)
	}
}

func TestNewHarnessACPRequiresCommand(t *testing.T) {
	cfg := config.Config{Harness: config.Harness{Provider: "acp"}}
	if _, err := newHarness(cfg, "", "", ""); err == nil {
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
		t.Fatalf("wireInfra(cfg, root, \"\"): %v", err)
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
		t.Fatalf("sqlite.Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	infra := &wiredInfra{
		store: store, changes: make(chan int, 1),
		decisions: app.DecisionLogger{Sink: store},
	}
	w := wireServices(cfg, t.TempDir(), "", infra, bareHarness{}, mergePolicy(cfg), nil)
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
	w := wireServices(cfg, t.TempDir(), "", infra, bareHarness{}, mergePolicy(cfg), nil)
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
	w := wireServices(cfg, t.TempDir(), "", infra, bareHarness{}, mergePolicy(cfg), nil)
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
	w := wireServices(cfg, t.TempDir(), "ses_from_env", infra, bareHarness{}, mergePolicy(cfg), nil)
	if w.recon.ParentSessionID != "ses_from_env" {
		t.Fatalf("reconciler parent = %q, want ses_from_env", w.recon.ParentSessionID)
	}
}

func TestWireServicesUsesConfiguredRebaseCap(t *testing.T) {
	cfg := config.Config{BaseBranch: "main", Merge: config.Merge{MaxAutoRebases: 7}}
	infra := &wiredInfra{changes: make(chan int, 1)}
	w := wireServices(cfg, t.TempDir(), "", infra, bareHarness{}, mergePolicy(cfg), nil)
	if w.recon.MaxAutoRebases != 7 {
		t.Fatalf("reconciler max auto rebases = %d, want 7", w.recon.MaxAutoRebases)
	}
}

func TestWirePassesConfiguredParentToHarnessAndServices(t *testing.T) {
	t.Setenv("BEACON_MODEL", "")
	var gets atomic.Int32
	srv := parentModelServer(&gets)
	defer srv.Close()

	path := wireParentModelConfig(t, srv.URL, "ses_from_config")
	w, err := wire(path, "")
	if err != nil {
		t.Fatalf("wire(path, \"\"): %v", err)
	}
	defer w.close()

	if w.dispatcher.ParentSessionID != "ses_from_config" {
		t.Errorf("dispatcher parent = %q, want ses_from_config", w.dispatcher.ParentSessionID)
	}
	if w.recon.ParentSessionID != "ses_from_config" {
		t.Errorf("reconciler parent = %q, want ses_from_config", w.recon.ParentSessionID)
	}
	reloadable, ok := w.harness.(*reloadableHarness)
	if !ok {
		t.Fatalf("wired harness = %T, want *reloadableHarness", w.harness)
	}
	oh, ok := reloadable.current().(*opencode.Harness)
	if !ok {
		t.Fatalf("harness behind wrapper = %T, want *opencode.Harness", reloadable.current())
	}
	if oh.ParentSessionID != "ses_from_config" {
		t.Errorf("harness parent = %q, want ses_from_config", oh.ParentSessionID)
	}
}

func TestWireInjectsReloadableWrappersAtConstruction(t *testing.T) {
	t.Setenv("BEACON_MODEL", "")
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	w, err := wire(path, "")
	if err != nil {
		t.Fatalf("wire(path, \"\"): %v", err)
	}
	defer w.close()

	h, ok := w.harness.(*reloadableHarness)
	if !ok {
		t.Fatalf("w.harness = %T, want *reloadableHarness", w.harness)
	}
	if w.dispatcher.Harness != h {
		t.Error("dispatcher did not get the reloadable harness")
	}
	if w.control.Harness != h {
		t.Error("control did not get the reloadable harness")
	}
	if w.recon.Harness != h {
		t.Error("reconciler did not get the reloadable harness")
	}
	if w.workspace.Refresher != h {
		t.Error("workspace did not get the reloadable refresher")
	}
	p, ok := w.recon.Policy.(*reloadablePolicy)
	if !ok {
		t.Fatalf("reconciler policy = %T, want *reloadablePolicy", w.recon.Policy)
	}
	if h.live != &w.watcher.live || p.live != &w.watcher.live {
		t.Error("wrappers do not follow the config watcher")
	}
	if got := w.dispatcher.ResolveModel(""); got.ID != "model-a" {
		t.Fatalf("initial model = %q, want model-a", got.ID)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(path): %v", err)
	}
	writeWatchFile(t, path, strings.Replace(string(body), "model: model-a", "model: model-b", 1))
	if !w.watcher.reload() {
		t.Fatal("reload did not apply the changed config")
	}
	if got := w.dispatcher.ResolveModel(""); got.ID != "model-b" {
		t.Fatalf("model after reload = %q, want model-b", got.ID)
	}
}

// parentModelServer counts session lookups and returns a model that can only
// come from the HTTP lookup, so a cached resolution is observable.
func parentModelServer(gets *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/session/") {
			gets.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":{"model":{"id":"parent-model","providerID":"opencode","variant":"default"}}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

// wireParentModelConfig writes a config with no model, so resolveModel must
// look up the fixed parent over HTTP.
func wireParentModelConfig(t *testing.T, endpoint, parent string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := fmt.Sprintf("state_db: %s\nworkspace_dir: %s\nbase_branch: main\n"+
		"task_source:\n  provider: markdown\n  dir: %s\n"+
		"scm:\n  provider: github\n  owner: owner\n  repo: repo\n"+
		"harness:\n  provider: opencode\n  endpoint: %s\n  token: %q\n  parent_session_id: %s\n",
		filepath.Join(dir, "state.db"), filepath.Join(dir, "worktrees"), dir, endpoint, "worker-token", parent)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWireDoesNotResolveParentModelOnFirstDispatch(t *testing.T) {
	t.Setenv("BEACON_MODEL", "")
	var gets atomic.Int32
	srv := parentModelServer(&gets)
	defer srv.Close()

	path := wireParentModelConfig(t, srv.URL, "ses_parent")
	w, err := wire(path, "")
	if err != nil {
		t.Fatalf("wire(): %v", err)
	}
	defer w.close()

	startup := gets.Load()
	if startup != 1 {
		t.Fatalf("parent session GETs at startup = %d, want 1", startup)
	}

	want := core.ModelRef{ProviderID: "opencode", ID: "parent-model", Variant: "default"}
	for i := 0; i < 5; i++ {
		if got := w.dispatcher.ResolveModel("ses_parent"); got != want {
			t.Fatalf("dispatch %d model = %+v, want %+v", i, got, want)
		}
	}
	if got := gets.Load(); got != startup {
		t.Fatalf("parent session GETs after five dispatches = %d, want %d", got, startup)
	}
}

func TestWireStillResolvesPerDispatchParentOverride(t *testing.T) {
	t.Setenv("BEACON_MODEL", "")
	var gets atomic.Int32
	srv := parentModelServer(&gets)
	defer srv.Close()

	path := wireParentModelConfig(t, srv.URL, "ses_parent")
	w, err := wire(path, "")
	if err != nil {
		t.Fatalf("wire(): %v", err)
	}
	defer w.close()

	want := core.ModelRef{ProviderID: "opencode", ID: "parent-model", Variant: "default"}
	if got := w.dispatcher.ResolveModel("ses_override"); got != want {
		t.Fatalf("override model = %+v, want %+v", got, want)
	}
	if got := gets.Load(); got != 2 {
		t.Fatalf("parent session GETs = %d, want 2 (startup plus override)", got)
	}
}

// stubTaskSource and stubSCM are identity markers for the wiring test.
type stubTaskSource struct{}

func (stubTaskSource) ListAvailable(context.Context) ([]core.Task, error) { return nil, nil }
func (stubTaskSource) Claim(context.Context, core.TaskID) error           { return nil }
func (stubTaskSource) Complete(context.Context, core.TaskID) error        { return nil }
func (stubTaskSource) MarkInWorkspace(context.Context, core.TaskID, string, string) error {
	return nil
}
func (stubTaskSource) Create(context.Context, core.Task) (core.Task, error) {
	return core.Task{}, nil
}

type stubSCM struct{}

func (stubSCM) Observe(context.Context, int) (core.Observation, error) {
	return core.Observation{}, nil
}
func (stubSCM) UpdateBranch(context.Context, int, string) (string, error) {
	return "", nil
}
func (stubSCM) Merge(context.Context, int, string, string) error { return nil }
func (stubSCM) BaseBranchStatus(context.Context, string) (core.BaseStatus, error) {
	return core.BaseStatus{}, nil
}
func (stubSCM) FindOpenPRByBranch(context.Context, string) (int, error)     { return 0, nil }
func (stubSCM) OpenPR(context.Context, string, string, string) (int, error) { return 0, nil }

func TestBuildInfraRunsConstructorsConcurrently(t *testing.T) {
	const stepDelay = 100 * time.Millisecond
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("sqlite.Open(...): %v", err)
	}
	defer store.Close()

	source := &stubTaskSource{}
	scmGW := &stubSCM{}
	h := bareHarness{}
	wt := new(workspace.GitWorktree)

	var active, peak int32
	enter := func() {
		now := atomic.AddInt32(&active, 1)
		for {
			seen := atomic.LoadInt32(&peak)
			if now <= seen || atomic.CompareAndSwapInt32(&peak, seen, now) {
				break
			}
		}
		time.Sleep(stepDelay)
		atomic.AddInt32(&active, -1)
	}

	start := time.Now()
	infra, err := buildInfra(infraConstructors{
		store:     func() (*sqlite.Store, error) { enter(); return store, nil },
		source:    func() (core.TaskSource, error) { enter(); return source, nil },
		scm:       func() (core.ScmGateway, error) { enter(); return scmGW, nil },
		harness:   func() (core.AgentHarness, error) { enter(); return h, nil },
		workspace: func() (*workspace.GitWorktree, error) { enter(); return wt, nil },
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("buildInfra(): %v", err)
	}
	if got := atomic.LoadInt32(&peak); got < 2 {
		t.Errorf("peak concurrent constructors = %d, want more than one", got)
	}
	if elapsed >= 4*stepDelay {
		t.Errorf("buildInfra took %v, want near %v, not serial %v", elapsed, stepDelay, 5*stepDelay)
	}

	if infra.store != store {
		t.Error("store did not come from its constructor")
	}
	if infra.source != source {
		t.Error("source did not come from its constructor")
	}
	if infra.scm != scmGW {
		t.Error("scm did not come from its constructor")
	}
	if infra.harness != h {
		t.Error("harness did not come from its constructor")
	}
	if infra.workspace != wt {
		t.Error("workspace did not come from its constructor")
	}
	if infra.changes == nil || cap(infra.changes) != 64 {
		t.Errorf("changes channel = %v, want buffered cap 64", infra.changes)
	}
	if infra.decisions.Sink != store {
		t.Error("decision sink is not the store")
	}
}

func TestBuildInfraReturnsErrorInFixedOrder(t *testing.T) {
	storeErr := errors.New("store failed")
	sourceErr := errors.New("source failed")

	infra, err := buildInfra(infraConstructors{
		store: func() (*sqlite.Store, error) {
			time.Sleep(30 * time.Millisecond)
			return nil, storeErr
		},
		source: func() (core.TaskSource, error) {
			time.Sleep(20 * time.Millisecond)
			return nil, sourceErr
		},
		scm:       func() (core.ScmGateway, error) { return nil, nil },
		harness:   func() (core.AgentHarness, error) { return nil, nil },
		workspace: func() (*workspace.GitWorktree, error) { return nil, errors.New("workspace failed") },
	})
	if infra != nil {
		t.Fatalf("buildInfra() = %v, want nil on failure", infra)
	}
	if !errors.Is(err, storeErr) {
		t.Fatalf("buildInfra() error = %v, want store error first in fixed order", err)
	}
}

// narrowControlStore implements only the methods Control calls. The assertion
// fails to compile if ControlStore ever carries a method Control does not use.
type narrowControlStore struct{}

func (narrowControlStore) Save(context.Context, *core.Run) error                 { return nil }
func (narrowControlStore) Get(context.Context, core.RunID) (*core.Run, error)    { return nil, nil }
func (narrowControlStore) FindByPR(context.Context, int) (*core.Run, error)      { return nil, nil }
func (narrowControlStore) SetApproval(context.Context, core.RunID, string) error { return nil }

var (
	_ app.ControlStore = narrowControlStore{}
	_ app.ControlStore = (*sqlite.Store)(nil)
)

func TestWireServicesPassesRealStoreToControl(t *testing.T) {
	cfg := config.Config{BaseBranch: "main"}
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("sqlite.Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	infra := &wiredInfra{store: store, changes: make(chan int, 1)}
	w := wireServices(cfg, t.TempDir(), "", infra, bareHarness{}, mergePolicy(cfg), nil)
	if w.control.Store != store {
		t.Error("control is missing the real store")
	}
}
