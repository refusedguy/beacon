package app

import (
	"context"
	"fmt"
	"log/slog"

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
	pr, err := r.SCM.FindOpenPRByBranch(ctx, run.Branch)
	if err == nil && pr > 0 {
		if err := run.Apply(core.Event{Kind: core.EvPRRegistered, PRNumber: pr}); err == nil {
			if err := r.Store.Save(ctx, run); err == nil {
				r.signal(pr)
			}
		}
		return
	}
	changed, err := r.Workspace.HasChanges(ctx, core.Workspace{Path: run.WorkspacePath, Branch: run.Branch}, r.BaseBranch)
	if err != nil {
		slog.Error("idle change check", "run", run.ID, "error", err)
		return
	}
	if changed {
		ok, err := r.Store.ClaimDelivery(ctx, "idle-nudge:"+string(run.ID))
		if err != nil {
			slog.Error("idle nudge key", "error", err)
			return
		}
		if ok {
			w := core.WakeContext{
				RunID: run.ID, Reason: "idle without PR", Branch: run.Branch,
				Workspace: run.WorkspacePath,
				Excerpt:   fmt.Sprintf("You finished a turn but no PR is open for branch %s. Push your changes, open the PR and call beacon_watch_pr.", run.Branch),
			}
			if err := r.Harness.Wake(ctx, run.SessionID, w); err != nil {
				slog.Error("idle nudge", "run", run.ID, "error", err)
			}
			return
		}
	}
	if err := run.Apply(core.Event{Kind: core.EvBlock, Reason: "idle without PR or changes"}); err != nil {
		return
	}
	if err := r.Store.Save(ctx, run); err != nil {
		slog.Error("idle block", "error", err)
		return
	}
	_ = r.notify(run, "idle without PR", "agent finished without opening a PR")
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
