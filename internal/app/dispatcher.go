package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// step labels err with the operation that failed. Keeping the operation name
// in the text lets a caller tell which step of a run failed without a log.
func step(label string, err error) error {
	return fmt.Errorf("%s: %w", label, err)
}

// logStep logs a failed operation step with the run and task that owned it.
func logStep(what string, run *core.Run, err error) {
	slog.Error(what, "run", run.ID, "task", run.TaskID, "error", err)
}

// ModelResolver resolves a worker model from its parent session. The
// composition root supplies the OpenCode session lookup. A nil resolver keeps
// the harness default.
type ModelResolver func(parentSession string) core.ModelRef

// DispatchStore is the store surface Dispatcher uses while it selects and
// starts tasks. The active-run guard reads one list per dispatch, so the port
// needs no per-task active lookup.
type DispatchStore interface {
	SyncTasks(ctx context.Context, tasks []core.Task) error
	ListReadyTasks(ctx context.Context) ([]core.Task, error)
	ListActive(ctx context.Context) ([]*core.Run, error)
	ListRecent(ctx context.Context, limit int) ([]*core.Run, error)
	Save(ctx context.Context, run *core.Run) error
	SetTaskStatus(ctx context.Context, taskID core.TaskID, status string) error
}

// skipCapFullCode is the reason a ready card lost to the capacity limit. It
// stays in the dispatcher because the planner only judges scope conflicts.
const skipCapFullCode SkipCode = "cap_full"

// runClaimStore is the store capability that serializes a task claim across
// processes. sqlite.Store satisfies it with its deliveries table. A store
// without the capability keeps the dispatcher's in-process reservations only.
type runClaimStore interface {
	ClaimDelivery(ctx context.Context, key string) (bool, error)
	ReleaseDelivery(ctx context.Context, key string) error
}

// runClaimKey namespaces dispatch claims away from the idempotency keys that
// share the store's deliveries table.
func runClaimKey(taskID core.TaskID) string {
	return "dispatch:claim:" + string(taskID)
}

// claimRun records a store-backed claim before any workspace work. release
// runs once the run is visible in the store or the failed start is cleaned up.
// ok is false when another dispatcher already owns the task.
func (d *Dispatcher) claimRun(ctx context.Context, taskID core.TaskID) (release func(), ok bool, err error) {
	store, capable := d.Store.(runClaimStore)
	if !capable {
		return func() {}, true, nil
	}
	key := runClaimKey(taskID)
	claimed, err := store.ClaimDelivery(ctx, key)
	if err != nil {
		return nil, false, err
	}
	if !claimed {
		return nil, false, nil
	}
	return func() {
		if err := store.ReleaseDelivery(context.WithoutCancel(ctx), key); err != nil {
			slog.WarnContext(ctx, "release run claim", "task", taskID, "error", err)
		}
	}, true, nil
}

// errTaskClaimed is the internal signal for a targeted start that lost the
// store claim. The caller turns it into a refusal with no error.
var errTaskClaimed = errors.New("task is claimed by another dispatcher")

