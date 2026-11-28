package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// defaultOrphanTimeout is how long a running run without a PR may go stale
// before its worker session is checked for liveness.
const defaultOrphanTimeout = 90 * time.Second

// defaultWatchdogAge bounds a watched state whose age is absent from the
// injected settings. Alerts stay off until WatchdogEnabled is set, so this
// fallback never changes a daemon that has not opted in.
const defaultWatchdogAge = 30 * time.Minute

// DefaultOrphanTimeout exposes the built-in fallback to the composition root.
// A configured timeout and the reconciler's own default then share one value.
const DefaultOrphanTimeout = defaultOrphanTimeout

// ReconcilerStore is the store surface Reconciler and its helpers use, from
// reconciliation through incident repair and idle-session wakeups.
type ReconcilerStore interface {
	FindByPR(ctx context.Context, pr int) (*core.Run, error)
	Save(ctx context.Context, run *core.Run) error
	Approved(ctx context.Context, id core.RunID, sha string) (bool, error)
	ClaimDelivery(ctx context.Context, key string) (bool, error)
	ReleaseDelivery(ctx context.Context, key string) error
	SetTaskStatus(ctx context.Context, taskID core.TaskID, status string) error
	ListActive(ctx context.Context) ([]*core.Run, error)
	ListReadyTasks(ctx context.Context) ([]core.Task, error)
	FindActiveByTask(ctx context.Context, task core.TaskID) (*core.Run, error)
	Get(ctx context.Context, id core.RunID) (*core.Run, error)
	CreateIncident(ctx context.Context, in *core.BaseIncident) (*core.BaseIncident, error)
	ListOpenIncidents(ctx context.Context) ([]*core.BaseIncident, error)
	ListByIncident(ctx context.Context, incidentID string) ([]*core.Run, error)
	UpdateIncident(ctx context.Context, in *core.BaseIncident) error
}

// stalePruner is the optional store capability for reclaiming old delivery and
// approval rows. The reconciler type-asserts its store so adapters without
// retention support keep working, and keeps the store ports free of a method
// that only the daemon tick needs.
type stalePruner interface {
	PruneStale(ctx context.Context, retention time.Duration) (int64, error)
}

// prOwnerFinder is the optional store capability for reading every active run
// bound to one PR. The reconciler type-asserts it so adapters without the
// method keep working. FindByPR returns only the newest owner, so a second
// active run on the same PR would otherwise stay invisible.
type prOwnerFinder interface {
	ActiveRunsByPR(ctx context.Context, pr int) ([]*core.Run, error)
}

// warnDuplicatePROwners logs the active owners of one PR. New bindings are
// refused by the store, so duplicates here are older rows that need an
// operator's attention instead of a silent newest-row mask.
func warnDuplicatePROwners(pr int, owners []*core.Run) {
	if len(owners) < 2 {
		return
	}
	ids := make([]string, 0, len(owners))
	for _, owner := range owners {
		ids = append(ids, string(owner.ID))
	}
	slog.Warn("multiple active runs bound to PR", "pr", pr, "runs", strings.Join(ids, ","))
}

// ReconcilerPolicy is the policy surface the reconciler uses. It composes the
// merge decision with the epic review gate, so the gate is part of the injected
// dependency rather than a capability discovered by assertion at use.
type ReconcilerPolicy interface {
	core.MergePolicy
	ReviewGate
}

// Promoter starts queued runs when an agent slot becomes free. Reconciler
// calls this port on each tick instead of reaching into a concrete Dispatcher,
// so a test can supply a fake without building a dispatcher.
type Promoter interface {
	PromotePending(ctx context.Context) ([]*core.Run, error)
}

// DispatchPort refills free agent slots after promotion. Available reports
// the current slot budget and Dispatch starts up to that many ready cards.
// The concrete Dispatcher implements the port, so manual and automatic intake
// share its conflict guards and its dispatch_open brake. A test can supply a
// fake without building a dispatcher.
type DispatchPort interface {
	Available(ctx context.Context) int
	Dispatch(ctx context.Context, n int) (DispatchResult, error)
}

