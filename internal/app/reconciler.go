package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

type Reconciler struct {
	Store           core.RunStore
	SCM             core.ScmGateway
	Policy          core.MergePolicy
	Harness         core.AgentHarness
	Source          core.TaskSource
	Workspace       core.WorkspaceManager
	Notifier        core.Notifier
	Dispatcher      *Dispatcher
	Changes         chan<- int
	Repo            string
	BaseBranch      string
	ParentSessionID string
	BaseRedMode     string
	MaxFixTries     int
	Interval        time.Duration
	Events          <-chan core.HarnessEvent
	AutoRebase      bool
}

func (r *Reconciler) Reconcile(ctx context.Context, pr int) (decision core.MergeDecision, err error) {
	run, err := r.Store.FindByPR(ctx, pr)
	if err != nil {
		return core.MergeDecision{}, err
	}
	if run == nil || run.State.Terminal() {
		return core.MergeDecision{Verdict: core.VerdictNoop}, nil
	}
	ctx = WithCycleID(ctx, string(run.ID))
	obs, err := r.SCM.Observe(ctx, pr)
	if err != nil {
		return core.MergeDecision{}, err
	}
	obs.PRNumber = pr
	defer logReconcileDecisionDeferred(ctx, pr, run, obs, &decision)
	switch strings.ToLower(obs.PRState) {
	case "merged", "closed", "locked":
		return core.MergeDecision{Verdict: core.VerdictWait, Reason: "pull request " + obs.PRState}, r.finalizeExternal(ctx, run, obs)
	}
	if ok, err := r.Store.Approved(ctx, run.ID, obs.HeadSHA); err != nil {
		return core.MergeDecision{}, err
	} else if ok {
		obs.Approvals++
	}
	decision = r.Policy.Evaluate(obs)
	if run.PRNumber == 0 {
		run.PRNumber = pr
	}

	switch decision.Verdict {
	case core.VerdictNoop:
		return decision, nil
	case core.VerdictMerge:
		return r.mergeIfReviewed(ctx, run, obs, decision)
	case core.VerdictNeedsRebase:
		if !r.AutoRebase {
			return core.MergeDecision{Verdict: core.VerdictWait, Reason: "auto_rebase disabled"}, nil
		}
		if !obs.Conflict && len(obs.Checks) > 0 && obs.AllRequiredPass(nil) {
			// Green checks mean the new base commits did not reach the PR scope.
			if run.State == core.RunNeedsAgentFix {
				run.ObserveSHA(obs.HeadSHA, obs.BaseSHA)
				return decision, r.applyAndSave(ctx, run, core.Event{Kind: core.EvChecksPassed, HeadSHA: obs.HeadSHA})
			}
			// A behind PR that GitHub still reports mergeable can merge as-is. Re-evaluate
			// without the behind invariant so approval rules still apply before merging.
			if run.State == core.RunWaitingCI {
				mergeable := obs
				mergeable.Behind = false
				if merged := r.Policy.Evaluate(mergeable); merged.Verdict == core.VerdictMerge {
					run.ObserveSHA(obs.HeadSHA, obs.BaseSHA)
					return r.mergeIfReviewed(ctx, run, obs, merged)
				}
			}
			return decision, nil
		}
		if sameObservation(run, obs) {
			return decision, nil
		}
		if run.State == core.RunWaitingCI || run.State == core.RunRunning {
			if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvBehind, Reason: decision.Reason}); err != nil {
				return decision, err
			}
		}
		return decision, r.rebase(ctx, run, obs)
	case core.VerdictNeedsAgentFix:
		if sameObservation(run, obs) {
			return decision, nil
		}
		if run.State == core.RunWaitingCI || run.State == core.RunRunning {
			if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvChecksFailed, Reason: decision.Reason}); err != nil {
				return decision, err
			}
		}
		if err := r.checkBaseRed(ctx, run, obs, decision.Reason); err != nil {
			return decision, err
		}
		fresh, err := r.Store.Get(ctx, run.ID)
		if err != nil {
			return decision, err
		}
		if fresh != nil && fresh.State == core.RunBlocked {
			return decision, nil
		}
		if fresh != nil {
			run = fresh
		}
		return decision, r.wake(ctx, run, obs, decision.Reason)
	case core.VerdictBlocked:
		if run.State != core.RunBlocked {
			if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvBlock, Reason: decision.Reason}); err != nil {
				return decision, err
			}
		}
		return decision, r.notify(run, "blocked", decision.Reason)
	default:
		if run.State == core.RunWaitingCI || run.State == core.RunRunning {
			if err := run.Apply(core.Event{Kind: core.EvChecksPending, HeadSHA: obs.HeadSHA}); err != nil {
				return decision, err
			}
			if run.ObserveSHA(obs.HeadSHA, obs.BaseSHA) {
				return decision, r.Store.Save(ctx, run)
			}
			return decision, nil
		}
		return decision, nil
	}
}

func (r *Reconciler) applyAndSave(ctx context.Context, run *core.Run, e core.Event, observed ...string) error {
	if err := run.Apply(e); err != nil {
		return err
	}
	if len(observed) == 2 {
		recordObservation(run, observed[0], observed[1])
	}
	return r.Store.Save(ctx, run)
}

