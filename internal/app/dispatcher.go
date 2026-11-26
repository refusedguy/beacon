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

	// ensured holds the epic base branches already published in this batch.
	// Every access happens under slotsMu and each batch clears it.
	ensured map[string]struct{}

	// slotsMu makes slot acquisition atomic across dispatch and promotion. A
	// caller holds it from the Available check until every agent it starts is
	// saved as active, so a concurrent path sees the updated store state.
	slotsMu sync.Mutex

	// modelRefs caches the resolved model per parent session for one batch.
	// Every access happens under slotsMu, and each batch clears it so a config
	// reload takes effect on the next dispatch.
	modelRefs map[string]core.ModelRef
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
	d.slotsMu.Lock()
	defer d.slotsMu.Unlock()
	d.modelRefs = nil
	d.ensured = nil

	var out DispatchResult
	tasks, err := d.Source.ListAvailable(ctx)
	if err != nil {
		return out, err
	}
	if err := d.Store.SyncTasks(ctx, tasks); err != nil {
		return out, err
	}
	ready, err := d.Store.ListReadyTasks(ctx)
	if err != nil {
		return out, err
	}
	active, err := d.Store.ListActive(ctx)
	if err != nil {
		return out, err
	}
	sel, err := selectDispatchCandidates(d.Planner, ready, active, n, d.inFlightLimit(), d.StrictScope)
	if err != nil {
		return out, err
	}
	budget := d.availableFrom(active)
	d.Decisions.logDispatchCandidates(ctx, len(ready), len(active), sel.Limit, budget)
	if sel.Limit <= 0 {
		return out, nil
	}
	out.Skipped = sel.Skipped
	for _, s := range sel.Skipped {
		d.Decisions.logDispatchSkip(ctx, s)
	}
	if err := d.launch(ctx, sel.Chosen, budget, activeRunsByTask(active), &out); err != nil {
		return out, err
	}
	return out, nil
}

// activeRunsByTask indexes the active list by the task that owns each run.
// launch uses it for the per-task guard instead of one store query per task.
// ListActive is ordered oldest first, so a later entry overwrites an earlier
// one and the map holds the newest run for a task.
func activeRunsByTask(active []*core.Run) map[core.TaskID]*core.Run {
	byTask := make(map[core.TaskID]*core.Run, len(active))
	for _, run := range active {
		byTask[run.TaskID] = run
	}
	return byTask
}

// launch starts a run for each chosen task within the agent budget and adds
// skipped and failed candidates to out. active holds the runs from the single
// list Dispatch read before selection. The caller holds slotsMu from the
// budget check through this call, so the re-read after a failed start stays
// under the lock and cannot oversubscribe the agent limit.
func (d *Dispatcher) launch(ctx context.Context, chosen []core.Task, budget int, active map[core.TaskID]*core.Run, out *DispatchResult) error {
	for _, task := range chosen {
		if existing, ok := active[task.ID]; ok {
			skip := SkippedTask{
				TaskID:       task.ID,
				Code:         SkipActiveRun,
				BlockedBy:    task.ID,
				BlockedByRun: existing.ID,
				Reason:       "already has an active run",
			}
			out.Skipped = append(out.Skipped, skip)
			d.Decisions.logDispatchSkip(ctx, skip)
			continue
		}
		run := core.NewRun(newRunID(), task.ID, time.Now().UTC())
		if d.MaxAttempts > 0 {
			run.MaxAttempts = d.MaxAttempts
		}
		run.Scope = task.Scope
		run.ParentSessionID = core.FirstNonEmpty(parentSession(ctx), d.ParentSessionID)
		if budget > 0 {
			if err := d.startOne(ctx, run, task); err != nil {
				out.Failures = append(out.Failures, DispatchFailure{task.ID, err.Error()})
				d.handleStartFailure(ctx, run, err)
				// The failed run may still hold a slot when its worker survived.
				// One re-read under slotsMu refreshes both the budget and the
				// task guard before the next task starts.
				budget, active = d.refreshActive(ctx, active)
				continue
			}
			budget--
		} else {
			// A run that cannot start yet still needs a row so PromotePending
			// can find it. A started run is saved once in startOne, where the
			// session ID and the final status land in the same write.
			if err := d.Store.Save(ctx, run); err != nil {
				return err
			}
		}
		out.Runs = append(out.Runs, run)
	}
	return nil
}