// ReconcilerConfig holds the settings the reconciler reads at construction.
// The composition root copies them from config.Config, so the service never
// receives unrelated configuration.
type ReconcilerConfig struct {
	Notifier           core.Notifier
	Repo               string
	BaseBranch         string
	ParentSession      string
	BaseRedMode        string
	WorkerInstructions string
	MaxFixTries        int
	MaxAttempts        int
	Interval           time.Duration
	OrphanTimeout      time.Duration
	AutoRebase         bool
	MaxAutoRebases     int
	RebaseCap          func() int

	// AutoDispatchEnabled, AutoDispatchBatchSize and DispatchOpen are the
	// reconciler's copy of the auto_dispatch config. A zero batch size fills
	// every free slot. The composition root sets DispatchOpen from the config
	// default, so a dispatcher built without it keeps intake frozen.
	AutoDispatchEnabled   bool
	AutoDispatchBatchSize int
	DispatchOpen          bool

	// WatchdogEnabled and WatchdogAges are the reconciler's copy of the
	// watchdog config. Ages keyed by run state replace defaultWatchdogAge.
	WatchdogEnabled bool
	WatchdogAges    map[core.RunState]time.Duration
}

// Reconciler observes pull requests, applies merge policy to their runs and
// drains the harness and timer events that drive the daemon loop.
type Reconciler struct {
	Store              ReconcilerStore
	SCM                core.ScmGateway
	Policy             ReconcilerPolicy
	Harness            core.AgentHarness
	Source             core.TaskSource
	Workspace          core.WorkspaceManager
	Notifier           core.Notifier
	Promoter           Promoter
	AgentSlots         core.AgentSlots
	Changes            chan<- int
	Repo               string
	BaseBranch         string
	ParentSessionID    string
	BaseRedMode        string
	WorkerInstructions string
	MaxFixTries        int
	MaxAttempts        int
	Interval           time.Duration
	OrphanTimeout      time.Duration
	Events             <-chan core.HarnessEvent
	AutoRebase         bool
	MaxAutoRebases     int
	RebaseCap          func() int
	Decisions          DecisionLogger

	// Dispatch is the intake port used after promotion on each tick. The
	// flags below are not hot reloaded: the composition root copies them from
	// config once when it builds the reconciler.
	Dispatch              DispatchPort
	AutoDispatchEnabled   bool
	AutoDispatchBatchSize int
	DispatchOpen          bool
	WatchdogEnabled       bool
	WatchdogAges          map[core.RunState]time.Duration

	// stateAgeSeen remembers the last state observed for each active run so
	// checkStateAges can release a delivery claim when the run leaves it.
	stateAgeMu   sync.Mutex
	stateAgeSeen map[core.RunID]core.RunState
}

// Reconcile observes pr once, evaluates the merge policy and applies the
// verdict to the run bound to that pull request.
func (r *Reconciler) Reconcile(ctx context.Context, pr int) (decision core.MergeDecision, err error) {
	ctx = withTickCache(ctx)
	run, err := r.Store.FindByPR(ctx, pr)
	if err != nil {
		return core.MergeDecision{}, err
	}
	if run == nil || run.State.Terminal() {
		return core.MergeDecision{Verdict: core.VerdictNoop}, nil
	}
	if finder, ok := r.Store.(prOwnerFinder); ok {
		owners, err := finder.ActiveRunsByPR(ctx, pr)
		if err != nil {
			return core.MergeDecision{}, err
		}
		warnDuplicatePROwners(pr, owners)
	}
	ctx = WithCycleID(ctx, string(run.ID))
	obs, err := r.SCM.Observe(ctx, pr)
	if err != nil {
		return core.MergeDecision{}, err
	}
	obs.PRNumber = pr
	run.Checks = obs.Checks
	if run.PRNumber == 0 {
		run.PRNumber = pr
	}
	defer r.Decisions.logReconcileDecisionDeferred(ctx, pr, run, obs, &decision)
	// Classify a final PR state before any approval lookup or policy decision.
	// A merged or closed PR is final even when a provider call fails, so the
	// run can close without waiting on unrelated signals.
	if core.DecideFinalState(obs.PRState).Closed {
		return core.MergeDecision{Verdict: core.VerdictWait, Reason: "pull request " + obs.PRState}, r.finalizeExternal(ctx, run, obs)
	}
	if ok, err := r.Store.Approved(ctx, run.ID, obs.HeadSHA); err != nil {
		return core.MergeDecision{}, err
	} else if ok {
		obs.Approvals++
	}
	decision = r.Policy.Evaluate(obs)
	return r.applyDecision(ctx, run, obs, decision)
}

