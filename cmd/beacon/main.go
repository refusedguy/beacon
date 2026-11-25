// Command beacon is the composition root of the controller.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/refusedguy/beacon/harnesses/acp"
	"github.com/refusedguy/beacon/harnesses/opencode"
	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/internal/infra/harness"
	"github.com/refusedguy/beacon/internal/infra/notify"
	"github.com/refusedguy/beacon/internal/infra/planner"
	"github.com/refusedguy/beacon/internal/infra/policy"
	"github.com/refusedguy/beacon/internal/infra/scm"
	"github.com/refusedguy/beacon/internal/infra/shared"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/internal/infra/tasksource"
	"github.com/refusedguy/beacon/internal/infra/workspace"
	"github.com/refusedguy/beacon/pkg/core"
)

type wiring struct {
	cfg         config.Config
	store       *sqlite.Store
	source      core.TaskSource
	harness     core.AgentHarness
	workspace   *workspace.GitWorktree
	scm         core.ScmGateway
	policy      core.MergePolicy
	changes     chan int
	dispatcher  *app.Dispatcher
	control     *app.Control
	status      app.StatusService
	wait        app.WaitService
	taskCreator *app.TaskCreator
	taskDeleter *app.TaskDeleter
	taskUpdater *app.TaskUpdater
	recon       *app.Reconciler
	reconcile   *app.ReconcileService
	watcher     *configWatcher
}

// wiredInfra holds the adapters shared by every application service.
type wiredInfra struct {
	store     *sqlite.Store
	source    core.TaskSource
	harness   core.AgentHarness
	workspace *workspace.GitWorktree
	scm       core.ScmGateway
	changes   chan int
	decisions app.DecisionLogger
}

// The store adapter must satisfy each consumer-side port before any service is
// built. A missing method fails the build at the composition root.
var (
	_ app.StatusStore     = (*sqlite.Store)(nil)
	_ app.ReconcileStore  = (*sqlite.Store)(nil)
	_ app.WaitStore       = (*sqlite.Store)(nil)
	_ app.DispatchStore   = (*sqlite.Store)(nil)
	_ app.ReconcilerStore = (*sqlite.Store)(nil)
	_ core.RunStore       = (*sqlite.Store)(nil)
)

// projectRoot prefers the explicit environment root. The pooled MCP daemon is
// started with a cwd, but a caller can pin the root without changing that cwd.
func projectRoot() (string, error) {
	if fromEnv := os.Getenv("BEACON_PROJECT_ROOT"); fromEnv != "" {
		return filepath.Abs(fromEnv)
	}
	return filepath.Abs(".")
}