// syncAndClaimStart syncs cards and takes the cross-process claim before any
// run or slot is reserved. A lost claim is reported on out and returns
// errTaskClaimed, so the caller gives the same answer as an active run.
func (d *Dispatcher) syncAndClaimStart(ctx context.Context, out *StartOneOutcome, available []core.Task, taskID core.TaskID) (func(), error) {
	if err := d.Store.SyncTasks(ctx, available); err != nil {
		return nil, err
	}
	release, claimed, err := d.claimRun(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !claimed {
		out.Code = string(SkipActiveRun)
		out.Reason = errTaskClaimed.Error()
		d.Decisions.logDispatchSkip(ctx, SkippedTask{
			TaskID: taskID, Code: SkipActiveRun, Reason: out.Reason,
		})
		return nil, errTaskClaimed
	}
	return release, nil
}

// reserve adds one in-process slot reservation. A nil map is created here so
// callers do not each carry a branch for the first start.
func (d *Dispatcher) reserve(res *slotReservation) {
	if d.reserved == nil {
		d.reserved = make(map[core.RunID]*slotReservation)
	}
	d.reserved[res.runID] = res
}

// releaseRunClaims frees every store claim taken for this batch. It is called
// after launch, so each run is visible in the store or cleaned up before
// another process can claim its task.
func releaseRunClaims(batch *dispatchBatch) {
	for i := len(batch.claims) - 1; i >= 0; i-- {
		batch.claims[i]()
	}
	batch.claims = nil
}

// capFullSkip gives one capacity-losing card an outcome. The reason carries
// the requested count and the active count so a caller can report both.
func capFullSkip(taskID core.TaskID, requested, active int) SkippedTask {
	return SkippedTask{
		TaskID: taskID,
		Code:   skipCapFullCode,
		Reason: fmt.Sprintf("requested %d runs with %d active runs: no free slot", requested, active),
		Next:   nextActionFor(SkipActiveRun),
	}
}

// addCapacitySkips accounts for every ready card the planner neither chose nor
// skipped. Those cards lost on capacity, so each gets exactly one cap_full
// skip and no card gets two outcomes.
func addCapacitySkips(batch *dispatchBatch, ready []core.Task, requested, active int) {
	accounted := make(map[core.TaskID]bool, len(batch.planned)+len(batch.skipped))
	for _, p := range batch.planned {
		accounted[p.task.ID] = true
	}
	for _, s := range batch.skipped {
		accounted[s.TaskID] = true
	}
	for _, task := range ready {
		if accounted[task.ID] {
			continue
		}
		batch.skipped = append(batch.skipped, capFullSkip(task.ID, requested, active))
	}
}

// Dispatcher selects ready cards and starts a worker for each within the
// configured concurrency and agent-slot limits.
type Dispatcher struct {
	Source             core.TaskSource
	Planner            ConflictPlanner
	Workspace          core.WorkspaceManager
	Harness            core.AgentHarness
	Store              DispatchStore
	Concurrency        core.Concurrency
	StrictScope        bool
	MaxAttempts        int
	ParentSessionID    string
	WorkerInstructions string
	ResolveModel       ModelResolver
	Decisions          DecisionLogger

	// DispatchOpen gates both intake paths. A nil pointer means open, so a
	// Dispatcher built without the auto-dispatch config keeps its old behavior.
	// The composition root wires the configured value by address.
	DispatchOpen *bool

	// WaiterSource names the longest-waiting card for a set of scope paths. An
	// empty task ID means no card waits on them. The composition root adapts
	// the sqlite waitlist store here, so app imports no infra package.
	WaiterSource func(ctx context.Context, paths []core.ScopePath) (core.TaskID, error)

	// StartCooldown is how long a task cancelled by the operator stays
	// ineligible for a fresh start. A non-positive value uses
	// defaultStartCooldown. An explicit retry is the way out before it expires.
	StartCooldown time.Duration

	// Spans records per-step timing when set. It never changes dispatch output.
	Spans *DispatchSpans

	// slotsMu guards the reservation set and the selection that fills it. A
	// batch reserves its slots and tasks here, then releases the lock so its
	// ensureBranch and Spawn calls can overlap with another batch's.
	slotsMu sync.Mutex

	// reserved holds starts committed by in-progress batches but not yet
	// visible as active in the store. Every active view used for the cap and
	// conflict checks includes these entries, so a concurrent call cannot
	// bypass the in-flight cap, the agent cap or the task and scope guards
	// while an earlier batch is doing its slow work.
	reserved map[core.RunID]*slotReservation
}

// dispatchBatch holds the caches that live for one dispatch or promotion.
// mu guards ensured and modelRefs when the launcher starts several runs at
// once. The other fields belong to the caller that planned the batch.
type dispatchBatch struct {
	mu        sync.Mutex
	ensured   map[string]struct{}
	ensuring  map[string]*branchEnsure
	modelRefs map[string]core.ModelRef
	planned   []plannedRun
	reserved  []core.RunID
	skipped   []SkippedTask
	active    map[core.TaskID]*core.Run
	claims    []func()
	limit     int
	budget    int
}

// branchEnsure is one batch's publish of an epic base branch. The leader runs
// ensureBranch; followers wait on done before their timed step begins, so a
// follower's span does not include the leader's remote round trip.
type branchEnsure struct {
	branch string
	done   chan struct{}
	err    error
	leader bool
}

// plannedRun pairs a created run with the card it will start. The run is
// already reserved when it reaches the launcher.
type plannedRun struct {
	run  *core.Run
	task core.Task
}

var _ core.AgentSlots = (*Dispatcher)(nil)

// DispatchResult reports the runs started by one dispatch, the candidates
// left out and the workers that failed to start.
type DispatchResult struct {
	Runs     []*core.Run
	Skipped  []SkippedTask
	Failures []DispatchFailure
}

// DispatchFailure names a task whose worker failed to start and the error.
type DispatchFailure struct {
	TaskID core.TaskID
	Err    string
}

// DispatchSpans collects the duration of each named startOne step. Install one
// on Dispatcher.Spans to profile a dispatch. Every observed step also emits a
// debug record named "dispatch.span" that carries the cycle ID for correlation.
// A nil Spans leaves the hot path free of collection.
type DispatchSpans struct {
	mu   sync.Mutex
	durs map[string][]time.Duration
}

func (s *DispatchSpans) observe(name string, spent time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.durs == nil {
		s.durs = make(map[string][]time.Duration)
	}
	s.durs[name] = append(s.durs[name], spent)
}

// Durations returns a copy of the recorded durations for name.
func (s *DispatchSpans) Durations(name string) []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.durs[name]...)
}

