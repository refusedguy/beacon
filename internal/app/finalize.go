package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// finalizeExternal closes a run whose PR was merged or closed outside Beacon.
func (r *Reconciler) finalizeExternal(ctx context.Context, run *core.Run, obs core.Observation) error {
	merged := obs.PRState == "merged"
	run.State = core.RunMerged
	if !merged {
		run.State = core.RunClosed
	}
	if obs.HeadSHA != "" {
		run.HeadSHA = obs.HeadSHA
	}
	run.Reason = "pull request " + obs.PRState
	run.UpdatedAt = time.Now().UTC()
	if err := r.Store.Save(ctx, run); err != nil {
		return err
	}
	r.notifyParent(ctx, run, run.PRNumber, string(run.State))
	if merged && !run.IsFixer {
		if err := r.Source.Complete(ctx, run.TaskID); err != nil {
			slog.Error("complete externally merged task", "run", run.ID, "error", err)
		} else if err := r.Store.SetTaskStatus(ctx, run.TaskID, "done"); err != nil {
			slog.Error("set task status", "run", run.ID, "error", err)
		}
	}
	return r.cleanupRun(ctx, run)
}

func (r *Reconciler) cleanupRun(ctx context.Context, run *core.Run) error {
	if run.SessionID != "" {
		_ = r.Harness.Stop(ctx, run.SessionID)
	}
	if run.WorkspacePath == "" {
		return nil
	}
	ws := core.Workspace{Path: run.WorkspacePath, Branch: run.Branch}
	clean, err := r.Workspace.Clean(ctx, ws)
	if err != nil {
		slog.Error("clean check", "run", run.ID, "error", err)
		return nil
	}
	if !clean {
		slog.Warn("worktree kept: uncommitted changes", "run", run.ID, "path", ws.Path)
		return nil
	}
	if err := r.Workspace.Remove(ctx, ws); err != nil {
		slog.Error("remove worktree", "run", run.ID, "error", err)
	}
	return nil
}