// applyAndSave applies e and persists the run. A non-zero obs notes both the
// head and base SHA; the zero value leaves the recorded observation untouched.
func (r *Reconciler) applyAndSave(ctx context.Context, run *core.Run, e core.Event, obs core.Observation) error {
	if e.Kind != "" {
		if err := run.Apply(e); err != nil {
			return err
		}
	}
	if obs.HeadSHA != "" || obs.BaseSHA != "" {
		run.NoteObservationIdentity(obs.HeadSHA, obs.BaseSHA, observationIdentity(obs))
	}
	return r.Store.Save(ctx, run)
}

func (r *Reconciler) merge(ctx context.Context, run *core.Run, obs core.Observation, isEpic bool) error {
	// A marked card must stop the merge itself. The card commit below only
	// logs a publish failure and still reaches SCM.Merge, so the corpus check
	// belongs here as well as in publishWithNFFRetry.
	if err := checkCardMarkers(run.WorkspacePath); err != nil {
		return err
	}
	run.NoteObservationIdentity(obs.HeadSHA, obs.BaseSHA, observationIdentity(obs))
	if !run.IsFixer && !isEpic && run.WorkspacePath != "" {
		if err := r.Source.MarkInWorkspace(ctx, run.TaskID, run.WorkspacePath, "done"); err != nil {
			return err
		}
		ws := core.Workspace{Path: run.WorkspacePath, Branch: run.Branch}
		message := "beacon: complete task " + string(run.TaskID)
		if err := r.commitCard(ctx, ws, cardsPath, message); err != nil {
			slog.Warn("card publish failed before merge", "run", run.ID, "pr", obs.PRNumber, "error", err)
			if saveErr := r.Store.Save(ctx, run); saveErr != nil {
				slog.Error("save observation after card publish failure", "run", run.ID, "error", saveErr)
			}
		} else {
			fresh, err := r.SCM.Observe(ctx, obs.PRNumber)
			if err != nil {
				return err
			}
			obs.HeadSHA = fresh.HeadSHA
		}
	}
	key := fmt.Sprintf("merge:%d:%s", obs.PRNumber, obs.HeadSHA)
	r.Decisions.logTransitionMerge(ctx, run.ID, obs.PRNumber, "attempt")
	if err := r.SCM.Merge(ctx, obs.PRNumber, obs.HeadSHA, string(run.TaskID)); err != nil {
		return err
	}
	if ok, err := r.Store.ClaimDelivery(ctx, key); err != nil {
		return err
	} else if !ok {
		r.Decisions.logTransitionDeliveryClaim(ctx, run.ID, obs.PRNumber, false)
		if err := r.Store.SetTaskStatus(ctx, run.TaskID, "done"); err != nil {
			return err
		}
		return nil
	}
	r.Decisions.logTransitionDeliveryClaim(ctx, run.ID, obs.PRNumber, true)
	if err := run.Apply(core.Event{
		Kind: core.EvMerged, HeadSHA: obs.HeadSHA,
		Reason: fmt.Sprintf("merged PR #%d via squash", obs.PRNumber),
	}); err != nil {
		return err
	}
	if err := r.Store.Save(ctx, run); err != nil {
		return err
	}
	r.releaseIdleNudge(ctx, run)
	r.notifyParent(ctx, run, obs.PRNumber, string(run.State))
	if !run.IsFixer {
		if err := r.Source.Complete(ctx, run.TaskID); err != nil {
			slog.Error("complete task after merge", "run", run.ID, "task", run.TaskID, "error", err)
		}
		if err := r.Store.SetTaskStatus(ctx, run.TaskID, "done"); err != nil {
			return err
		}
		if parentEpicID(run.TaskID) != "" {
			if err := r.reconcileEpics(ctx); err != nil {
				slog.Error("reconcile epics after child merge", "error", err)
			}
		}
	}
	return r.cleanupRun(ctx, run)
}

// commitCard publishes the card from the run worktree. A push rejected as
// non-fast-forward gets one refresh from the run's own branch and one retry.
func (r *Reconciler) commitCard(ctx context.Context, ws core.Workspace, pathspec, message string) error {
	return publishWithNFFRetry(ctx, r.Workspace, ws, ws.Branch, pathspec, message)
}