// PromotePending starts queued runs when an agent slot becomes free.
func (d *Dispatcher) PromotePending(ctx context.Context) ([]*core.Run, error) {
	defer d.Decisions.flushDeferred()
	d.slotsMu.Lock()
	defer d.slotsMu.Unlock()
	d.modelRefs = nil
	d.ensured = nil

	active, err := d.Store.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	budget := d.Available(ctx)
	if budget <= 0 {
		return nil, nil
	}
	tasks, err := d.Store.ListReadyTasks(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[core.TaskID]core.Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	var started []*core.Run
	for _, run := range active {
		if budget <= 0 {
			break
		}
		if run.State != core.RunPending {
			continue
		}
		task, ok := byID[run.TaskID]
		if !ok {
			continue
		}
		if err := d.startOne(ctx, run, task); err != nil {
			d.handleStartFailure(ctx, run, err)
			continue
		}
		budget--
		started = append(started, run)
	}
	return started, nil
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

// ensureOnce publishes an epic base branch at most once per batch. Children of
// one epic share the branch, and ensureBranch starts with a remote round trip
// even when the branch is already present, so repeating it for every child is
// redundant git work. Only a successful publish is cached: a failure is retried
// by the next child instead of being remembered for the rest of the batch.
func (d *Dispatcher) ensureOnce(ctx context.Context, branch string) error {
	branch = strings.TrimSpace(branch)
	if !IsEpicBranch(branch) {
		return nil
	}
	if _, ok := d.ensured[branch]; ok {
		return nil
	}
	if err := ensureBranch(ctx, d.Workspace, branch, ""); err != nil {
		return err
	}
	if d.ensured == nil {
		d.ensured = make(map[string]struct{})
	}
	d.ensured[branch] = struct{}{}
	return nil
}

func (d *Dispatcher) startOne(ctx context.Context, run *core.Run, task core.Task) error {
	ctx = WithCycleID(ctx, string(run.ID))
	// A child card may outlive the epic creation that was supposed to publish
	// its base branch. Check again here so a missing branch never closes a run.
	if err := d.timed(ctx, "ensureBranch", func() error {
		return d.ensureOnce(ctx, task.BaseBranch)
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
			ModelRef: d.modelRef(run.ParentSessionID),
			Prompt:   spawnPrompt(task, run.ID, ws.Path, d.WorkerInstructions),
		})
		return err
	}); err != nil {
		return step("spawn", err)
	}
	run.SessionID = session.ID
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
// configured. A failed active-run listing fails closed with zero slots so a
// transient store error cannot make dispatch, promotion or wake oversubscribe.
// Callers that act on the result hold slotsMu from this check through the start.
func (d *Dispatcher) Available(ctx context.Context) int {
	active, err := d.Store.ListActive(ctx)
	if err != nil {
		return 0
	}
	return d.availableFrom(active)
}

// availableFrom applies the agent-slot rule to a list the caller already has.
// Dispatch derives its budget from the first active list instead of calling
// Available, which would read the same rows a second time.
func (d *Dispatcher) availableFrom(active []*core.Run) int {
	limit := d.Concurrency.MaxActiveAgents
	if limit <= 0 {
		limit = d.inFlightLimit()
	}
	used := 0
	for _, r := range active {
		if r.State.ActiveAgent() {
			used++
		}
	}
	free := limit - used
	if free < 0 {
		return 0
	}
	return free
}

// refreshActive re-reads the active list after a failed start and derives the
// budget and the task guard from that one read. A store error fails closed by
// zeroing the budget and keeping the last known task guard.
func (d *Dispatcher) refreshActive(ctx context.Context, active map[core.TaskID]*core.Run) (int, map[core.TaskID]*core.Run) {
	runs, err := d.Store.ListActive(ctx)
	if err != nil {
		return 0, active
	}
	return d.availableFrom(runs), activeRunsByTask(runs)
}

// modelRef resolves the worker model from the run's parent session. The MCP
// daemon has no BEACON_PARENT_SESSION, so the parent arrives per dispatch in
// the request metadata and must be resolved here, not at process start. Every
// card in a batch shares one parent, so the first lookup serves the rest.
func (d *Dispatcher) modelRef(parent string) core.ModelRef {
	if d.ResolveModel == nil || parent == "" {
		return core.ModelRef{}
	}
	if d.modelRefs == nil {
		d.modelRefs = make(map[string]core.ModelRef)
	}
	if ref, ok := d.modelRefs[parent]; ok {
		return ref
	}
	ref := d.ResolveModel(parent)
	d.modelRefs[parent] = ref
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
	prependWorkerInstructions(&b, instructions)
	fmt.Fprintf(&b, "Run ID: %s\nTask: %s (%s)\nWorkspace: %s\n", id, t.ID, t.Title, ws)
	if base := strings.TrimSpace(t.BaseBranch); base != "" {
		fmt.Fprintf(&b, "BaseBranch: %s\nOpen the pull request against %s, not the default branch.\n", base, base)
	}
	b.WriteString("Implement the task, " + core.OpenPRAndWatchInstruction + "\n")
	b.WriteString(prePRCheck)
	appendWorkerPrompt(&b, t)
	return b.String()
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
