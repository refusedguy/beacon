package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// defaultOrphanTimeout is how long a running run without a PR may go stale
// before its worker session is checked for liveness.
const defaultOrphanTimeout = 90 * time.Second

type Reconciler struct {
	Store           core.RunStore
	SCM             core.ScmGateway
	Policy          core.MergePolicy
	Harness         core.AgentHarness
	Source          core.TaskSource
	Workspace       core.WorkspaceManager
	Notifier        core.Notifier
	Dispatcher      *Dispatcher
	AgentSlots      core.AgentSlots
	Changes         chan<- int
	Repo            string
	BaseBranch      string
	ParentSessionID string
	BaseRedMode     string
	MaxFixTries     int
	Interval        time.Duration
	OrphanTimeout   time.Duration
	Events          <-chan core.HarnessEvent
	AutoRebase      bool
	Decisions       DecisionLogger
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
	run.Checks = obs.Checks
	if err := r.Store.Save(ctx, run); err != nil {
		return core.MergeDecision{}, err
	}
	defer r.Decisions.logReconcileDecisionDeferred(ctx, pr, run, obs, &decision)
	if closed := closedPRVerdict(obs.PRState); closed != nil {
		return *closed, r.finalizeExternal(ctx, run, obs)
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
	return r.applyDecision(ctx, run, obs, decision)
}

// applyAndSave applies e and persists the run. A non-zero obs records both the
// head and base SHA; the zero value leaves the recorded observation untouched.
func (r *Reconciler) applyAndSave(ctx context.Context, run *core.Run, e core.Event, obs core.Observation) error {
	if err := run.Apply(e); err != nil {
		return err
	}
	if obs.HeadSHA != "" || obs.BaseSHA != "" {
		recordObservation(run, obs.HeadSHA, obs.BaseSHA)
	}
	return r.Store.Save(ctx, run)
}

func (r *Reconciler) merge(ctx context.Context, run *core.Run, obs core.Observation) error {
	recordObservation(run, obs.HeadSHA, obs.BaseSHA)
	_, isEpic := r.epicTask(ctx, run.TaskID)
	if !run.IsFixer && !isEpic && run.WorkspacePath != "" {
		if err := r.Source.MarkInWorkspace(ctx, run.TaskID, run.WorkspacePath, "done"); err != nil {
			return err
		}
		ws := core.Workspace{Path: run.WorkspacePath, Branch: run.Branch}
		message := "beacon: complete task " + string(run.TaskID)
		if err := r.commitCard(ctx, ws, cardsPath, message); err != nil {
			slog.Warn("card publish failed before merge", "run", run.ID, "pr", obs.PRNumber, "error", err)
			if saveErr := r.Store.Save(ctx, run); saveErr != nil {
				slog.Error("save observation after card publish failure", "run", run.ID, "error", saveErr)
			}
		} else {
			fresh, err := r.SCM.Observe(ctx, obs.PRNumber)
			if err != nil {
				return err
			}
			obs.HeadSHA = fresh.HeadSHA
		}
	}
	key := fmt.Sprintf("merge:%d:%s", obs.PRNumber, obs.HeadSHA)
	r.Decisions.logTransitionMerge(ctx, run.ID, obs.PRNumber, "attempt")
	if err := r.SCM.Merge(ctx, obs.PRNumber, obs.HeadSHA, string(run.TaskID)); err != nil {
		return err
	}
	if ok, err := r.Store.ClaimDelivery(ctx, key); err != nil {
		return err
	} else if !ok {
		r.Decisions.logTransitionDeliveryClaim(ctx, run.ID, obs.PRNumber, false)
		return nil
	}
	r.Decisions.logTransitionDeliveryClaim(ctx, run.ID, obs.PRNumber, true)
	if err := run.Apply(core.Event{
		Kind: core.EvMerged, HeadSHA: obs.HeadSHA,
		Reason: fmt.Sprintf("merged PR #%d via squash", obs.PRNumber),
	}); err != nil {
		return err
	}
	if err := r.Store.Save(ctx, run); err != nil {
		return err
	}
	r.notifyParent(ctx, run, obs.PRNumber, string(run.State))
	if !run.IsFixer {
		if err := r.Source.Complete(ctx, run.TaskID); err != nil {
			slog.Error("complete task after merge", "run", run.ID, "task", run.TaskID, "error", err)
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

// commitCard publishes the card from the run worktree. A push rejected as
// non-fast-forward gets one refresh from the remote branch and one retry.
func (r *Reconciler) commitCard(ctx context.Context, ws core.Workspace, pathspec, message string) error {
	publish := func() error { return r.Workspace.CommitPath(ctx, ws, pathspec, message) }
	err := publish()
	var nff *NonFastForwardError
	rebaser, ok := r.Workspace.(BaseRebaser)
	if !errors.As(err, &nff) || !ok {
		return err
	}
	if err := rebaser.RebaseBase(ctx, ws, ws.Branch); err != nil {
		return err
	}
	return publish()
}

func (r *Reconciler) rebase(ctx context.Context, run *core.Run, obs core.Observation) error {
	if !run.CanRebase() {
		if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvBlock, Reason: "rebase attempts exhausted"}, core.Observation{}); err != nil {
			return err
		}
		return r.notify(run, "rebase exhausted", "automatic rebase attempts exhausted")
	}
	if sameObservation(run, obs) {
		return nil
	}
	newSHA, err := r.SCM.UpdateBranch(ctx, obs.PRNumber, obs.HeadSHA)
	if err != nil {
		r.Decisions.logTransitionRebase(ctx, run.ID, obs.PRNumber, run.RebaseAttempts+1, "", "failed")
		if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvRebaseFailed, Reason: err.Error()}, obs); err != nil {
			return err
		}
		return r.wake(ctx, run, obs, "automatic rebase failed: "+err.Error())
	}
	if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvRebased, HeadSHA: newSHA, Reason: "automatic rebase"}, core.Observation{HeadSHA: newSHA, BaseSHA: obs.BaseSHA}); err != nil {
		return err
	}
	r.Decisions.logTransitionRebase(ctx, run.ID, obs.PRNumber, run.RebaseAttempts, newSHA, "rebased")
	return nil
}

func (r *Reconciler) wake(ctx context.Context, run *core.Run, obs core.Observation, reason string) error {
	if !r.hasAgentSlot(ctx) {
		return nil
	}
	if !run.CanWake() {
		if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvBlock, Reason: "fix attempts exhausted"}, core.Observation{}); err != nil {
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
	r.Decisions.logTransitionWake(ctx, run.ID, obs.PRNumber, "woke")
	return r.applyAndSave(ctx, run, core.Event{Kind: core.EvAgentFixed, Reason: reason}, obs)
}

// agentSlots resolves the slot policy through the port. The concrete
// dispatcher remains wired for promotion and retry settings, and satisfies the
// port itself, so existing wiring keeps working while tests can inject a fake.
func (r *Reconciler) agentSlots() core.AgentSlots {
	if r.AgentSlots != nil {
		return r.AgentSlots
	}
	if r.Dispatcher != nil {
		return r.Dispatcher
	}
	return nil
}

// hasAgentSlot delegates the wake decision to the same AgentSlots policy used
// by dispatch and promotion. A store error makes Available return zero, so a
// wake is skipped rather than started on an unreadable budget.
func (r *Reconciler) hasAgentSlot(ctx context.Context) bool {
	slots := r.agentSlots()
	if slots == nil {
		return true
	}
	return slots.Available(ctx) > 0
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
			r.reconcileOrphanRunning(ctx, runs)
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
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("reconcile panic", "pr", pr, "panic", rec)
		}
	}()
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
