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

	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/pkg/core"
)

// configWatchInterval is short enough that an operator sees a model change
// without reconnecting, and long enough that a ticker does not churn on a
// weak machine.
const configWatchInterval = 2 * time.Second

// maxModelCacheEntries bounds model resolutions kept per config generation.
// A dispatch can introduce a new parent session, so an unbounded cache grows
// with every distinct parent until the next reload advances the generation.
const maxModelCacheEntries = 64

// liveState is an immutable snapshot. Reloads build a new one and swap the
// pointer instead of editing maps or fields that a running worker may read.
// generation identifies the config that produced this snapshot, so a cached
// model resolution cannot outlive the config it came from.
type liveState struct {
	cfg        config.Config
	harness    core.AgentHarness
	policy     core.MergePolicy
	relay      *eventRelay
	generation uint64
}

// eventRelay is the one event channel every consumer holds for the life of the
// proxy. A reload closes changed to wake listeners and then publishes a fresh
// one, so both the event pump and the idle watcher can retarget themselves.
type eventRelay struct {
	out     chan core.HarnessEvent
	mu      sync.Mutex
	changed chan struct{}
	reloads uint64
}

// newEventRelay creates a relay with a fresh event channel and reload signal.
func newEventRelay() *eventRelay {
	return &eventRelay{
		out:     make(chan core.HarnessEvent, 64),
		changed: make(chan struct{}),
	}
}

// reloadSignal returns the channel that fires on the next config reload.
func (r *eventRelay) reloadSignal() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.changed
}

// reloadState returns the reload count and the channel for the next one under
// one lock, so a listener cannot miss a signal that lands between the two.
func (r *eventRelay) reloadState() (uint64, <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reloads, r.changed
}

// signalReload wakes listeners and installs a fresh channel for the next reload.
func (r *eventRelay) signalReload() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloads++
	close(r.changed)
	r.changed = make(chan struct{})
}

// configWatcher polls the config file and swaps live state when a valid change lands.
type configWatcher struct {
	path     string
	root     string
	parent   string
	interval time.Duration

	mu          sync.Mutex
	hash        [sha256.Size]byte
	generations uint64

	live atomic.Pointer[liveState]

	// modelMu guards the model cache. A reload that swaps live state also
	// invalidates the cache by advancing generation, so no explicit clear is
	// needed here.
	modelMu         sync.Mutex
	models          map[string]core.ModelRef
	modelOrder      []string
	modelGeneration uint64
}

// newConfigWatcher builds a watcher seeded with the initial state and current file hash.
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

// newLiveState builds a config snapshot with its harness, policy and event relay.
func newLiveState(cfg config.Config, h core.AgentHarness) *liveState {
	return &liveState{cfg: cfg, harness: h, policy: mergePolicy(cfg), relay: newEventRelay()}
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
	previous := cw.live.Load()
	restart, modelOnly := configChange(previous.cfg, cfg)
	if len(restart) > 0 {
		// The running services were wired with the previous values for these
		// fields. Storing the new config would make the snapshot disagree with
		// what the services use, so keep the whole previous config instead.
		for _, field := range restart {
			slog.Warn("config watch: change needs restart, kept previous config", "field", field,
				"action", "restart beacon mcp and reconnect OpenCode")
		}
		return false
	}
	if modelOnly {
		// Neither the harness nor the policy depends on the model. Dispatch
		// resolves it from the live config on every spawn, so store the new
		// config with the existing services and advance the generation to
		// invalidate the resolver cache. No new harness, no policy rebuild and
		// no reload broadcast: the watchers keep watching the same harness.
		state := &liveState{
			cfg:        cfg,
			harness:    previous.harness,
			policy:     previous.policy,
			relay:      previous.relay,
			generation: cw.generations + 1,
		}
		cw.live.Store(state)
		cw.generations++
		return true
	}
	state := cw.build(cfg, root)
	if state == nil {
		return false
	}
	state.relay = previous.relay
	state.generation = cw.generations + 1
	cw.live.Store(state)
	state.relay.signalReload()
	cw.generations++
	return true
}

// build constructs a new live state for cfg, or nil when the harness needs a restart.
func (cw *configWatcher) build(cfg config.Config, root string) *liveState {
	_, _, token := loadTokens(cfg)
	h, err := newHarness(cfg, token, cw.parent, root)
	if err != nil {
		slog.Warn("config watch: harness change needs restart", "field", "harness.provider")
		return nil
	}
	return newLiveState(cfg, h)
}

// resetModelCacheIfStale drops resolutions from an older config generation.
// Callers hold modelMu.
func (cw *configWatcher) resetModelCacheIfStale(state *liveState) {
	if cw.models == nil || cw.modelGeneration != state.generation {
		cw.models = make(map[string]core.ModelRef)
		cw.modelOrder = nil
		cw.modelGeneration = state.generation
	}
}

// cacheModel stores a resolution under parent, evicting the oldest entry when
// the cache is full. Callers hold modelMu.
func (cw *configWatcher) cacheModel(parent string, ref core.ModelRef) {
	if _, ok := cw.models[parent]; !ok {
		if len(cw.models) >= maxModelCacheEntries {
			oldest := cw.modelOrder[0]
			delete(cw.models, oldest)
			cw.modelOrder = cw.modelOrder[1:]
		}
		cw.modelOrder = append(cw.modelOrder, parent)
	}
	cw.models[parent] = ref
}