func (r *Reconciler) rebase(ctx context.Context, run *core.Run, obs core.Observation) error {
	if !run.CanRebase() {
		if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvBlock, Reason: "rebase attempts exhausted"}, core.Observation{}); err != nil {
			return err
		}
		return r.notify(run, "rebase exhausted", "automatic rebase attempts exhausted")
	}
	if sameObservation(run, obs) {
		return nil
	}
	newSHA, err := r.SCM.UpdateBranch(ctx, obs.PRNumber, obs.HeadSHA)
	if err != nil {
		r.Decisions.logTransitionRebase(ctx, run.ID, obs.PRNumber, run.RebaseAttempts+1, "", "failed")
		if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvRebaseFailed, Reason: err.Error()}, attemptedRebaseObservation(obs)); err != nil {
			return err
		}
		// An UpdateBranch failure is a provider call the worker cannot fix.
		// Spending a fix attempt on it would ask the worker to repair Beacon's
		// own request, so the operator gets the notice instead.
		return r.notify(run, "automatic rebase failed", "automatic rebase failed: "+err.Error())
	}
	if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvRebased, HeadSHA: newSHA, Reason: "automatic rebase"}, core.Observation{HeadSHA: newSHA, BaseSHA: obs.BaseSHA}); err != nil {
		return err
	}
	r.Decisions.logTransitionRebase(ctx, run.ID, obs.PRNumber, run.RebaseAttempts, newSHA, "rebased")
	return nil
}

func (r *Reconciler) wake(ctx context.Context, run *core.Run, obs core.Observation, reason string, action core.WakeAction) error {
	return r.wakeWithEvent(ctx, run, obs, reason, core.EvAgentFixed, action)
}

// wakeWithEvent applies the wake guards once and records the delivered wake
// with kind. An idle nudge uses EvIdleNudge so the run stays in running state
// while it spends the same bounded fix budget as a check failure. The action
// selects the prompt that matches the reason for the wake.
func (r *Reconciler) wakeWithEvent(ctx context.Context, run *core.Run, obs core.Observation, reason string, kind core.EventKind, action core.WakeAction) error {
	if !run.CanWake() {
		if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvBlock, Reason: "fix attempts exhausted"}, core.Observation{}); err != nil {
			return err
		}
		return r.notify(run, "fix exhausted", "agent fix attempts exhausted")
	}
	if sameObservation(run, obs) {
		return nil
	}
	if !r.reserveAgentSlot(ctx, run.ID) {
		return nil
	}
	defer r.releaseAgentSlot(ctx, run.ID)
	w := core.WakeContext{
		RunID: run.ID, PRNumber: obs.PRNumber, HeadSHA: obs.HeadSHA,
		Reason: reason, Excerpt: compact(obs.FailureExcerpt),
		Workspace: run.WorkspacePath, Branch: run.Branch,
		RebaseTry: run.RebaseAttempts, FixTry: run.FixAttempts,
		Action: action,
	}
	if err := r.Harness.Wake(ctx, run.SessionID, w); err != nil {
		return fmt.Errorf("wake: %w", err)
	}
	// The session is still live after a wake, even though the run may now move
	// to waiting_ci. Keep its slot counted until the session is confirmed gone.
	run.HoldAgent()
	r.Decisions.logTransitionWake(ctx, run.ID, obs.PRNumber, "woke")
	// The wake has been delivered. The run must not keep showing the old
	// failure as its current reason.
	return r.applyAndSave(ctx, run, core.Event{Kind: kind}, obs)
}

// wakeSlots is the richer slot policy used by wake and by injected spawns. It
// replaces the plain availability check with an atomic check-and-reserve so two
// forced reconciles cannot spend the same free slot. A port that implements
// only Available keeps the legacy check.
type wakeSlots interface {
	AvailableFor(ctx context.Context, runID core.RunID) int
	ReserveForWake(ctx context.Context, runID core.RunID) bool
	ReleaseWake(ctx context.Context, runID core.RunID)
}

func (r *Reconciler) slotReserver() wakeSlots {
	slots, _ := r.AgentSlots.(wakeSlots)
	return slots
}

// reserveAgentSlot checks and reserves one slot for runID in one critical
// section. A port without the reservation methods falls back to the shared
// availability check.
func (r *Reconciler) reserveAgentSlot(ctx context.Context, runID core.RunID) bool {
	if slots := r.slotReserver(); slots != nil {
		return slots.ReserveForWake(ctx, runID)
	}
	return r.hasAgentSlot(ctx)
}

