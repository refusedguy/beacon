// Command beacon is the composition root of the controller.
package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"golang.org/x/sync/errgroup"

	_ "modernc.org/sqlite" // register the "sqlite" driver for the store adapter

	"github.com/refusedguy/beacon/harnesses/acp"
	"github.com/refusedguy/beacon/harnesses/opencode"
	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/checksource"
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
	explain     app.ExplainService
	taskCreator *app.TaskCreator
	taskDeleter *app.TaskDeleter
	taskUpdater *app.TaskUpdater
	recon       *app.Reconciler
	reconcile   *app.ReconcileService
	watcher     *configWatcher
}

// refreshHarness is the harness surface the composition root injects into the
// workspace. Composing refresh with the base port makes the capability part of
// the injected dependency instead of a runtime probe at injection.
type refreshHarness interface {
	core.AgentHarness
	core.WorktreeRefresher
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
	if fromEnv := envOr("BEACON_PROJECT_ROOT", ""); fromEnv != "" {
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
	abs, err := filepath.Abs(path)
	if err != nil {
		logDiscard("resolve config path", err)
		return path
	}
	return abs
}

func wire(path, parentSession string) (*wiring, error) {
	cfg, root, err := loadConfig(path)
	if err != nil {
		return nil, err
	}
	parentSession = coalesce(parentSession, cfg.Harness.ParentSessionID)
	infra, err := wireInfra(cfg, root, parentSession)
	if err != nil {
		return nil, err
	}
	cw := newConfigWatcher(resolvedConfigPath(path, root), root, parentSession,
		newLiveState(cfg, infra.harness), configWatchInterval)
	seedParentModel(cw, parentSession, startupModel(infra.harness, cfg, parentSession))
	h := &reloadableHarness{live: &cw.live, events: infra.harness.Events()}
	p := &reloadablePolicy{live: &cw.live}
	w := wireServices(cfg, root, parentSession, infra, h, p, cw.modelResolver)
	w.watcher = cw
	return w, nil
}

// startupModel returns the model already resolved for the fixed parent when
// the harness was built. The OpenCode harness resolves it during wireInfra, so
// reusing that value keeps the parent session GET off the first dispatch.
// Providers without a harness-level model resolve it here instead.
func startupModel(h core.AgentHarness, cfg config.Config, parent string) core.ModelRef {
	if oh, ok := h.(*opencode.Harness); ok {
		return oh.ModelRef
	}
	_, _, token := loadTokens(cfg)
	return resolveModel(cfg, token, parent)
}

// seedParentModel primes the watcher's resolver cache with the model resolved at
// composition time. The first dispatch for the fixed parent reuses it instead
// of paying a parent session GET. Other parents still resolve through the
// watcher, and a config reload advances the generation so the cache refreshes.
func seedParentModel(cw *configWatcher, parent string, ref core.ModelRef) {
	state := cw.live.Load()
	cw.modelMu.Lock()
	defer cw.modelMu.Unlock()
	cw.resetModelCacheIfStale(state)
	cw.cacheModel(parent, ref)
}

// wireInfra builds the adapters. It has no dependency on the service layer.
// The constructors do not depend on each other, so they run concurrently and
// time to first API is the slowest one rather than their sum.
func wireInfra(cfg config.Config, root, parentSession string) (*wiredInfra, error) {
	scmToken, sourceToken, harnessToken := loadTokens(cfg)
	return buildInfra(infraConstructors{
		store: func() (*sqlite.Store, error) {
			return sqlite.Open(cfg.StateDB)
		},
		source: func() (core.TaskSource, error) {
			return tasksource.New(tasksource.Config{
				Provider: cfg.TaskSource.Provider, Dir: cfg.TaskSource.Dir,
				Owner: cfg.SCM.Owner, Repo: cfg.SCM.Repo, GitlabURL: cfg.SCM.BaseURL,
			}, sourceToken)
		},
		scm: func() (core.ScmGateway, error) {
			return scm.New(scm.Config{
				Provider: cfg.SCM.Provider, Owner: cfg.SCM.Owner, Repo: cfg.SCM.Repo,
				BaseURL: cfg.SCM.BaseURL, BaseBranch: cfg.BaseBranch,
			}, scmToken)
		},
		harness: func() (core.AgentHarness, error) {
			return newHarness(cfg, harnessToken, parentSession, root)
		},
		workspace: func() (*workspace.GitWorktree, error) {
			return workspace.New(root, cfg.BaseBranch, cfg.WorkspaceDir)
		},
	})
}

// infraConstructors holds the independent adapter constructors used at daemon
// startup. Each closure captures the config it needs, so buildInfra can run
// them concurrently without changing what the composition root wires.
type infraConstructors struct {
	store     func() (*sqlite.Store, error)
	source    func() (core.TaskSource, error)
	scm       func() (core.ScmGateway, error)
	harness   func() (core.AgentHarness, error)
	workspace func() (*workspace.GitWorktree, error)
}

// startupParallelLimit bounds the goroutines that build independent adapters.
const startupParallelLimit = 5

// buildInfra runs the adapter constructors under a bounded errgroup and
// assembles the shared infrastructure. Each result lands in a fixed slot, so
// the returned error is deterministic even when several constructors fail out
// of order.
func buildInfra(c infraConstructors) (*wiredInfra, error) {
	var (
		store        *sqlite.Store
		source       core.TaskSource
		scmGW        core.ScmGateway
		h            core.AgentHarness
		wt           *workspace.GitWorktree
		errStore     error
		errSource    error
		errSCM       error
		errHarness   error
		errWorkspace error
	)

	var group errgroup.Group
	group.SetLimit(startupParallelLimit)
	group.Go(func() error { store, errStore = c.store(); return errStore })
	group.Go(func() error { source, errSource = c.source(); return errSource })
	group.Go(func() error { scmGW, errSCM = c.scm(); return errSCM })
	group.Go(func() error { h, errHarness = c.harness(); return errHarness })
	group.Go(func() error { wt, errWorkspace = c.workspace(); return errWorkspace })
	// Wait's aggregate error is order-dependent. The slots below are inspected
	// in a fixed order instead so failures stay reproducible. The aggregate is
	// still logged so a constructor failure is never invisible.
	logDiscard("wait for infra constructors", group.Wait())

	for _, err := range []error{errStore, errSource, errSCM, errHarness, errWorkspace} {
		if err != nil {
			if store != nil {
				_ = store.Close()
			}
			return nil, err
		}
	}

	return &wiredInfra{
		store: store, source: source, harness: h, workspace: wt, scm: scmGW,
		changes: make(chan int, 64), decisions: app.DecisionLogger{Sink: store},
	}, nil
}

// wireServices builds the application services on top of the adapters. The
// reloadable wrappers are injected here so no service is built with an
// original adapter and patched afterwards.
func wireServices(cfg config.Config, root, parentSession string, infra *wiredInfra, h refreshHarness, p app.ReconcilerPolicy, resolveModel app.ModelResolver) *wiring {
	// wire passes the resolved parent, but direct callers may supply only the
	// environment value. Resolve once here so both services agree on it.
	parentSession = coalesce(parentSession, cfg.Harness.ParentSessionID)
	workspaceRoot := core.Workspace{Path: root, Branch: cfg.BaseBranch}
	if infra.workspace != nil {
		infra.workspace.Refresher = h
	}
	creator := &app.TaskCreator{
		Store: infra.store, Source: infra.source,
		Workspace: infra.workspace, Root: workspaceRoot,
	}
	deleter := &app.TaskDeleter{
		Store: infra.store, Source: infra.source, Workspace: infra.workspace, Root: workspaceRoot,
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
		ParentSessionID:    parentSession,
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
	recon := newReconciler(app.ReconcilerConfig{
		Notifier:           notify.NewTelegram(cfg.Notify.TelegramToken, cfg.Notify.ChatID),
		Repo:               cfg.RepoSlug(),
		BaseBranch:         cfg.BaseBranch,
		ParentSession:      parentSession,
		BaseRedMode:        cfg.OnBaseRed.Mode,
		WorkerInstructions: cfg.Harness.WorkerInstructions,
		MaxFixTries:        cfg.OnBaseRed.MaxFixAttempts,
		MaxAttempts:        cfg.Merge.MaxAttempts,
		Interval:           cfg.Interval(),
		OrphanTimeout:      cfg.OrphanTimeoutDuration(),
		AutoRebase:         cfg.Merge.AutoRebase,
		MaxAutoRebases:     cfg.Merge.MaxAutoRebases,
	}, infra.store, infra.scm, h, p, infra.source, infra.workspace, disp, infra.changes)
	recon.Decisions = infra.decisions
	explain := app.ExplainService{Store: infra.store, SCM: infra.scm}
	if traceable, ok := p.(app.ExplainPolicy); ok {
		explain.Policy = traceable
	}
	return &wiring{
		cfg: cfg, store: infra.store, source: infra.source, harness: h,
		workspace: infra.workspace, scm: infra.scm, policy: p,
		changes: infra.changes, dispatcher: disp, control: ctrl,
		status:      app.StatusService{Store: infra.store},
		wait:        app.WaitService{Store: infra.store, Interval: time.Second},
		explain:     explain,
		taskCreator: creator, taskDeleter: deleter, taskUpdater: updater, recon: recon,
		reconcile: &app.ReconcileService{Reconciler: recon, Store: infra.store},
	}
}

func mergePolicy(cfg config.Config) app.ReconcilerPolicy {
	// Required checks reach policy only through the source port. Config load
	// resolves workflow job names into cfg.Merge.RequiredChecks in workflow
	// mode, and the manual list is the override the source returns on both the
	// initial load and every watcher reload. SCM adapters can add a fallback
	// source later without a policy change.
	source := checksource.Config{Explicit: cfg.Merge.RequiredChecks}
	return policy.New(policy.Config{
		RequiredCheckSource: source, RequiredApprovals: cfg.Merge.RequiredApprovals,
		Labels: cfg.Merge.Labels, CommandGate: cfg.Merge.CommandGate,
	})
}

func newReconciler(
	cfg app.ReconcilerConfig,
	store core.RunStore,
	scmGW core.ScmGateway,
	h core.AgentHarness,
	p app.ReconcilerPolicy,
	source core.TaskSource,
	wt core.WorkspaceManager,
	disp *app.Dispatcher,
	changes chan int,
) *app.Reconciler {
	return &app.Reconciler{
		Store: store, SCM: scmGW, Policy: p, Harness: h,
		Source: source, Workspace: wt,
		Notifier:           cfg.Notifier,
		Promoter:           disp,
		AgentSlots:         disp,
		Changes:            changes,
		Repo:               cfg.Repo,
		BaseBranch:         cfg.BaseBranch,
		BaseRedMode:        cfg.BaseRedMode,
		WorkerInstructions: cfg.WorkerInstructions,
		MaxFixTries:        cfg.MaxFixTries,
		MaxAttempts:        cfg.MaxAttempts,
		ParentSessionID:    cfg.ParentSession,
		Interval:           cfg.Interval,
		OrphanTimeout:      cfg.OrphanTimeout,
		AutoRebase:         cfg.AutoRebase,
		MaxAutoRebases:     cfg.MaxAutoRebases,
	}
}

// Explain forwards the dry-run trace to the policy in the current snapshot.
// A policy without Explain yields no steps, which the explain service reports
// as a merge summary with an empty chain instead of panicking.
func (p *reloadablePolicy) Explain(obs core.Observation) []app.ExplainStep {
	traceable, ok := (*p.live).Load().policy.(app.ExplainPolicy)
	if !ok {
		return nil
	}
	return traceable.Explain(obs)
}

// ReviewRequired forwards the epic review gate from the live policy snapshot.
// The composition root is the only place that probes the snapshot's concrete
// policy; downstream services receive a policy that already has the gate.
func (p *reloadablePolicy) ReviewRequired(mode core.ReviewMode, obs core.Observation) bool {
	gate, ok := (*p.live).Load().policy.(app.ReviewGate)
	if !ok {
		return true
	}
	return gate.ReviewRequired(mode, obs)
}

func loadTokens(cfg config.Config) (scmToken, sourceToken, harnessToken string) {
	scmToken = envOr(cfg.SCM.TokenEnv, cfg.SCM.Token)
	sourceToken = envOr(cfg.TaskSource.TokenEnv, scmToken)
	harnessToken = envOr(cfg.Harness.TokenEnv, cfg.Harness.Token)
	return
}

func newHarness(cfg config.Config, token, parent, root string) (core.AgentHarness, error) {
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
			Command: cfg.Harness.Command,
		}), nil
	default:
		return harness.New(harness.Config{
			Provider: cfg.Harness.Provider, Command: cfg.Harness.Command,
			PromptTemplate: cfg.Harness.PromptTemplate,
		})
	}
}