// modelResolver reads the current snapshot so a dispatch uses the model the
// operator configured most recently. Resolution can be an HTTP session lookup,
// so the result is cached per parent session and config generation. The cache
// is bounded so many distinct parents cannot grow it without limit.
func (cw *configWatcher) modelResolver(parent string) core.ModelRef {
	state := cw.live.Load()
	cw.modelMu.Lock()
	defer cw.modelMu.Unlock()
	cw.resetModelCacheIfStale(state)
	if ref, ok := cw.models[parent]; ok {
		return ref
	}
	_, _, token := loadTokens(state.cfg)
	ref := resolveModel(state.cfg, token, parent)
	cw.cacheModel(parent, ref)
	return ref
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

// configChange classifies a config edit before any service is rebuilt. It
// returns the restart fields that changed and whether the edit is confined to
// the model. Restart changes cannot be applied to running services. A
// model-only change needs no new harness or policy, because dispatch resolves
// the model from the live config. Any other change rebuilds the harness or
// policy from the new config.
func configChange(old, next config.Config) (restart []string, modelOnly bool) {
	restart = restartFields(old, next)
	if len(restart) > 0 {
		return restart, false
	}
	probe := old
	probe.Harness.Model = next.Harness.Model
	return nil, reflect.DeepEqual(probe, next)
}

// eventForwarder copies events from one harness channel into the stable relay.
// cancel stops it and done closes when it exits.
type eventForwarder struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// reloadableHarness delegates to the current harness. Every service holds one
// stable wrapper, so a reload never mutates a harness a worker is using. The
// wrapper owns one event relay and one idle watcher that follow the current
// harness across reloads. A reload replaces the single forwarder instead of
// accumulating one per harness, so the goroutine count stays constant.
type reloadableHarness struct {
	live *atomic.Pointer[liveState]

	// events is the source captured at setup. It seeds the relay; a reload
	// replaces it with the current harness's channel.
	events <-chan core.HarnessEvent

	once  sync.Once
	relay *eventRelay

	mu     sync.Mutex
	active *eventForwarder
}

// init starts the event pump once, seeding the relay from the current harness.
func (h *reloadableHarness) init() {
	h.once.Do(func() {
		h.relay = (*h.live).Load().relay
		h.setForwarder(h.events)
		h.setForwarder(h.current().Events())
		go h.pump()
	})
}

// current returns the harness held by the latest live snapshot.
func (h *reloadableHarness) current() core.AgentHarness {
	return (*h.live).Load().harness
}

// setForwarder stops the active forwarder and starts one for src. A retired
// harness loses its forwarder, so reloads do not accumulate goroutines.
func (h *reloadableHarness) setForwarder(src <-chan core.HarnessEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active != nil {
		h.active.cancel()
		<-h.active.done
		h.active = nil
	}
	if src == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	h.active = &eventForwarder{cancel: cancel, done: done}
	go func() {
		defer close(done)
		for {
			select {
			case ev, ok := <-src:
				if !ok {
					return
				}
				select {
				case h.relay.out <- ev:
				default:
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// activeForwarder returns the forwarder currently reading a harness channel.
func (h *reloadableHarness) activeForwarder() *eventForwarder {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active
}

// pump retargets the relay's single forwarder whenever a reload is broadcast.
// It also picks up reloads that landed before the goroutine started.
func (h *reloadableHarness) pump() {
	var processed uint64
	for {
		count, changed := h.relay.reloadState()
		if count > processed {
			h.setForwarder(h.current().Events())
			processed = count
			continue
		}
		<-changed
	}
}

// Spawn delegates to the current harness.
func (h *reloadableHarness) Spawn(ctx context.Context, req core.SpawnRequest) (core.Session, error) {
	return h.current().Spawn(ctx, req)
}

// Wake delegates to the current harness.
func (h *reloadableHarness) Wake(ctx context.Context, session string, wake core.WakeContext) error {
	return h.current().Wake(ctx, session, wake)
}

// Stop delegates to the current harness.
func (h *reloadableHarness) Stop(ctx context.Context, session string) error {
	return h.current().Stop(ctx, session)
}

// IsAlive delegates to the current harness.
func (h *reloadableHarness) IsAlive(ctx context.Context, session string) (bool, error) {
	return h.current().IsAlive(ctx, session)
}

// NotifyParent delegates to the current harness.
func (h *reloadableHarness) NotifyParent(ctx context.Context, session, text string) error {
	return h.current().NotifyParent(ctx, session, text)
}

// Events returns the stable relay channel that collects events across reloads.
func (h *reloadableHarness) Events() <-chan core.HarnessEvent {
	h.init()
	return h.relay.out
}

// RefreshWorktrees delegates to the current harness when it supports worktree refresh.
func (h *reloadableHarness) RefreshWorktrees(ctx context.Context) error {
	refresher, ok := h.current().(core.WorktreeRefresher)
	if !ok {
		return nil
	}
	return refresher.RefreshWorktrees(ctx)
}

// WatchIdle forwards idle watching to the current harness and retargets on reload.
func (h *reloadableHarness) WatchIdle(ctx context.Context) {
	h.init()
	for {
		changed := h.relay.reloadSignal()
		watcher, ok := h.current().(idleWatcher)
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-changed:
			}
			continue
		}
		child, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			watcher.WatchIdle(child)
		}()
		select {
		case <-ctx.Done():
			cancel()
			<-done
			return
		case <-changed:
			cancel()
			<-done
		}
	}
}

// reloadablePolicy reads merge settings from the current snapshot.
type reloadablePolicy struct {
	live *atomic.Pointer[liveState]
}

// Evaluate delegates to the merge policy in the current snapshot.
func (p *reloadablePolicy) Evaluate(obs core.Observation) core.MergeDecision {
	return (*p.live).Load().policy.Evaluate(obs)
}