// withRoot resolves a path from the config against the project root. Absolute
// paths are already pinned and stay as they are.
func withRoot(root, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

// loadConfig reads the config and pins every relative filesystem path to the
// project root. A pooled MCP process can start from a parent directory, and
// without this it would open another checkout's database and worktrees.
func loadConfig(path string) (config.Config, string, error) {
	root, err := projectRoot()
	if err != nil {
		return config.Config{}, "", err
	}
	if path == "" {
		path = filepath.Join(root, ".beacon", "config.yaml")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, "", err
	}
	cfg.StateDB = withRoot(root, cfg.StateDB)
	cfg.WorkspaceDir = withRoot(root, cfg.WorkspaceDir)
	if cfg.TaskSource.Provider == "markdown" || cfg.TaskSource.Provider == "" {
		cfg.TaskSource.Dir = withRoot(root, cfg.TaskSource.Dir)
	}
	return cfg, root, nil
}

// resolvedConfigPath returns the file the watcher must observe. The empty
// flag means the default config under the pinned project root.
func resolvedConfigPath(path, root string) string {
	if path == "" {
		return filepath.Join(root, ".beacon", "config.yaml")
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func wire(path, parentSession string) (*wiring, error) {
	cfg, root, err := loadConfig(path)
	if err != nil {
		return nil, err
	}
	infra, err := wireInfra(cfg, root, parentSession)
	if err != nil {
		return nil, err
	}
	cw := newConfigWatcher(resolvedConfigPath(path, root), root, parentSession,
		newLiveState(cfg, infra.harness), configWatchInterval)
	h := &reloadableHarness{live: &cw.live, events: infra.harness.Events()}
	p := &reloadablePolicy{live: &cw.live}
	w := wireServices(cfg, root, parentSession, infra, h, p, cw.modelResolver)
	w.watcher = cw
	return w, nil
}

// wireInfra builds the adapters. It has no dependency on the service layer.
func wireInfra(cfg config.Config, root, parentSession string) (*wiredInfra, error) {
	scmToken, sourceToken, harnessToken := loadTokens(cfg)
	store, err := sqlite.Open(cfg.StateDB)
	if err != nil {
		return nil, err
	}
	source, err := tasksource.New(tasksource.Config{
		Provider: cfg.TaskSource.Provider, Dir: cfg.TaskSource.Dir,
		Owner: cfg.SCM.Owner, Repo: cfg.SCM.Repo, GitlabURL: cfg.SCM.BaseURL,
	}, sourceToken)
	if err != nil {
		return nil, err
	}
	scmGW, err := scm.New(scm.Config{
		Provider: cfg.SCM.Provider, Owner: cfg.SCM.Owner, Repo: cfg.SCM.Repo,
		BaseURL: cfg.SCM.BaseURL, BaseBranch: cfg.BaseBranch,
	}, scmToken)
	if err != nil {
		return nil, err
	}
	h, err := newHarness(cfg, harnessToken, parentSession, root)
	if err != nil {
		return nil, err
	}
	wt, err := workspace.New(root, cfg.BaseBranch, cfg.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	infra := &wiredInfra{store: store, source: source, harness: h,
		workspace: wt, scm: scmGW, changes: make(chan int, 64),
		decisions: app.DecisionLogger{Sink: store}}
	return infra, nil
}

// wireServices builds the application services on top of the adapters. The
// reloadable wrappers are injected here so no service is built with an
// original adapter and patched afterwards.
func wireServices(cfg config.Config, root, parentSession string, infra *wiredInfra, h core.AgentHarness, p core.MergePolicy, resolveModel app.ModelResolver) *wiring {
	workspaceRoot := core.Workspace{Path: root, Branch: cfg.BaseBranch}
	if refresher, ok := h.(core.WorktreeRefresher); ok {
		infra.workspace.Refresher = refresher
	}
	creator := &app.TaskCreator{
		Store: infra.store, Source: infra.source,
		Workspace: infra.workspace, Root: workspaceRoot,
	}
	deleter := &app.TaskDeleter{
		Store: infra.store, Workspace: infra.workspace, Root: workspaceRoot,
	}
	updater := &app.TaskUpdater{
		Store: infra.store, Workspace: infra.workspace, Root: workspaceRoot,
	}
	disp := &app.Dispatcher{
		Source:    infra.source,
		Planner:   planner.New(planner.Options{ByPath: true, ByDependency: true, Strict: cfg.StrictScope}),
		Workspace: infra.workspace, Harness: h, Store: infra.store,
		Concurrency:        core.Concurrency{MaxInFlight: cfg.Concurrency.MaxInFlight, MaxActiveAgents: cfg.Concurrency.MaxActiveAgents},
		StrictScope:        cfg.StrictScope,
		MaxAttempts:        cfg.Merge.MaxAttempts,
		ParentSessionID:    firstNonEmpty(parentSession, cfg.Harness.ParentSessionID),
		WorkerInstructions: cfg.Harness.WorkerInstructions,
		ResolveModel:       resolveModel,
		Decisions:          infra.decisions,
	}
	ctrl := &app.Control{Store: infra.store, Harness: h, Workspace: infra.workspace, OnChange: func(pr int) {
		select {
		case infra.changes <- pr:
		default:
		}
	}}
	recon := newReconciler(cfg, infra.store, infra.scm, h, p,
		infra.source, infra.workspace, disp, infra.changes,
		firstNonEmpty(parentSession, cfg.Harness.ParentSessionID))
	recon.Decisions = infra.decisions
	return &wiring{
		cfg: cfg, store: infra.store, source: infra.source, harness: h,
		workspace: infra.workspace, scm: infra.scm, policy: p,
		changes: infra.changes, dispatcher: disp, control: ctrl,
		status:      app.StatusService{Store: infra.store},
		wait:        app.WaitService{Store: infra.store, Interval: time.Second},
		taskCreator: creator, taskDeleter: deleter, taskUpdater: updater, recon: recon,
		reconcile: &app.ReconcileService{Reconciler: recon, Store: infra.store},
	}
}

func mergePolicy(cfg config.Config) core.MergePolicy {
	return policy.New(policy.Config{
		RequiredChecks: cfg.Merge.RequiredChecks, RequiredApprovals: cfg.Merge.RequiredApprovals,
		Labels: cfg.Merge.Labels, CommandGate: cfg.Merge.CommandGate,
	})
}

func newReconciler(
	cfg config.Config,
	store *sqlite.Store,
	scmGW core.ScmGateway,
	h core.AgentHarness,
	p core.MergePolicy,
	source core.TaskSource,
	wt *workspace.GitWorktree,
	disp *app.Dispatcher,
	changes chan int,
	parentSession string,
) *app.Reconciler {
	return &app.Reconciler{
		Store: store, SCM: scmGW, Policy: p, Harness: h,
		Source: source, Workspace: wt,
		Notifier:   notify.NewTelegram(cfg.Notify.TelegramToken, cfg.Notify.ChatID),
		Dispatcher: disp, Changes: changes, Repo: cfg.SCM.Owner + "/" + cfg.SCM.Repo,
		BaseBranch: cfg.BaseBranch, BaseRedMode: cfg.OnBaseRed.Mode,
		MaxFixTries: cfg.OnBaseRed.MaxFixAttempts, ParentSessionID: parentSession,
		Interval:   cfg.Interval(),
		AutoRebase: cfg.Merge.AutoRebase,
	}
}

func loadTokens(cfg config.Config) (scmToken, sourceToken, harnessToken string) {
	scmToken = firstNonEmpty(os.Getenv(cfg.SCM.TokenEnv), cfg.SCM.Token)
	sourceToken = os.Getenv(cfg.TaskSource.TokenEnv)
	if sourceToken == "" {
		sourceToken = scmToken
	}
	harnessToken = firstNonEmpty(os.Getenv(cfg.Harness.TokenEnv), cfg.Harness.Token)
	return
}

func newHarness(cfg config.Config, token, parent, root string) (core.AgentHarness, error) {
	parent = firstNonEmpty(parent, cfg.Harness.ParentSessionID)
	switch strings.ToLower(cfg.Harness.Provider) {
	case "opencode", "":
		h := opencode.New(opencode.Config{
			Endpoint:        cfg.Harness.Endpoint,
			Repo:            root,
			Model:           cfg.Harness.Model,
			Agent:           cfg.Harness.Command,
			Token:           token,
			ParentSessionID: parent,
		})
		h.ModelRef = resolveModel(cfg, token, parent)
		return h, nil
	case "acp":
		if strings.TrimSpace(cfg.Harness.Command) == "" {
			return nil, fmt.Errorf("acp harness requires command")
		}
		return acp.NewFromConfig(acp.Config{
			Command:         cfg.Harness.Command,
			Model:           cfg.Harness.Model,
			ParentSessionID: parent,
		}), nil
	default:
		return harness.New(harness.Config{
			Provider: cfg.Harness.Provider, Command: cfg.Harness.Command,
			PromptTemplate: cfg.Harness.PromptTemplate,
		})
	}
}

// resolveModel uses the configured model, then BEACON_MODEL, then the model
// of the orchestrator session so workers run with the user's current choice.
func resolveModel(cfg config.Config, token, parent string) core.ModelRef {
	if v := os.Getenv("BEACON_MODEL"); v != "" {
		return core.ModelRef{ProviderID: "opencode", ID: v}
	}
	if cfg.Harness.Model != "" {
		return core.ModelRef{ProviderID: "opencode", ID: cfg.Harness.Model}
	}
	if parent == "" || token == "" || cfg.Harness.Endpoint == "" {
		return core.ModelRef{}
	}
	endpoint := strings.TrimRight(cfg.Harness.Endpoint, "/") + "/api/session/" + url.PathEscape(parent)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return core.ModelRef{}
	}
	if user, pass, ok := strings.Cut(token, ":"); ok {
		req.SetBasicAuth(user, pass)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return core.ModelRef{}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return core.ModelRef{}
	}
	var info struct {
		Data struct {
			Model struct {
				ID         string `json:"id"`
				ProviderID string `json:"providerID"`
				Variant    string `json:"variant"`
			} `json:"model"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.Data.Model.ID == "" {
		return core.ModelRef{}
	}
	return core.ModelRef{
		ProviderID: info.Data.Model.ProviderID,
		ID:         info.Data.Model.ID,
		Variant:    info.Data.Model.Variant,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (w *wiring) close() { _ = w.store.Close() }

func main() {
	slog.SetDefault(shared.NewLogger(os.Getenv("BEACON_LOG_LEVEL")))
	root := &cobra.Command{Use: "beacon", Short: "Parallel AI agent controller"}
	configPath := root.PersistentFlags().String("config", "", "path to config.yaml")

	root.AddCommand(newInitCommand())
	root.AddCommand(newDoctorCommand(configPath))
	root.AddCommand(newDispatchCommand(configPath))
	root.AddCommand(newStatusCommand(configPath))
	root.AddCommand(newControlCommand(configPath))
	root.AddCommand(newMCPCommand(configPath))
	root.AddCommand(newServeCommand(configPath))
	root.AddCommand(newGCCommand(configPath))

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
