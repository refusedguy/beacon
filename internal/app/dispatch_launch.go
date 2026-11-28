package app

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

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