func (r *Reconciler) merge(ctx context.Context, run *core.Run, obs core.Observation) error {
	_, isEpic := r.epicTask(ctx, run.TaskID)
	if !run.IsFixer && !isEpic {
		if err := r.Source.MarkInWorkspace(ctx, run.TaskID, run.WorkspacePath, "done"); err != nil {
			return err
		}
		ws := core.Workspace{Path: run.WorkspacePath, Branch: run.Branch}
		message := "beacon: complete task " + string(run.TaskID)
		if err := r.Workspace.CommitPath(ctx, ws, "docs/tasks", message); err != nil {
			return err
		}
		fresh, err := r.SCM.Observe(ctx, obs.PRNumber)
		if err != nil {
			return err
		}
		obs.HeadSHA = fresh.HeadSHA
	}
	key := fmt.Sprintf("merge:%d:%s", obs.PRNumber, obs.HeadSHA)
	logTransitionMerge(ctx, run.ID, obs.PRNumber, "attempt")
	if err := r.SCM.Merge(ctx, obs.PRNumber, obs.HeadSHA, string(run.TaskID)); err != nil {
		return err
	}
	if ok, err := r.Store.ClaimDelivery(ctx, key); err != nil {
		return err
	} else if !ok {
		logTransitionDeliveryClaim(ctx, run.ID, obs.PRNumber, false)
		return nil
	}
	logTransitionDeliveryClaim(ctx, run.ID, obs.PRNumber, true)
	if err := run.Apply(core.Event{Kind: core.EvMerged, HeadSHA: obs.HeadSHA}); err != nil {
		return err
	}
	if err := r.Store.Save(ctx, run); err != nil {
		return err
	}
	r.notifyParent(ctx, run, obs.PRNumber, string(run.State))
	if !run.IsFixer {
		if err := r.Source.Complete(ctx, run.TaskID); err != nil {
			return err
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

func (r *Reconciler) rebase(ctx context.Context, run *core.Run, obs core.Observation) error {
	if !run.CanRebase() {
		if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvBlock, Reason: "rebase attempts exhausted"}); err != nil {
			return err
		}
		return r.notify(run, "rebase exhausted", "automatic rebase attempts exhausted")
	}
	if sameObservation(run, obs) {
		return nil
	}
	newSHA, err := r.SCM.UpdateBranch(ctx, obs.PRNumber, obs.HeadSHA)
	if err != nil {
		logTransitionRebase(ctx, run.ID, obs.PRNumber, run.RebaseAttempts+1, "", "failed")
		if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvRebaseFailed, Reason: err.Error()}, obs.HeadSHA, obs.BaseSHA); err != nil {
			return err
		}
		return r.wake(ctx, run, obs, "automatic rebase failed: "+err.Error())
	}
	if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvRebased, HeadSHA: newSHA, Reason: "automatic rebase"}, newSHA, obs.BaseSHA); err != nil {
		return err
	}
	logTransitionRebase(ctx, run.ID, obs.PRNumber, run.RebaseAttempts, newSHA, "rebased")
	return nil
}

func (r *Reconciler) wake(ctx context.Context, run *core.Run, obs core.Observation, reason string) error {
	if !r.hasAgentSlot(ctx) {
		return nil
	}
	if !run.CanWake() {
		if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvBlock, Reason: "fix attempts exhausted"}); err != nil {
			return err
		}
		return r.notify(run, "fix exhausted", "agent fix attempts exhausted")
	}
	if sameObservation(run, obs) {
		return nil
	}
	w := core.WakeContext{
		RunID: run.ID, PRNumber: obs.PRNumber, HeadSHA: obs.HeadSHA,
		Reason: reason, Excerpt: compact(obs.FailureExcerpt),
		Workspace: run.WorkspacePath, Branch: run.Branch,
		RebaseTry: run.RebaseAttempts, FixTry: run.FixAttempts,
	}
	if err := r.Harness.Wake(ctx, run.SessionID, w); err != nil {
		return fmt.Errorf("wake: %w", err)
	}
	logTransitionWake(ctx, run.ID, obs.PRNumber, "woke")
	return r.applyAndSave(ctx, run, core.Event{Kind: core.EvAgentFixed, Reason: reason}, obs.HeadSHA, obs.BaseSHA)
}

func (r *Reconciler) hasAgentSlot(ctx context.Context) bool {
	if r.Dispatcher == nil {
		return true
	}
	active, err := r.Store.ListActive(ctx)
	if err != nil {
		return true
	}
	return r.Dispatcher.agentBudget(active) > 0
}

// Run drains change events and polls. One failing PR never stops the loop.
func (r *Reconciler) Run(ctx context.Context, changes <-chan int) error {
	ticker := time.NewTicker(r.interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case pr := <-changes:
			if pr > 0 {
				r.safeReconcile(ctx, pr)
			}
		case event := <-r.harnessEvents():
			r.HandleHarnessEvent(ctx, event)
		case <-ticker.C:
			if err := r.checkBaseRecovery(ctx); err != nil {
				slog.Error("base recovery", "error", err)
			}
			if err := r.reconcileEpics(ctx); err != nil {
				slog.Error("reconcile epics", "error", err)
			}
			runs, err := r.Store.ListActive(ctx)
			if err != nil {
				slog.Error("list runs", "error", err)
				continue
			}
			for _, run := range runs {
				if run.PRNumber > 0 {
					r.safeReconcile(ctx, run.PRNumber)
				}
			}
			if r.Dispatcher != nil {
				if _, err := r.Dispatcher.PromotePending(ctx); err != nil {
					slog.Error("promote pending", "error", err)
				}
			}
		}
	}
}

func (r *Reconciler) safeReconcile(ctx context.Context, pr int) {
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
