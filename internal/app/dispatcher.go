package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

type Dispatcher struct {
	Source          core.TaskSource
	Planner         ConflictPlanner
	Workspace       core.WorkspaceManager
	Harness         core.AgentHarness
	Store           core.RunStore
	Concurrency     core.Concurrency
	StrictScope     bool
	MaxAttempts     int
	ParentSessionID string
}

type DispatchResult struct {
	Runs     []*core.Run
	Skipped  []SkippedTask
	Failures []DispatchFailure
}

type DispatchFailure struct {
	TaskID core.TaskID
	Err    error
}

func newRunID() core.RunID {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return core.RunID(fmt.Sprintf("run-%d", time.Now().UnixNano()))
	}
	return core.RunID("run-" + hex.EncodeToString(b[:]))
}

func (d *Dispatcher) Dispatch(ctx context.Context, n int) (DispatchResult, error) {
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
	limit := n
	if free := d.inFlightLimit() - len(active); free < limit {
		limit = free
	}
	if limit <= 0 {
		return out, nil
	}
	chosen, skipped, err := d.Planner.Select(ready, active, limit, d.StrictScope)
	if err != nil {
		return out, err
	}
	out.Skipped = skipped
	budget := d.agentBudget(active)
	for _, task := range chosen {
		if existing, err := d.Store.FindActiveByTask(ctx, task.ID); err != nil {
			return out, err
		} else if existing != nil {
			out.Skipped = append(out.Skipped, SkippedTask{task.ID, "already has an active run"})
			continue
		}
		run := core.NewRun(newRunID(), task.ID, time.Now().UTC())
		if d.MaxAttempts > 0 {
			run.MaxAttempts = d.MaxAttempts
		}
		run.Scope = task.Scope
		run.ParentSessionID = firstNonEmptyLocal(d.parentSession(ctx), d.ParentSessionID)
		if err := d.Store.Save(ctx, run); err != nil {
			return out, err
		}
		if budget > 0 {
			if err := d.startOne(ctx, run, task); err != nil {
				slog.Error("dispatch task", "task", task.ID, "error", err)
				out.Failures = append(out.Failures, DispatchFailure{task.ID, err})
				_ = run.Apply(core.Event{Kind: core.EvCancel, Reason: err.Error()})
				_ = d.Store.Save(ctx, run)
				continue
			}
			budget--
		}
		out.Runs = append(out.Runs, run)
	}
	return out, nil
}

// PromotePending starts queued runs when an agent slot becomes free.
func (d *Dispatcher) PromotePending(ctx context.Context) ([]*core.Run, error) {
	active, err := d.Store.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	budget := d.agentBudget(active)
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
			slog.Error("promote pending run", "run", run.ID, "error", err)
			_ = run.Apply(core.Event{Kind: core.EvCancel, Reason: err.Error()})
			if err := d.Store.Save(ctx, run); err != nil {
				slog.Error("save cancelled run", "run", run.ID, "error", err)
			}
			continue
		}
		budget--
		started = append(started, run)
	}
	return started, nil
}

func (d *Dispatcher) startOne(ctx context.Context, run *core.Run, task core.Task) error {
	ws, err := d.Workspace.Create(ctx, task)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	run.WorkspacePath = ws.Path
	run.Branch = ws.Branch
	run.Scope = task.Scope
	session, err := d.Harness.Spawn(ctx, core.SpawnRequest{
		Task: task, Workspace: ws, RunID: run.ID,
		ParentSessionID: run.ParentSessionID, IsFixer: run.IsFixer,
		Prompt: spawnPrompt(task, run.ID, ws.Path),
	})
	if err != nil {
		_ = d.Workspace.Remove(ctx, ws)
		return fmt.Errorf("spawn: %w", err)
	}
	run.SessionID = session.ID
	if session.PR != 0 {
		run.PRNumber = session.PR
	}
	if err := run.Apply(core.Event{Kind: core.EvStart}); err != nil {
		return err
	}
	if err := d.Store.Save(ctx, run); err != nil {
		_ = d.Harness.Stop(ctx, run.SessionID)
		_ = d.Workspace.Remove(ctx, ws)
		return fmt.Errorf("save: %w", err)
	}
	if err := d.Source.Claim(ctx, task.ID); err != nil {
		return err
	}
	return d.Store.SetTaskStatus(ctx, task.ID, "claimed")
}

func (d *Dispatcher) inFlightLimit() int {
	if d.Concurrency.MaxInFlight > 0 {
		return d.Concurrency.MaxInFlight
	}
	return 20
}

func (d *Dispatcher) agentBudget(active []*core.Run) int {
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
	return limit - used
}

func (d *Dispatcher) parentSession(ctx context.Context) string {
	if v, ok := ctx.Value(parentSessionKey{}).(string); ok {
		return v
	}
	return ""
}

func firstNonEmptyLocal(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

type parentSessionKey struct{}

func WithParentSession(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, parentSessionKey{}, id)
}

func spawnPrompt(t core.Task, id core.RunID, ws string) string {
	return fmt.Sprintf("Run ID: %s\nTask: %s (%s)\nWorkspace: %s\nImplement the task, open a pull request, then call beacon_watch_pr with the run ID and PR number.\n", id, t.ID, t.Title, ws)
}