// modelProvider is the provider recorded on a model taken from the config or
// the environment. An empty harness provider builds the default harness, so
// the same default applies to the model reference.
func modelProvider(cfg config.Config) string {
	if p := strings.TrimSpace(cfg.Harness.Provider); p != "" {
		return p
	}
	return "opencode"
}

// resolveModel uses the configured model, then BEACON_MODEL, then the model
// of the orchestrator session so workers run with the user's current choice.
func resolveModel(cfg config.Config, token, parent string) core.ModelRef {
	provider := modelProvider(cfg)
	if v := envOr("BEACON_MODEL", ""); v != "" {
		return core.ModelRef{ProviderID: provider, ID: v}
	}
	if cfg.Harness.Model != "" {
		return core.ModelRef{ProviderID: provider, ID: cfg.Harness.Model}
	}
	if parent == "" || token == "" || cfg.Harness.Endpoint == "" {
		return core.ModelRef{}
	}
	endpoint := strings.TrimRight(cfg.Harness.Endpoint, "/") + "/api/session/" + url.PathEscape(parent)
	timeout := cfg.ModelTimeoutDuration()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return core.ModelRef{}
	}
	if user, pass, ok := strings.Cut(token, ":"); ok {
		req.SetBasicAuth(user, pass)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			slog.Warn("model resolution timed out", "provider", provider, "timeout", timeout, "parent", parent)
		}
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

