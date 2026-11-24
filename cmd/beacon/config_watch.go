package main

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/refusedguy/beacon/harnesses/opencode"
	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/pkg/core"
)

// configWatchInterval is short enough that an operator sees a model change
// without reconnecting, and long enough that a ticker does not churn on a
// weak machine.
const configWatchInterval = 2 * time.Second

// liveState is an immutable snapshot. Reloads build a new one and swap the
// pointer instead of editing maps or fields that a running worker may read.
type liveState struct {
	cfg     config.Config
	harness core.AgentHarness
	policy  core.MergePolicy
}

type configWatcher struct {
	path     string
	root     string
	parent   string
	interval time.Duration

	mu          sync.Mutex
	hash        [sha256.Size]byte
	generations uint64

	live atomic.Pointer[liveState]
}

func newConfigWatcher(path, root, parent string, initial *liveState, interval time.Duration) *configWatcher {
	cw := &configWatcher{path: path, root: root, parent: parent, interval: interval}
	if cw.interval <= 0 {
		cw.interval = configWatchInterval
	}
	cw.live.Store(initial)
	if data, err := os.ReadFile(cw.path); err == nil {
		cw.hash = sha256.Sum256(data)
	}
	return cw
}

func newLiveState(cfg config.Config, h core.AgentHarness) *liveState {
	return &liveState{cfg: cfg, harness: h, policy: mergePolicy(cfg)}
}

// Run watches the config until ctx is cancelled. The MCP stdio session is
// owned by the caller and is never restarted by a reload.
func (cw *configWatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(cw.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cw.reload()
		}
	}
}

// reload applies a changed, valid config and reports whether it swapped state.
// The previous state stays live on any read, parse or build failure.
func (cw *configWatcher) reload() bool {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	data, err := os.ReadFile(cw.path)
	if err != nil {
		slog.Warn("config watch: read failed", "path", cw.path)
		return false
	}
	sum := sha256.Sum256(data)
	if sum == cw.hash {
		return false
	}
	cw.hash = sum
	cfg, root, err := loadConfig(cw.path)
	if err != nil || !cfg.Complete() {
		slog.Warn("config watch: invalid config ignored", "path", cw.path)
		return false
	}
	state := cw.build(cfg, root)
	if state == nil {
		return false
	}
	previous := cw.live.Load()
	cw.live.Store(state)
	for _, field := range restartFields(previous.cfg, cfg) {
		slog.Warn("config watch: kept previous value; restart needed", "field", field,
			"action", "restart beacon mcp and reconnect OpenCode")
	}
	cw.generations++
	return true
}

func (cw *configWatcher) build(cfg config.Config, root string) *liveState {
	_, _, token := loadTokens(cfg)
	h, err := newHarness(cfg, token, cw.parent)
	if err != nil {
		slog.Warn("config watch: harness change needs restart", "field", "harness.provider")
		return nil
	}
	if oc, ok := h.(*opencode.Harness); ok {
		oc.Repo = root
	}
	return newLiveState(cfg, h)
}

// modelResolver reads the current snapshot so a dispatch uses the model the
// operator configured most recently.
func (cw *configWatcher) modelResolver(parent string) map[string]string {
	state := cw.live.Load()
	_, _, token := loadTokens(state.cfg)
	return resolveModel(state.cfg, token, firstNonEmpty(parent, state.cfg.Harness.ParentSessionID))
}

// restartFields names changed fields that cannot be applied without replacing
// a service the MCP server holds. Values are never included in the result.
func restartFields(old, next config.Config) []string {
	checks := []struct {
		name  string
		equal bool
	}{
		{"harness.worker_instructions", old.Harness.WorkerInstructions == next.Harness.WorkerInstructions},
		{"harness.command", old.Harness.Command == next.Harness.Command},
		{"harness.prompt_template", old.Harness.PromptTemplate == next.Harness.PromptTemplate},
		{"harness.parent_session_id", old.Harness.ParentSessionID == next.Harness.ParentSessionID},
		{"concurrency.max_in_flight", old.Concurrency.MaxInFlight == next.Concurrency.MaxInFlight},
		{"concurrency.max_active_agents", old.Concurrency.MaxActiveAgents == next.Concurrency.MaxActiveAgents},
		{"merge.max_attempts", old.Merge.MaxAttempts == next.Merge.MaxAttempts},
		{"merge.auto_rebase", old.Merge.AutoRebase == next.Merge.AutoRebase},
		{"on_base_red.mode", old.OnBaseRed.Mode == next.OnBaseRed.Mode},
		{"on_base_red.max_fix_attempts", old.OnBaseRed.MaxFixAttempts == next.OnBaseRed.MaxFixAttempts},
		{"base_branch", old.BaseBranch == next.BaseBranch},
		{"state_db", old.StateDB == next.StateDB},
		{"workspace_dir", old.WorkspaceDir == next.WorkspaceDir},
		{"strict_scope", old.StrictScope == next.StrictScope},
		{"poll_interval", old.PollInterval == next.PollInterval},
		{"task_source", reflect.DeepEqual(old.TaskSource, next.TaskSource)},
		{"scm", reflect.DeepEqual(old.SCM, next.SCM)},
		{"notify", reflect.DeepEqual(old.Notify, next.Notify)},
		{"webhook", reflect.DeepEqual(old.Webhook, next.Webhook)},
	}
	var out []string
	for _, check := range checks {
		if !check.equal {
			out = append(out, check.name)
		}
	}
	return out
}

// reloadableHarness delegates to the current harness. Every service holds one
// stable wrapper, so a reload never mutates a harness a worker is using.
type reloadableHarness struct {
	live   *atomic.Pointer[liveState]
	events <-chan core.HarnessEvent
}

func (h *reloadableHarness) current() core.AgentHarness {
	return (*h.live).Load().harness
}

func (h *reloadableHarness) Spawn(ctx context.Context, req core.SpawnRequest) (core.Session, error) {
	return h.current().Spawn(ctx, req)
}

func (h *reloadableHarness) Wake(ctx context.Context, session string, wake core.WakeContext) error {
	return h.current().Wake(ctx, session, wake)
}

func (h *reloadableHarness) Stop(ctx context.Context, session string) error {
	return h.current().Stop(ctx, session)
}

func (h *reloadableHarness) IsAlive(ctx context.Context, session string) (bool, error) {
	return h.current().IsAlive(ctx, session)
}

func (h *reloadableHarness) NotifyParent(ctx context.Context, session, text string) error {
	return h.current().NotifyParent(ctx, session, text)
}

func (h *reloadableHarness) Events() <-chan core.HarnessEvent {
	return h.events
}

func (h *reloadableHarness) RefreshWorktrees(ctx context.Context) error {
	refresher, ok := h.current().(core.WorktreeRefresher)
	if !ok {
		return nil
	}
	return refresher.RefreshWorktrees(ctx)
}

func (h *reloadableHarness) WatchIdle(ctx context.Context) {
	if watcher, ok := h.current().(idleWatcher); ok {
		watcher.WatchIdle(ctx)
	}
}

// reloadablePolicy reads merge settings from the current snapshot.
type reloadablePolicy struct {
	live *atomic.Pointer[liveState]
}

func (p *reloadablePolicy) Evaluate(obs core.Observation) core.MergeDecision {
	return (*p.live).Load().policy.Evaluate(obs)
}
