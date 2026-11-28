package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// idleHead is the observation identity used until a PR is registered. A run
// without a PR has no head SHA, so one stable token stands in for its head.
func idleHead(run *core.Run) string {
	if run.HeadSHA != "" {
		return run.HeadSHA
	}
	return core.IdleHead
}

// idleNudgeKey binds the claim to one run and one observed head. The claim
// survives later idle turns on that head, so the same instruction is delivered
// once instead of once per turn.
func idleNudgeKey(run *core.Run) string {
	return fmt.Sprintf("idle-nudge:%s:%s", run.ID, idleHead(run))
}

// idleObservation gives the shared wake guards an identity for an idle turn.
func idleObservation(run *core.Run) core.Observation {
	return core.Observation{
		HeadSHA: idleHead(run),
		BaseSHA: run.ObservedBaseSHA,
		FailureExcerpt: fmt.Sprintf(
			"You finished a turn but no PR is open for branch %s. Push your changes, %s",
			run.Branch, core.OpenPRAndWatchInstruction),
	}
}

// releaseIdleNudge drops the claim for a run whose idle turn has ended. A
// state change or a wake failure must not leave a claim that blocks later work.
func (r *Reconciler) releaseIdleNudge(ctx context.Context, run *core.Run) {
	if r.Store == nil || run == nil {
		return
	}
	if err := r.Store.ReleaseDelivery(ctx, idleNudgeKey(run)); err != nil {
		logStep("release idle nudge key", run, err)
	}
}

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
	if run == nil {
		return
	}
	if run.State != core.RunRunning || run.PRNumber != 0 {
		r.releaseIdleNudge(ctx, run)
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
		logStep("idle change check", run, err)
		return
	}
	if changed {
		r.nudgeIdleRun(ctx, run)
		return
	}
	r.blockIdle(ctx, run, "idle without PR or changes")
}

// nudgeIdleRun delivers one no-PR instruction per head. The claim stays in
// place after delivery, so a second idle turn blocks instead of waking again.
func (r *Reconciler) nudgeIdleRun(ctx context.Context, run *core.Run) {
	key := idleNudgeKey(run)
	ok, err := r.Store.ClaimDelivery(ctx, key)
	if err != nil {
		logStep("idle nudge key", run, err)
		return
	}
	if !ok {
		r.blockIdle(ctx, run, "idle without PR: nudge already delivered")
		return
	}
	wakeErr := r.wakeWithEvent(ctx, run, idleObservation(run), "idle without PR", core.EvIdleNudge, core.WakeIdle)
	if wakeErr != nil {
		logStep("idle nudge", run, wakeErr)
		if err := r.Store.ReleaseDelivery(ctx, key); err != nil {
			logStep("release idle nudge key", run, err)
		}
		return
	}
	if run.State != core.RunRunning {
		r.releaseIdleNudge(ctx, run)
	}
}

// blockIdle ends an idle loop with a reason the operator can see. The claim
// goes with it because the run no longer occupies the state that earned it.
func (r *Reconciler) blockIdle(ctx context.Context, run *core.Run, reason string) {
	if err := run.Apply(core.Event{Kind: core.EvBlock, Reason: reason}); err != nil {
		logStep("idle block transition", run, err)
		return
	}
	if err := r.Store.Save(ctx, run); err != nil {
		logStep("idle block", run, err)
		return
	}
	r.releaseIdleNudge(ctx, run)
	if err := r.notify(run, "idle without PR", reason); err != nil {
		logStep("idle block notify", run, err)
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
	r.releaseIdleNudge(ctx, run)
	r.signal(pr)
	return true, nil
}

// reconcileOrphanRunning binds missing PRs and closes runs whose worker
// session is gone. Every nonterminal run with a session is inspected: a
// PR-bound run in waiting_ci can hold a slot just as a running run can. A live
// session and a failed liveness check are both left alone; only a confirmed
// dead session frees its slot.
func (r *Reconciler) reconcileOrphanRunning(ctx context.Context, runs []*core.Run) {
	for _, run := range runs {
		if run == nil || run.State.Terminal() || run.SessionID == "" {
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
	// The liveness check confirmed the session is gone, so it no longer holds
	// an agent slot regardless of the state it leaves behind.
	run.ReleaseAgent()
	if err := run.Apply(core.Event{Kind: core.EvCancel, Reason: reason}); err != nil {
		slog.Error("orphan close", "run", run.ID, "error", err)
		return
	}
	if err := r.Store.Save(ctx, run); err != nil {
		slog.Error("orphan close save", "run", run.ID, "error", err)
		return
	}
	r.releaseIdleNudge(ctx, run)
	r.notifyParent(ctx, run, run.PRNumber, reason)
	_ = r.notify(run, "orphan run closed", reason)
	bestEffort("orphan cleanup", func() error {
		return r.cleanupRun(ctx, run)
	}, "run", run.ID)
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