// coalesce returns the first non-empty value. Values are used verbatim; callers
// that need trimmed values trim their own inputs.
func coalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// firstNonEmpty keeps the former name for callers outside this task's scope.
func firstNonEmpty(values ...string) string {
	return coalesce(values...)
}

// logDiscard records an error the caller deliberately treats as advisory. The
// error never reaches the CLI exit path, so the log is the only place a user
// can see it. A nil error logs nothing.
func logDiscard(what string, err error) {
	if err != nil {
		slog.Warn("discarded error", "what", what, "error", err)
	}
}

func (w *wiring) close() { _ = w.store.Close() }

func main() {
	slog.SetDefault(shared.NewLogger(envOr("BEACON_LOG_LEVEL", "")))
	root := &cobra.Command{Use: "beacon", Short: "Parallel AI agent controller"}
	configPath := root.PersistentFlags().String("config", "", "path to config.yaml")

	root.AddCommand(newInitCommand())
	root.AddCommand(newDoctorCommand(configPath))
	root.AddCommand(newDispatchCommand(configPath, wire))
	root.AddCommand(newStatusCommand(configPath))
	root.AddCommand(newExplainCommand(configPath, wire))
	root.AddCommand(newControlCommand(configPath, wire))
	root.AddCommand(newMCPCommand(configPath))
	root.AddCommand(newServeCommand(configPath))
	root.AddCommand(newGCCommand(configPath))

	if err := root.Execute(); err != nil {
		var coder exitCoder
		if errors.As(err, &coder) {
			os.Exit(coder.ExitCode())
		}
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