// Steps returns the names of all recorded steps in sorted order.
func (s *DispatchSpans) Steps() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.durs))
	for name := range s.durs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func newRunID() core.RunID {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return core.RunID(fmt.Sprintf("run-%d", time.Now().UnixNano()))
	}
	return core.RunID("run-" + hex.EncodeToString(b[:]))
}

// Dispatch starts workers for up to n non-overlapping ready cards and returns
// the runs it created, the skipped candidates and any start failures.
func (d *Dispatcher) Dispatch(ctx context.Context, n int) (DispatchResult, error) {
	defer d.Decisions.flushDeferred()

	var out DispatchResult
	batch, err := d.planDispatch(ctx, n)
	if err != nil {
		return out, err
	}
	defer releaseRunClaims(batch)
	defer d.releaseReservations(batch)
	out.Skipped = batch.skipped
	for _, s := range batch.skipped {
		d.Decisions.logDispatchSkip(ctx, s)
	}
	if err := d.launch(ctx, batch, &out); err != nil {
		return out, err
	}
	return out, nil
}

// StartOneOutcome is the caller-facing result of one targeted start. A refusal
// carries a code and a reason with no run. Blockers names the dependencies
// that still hold the task, and Active reports the in-flight count when a
// global budget is full.
type StartOneOutcome struct {
	TaskID   core.TaskID      `json:"task_id"`
	RunID    core.RunID       `json:"run_id,omitempty"`
	State    core.RunState    `json:"state,omitempty"`
	Scope    []core.ScopePath `json:"scope,omitempty"`
	Code     string           `json:"code,omitempty"`
	Reason   string           `json:"reason,omitempty"`
	Blockers []StartBlocker   `json:"blockers,omitempty"`
	Active   int              `json:"active,omitempty"`
}

// StartBlocker names one unresolved dependency and the status observed for it.
// A blocker is read only. A targeted start never changes another card.
type StartBlocker struct {
	TaskID core.TaskID `json:"task_id"`
	Status string      `json:"status"`
}

// StartOneCode is a stable refusal reason that the planner codes do not cover.
type StartOneCode string

const (
	StartUnknownTask    StartOneCode = "unknown_task"
	StartUnresolvedDeps StartOneCode = "unresolved_dependencies"
	StartNoSlot         StartOneCode = "no_slot"
	StartRecentlyClosed StartOneCode = "recently_closed"
	StartFailed         StartOneCode = "start_failed"
)

// defaultStartCooldown is how long a task cancelled by the operator stays
// ineligible for an automatic start. The operator can shorten the wait with
// beacon_control retry on the cancelled run.
const defaultStartCooldown = 15 * time.Minute

// cooldownRecentLimit bounds the finished-run scan for the start cooldown. It
// is deliberately larger than the status default so a busy tick cannot hide a
// fresh cancel behind unrelated finished runs.
const cooldownRecentLimit = 100

// waiterRecentLimit bounds the finished-run scan that frees scope paths for
// waiters. It matches the cooldown window so a busy tick cannot hide a recent
// terminal run behind unrelated finished runs.
const waiterRecentLimit = 100

// cancelledByOperator is the reason Control.Cancel stamps on a closed run. It
// is the only close reason that triggers the start cooldown.
const cancelledByOperator = "cancelled by operator"