func (r *Reconciler) releaseAgentSlot(ctx context.Context, runID core.RunID) {
	if slots := r.slotReserver(); slots != nil {
		slots.ReleaseWake(ctx, runID)
	}
}

// hasAgentSlot delegates the wake decision to the injected AgentSlots policy.
// The composition root injects the dispatcher as the port, so reconciliation
// no longer probes a concrete type for the budget. A missing port fails open;
// a store error makes Available return zero, so a wake is skipped rather than
// started on an unreadable budget.
func (r *Reconciler) hasAgentSlot(ctx context.Context) bool {
	slots := r.AgentSlots
	if slots == nil {
		return true
	}
	return slots.Available(ctx) > 0
}

// autoDispatch refills the slots promotion left free. A positive batch size
// caps the tick; zero fills every free slot. The enabled and dispatch_open
// gates live in Run so a brake also stops a fake port in tests.
func (r *Reconciler) autoDispatch(ctx context.Context) {
	if r.Dispatch == nil {
		return
	}
	budget := r.Dispatch.Available(ctx)
	if r.AutoDispatchBatchSize > 0 && r.AutoDispatchBatchSize < budget {
		budget = r.AutoDispatchBatchSize
	}
	if budget <= 0 {
		return
	}
	result, err := r.Dispatch.Dispatch(ctx, budget)
	if err != nil {
		slog.Error("auto dispatch", "error", err)
		return
	}
	r.Decisions.Record(ctx, Decision{
		CycleID: cycleID(ctx),
		Kind:    "dispatch.auto",
		Fields: map[string]any{
			"budget":  budget,
			"started": len(result.Runs),
			"skipped": len(result.Skipped),
		},
	})
}

// watchdogDeliveryKey claims one age alert per run and state. A release when
// the run leaves the state lets the same run alert again on a later stall.
func watchdogDeliveryKey(runID core.RunID, state core.RunState) string {
	return fmt.Sprintf("state-age:%s:%s", runID, state)
}

// watchdogAge reports the limit for a watched state. The four watched states
// are the ones observations can keep fresh without progress; every other
// state is left to its own transition rules.
func (r *Reconciler) watchdogAge(state core.RunState) (time.Duration, bool) {
	switch state {
	case core.RunWaitingCI, core.RunNeedsRebase, core.RunNeedsAgentFix, core.RunBlocked:
	default:
		return 0, false
	}
	if age, ok := r.WatchdogAges[state]; ok && age > 0 {
		return age, true
	}
	return defaultWatchdogAge, true
}

// checkStateAges alerts once for each active run that has held a watched state
// past its age. Time in state uses UpdatedAt, the only timestamp a run carries.
// A claimed key blocks later ticks until the run leaves the state or its send
// fails.
func (r *Reconciler) checkStateAges(ctx context.Context, runs []*core.Run) {
	if !r.WatchdogEnabled || r.Store == nil {
		return
	}
	current := make(map[core.RunID]core.RunState, len(runs))
	for _, run := range runs {
		if run != nil && !run.State.Terminal() {
			current[run.ID] = run.State
		}
	}

	r.stateAgeMu.Lock()
	if r.stateAgeSeen == nil {
		r.stateAgeSeen = make(map[core.RunID]core.RunState, len(current))
	}
	for id, prior := range r.stateAgeSeen {
		if current[id] != prior {
			r.releaseWatchdogClaim(ctx, id, prior)
			delete(r.stateAgeSeen, id)
		}
	}
	for id, state := range current {
		r.stateAgeSeen[id] = state
	}
	r.stateAgeMu.Unlock()

	for _, run := range runs {
		if run != nil && !run.State.Terminal() {
			r.checkOneStateAge(ctx, run)
		}
	}
}

