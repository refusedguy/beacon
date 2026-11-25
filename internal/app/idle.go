package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// HandleHarnessEvent attaches a PR by branch name when an agent finishes a turn.
func (r *Reconciler) HandleHarnessEvent(ctx context.Context, ev core.HarnessEvent) {
	if ev.Kind != "idle" || ev.RunID == "" {
		return
	}
	run, err := r.Store.Get(ctx, ev.RunID)
	if err != nil {
		slog.Error("idle run lookup", "error", err)
		return
	}
	if run == nil || run.State != core.RunRunning || run.PRNumber != 0 {
		return
	}
	bound, bindErr := r.bindOpenPR(ctx, run)
	if bindErr != nil {
		slog.Error("idle PR lookup", "run", run.ID, "error", bindErr)
	}
	if bound {
		return
	}
	changed, err := r.Workspace.HasChanges(ctx, core.Workspace{Path: run.WorkspacePath, Branch: run.Branch}, r.BaseBranch)
	if err != nil {
		slog.Error("idle change check", "run", run.ID, "task", run.TaskID, "error", err)
		return
	}
	if changed {
		key := "idle-nudge:" + string(run.ID)
		ok, err := r.Store.ClaimDelivery(ctx, key)
		if err != nil {
			slog.Error("idle nudge key", "run", run.ID, "task", run.TaskID, "error", err)
			return
		}
		if ok {
			w := core.WakeContext{
				RunID: run.ID, Reason: "idle without PR", Branch: run.Branch,
				Workspace: run.WorkspacePath,
				Excerpt:   fmt.Sprintf("You finished a turn but no PR is open for branch %s. Push your changes, open the PR and call beacon_watch_pr.", run.Branch),
			}
			wakeErr := r.Harness.Wake(ctx, run.SessionID, w)
			// The claim guards one idle event, not the run's lifetime. Release it
			// so a later idle turn can nudge again instead of blocking the run.
			if err := r.Store.ReleaseDelivery(ctx, key); err != nil {
				slog.Error("release idle nudge key", "run", run.ID, "task", run.TaskID, "error", err)
			}
			if wakeErr != nil {
				slog.Error("idle nudge", "run", run.ID, "task", run.TaskID, "error", wakeErr)
			}
			return
		}
	}
	if err := run.Apply(core.Event{Kind: core.EvBlock, Reason: "idle without PR or changes"}); err != nil {
		slog.Error("idle block transition", "run", run.ID, "task", run.TaskID, "error", err)
		return
	}
	if err := r.Store.Save(ctx, run); err != nil {
		slog.Error("idle block", "run", run.ID, "task", run.TaskID, "error", err)
		return
	}
	if err := r.notify(run, "idle without PR", "agent finished without opening a PR"); err != nil {
		slog.Error("idle block notify", "run", run.ID, "task", run.TaskID, "error", err)
	}
}

// bindOpenPR records an open PR for a running run that has none. Agents can
// open a PR without calling beacon_watch_pr, so the branch name is the link.
func (r *Reconciler) bindOpenPR(ctx context.Context, run *core.Run) (bool, error) {
	if r.SCM == nil || run.PRNumber != 0 || run.Branch == "" {
		return false, nil
	}
	pr, err := r.SCM.FindOpenPRByBranch(ctx, run.Branch)
	if err != nil {
		// No open PR is the expected state for a run that has not pushed
		// yet. Only a real lookup failure should surface as an error.
		if core.CodeOf(err) == core.CodeNotFound {
			return false, nil
		}
		return false, err
	}
	if pr <= 0 {
		return false, nil
	}
	if err := run.Apply(core.Event{Kind: core.EvPRRegistered, PRNumber: pr}); err != nil {
		return false, err
	}
	if err := r.Store.Save(ctx, run); err != nil {
		return false, err
	}
	r.signal(pr)
	return true, nil
}

// reconcileOrphanRunning binds missing PRs and closes running runs whose
// worker session is gone. A live session and a failed liveness check are both
// left alone; only a confirmed dead session frees its slot.
func (r *Reconciler) reconcileOrphanRunning(ctx context.Context, runs []*core.Run) {
	for _, run := range runs {
		if run.State != core.RunRunning || run.PRNumber != 0 {
			continue
		}
		r.reconcileOneOrphan(ctx, run)
	}
}

func (r *Reconciler) reconcileOneOrphan(ctx context.Context, run *core.Run) {
	bound, err := r.bindOpenPR(ctx, run)
	if err != nil {
		slog.Error("orphan PR lookup", "run", run.ID, "error", err)
		return
	}
	if bound || r.Harness == nil || run.SessionID == "" {
		return
	}
	if time.Since(run.UpdatedAt) <= r.orphanTimeout() {
		return
	}
	alive, err := r.Harness.IsAlive(ctx, run.SessionID)
	if err != nil || alive {
		return
	}
	r.closeOrphan(ctx, run)
}

func (r *Reconciler) closeOrphan(ctx context.Context, run *core.Run) {
	reason := "worker session exited without PR"
	if err := run.Apply(core.Event{Kind: core.EvCancel, Reason: reason}); err != nil {
		slog.Error("orphan close", "run", run.ID, "error", err)
		return
	}
	if err := r.Store.Save(ctx, run); err != nil {
		slog.Error("orphan close save", "run", run.ID, "error", err)
		return
	}
	r.notifyParent(ctx, run, run.PRNumber, string(run.State))
	_ = r.notify(run, "orphan run closed", reason)
	if err := r.cleanupRun(ctx, run); err != nil {
		slog.Error("orphan cleanup", "run", run.ID, "error", err)
	}
}

func (r *Reconciler) orphanTimeout() time.Duration {
	if r.OrphanTimeout > 0 {
		return r.OrphanTimeout
	}
	return defaultOrphanTimeout
}

func (r *Reconciler) signal(pr int) {
	if r.Changes == nil {
		return
	}
	select {
	case r.Changes <- pr:
	default:
	}
}