// recentlyCancelled returns the newest run of taskID that the operator
// cancelled inside the cooldown window. Older cancels have expired and no
// longer hold the task. A nil result means the task is free to start.
func (d *Dispatcher) recentlyCancelled(recent []*core.Run, taskID core.TaskID, now time.Time) *core.Run {
	cooldown := d.StartCooldown
	if cooldown <= 0 {
		cooldown = defaultStartCooldown
	}
	cutoff := now.Add(-cooldown)
	var match *core.Run
	for _, run := range recent {
		if run == nil || run.TaskID != taskID || run.State != core.RunClosed {
			continue
		}
		if run.Reason != cancelledByOperator || run.UpdatedAt.Before(cutoff) {
			continue
		}
		if match == nil || run.UpdatedAt.After(match.UpdatedAt) {
			match = run
		}
	}
	return match
}

// StartOne starts exactly one named task. Every refusal is decided before the
// task sync or any run write, so a blocked call leaves every other card and run
// untouched. Unlike Dispatch, it never evicts or reprioritates another task.
func (d *Dispatcher) StartOne(ctx context.Context, taskID core.TaskID) (StartOneOutcome, error) {
	defer d.Decisions.flushDeferred()

	out := StartOneOutcome{TaskID: taskID}
	d.slotsMu.Lock()
	available, err := d.Source.ListAvailable(ctx)
	if err != nil {
		d.slotsMu.Unlock()
		return out, err
	}
	active, err := d.Store.ListActive(ctx)
	if err != nil {
		d.slotsMu.Unlock()
		return out, err
	}
	view := d.withReserved(active)

	// A run owns the task even when the card no longer reads as available, so
	// this guard runs before the lookup in the task source.
	for _, run := range view {
		if run != nil && run.TaskID == taskID {
			d.slotsMu.Unlock()
			out.Code = string(SkipActiveRun)
			out.Reason = "task already has an active run"
			out.Blockers = []StartBlocker{{TaskID: taskID, Status: string(run.State)}}
			d.Decisions.logDispatchSkip(ctx, SkippedTask{
				TaskID: taskID, Code: SkipActiveRun, BlockedBy: taskID,
				BlockedByRun: run.ID, Reason: out.Reason,
			})
			return out, nil
		}
	}

	// A closed run cancelled by the operator is invisible to ListActive. Read
	// the finished runs and hold the card for the cooldown window so an
	// automation tick cannot start it again behind the operator's back.
	recent, err := d.Store.ListRecent(ctx, cooldownRecentLimit)
	if err != nil {
		d.slotsMu.Unlock()
		return out, err
	}
	if cancelled := d.recentlyCancelled(recent, taskID, time.Now().UTC()); cancelled != nil {
		d.slotsMu.Unlock()
		out.Code = string(StartRecentlyClosed)
		out.RunID = cancelled.ID
		out.Reason = "task was cancelled by operator; retry run " + string(cancelled.ID) + " to start it again"
		out.Blockers = []StartBlocker{{TaskID: taskID, Status: string(core.RunClosed)}}
		d.Decisions.logDispatchSkip(ctx, SkippedTask{
			TaskID: taskID, Code: SkipCode(StartRecentlyClosed),
			BlockedByRun: cancelled.ID, Reason: out.Reason,
		})
		return out, nil
	}

	var task core.Task
	found := false
	for _, item := range available {
		if item.ID == taskID {
			task, found = item, true
			break
		}
	}
	if !found {
		d.slotsMu.Unlock()
		out.Code = string(StartUnknownTask)
		out.Reason = "task is unknown or not available"
		d.Decisions.logDispatchSkip(ctx, SkippedTask{
			TaskID: taskID, Code: SkipCode(StartUnknownTask), Reason: out.Reason,
		})
		return out, nil
	}
	if task.Status != "" && task.Status != core.TaskAvailable {
		d.slotsMu.Unlock()
		out.Code = string(SkipBadStatus)
		out.Reason = "task status is " + string(task.Status)
		d.Decisions.logDispatchSkip(ctx, SkippedTask{
			TaskID: taskID, Code: SkipBadStatus, Reason: out.Reason,
		})
		return out, nil
	}

	if blockers := unresolvedStartDeps(task, available, view); len(blockers) > 0 {
		d.slotsMu.Unlock()
		out.Code = string(StartUnresolvedDeps)
		out.Reason = "unresolved dependencies: " + formatStartBlockers(blockers)
		out.Blockers = blockers
		d.Decisions.logDispatchSkip(ctx, SkippedTask{
			TaskID: taskID, Code: SkipCode(StartUnresolvedDeps),
			BlockedBy: blockers[0].TaskID, Reason: out.Reason,
		})
		return out, nil
	}

	limit := d.inFlightLimit()
	out.Active = len(view)
	if len(view) >= limit {
		d.slotsMu.Unlock()
		out.Code = string(StartNoSlot)
		out.Reason = fmt.Sprintf("in-flight limit %d reached with %d active runs", limit, len(view))
		d.Decisions.logDispatchSkip(ctx, SkippedTask{
			TaskID: taskID, Code: SkipCode(StartNoSlot), Reason: out.Reason,
		})
		return out, nil
	}
	if d.availableFrom(view) <= 0 {
		d.slotsMu.Unlock()
		out.Code = string(StartNoSlot)
		out.Reason = fmt.Sprintf("no agent slot free with %d active runs", len(view))
		d.Decisions.logDispatchSkip(ctx, SkippedTask{
			TaskID: taskID, Code: SkipCode(StartNoSlot), Reason: out.Reason,
		})
		return out, nil
	}

	// Every refusal is decided before any run write. The claim is the
	// cross-process authority, so a CLI start cannot race a daemon start into
	// two runs for one task.
	release, err := d.syncAndClaimStart(ctx, &out, available, task.ID)
	if err != nil {
		d.slotsMu.Unlock()
		if errors.Is(err, errTaskClaimed) {
			return out, nil
		}
		return out, err
	}
	run := core.NewRun(newRunID(), task.ID, time.Now().UTC())
	if d.MaxAttempts > 0 {
		run.MaxAttempts = d.MaxAttempts
	}
	run.Scope = task.Scope
	run.ParentSessionID = core.FirstNonEmpty(parentSession(ctx), d.ParentSessionID)
	d.reserve(&slotReservation{runID: run.ID, taskID: task.ID, scope: task.Scope, agent: true})
	d.slotsMu.Unlock()

	defer release()
	defer d.dropReservation(run.ID)
	if err := d.startOne(ctx, &dispatchBatch{}, run, task); err != nil {
		d.handleStartFailure(ctx, run, err)
		out.Code = string(StartFailed)
		out.Reason = err.Error()
		return out, nil
	}
	out.RunID = run.ID
	out.State = run.State
	out.Scope = run.Scope
	return out, nil
}