func (r *Reconciler) checkOneStateAge(ctx context.Context, run *core.Run) {
	limit, watched := r.watchdogAge(run.State)
	if !watched {
		return
	}
	age := time.Since(run.UpdatedAt)
	if age <= limit {
		return
	}
	key := watchdogDeliveryKey(run.ID, run.State)
	claimed, err := r.Store.ClaimDelivery(ctx, key)
	if err != nil {
		slog.Error("claim state age alert", "run", run.ID, "state", run.State, "error", err)
		return
	}
	if !claimed {
		return
	}
	body := fmt.Sprintf("run %s task %s has been in %s for %s (limit %s); last reason: %s",
		run.ID, run.TaskID, run.State, age.Round(time.Second), limit, run.Reason)
	if err := r.notifyStateAge(ctx, run, body); err != nil {
		r.releaseWatchdogClaim(ctx, run.ID, run.State)
		return
	}
	r.Decisions.Record(ctx, Decision{
		CycleID:  cycleID(ctx),
		Kind:     "watchdog.state_age",
		RunID:    string(run.ID),
		TaskID:   string(run.TaskID),
		PRNumber: run.PRNumber,
		Action:   string(run.State),
		Reason:   run.Reason,
		Fields: map[string]any{
			"state": string(run.State),
			"age":   age.Round(time.Second).String(),
			"limit": limit.String(),
		},
	})
}

// notifyStateAge sends the operator notice. A nil notifier still consumes the
// claim, so enabling the watchdog without one does not repeat quiet alerts.
func (r *Reconciler) notifyStateAge(ctx context.Context, run *core.Run, body string) error {
	if r.Notifier == nil {
		return nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, operatorNotifyTimeout)
	defer cancel()
	return r.Notifier.Notify(sendCtx, core.Notification{
		RunID:   run.ID,
		Subject: "stalled in " + string(run.State),
		Body:    compact(body),
	})
}

func (r *Reconciler) releaseWatchdogClaim(ctx context.Context, id core.RunID, state core.RunState) {
	if err := r.Store.ReleaseDelivery(ctx, watchdogDeliveryKey(id, state)); err != nil {
		slog.Error("release state age alert", "run", id, "state", state, "error", err)
	}
}

// Run drains change events and polls. One failing PR never stops the loop.
func (r *Reconciler) Run(ctx context.Context, changes <-chan int) error {
	ticker := time.NewTicker(r.interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case pr, ok := <-changes:
			if !ok {
				return nil
			}
			if pr > 0 {
				r.safeReconcile(ctx, pr)
			}
		case event, ok := <-r.harnessEvents():
			if !ok {
				return nil
			}
			r.HandleHarnessEvent(ctx, event)
		case <-ticker.C:
			// Every read on this tick shares one task tree and one base status
			// per branch. safeReconcile sees the same cache, so the several
			// relation checks across all PRs read the source only once.
			tickCtx := withTickCache(ctx)
			// Retention runs on this fixed schedule rather than on ListActive,
			// so status, dispatch and MCP reads stay free of DELETE statements.
			// Cleanup is best effort: a failed prune must not stop the tick.
			if pruner, ok := r.Store.(stalePruner); ok {
				if _, err := pruner.PruneStale(tickCtx, 0); err != nil {
					slog.Warn("prune stale delivery rows", "error", err)
				}
			}
			if err := r.checkBaseRecovery(tickCtx); err != nil {
				slog.Error("base recovery", "error", err)
			}
			if err := r.reconcileEpics(tickCtx); err != nil {
				slog.Error("reconcile epics", "error", err)
			}
			runs, err := r.Store.ListActive(tickCtx)
			if err != nil {
				slog.Error("list runs", "error", err)
				continue
			}
			r.checkStateAges(tickCtx, runs)
			r.reconcileOrphanRunning(tickCtx, runs)
			for _, run := range runs {
				if run.PRNumber > 0 {
					r.safeReconcile(tickCtx, run.PRNumber)
				}
			}
			if r.Promoter != nil {
				if _, err := r.Promoter.PromotePending(tickCtx); err != nil {
					slog.Error("promote pending", "error", err)
				}
			}
			if r.AutoDispatchEnabled && r.DispatchOpen {
				r.autoDispatch(tickCtx)
			}
		}
	}
}

func (r *Reconciler) safeReconcile(ctx context.Context, pr int) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("reconcile panic", "pr", pr, "panic", rec)
		}
	}()
	if _, err := r.Reconcile(ctx, pr); err != nil {
		slog.Error("reconcile PR", "pr", pr, "error", err)
	}
}

func (r *Reconciler) interval() time.Duration {
	if r.Interval > 0 {
		return r.Interval
	}
	return 30 * time.Second
}

func (r *Reconciler) harnessEvents() <-chan core.HarnessEvent {
	if r.Events != nil {
		return r.Events
	}
	if r.Harness == nil {
		return nil
	}
	return r.Harness.Events()
}
