package app

import (
	"context"

	"github.com/refusedguy/beacon/pkg/core"
)

// sameObservation reports whether the run already recorded the exact head and
// base SHA pair from obs. Reconcile uses it to skip duplicate rebase and wake
// work when the PR has not actually changed.
func sameObservation(run *core.Run, obs core.Observation) bool {
	return run.ObservedSHA == obs.HeadSHA && run.ObservedBaseSHA == obs.BaseSHA
}

// maxAutoRebases caps automatic branch updates per run. Rebases get their own
// small budget so a moving base cannot spend the whole fix budget before a real
// check failure is handled.
const maxAutoRebases = 2

// recordObservation stores the head and base SHA pair the reconciler has just
// acted on, so the next poll can skip an identical observation.
func recordObservation(run *core.Run, headSHA, baseSHA string) {
	run.ObservedSHA = headSHA
	run.ObservedBaseSHA = baseSHA
}

// closedPRVerdict returns the decision for a PR the provider no longer keeps
// open. It shares the core final-state classification with every other
// final-state handler, so locked counts as final here and in Decide.
func closedPRVerdict(state string) *core.MergeDecision {
	if !core.DecideFinalState(state).Closed {
		return nil
	}
	return &core.MergeDecision{Verdict: core.VerdictWait, Reason: "pull request " + state}
}

func (r *Reconciler) applyDecision(
	ctx context.Context,
	run *core.Run,
	obs core.Observation,
	decision core.MergeDecision,
) (core.MergeDecision, error) {
	switch core.ActionForVerdict(decision.Verdict).Dispatch {
	case core.DispatchNoop:
		return decision, nil
	case core.DispatchMerge:
		return r.mergeIfReviewed(ctx, run, obs, decision)
	case core.DispatchRebase:
		return r.handleNeedsRebase(ctx, run, obs, decision)
	case core.DispatchFix:
		return r.handleNeedsAgentFix(ctx, run, obs, decision)
	case core.DispatchBlock:
		return r.handleBlocked(ctx, run, decision)
	default:
		return r.handlePending(ctx, run, obs, decision)
	}
}

// handleNeedsRebase turns a behind observation into a rebase, or into a merge
// when the checks are green and GitHub still reports the branch mergeable.
func (r *Reconciler) handleNeedsRebase(
	ctx context.Context,
	run *core.Run,
	obs core.Observation,
	decision core.MergeDecision,
) (core.MergeDecision, error) {
	if !r.AutoRebase {
		return core.MergeDecision{Verdict: core.VerdictWait, Reason: "auto_rebase disabled"}, nil
	}
	if !obs.Conflict && len(obs.Checks) > 0 && obs.AllRequiredPass(nil) {
		return r.greenButBehind(ctx, run, obs, decision)
	}
	// A pending check can still fail. Updating the branch now spends a rebase
	// attempt and restarts the checks on the new head, so wait for a verdict.
	if obs.HasPending() {
		return decision, nil
	}
	if sameObservation(run, obs) {
		return decision, nil
	}
	if run.RebaseBudgetExhausted(maxAutoRebases) {
		if run.State != core.RunBlocked {
			return decision, r.applyAndSave(ctx, run, core.Event{Kind: core.EvBlock, Reason: "rebase budget exhausted"}, obs)
		}
		return decision, nil
	}
	if run.State == core.RunWaitingCI || run.State == core.RunRunning {
		if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvBehind, Reason: decision.Reason}, core.Observation{}); err != nil {
			return decision, err
		}
	}
	return decision, r.rebase(ctx, run, obs)
}

// greenButBehind handles a behind PR whose checks already pass. It merges when
// policy allows it, otherwise moves the run out of the stuck state.
func (r *Reconciler) greenButBehind(
	ctx context.Context,
	run *core.Run,
	obs core.Observation,
	decision core.MergeDecision,
) (core.MergeDecision, error) {
	if run.State == core.RunNeedsAgentFix {
		return decision, r.applyAndSave(ctx, run, core.Event{Kind: core.EvChecksPassed, HeadSHA: obs.HeadSHA}, obs)
	}
	mergeable := obs
	mergeable.Behind = false
	if merged := r.Policy.Evaluate(mergeable); merged.Verdict == core.VerdictMerge {
		return r.mergeIfReviewed(ctx, run, obs, merged)
	}
	if run.State == core.RunNeedsRebase || run.State == core.RunRunning {
		if run.State == core.RunNeedsRebase && !run.CanRebase() {
			return decision, r.applyAndSave(ctx, run, core.Event{Kind: core.EvBlock, Reason: "rebase attempts exhausted"}, obs)
		}
		kind := core.EvChecksPassed
		if run.State == core.RunNeedsRebase {
			kind = core.EvRebased
		}
		return decision, r.applyAndSave(ctx, run, core.Event{Kind: kind, HeadSHA: obs.HeadSHA, Reason: "base green"}, obs)
	}
	return decision, nil
}

func (r *Reconciler) handleNeedsAgentFix(
	ctx context.Context,
	run *core.Run,
	obs core.Observation,
	decision core.MergeDecision,
) (core.MergeDecision, error) {
	if sameObservation(run, obs) {
		return decision, nil
	}
	if run.State == core.RunWaitingCI || run.State == core.RunRunning {
		if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvChecksFailed, Reason: decision.Reason}, core.Observation{}); err != nil {
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
}

func (r *Reconciler) handleBlocked(ctx context.Context, run *core.Run, decision core.MergeDecision) (core.MergeDecision, error) {
	if run.State != core.RunBlocked {
		if err := r.applyAndSave(ctx, run, core.Event{Kind: core.EvBlock, Reason: decision.Reason}, core.Observation{}); err != nil {
			return decision, err
		}
	}
	return decision, r.notify(run, "blocked", decision.Reason)
}

func (r *Reconciler) handlePending(
	ctx context.Context,
	run *core.Run,
	obs core.Observation,
	decision core.MergeDecision,
) (core.MergeDecision, error) {
	if run.State != core.RunWaitingCI && run.State != core.RunRunning {
		return decision, nil
	}
	if err := run.Apply(core.Event{Kind: core.EvChecksPending, HeadSHA: obs.HeadSHA}); err != nil {
		return decision, err
	}
	if run.ObserveSHA(obs.HeadSHA, obs.BaseSHA) {
		return decision, r.Store.Save(ctx, run)
	}
	return decision, nil
}