// unresolvedStartDeps reports only this task's dependencies that are still
// open. An active run marks a dependency in progress, an available card marks
// it ready. An absent card is done, dropped or a pruned dangling edge, all of
// which resolve the same way in the store's dependency predicate.
func unresolvedStartDeps(task core.Task, available []core.Task, active []*core.Run) []StartBlocker {
	byID := make(map[core.TaskID]core.Task, len(available))
	for _, item := range available {
		byID[item.ID] = item
	}
	busy := activeRunsByTask(active)
	seen := make(map[core.TaskID]bool, len(task.DependsOn))
	var out []StartBlocker
	for _, dep := range task.DependsOn {
		if seen[dep] {
			continue
		}
		seen[dep] = true
		if run, ok := busy[dep]; ok && !run.State.Terminal() {
			out = append(out, StartBlocker{TaskID: dep, Status: string(core.TaskClaimed)})
			continue
		}
		if item, ok := byID[dep]; ok {
			status := string(item.Status)
			if status == "" {
				status = string(core.TaskAvailable)
			}
			out = append(out, StartBlocker{TaskID: dep, Status: status})
		}
	}
	return out
}

func formatStartBlockers(blockers []StartBlocker) string {
	parts := make([]string, 0, len(blockers))
	for _, b := range blockers {
		parts = append(parts, string(b.TaskID)+" ("+b.Status+")")
	}
	return strings.Join(parts, ", ")
}

// dispatchOpen reports whether the dispatcher may start new runs. A nil
// DispatchOpen keeps the gate open for callers that do not wire the flag.
func (d *Dispatcher) dispatchOpen() bool {
	return d.DispatchOpen == nil || *d.DispatchOpen
}

