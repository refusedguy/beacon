package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// slotReservation is one committed start. agent is set once the start holds an
// agent slot. A run still waiting for a slot reserves only an in-flight slot.
type slotReservation struct {
	runID  core.RunID
	taskID core.TaskID
	scope  []core.ScopePath
	agent  bool
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

// dispatchSelection is the result of budgeting and candidate selection. It
// holds no IO state, so both rules can be tested without a launcher.
type dispatchSelection struct {
	Limit   int
	Chosen  []core.Task
	Skipped []SkippedTask
}

// selectDispatchCandidates caps the batch at the free in-flight slots and picks
// non-overlapping candidates within that cap. It does no IO and starts no
// agents. A non-positive cap skips planning entirely.
func selectDispatchCandidates(planner ConflictPlanner, ready []core.Task, active []*core.Run, n, inFlight int, strict bool) (dispatchSelection, error) {
	limit := n
	if free := inFlight - len(active); free < limit {
		limit = free
	}
	sel := dispatchSelection{Limit: limit}
	if limit <= 0 {
		return sel, nil
	}
	chosen, skipped, err := planner.Select(ready, active, limit, strict)
	if err != nil {
		return dispatchSelection{}, err
	}
	sel.Chosen = chosen
	sel.Skipped = skipped
	return sel, nil
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
	StartFailed         StartOneCode = "start_failed"
)

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

	// Every refusal is decided before this write, so a blocked call changes no
	// card, run or status. Dispatch uses the same sync before it starts a run.
	if err := d.Store.SyncTasks(ctx, available); err != nil {
		d.slotsMu.Unlock()
		return out, err
	}
	run := core.NewRun(newRunID(), task.ID, time.Now().UTC())
	if d.MaxAttempts > 0 {
		run.MaxAttempts = d.MaxAttempts
	}
	run.Scope = task.Scope
	run.ParentSessionID = core.FirstNonEmpty(parentSession(ctx), d.ParentSessionID)
	if d.reserved == nil {
		d.reserved = make(map[core.RunID]*slotReservation)
	}
	d.reserved[run.ID] = &slotReservation{runID: run.ID, taskID: task.ID, scope: task.Scope, agent: true}
	d.slotsMu.Unlock()

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

// planDispatch selects and reserves one batch under slotsMu. The reservation
// is visible to other batches before the lock is released, so none of them
// can select a reserved task or spend a reserved slot while this batch runs
// its slow steps.
func (d *Dispatcher) planDispatch(ctx context.Context, n int) (*dispatchBatch, error) {
	d.slotsMu.Lock()
	defer d.slotsMu.Unlock()

	batch := &dispatchBatch{}
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
	sel, err := selectDispatchCandidates(d.Planner, ready, view, n, d.inFlightLimit(), d.StrictScope)
	if err != nil {
		return nil, err
	}
	batch.limit = sel.Limit
	batch.skipped = sel.Skipped
	batch.budget = d.availableFrom(view)
	d.Decisions.logDispatchCandidates(ctx, len(ready), len(stored), sel.Limit, batch.budget)
	if sel.Limit <= 0 {
		return batch, nil
	}
	batch.active = activeRunsByTask(view)
	if d.reserved == nil {
		d.reserved = make(map[core.RunID]*slotReservation)
	}
	agentLeft := batch.budget
	for _, task := range sel.Chosen {
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
		d.reserved[run.ID] = &slotReservation{runID: run.ID, taskID: task.ID, scope: task.Scope, agent: agent}
		batch.reserved = append(batch.reserved, run.ID)
		batch.planned = append(batch.planned, plannedRun{run: run, task: task})
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

// withReserved appends a virtual run for every committed start to a store
// active list. An agent reservation appears as a running run so the in-flight
// cap and the agent cap both count it. The caller holds slotsMu.
func (d *Dispatcher) withReserved(active []*core.Run) []*core.Run {
	if len(d.reserved) == 0 {
		return active
	}
	view := make([]*core.Run, 0, len(active)+len(d.reserved))
	view = append(view, active...)
	for _, res := range d.reserved {
		r := core.NewRun(res.runID, res.taskID, time.Time{})
		r.Scope = res.scope
		if res.agent {
			r.State = core.RunRunning
			r.HoldAgent()
		}
		view = append(view, r)
	}
	return view
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

// launch starts the reserved runs without holding slotsMu. Runs holding an
// agent slot start in one wave so their Create and Spawn calls overlap. Each
// goroutine writes only its own result slot; the caller merges the slots in
// planned order after the wave, which keeps the result deterministic without a
// shared lock on out. A failed start frees its agent slot, so claimSlots runs
// once after the wave rather than once per failure. Pending runs only persist
// a row and stay on the caller, so a batch with one agent slot does not fan
// out store writes.
func (d *Dispatcher) launch(ctx context.Context, batch *dispatchBatch, out *DispatchResult) error {
	type startResult struct {
		out    DispatchResult
		failed bool
		err    error
	}
	results := make([]startResult, len(batch.planned))
	var wg sync.WaitGroup
	for i, p := range batch.planned {
		if !d.hasAgent(p.run.ID) {
			results[i].failed, results[i].err = d.launchOne(ctx, batch, p, &results[i].out)
			continue
		}
		wg.Add(1)
		go func(i int, p plannedRun) {
			defer wg.Done()
			results[i].failed, results[i].err = d.launchOne(ctx, batch, p, &results[i].out)
		}(i, p)
	}
	wg.Wait()

	var firstErr error
	anyFailed := false
	for i := range results {
		r := &results[i]
		out.Skipped = append(out.Skipped, r.out.Skipped...)
		out.Failures = append(out.Failures, r.out.Failures...)
		out.Runs = append(out.Runs, r.out.Runs...)
		if firstErr == nil && r.err != nil {
			firstErr = r.err
		}
		if r.failed {
			anyFailed = true
		}
	}
	if firstErr != nil {
		return firstErr
	}
	if anyFailed {
		d.claimSlots(ctx, batch, 0)
	}
	return nil
}

// launchOne returns failed when a start failed, so launch can hand the freed
// slot to a later run in the batch.
func (d *Dispatcher) launchOne(ctx context.Context, batch *dispatchBatch, p plannedRun, out *DispatchResult) (bool, error) {
	run, task := p.run, p.task
	defer d.dropReservation(run.ID)
	if existing, ok := batch.active[task.ID]; ok {
		skip := SkippedTask{
			TaskID:       task.ID,
			Code:         SkipActiveRun,
			BlockedBy:    task.ID,
			BlockedByRun: existing.ID,
			Reason:       "already has an active run",
		}
		out.Skipped = append(out.Skipped, skip)
		d.Decisions.logDispatchSkip(ctx, skip)
		return false, nil
	}
	if d.hasAgent(run.ID) {
		if err := d.startOne(ctx, batch, run, task); err != nil {
			out.Failures = append(out.Failures, DispatchFailure{task.ID, err.Error()})
			d.handleStartFailure(ctx, run, err)
			return true, nil
		}
	} else {
		// A run that cannot start yet still needs a row so PromotePending
		// can find it. A started run is saved once in startOne, where the
		// session ID and the final status land in the same write.
		if err := d.Store.Save(ctx, run); err != nil {
			return false, err
		}
	}
	out.Runs = append(out.Runs, run)
	return false, nil
}

// PromotePending starts queued runs when an agent slot becomes free.
func (d *Dispatcher) PromotePending(ctx context.Context) ([]*core.Run, error) {
	defer d.Decisions.flushDeferred()

	batch, err := d.planPromotion(ctx)
	if err != nil {
		return nil, err
	}
	defer d.releaseReservations(batch)
	var started []*core.Run
	for _, p := range batch.planned {
		if d.promoteOne(ctx, batch, p.run, p.task) {
			started = append(started, p.run)
		}
	}
	return started, nil
}

// planPromotion reserves the pending runs it will start under slotsMu, so a
// concurrent dispatch or promotion sees them as active before the slow steps.
func (d *Dispatcher) planPromotion(ctx context.Context) (*dispatchBatch, error) {
	d.slotsMu.Lock()
	defer d.slotsMu.Unlock()

	batch := &dispatchBatch{}
	stored, err := d.Store.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	view := d.withReserved(stored)
	budget := d.availableFrom(view)
	if budget <= 0 {
		return batch, nil
	}
	tasks, err := d.Store.ListReadyTasks(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[core.TaskID]core.Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	if d.reserved == nil {
		d.reserved = make(map[core.RunID]*slotReservation)
	}
	for _, run := range stored {
		if budget <= 0 {
			break
		}
		if run.State != core.RunPending {
			continue
		}
		if _, taken := d.reserved[run.ID]; taken {
			continue
		}
		task, ok := byID[run.TaskID]
		if !ok {
			continue
		}
		d.reserved[run.ID] = &slotReservation{runID: run.ID, taskID: task.ID, scope: task.Scope, agent: true}
		batch.reserved = append(batch.reserved, run.ID)
		batch.planned = append(batch.planned, plannedRun{run: run, task: task})
		budget--
	}
	return batch, nil
}

// promoteOne starts one reserved pending run. Its reservation is freed once
// the start lands in the store or is closed after a failure.
func (d *Dispatcher) promoteOne(ctx context.Context, batch *dispatchBatch, run *core.Run, task core.Task) bool {
	defer d.dropReservation(run.ID)
	if err := d.startOne(ctx, batch, run, task); err != nil {
		d.handleStartFailure(ctx, run, err)
		return false
	}
	return true
}

// timed runs one named startOne step and records its duration. A nil Spans
// skips the clock entirely. The debug record carries the cycle ID so a span
// can be matched to the dispatch that produced it.
func (d *Dispatcher) timed(ctx context.Context, name string, fn func() error) error {
	if d.Spans == nil {
		return fn()
	}
	start := time.Now()
	err := fn()
	spent := time.Since(start)
	d.Spans.observe(name, spent)
	slog.DebugContext(ctx, "dispatch.span", "step", name,
		"cycle_id", cycleID(ctx), "elapsed_ms", float64(spent.Microseconds())/1000)
	return err
}

// waitEnsure joins this batch's publish of an epic base branch. Children of
// one epic share the branch, and ensureBranch starts with a remote round trip
// even when the branch is already present, so repeating it for every child is
// redundant git work. The leader runs the publish; followers wait here, before
// their timed step starts, so a follower's span measures only the cache hit.
func (d *Dispatcher) waitEnsure(batch *dispatchBatch, branch string) *branchEnsure {
	branch = strings.TrimSpace(branch)
	if !IsEpicBranch(branch) {
		return &branchEnsure{}
	}
	batch.mu.Lock()
	if _, ok := batch.ensured[branch]; ok {
		batch.mu.Unlock()
		return &branchEnsure{}
	}
	if tok, ok := batch.ensuring[branch]; ok {
		batch.mu.Unlock()
		<-tok.done
		// The close publishes tok.err. Return a follower token so only the
		// original leader can run and close the publish again.
		return &branchEnsure{err: tok.err}
	}
	tok := &branchEnsure{branch: branch, done: make(chan struct{}), leader: true}
	if batch.ensuring == nil {
		batch.ensuring = make(map[string]*branchEnsure)
	}
	batch.ensuring[branch] = tok
	batch.mu.Unlock()
	return tok
}

// publish runs the leader's ensureBranch and wakes the followers. A failed
// publish is not cached, so a later start retries it.
func (d *Dispatcher) publish(ctx context.Context, batch *dispatchBatch, tok *branchEnsure) error {
	if !tok.leader {
		return tok.err
	}
	err := ensureBranch(ctx, d.Workspace, tok.branch, "")
	batch.mu.Lock()
	if err == nil {
		if batch.ensured == nil {
			batch.ensured = make(map[string]struct{})
		}
		batch.ensured[tok.branch] = struct{}{}
	}
	delete(batch.ensuring, tok.branch)
	tok.err = err
	close(tok.done)
	batch.mu.Unlock()
	return err
}

func (d *Dispatcher) startOne(ctx context.Context, batch *dispatchBatch, run *core.Run, task core.Task) error {
	ctx = WithCycleID(ctx, string(run.ID))
	// A child card may outlive the epic creation that was supposed to publish
	// its base branch. Check again here so a missing branch never closes a run.
	tok := d.waitEnsure(batch, task.BaseBranch)
	if err := d.timed(ctx, "ensureBranch", func() error {
		return d.publish(ctx, batch, tok)
	}); err != nil {
		return step("base branch", err)
	}
	var ws core.Workspace
	if err := d.timed(ctx, "Create", func() (err error) {
		ws, err = d.Workspace.Create(ctx, task)
		return err
	}); err != nil {
		return step("workspace", err)
	}
	run.WorkspacePath = ws.Path
	run.Branch = ws.Branch
	run.Scope = task.Scope
	if err := d.timed(ctx, "MarkInWorkspace", func() error {
		return d.Source.MarkInWorkspace(ctx, task.ID, ws.Path, "in_progress")
	}); err != nil {
		return step("mark in workspace", err)
	}
	var session core.Session
	if err := d.timed(ctx, "Spawn", func() (err error) {
		session, err = d.Harness.Spawn(ctx, core.SpawnRequest{
			Task: task, Workspace: ws, RunID: run.ID,
			ParentSessionID: run.ParentSessionID, IsFixer: run.IsFixer,
			ModelRef: d.modelRef(batch, run.ParentSessionID),
			Prompt:   spawnPrompt(task, run.ID, ws.Path, d.WorkerInstructions),
		})
		return err
	}); err != nil {
		return step("spawn", err)
	}
	run.SessionID = session.ID
	run.HoldAgent()
	if session.PR != 0 {
		run.PRNumber = session.PR
		d.Decisions.logTransitionWatchPR(ctx, run.ID, session.PR)
	}
	if err := run.Apply(core.Event{Kind: core.EvStart}); err != nil {
		return err
	}
	if err := d.timed(ctx, "Save", func() error {
		return d.Store.Save(ctx, run)
	}); err != nil {
		// The session is live but no row records it. Stop the worker and drop
		// its worktree so no live session is left without a run row.
		d.stopUnsavedWorker(ctx, run, session.ID, ws)
		return step("save", err)
	}
	d.Decisions.logTransitionSpawn(ctx, run.ID, task.ID, ws.Branch)
	// MarkInWorkspace already sets in_progress inside the worker branch, and
	// the active-run guard blocks a second run. Claiming through the root
	// source would only rewrite the card on the operator's main checkout,
	// leaving it dirty for the next rebase. SQLite is the authoritative claim.
	return d.timed(ctx, "SetTaskStatus", func() error {
		return d.Store.SetTaskStatus(ctx, task.ID, "claimed")
	})
}

// stopUnsavedWorker cleans up a spawn whose final save failed. The run has no
// row, so the session and worktree are the only trace left. Clearing the IDs
// also keeps handleStartFailure from trying to save a live session again.
func (d *Dispatcher) stopUnsavedWorker(ctx context.Context, run *core.Run, sessionID string, ws core.Workspace) {
	bestEffort("stop unsaved worker", func() error {
		return d.Harness.Stop(ctx, sessionID)
	}, "run", run.ID, "task", run.TaskID)
	bestEffort("force remove unsaved worktree", func() error {
		return d.Workspace.ForceRemove(ctx, ws)
	}, "run", run.ID, "task", run.TaskID)
	run.ReleaseAgent()
	run.SessionID = ""
	run.WorkspacePath = ""
}

// handleStartFailure closes a run only after its worker is gone. A worker that
// survived the failure can still open a PR, and a closed run cannot receive it.
func (d *Dispatcher) handleStartFailure(ctx context.Context, run *core.Run, cause error) {
	logStep("dispatch task", run, cause)
	if d.workerAlive(ctx, run) {
		if run.State == core.RunPending {
			// The worker is alive and the cause is recorded on the run. A newer
			// event may already have moved it out of pending, so this is best effort.
			if err := run.Apply(core.Event{Kind: core.EvStart}); err != nil {
				logStep("start live run", run, err)
			}
		}
		run.Reason = cause.Error()
		run.UpdatedAt = time.Now().UTC()
		if err := d.Store.Save(ctx, run); err != nil {
			logStep("save live run", run, err)
			// A live worker with no row would hold a slot no one can see.
			d.stopUnsavedWorker(ctx, run, run.SessionID, core.Workspace{Path: run.WorkspacePath, Branch: run.Branch})
		}
		return
	}
	// A failed cancel transition means the run already reached a terminal state
	// or moved on. The cause is logged above; the transition error is not silent.
	if err := run.Apply(core.Event{Kind: core.EvCancel, Reason: cause.Error()}); err != nil {
		logStep("cancel failed start", run, err)
	}
	if err := d.Store.Save(ctx, run); err != nil {
		logStep("save cancelled run", run, err)
	}
	if run.SessionID != "" {
		if err := d.Harness.Stop(ctx, run.SessionID); err != nil {
			logStep("stop failed worker", run, err)
		}
	}
	if run.WorkspacePath != "" {
		// A failed spawn can leave the card rewritten to in_progress inside the
		// worktree, making it dirty. A plain Remove would refuse it and leave a
		// stale path that blocks the next dispatch, so force the removal.
		if err := d.Workspace.ForceRemove(ctx, core.Workspace{Path: run.WorkspacePath, Branch: run.Branch}); err != nil {
			logStep("force remove failed start worktree", run, err)
		}
	}
}

// workerAlive treats a failed liveness check as alive. Losing the session
// briefly must not close a run whose worker may still be publishing a PR.
func (d *Dispatcher) workerAlive(ctx context.Context, run *core.Run) bool {
	if run.SessionID == "" {
		return false
	}
	alive, err := d.Harness.IsAlive(ctx, run.SessionID)
	if err != nil {
		slog.Warn("worker liveness check", "run", run.ID, "error", err)
		return true
	}
	return alive
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

// ReserveForWake checks and reserves one slot for runID inside a single
// slotsMu section. Two concurrent wakes cannot both pass the same free-slot
// check. The caller releases the reservation with ReleaseWake once the wake or
// the injected spawn has landed a run row.
func (d *Dispatcher) ReserveForWake(ctx context.Context, runID core.RunID) bool {
	d.slotsMu.Lock()
	defer d.slotsMu.Unlock()
	active, err := d.Store.ListActive(ctx)
	if err != nil {
		return false
	}
	if d.availableFromExcept(d.withReserved(active), runID) <= 0 {
		return false
	}
	if d.reserved == nil {
		d.reserved = make(map[core.RunID]*slotReservation)
	}
	if _, held := d.reserved[runID]; !held {
		d.reserved[runID] = &slotReservation{runID: runID, agent: true}
	}
	return true
}

// ReleaseWake frees a reservation taken by ReserveForWake. Dropping an ID that
// was already released is a no-op, so a caller can defer it safely.
func (d *Dispatcher) ReleaseWake(_ context.Context, runID core.RunID) {
	d.dropReservation(runID)
}

// availableFrom applies the agent-slot rule to a list the caller already has.
// Dispatch derives its budget from the same active view used for selection
// instead of calling Available, which would read the store a second time.
func (d *Dispatcher) availableFrom(active []*core.Run) int {
	return d.availableFromExcept(active, "")
}

// availableFromExcept is availableFrom with one run omitted from the used
// count. An empty except keeps the ordinary whole-view count.
func (d *Dispatcher) availableFromExcept(active []*core.Run, except core.RunID) int {
	limit := d.Concurrency.MaxActiveAgents
	if limit <= 0 {
		limit = d.inFlightLimit()
	}
	used := 0
	for _, r := range active {
		if r == nil || (except != "" && r.ID == except) {
			continue
		}
		// A persisted session flag keeps a woken run in waiting_ci counted
		// while its worker is still alive. The state check covers rows written
		// before the flag existed and in-flight reservations.
		if r.HoldsAgent() || r.State.ActiveAgent() {
			used++
		}
	}
	free := limit - used
	if free < 0 {
		return 0
	}
	return free
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

// prePRCheck is appended to the stock worker prompt before the task card's
// override. It makes a known formatting or test failure stop the worker before
// it opens a PR, so CI does not spend a fix round on it. The Go check commands
// are concrete; the verification rule they serve comes from pkg/core so the
// spawn prompt cannot contradict the config default or the init instructions.
const prePRCheck = `Before opening a pull request, run the local format and test check.

Run: gofmt -l <changed Go files>
The command must print nothing. If it prints a file, format that file and run it again.

Run: go test <changed Go packages>
The command must pass. If a test fails, fix it and run the command again.

If gofmt -l still prints a file or a targeted test still fails, stop. Do not open a pull request. Report the exact command you ran and its full output, then stop.

` + core.WeakMachineVerificationRule + `
The gofmt -l and targeted go test checks are required on every machine.
`

func spawnPrompt(t core.Task, id core.RunID, ws, instructions string) string {
	var b strings.Builder
	// Reserve the fixed text plus the variable fields in one allocation. The
	// stock prompt is a few kilobytes, so growing from zero reallocates several
	// times for every dispatch.
	b.Grow(len(prePRCheck) + len(core.OpenPRAndWatchInstruction) + len(instructions) +
		len(id) + len(t.ID) + len(t.Title) + len(ws) +
		len(t.BaseBranch) + len(t.WorkerPrompt) + 256)
	prependWorkerInstructions(&b, instructions)
	b.WriteString("Run ID: ")
	b.WriteString(string(id))
	b.WriteString("\nTask: ")
	b.WriteString(string(t.ID))
	b.WriteString(" (")
	b.WriteString(t.Title)
	b.WriteString(")\nWorkspace: ")
	b.WriteString(ws)
	b.WriteString("\n")
	if base := strings.TrimSpace(t.BaseBranch); base != "" {
		b.WriteString("BaseBranch: ")
		b.WriteString(base)
		b.WriteString("\nOpen the pull request against ")
		b.WriteString(base)
		b.WriteString(", not the default branch.\n")
	}
	b.WriteString("Implement the task, " + core.OpenPRAndWatchInstruction + "\n")
	b.WriteString(prePRCheck)
	appendWorkerPrompt(&b, t)
	return b.String()
}

// BuildSpawnPrompt builds the prompt a dispatched task receives. The bench
// harness calls it to time prompt construction without running a Dispatcher.
func BuildSpawnPrompt(t core.Task, id core.RunID, ws, instructions string) string {
	return spawnPrompt(t, id, ws, instructions)
}

// prependWorkerInstructions puts the operator's worker instructions at the
// start of a spawn prompt. An empty value leaves the stock prompt unchanged.
func prependWorkerInstructions(b *strings.Builder, instructions string) {
	text := strings.TrimSpace(instructions)
	if text == "" {
		return
	}
	b.WriteString(text)
	b.WriteString("\n\n")
}

// appendWorkerPrompt adds a card's worker_prompt after the global instructions.
// A task without an override keeps the stock prompt.
func appendWorkerPrompt(b *strings.Builder, t core.Task) {
	if override := strings.TrimSpace(t.WorkerPrompt); override != "" {
		b.WriteString("\nWorker prompt from task card:\n")
		b.WriteString(override)
		b.WriteString("\n")
	}
}