// releasedPaths returns the scope paths of recent terminal runs that no
// active run still claims. A terminal run leaves the active index, so its
// claims free up without a store write.
func (d *Dispatcher) releasedPaths(ctx context.Context, view []*core.Run) []core.ScopePath {
	recent, err := d.Store.ListRecent(ctx, waiterRecentLimit)
	if err != nil {
		return nil
	}
	claimed := make(map[core.ScopePath]struct{})
	for _, run := range view {
		if run == nil {
			continue
		}
		for _, path := range run.Scope {
			claimed[path] = struct{}{}
		}
	}
	var out []core.ScopePath
	seen := make(map[core.ScopePath]struct{})
	for _, run := range recent {
		if run == nil || !run.State.Terminal() {
			continue
		}
		for _, path := range run.Scope {
			if _, ok := claimed[path]; ok {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			out = append(out, path)
		}
	}
	return out
}

// withWaiter moves the longest waiter for released paths to the front of
// ready, so the unchanged planner selects it under the same conflict rules.
func (d *Dispatcher) withWaiter(ctx context.Context, ready []core.Task, view []*core.Run) []core.Task {
	if d.WaiterSource == nil {
		return ready
	}
	paths := d.releasedPaths(ctx, view)
	if len(paths) == 0 {
		return ready
	}
	id, err := d.WaiterSource(ctx, paths)
	if err != nil {
		slog.WarnContext(ctx, "waiter lookup", "error", err)
		return ready
	}
	if id == "" {
		return ready
	}
	for i, task := range ready {
		if task.ID != id {
			continue
		}
		if i == 0 {
			return ready
		}
		out := make([]core.Task, 0, len(ready))
		out = append(out, ready[i])
		out = append(out, ready[:i]...)
		out = append(out, ready[i+1:]...)
		return out
	}
	return ready
}

// planDispatch selects and reserves one batch under slotsMu. The reservation
// is visible to other batches before the lock is released, so none of them
// can select a reserved task or spend a reserved slot while this batch runs
// its slow steps.
func (d *Dispatcher) planDispatch(ctx context.Context, n int) (*dispatchBatch, error) {
	d.slotsMu.Lock()
	defer d.slotsMu.Unlock()

	batch := &dispatchBatch{}
	if !d.dispatchOpen() {
		return batch, nil
	}
	tasks, err := d.Source.ListAvailable(ctx)
	if err != nil {
		return nil, err
	}
	if err := d.Store.SyncTasks(ctx, tasks); err != nil {
		return nil, err
	}
	ready, err := d.Store.ListReadyTasks(ctx)
	if err != nil {
		return nil, err
	}
	stored, err := d.Store.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	view := d.withReserved(stored)
	ready = d.withWaiter(ctx, ready, view)
	sel, err := selectDispatchCandidates(d.Planner, ready, view, n, d.inFlightLimit(), d.StrictScope)
	if err != nil {
		return nil, err
	}
	batch.limit = sel.Limit
	batch.skipped = sel.Skipped
	batch.budget = d.availableFrom(view)
	d.Decisions.logDispatchCandidates(ctx, len(ready), len(stored), sel.Limit, batch.budget)
	batch.active = activeRunsByTask(view)
	agentLeft := batch.budget
	for _, task := range sel.Chosen {
		release, claimed, err := d.claimRun(ctx, task.ID)
		if err != nil {
			releaseRunClaims(batch)
			return nil, err
		}
		if !claimed {
			batch.skipped = append(batch.skipped, SkippedTask{
				TaskID: task.ID, Code: SkipActiveRun,
				Reason: "task is claimed by another dispatcher",
			})
			continue
		}
		batch.claims = append(batch.claims, release)
		run := core.NewRun(newRunID(), task.ID, time.Now().UTC())
		if d.MaxAttempts > 0 {
			run.MaxAttempts = d.MaxAttempts
		}
		run.Scope = task.Scope
		run.ParentSessionID = core.FirstNonEmpty(parentSession(ctx), d.ParentSessionID)
		agent := false
		if agentLeft > 0 {
			agent = true
			agentLeft--
		}
		d.reserve(&slotReservation{runID: run.ID, taskID: task.ID, scope: task.Scope, agent: agent})
		batch.reserved = append(batch.reserved, run.ID)
		batch.planned = append(batch.planned, plannedRun{run: run, task: task})
	}
	if n > 0 {
		addCapacitySkips(batch, ready, n, len(view))
	}
	return batch, nil
}

// activeRunsByTask indexes the active list by the task that owns each run.
// launch uses it for the per-task guard instead of one store query per task.
// Reserved runs from other batches are part of the view, so their tasks are
// guarded too.
func activeRunsByTask(active []*core.Run) map[core.TaskID]*core.Run {
	byTask := make(map[core.TaskID]*core.Run, len(active))
	for _, run := range active {
		byTask[run.TaskID] = run
	}
	return byTask
}

// dropReservation frees one committed start. Called after the run lands in the
// store or after a failed start is closed, so no slot is lost and none is
// counted twice once the store reflects the run.
func (d *Dispatcher) dropReservation(id core.RunID) {
	d.slotsMu.Lock()
	defer d.slotsMu.Unlock()
	delete(d.reserved, id)
}

// releaseReservations is the batch's safety net when a launcher step returns
// before reaching a planned run. Dropping an already removed ID is a no-op.
func (d *Dispatcher) releaseReservations(batch *dispatchBatch) {
	for _, id := range batch.reserved {
		d.dropReservation(id)
	}
}

// hasAgent reports whether run holds an agent slot. The read is under
// slotsMu, so a concurrent batch cannot claim the slot between this check and
// the start.
func (d *Dispatcher) hasAgent(runID core.RunID) bool {
	d.slotsMu.Lock()
	defer d.slotsMu.Unlock()
	res := d.reserved[runID]
	return res != nil && res.agent
}

// claimSlots re-reads the active list after a failed start and upgrades the
// next still-pending planned runs into agent slots. The check and the upgrades
// share one slotsMu critical section, so another batch cannot take a freed
// slot between the read and the upgrade.
func (d *Dispatcher) claimSlots(ctx context.Context, batch *dispatchBatch, from int) {
	d.slotsMu.Lock()
	defer d.slotsMu.Unlock()
	active, err := d.Store.ListActive(ctx)
	if err != nil {
		return
	}
	free := d.availableFrom(d.withReserved(active))
	for i := from; i < len(batch.planned) && free > 0; i++ {
		res := d.reserved[batch.planned[i].run.ID]
		if res == nil || res.agent {
			continue
		}
		res.agent = true
		free--
	}
}

func (d *Dispatcher) inFlightLimit() int {
	if d.Concurrency.MaxInFlight > 0 {
		return d.Concurrency.MaxInFlight
	}
	return 20
}

// Available is the single agent-slot operation. It counts free slots against
// MaxActiveAgents, falling back to the in-flight limit when no agent limit is
// configured. Reservations count as used slots until their runs land in the
// store. A failed active-run listing fails closed with zero slots so a
// transient store error cannot make dispatch, promotion or wake oversubscribe.
func (d *Dispatcher) Available(ctx context.Context) int {
	d.slotsMu.Lock()
	defer d.slotsMu.Unlock()
	active, err := d.Store.ListActive(ctx)
	if err != nil {
		return 0
	}
	return d.availableFrom(d.withReserved(active))
}

// AvailableFor reports the free slots visible to runID. The run being woken
// already owns a live session, so counting its own slot would make a single
// agent run unable to wake itself. Both the stored run and its wake reservation
// are excluded by ID; every other holder still counts.
func (d *Dispatcher) AvailableFor(ctx context.Context, runID core.RunID) int {
	d.slotsMu.Lock()
	defer d.slotsMu.Unlock()
	active, err := d.Store.ListActive(ctx)
	if err != nil {
		return 0
	}
	return d.availableFromExcept(d.withReserved(active), runID)
}

// modelRef resolves the worker model from the run's parent session. The MCP
// daemon has no BEACON_PARENT_SESSION, so the parent arrives per dispatch in
// the request metadata and must be resolved here, not at process start. Every
// card in a batch shares one parent, so the first lookup serves the rest.
func (d *Dispatcher) modelRef(batch *dispatchBatch, parent string) core.ModelRef {
	if d.ResolveModel == nil || parent == "" {
		return core.ModelRef{}
	}
	batch.mu.Lock()
	defer batch.mu.Unlock()
	if batch.modelRefs == nil {
		batch.modelRefs = make(map[string]core.ModelRef)
	}
	if ref, ok := batch.modelRefs[parent]; ok {
		return ref
	}
	ref := d.ResolveModel(parent)
	batch.modelRefs[parent] = ref
	return ref
}
