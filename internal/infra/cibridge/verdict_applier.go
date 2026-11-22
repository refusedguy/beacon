package cibridge

import (
	"context"
	"fmt"
	"log"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// handleHeadChanged closes w with domain.HeadChangedEvent and persists the
// successor described by EffectCreateWatch. Subscriber and OrchestratorID are
// carried over; wake routing stays on the successor because its obligations
// are staged there.
func (b *verdictApplier) handleHeadChanged(w domain.Watch, head string, now time.Time, cycleID string) {
	current, err := b.store.Watch(w.ID)
	if err != nil || current.Status == domain.StatusClosed || current.HeadSHA == head {
		return
	}
	closed, effects := domain.Transition(current, domain.HeadChangedEvent(head, now))
	if closed.Status != domain.StatusClosed || closed.CloseReason != string(domain.CloseReasonHeadChanged) {
		return
	}
	if err := b.store.SaveWatch(closed); err != nil {
		observe.ActorError("cibridge-head", closed.ID, err)
		return
	}

	var subs []domain.Subscriber
	for _, e := range effects {
		if e.Kind == domain.EffectCreateWatch {
			subs = e.Subscribers
			break
		}
	}
	newID := successorWatchID(current, head)
	if _, err := b.store.Watch(newID); err == nil {
		b.cycleEvent(cycleID, "head_successor_exists", cycleFields{watchID: newID}, nil)
		return
	}
	next := domain.Watch{
		ID:              newID,
		Repo:            current.Repo,
		PR:              current.PR,
		HeadSHA:         head,
		BaseRef:         current.BaseRef,
		Issue:           current.Issue,
		Status:          domain.StatusWatching,
		Verdict:         domain.VerdictWatching,
		Flaky:           current.Flaky,
		Policy:          current.Policy,
		MergePolicy:     current.MergePolicy,
		Subscribers:     subs,
		OrchestratorID:  current.OrchestratorID,
		FixFor:          current.FixFor,
		BlockerID:       current.BlockerID,
		FixBlockerID:    current.FixBlockerID,
		DeadlineAt:      current.DeadlineAt,
		NoChecksSince:   current.NoChecksSince,
		NoChecksTimeout: current.NoChecksTimeout,
		ConfigID:        current.ConfigID,
		CreatedAt:       now,
	}
	for _, sub := range subs {
		id := domain.ObligationKey(next.ID, next.Revision, sub.ID)
		if _, err := b.store.Obligation(id); err != nil {
			if err := b.store.SaveObligation(domain.Obligation{
				ID: id, WatchID: next.ID, Revision: next.Revision,
				SubscriberID: sub.ID, VerdictPointer: domain.VerdictWatching, StagedAt: now,
			}); err != nil {
				observe.ActorError("cibridge-head-obligation", next.ID, err)
				return
			}
		}
	}
	if err := b.store.SaveWatch(next); err != nil {
		observe.ActorError("cibridge-head-save", next.ID, err)
		return
	}
	b.cycleEvent(cycleID, "head_changed", cycleFields{watchID: current.ID}, observe.Fields{
		"newWatchID": newID, "headSHA": head,
	})
	log.Printf("cibridge: %s head %s -> %s (closeReason=%s)", current.ID, current.HeadSHA, head, domain.CloseReasonHeadChanged)
}

// DependencyResolver answers whether one waitFor or waitForMain reference is
// already satisfied. The port is declared here so cibridge never imports the
// app layer (ADR 0005): the orchestrator resolver satisfies it structurally.
// An implementation must return an error instead of true when a fact cannot
// be verified, so a failed resolution holds the merge.
type DependencyResolver interface {
	Satisfied(ctx context.Context, ref string) (bool, error)
}

// mergeResolvers carries the resolver bound to each Bridge. The Bridge fields
// live in a file outside this card's owner files, so the setter and the value
// it stores sit next to the gate that reads them.
var mergeResolvers sync.Map // map[*Bridge]DependencyResolver

// WithDependencyResolver binds the port used to resolve waitFor and
// waitForMain before an auto-merge. A nil binding clears it: without a
// resolver every Watch that names a reference stays on wait.
func (b *Bridge) WithDependencyResolver(r DependencyResolver) *Bridge {
	if b == nil {
		return nil
	}
	if r == nil {
		mergeResolvers.Delete(b)
		return b
	}
	mergeResolvers.Store(b, r)
	return b
}

// dependencyResolver returns the resolver bound to this Bridge. A Bridge
// built without the setter may still carry a resolver on one of its existing
// ports, so those are tried before falling back to nil.
func (b *verdictApplier) dependencyResolver() DependencyResolver {
	if b == nil || b.Bridge == nil {
		return nil
	}
	if v, ok := mergeResolvers.Load(b.Bridge); ok {
		if r, ok := v.(DependencyResolver); ok {
			return r
		}
	}
	for _, candidate := range []any{b.control, b.merger, b.store, b.ci} {
		if candidate == nil {
			continue
		}
		if r, ok := candidate.(DependencyResolver); ok {
			return r
		}
	}
	return nil
}

// applyVerdict routes one EvaluatePR result to the right follow-up action.
func (b *verdictApplier) applyVerdict(ctx context.Context, w domain.Watch, obs ciObservation, verdict domain.Verdict, targets []domain.WakeTarget, now time.Time, cycleID string) int {
	current, err := b.store.Watch(w.ID)
	if err != nil || current.Status == domain.StatusClosed || current.Status == domain.StatusParked || current.Status == domain.StatusBlocked {
		return 0
	}
	// Re-read under the single-writer assumption: a head change between the
	// scan and here makes this observation stale.
	if current.HeadSHA != w.HeadSHA {
		return 0
	}
	// Keep the persisted observation so a watching verdict can tell a fresh
	// CI fact from the one already in the store.
	persistedChecks := current.Checks
	persistedMerge := current.Merge
	current.Checks = obs.pr.Checks
	current.Merge = domain.MergeInfo{
		Mergeable:        obs.pr.Mergeable,
		MergeStateStatus: obs.pr.MergeStateStatus,
		ReviewDecision:   obs.pr.ReviewDecision,
	}

	switch verdict {
	case domain.VerdictWatching:
		// Watching is not terminal: stage no obligation and wake nobody. The
		// observations still have to reach the store, or verdict_source never
		// publishes them and the Watch stays stuck with checks=null. Save only
		// on a real change, so a repeat with the same checks writes nothing and
		// leaves the revision alone.
		if !maps.Equal(persistedChecks, current.Checks) || persistedMerge != current.Merge {
			if err := b.store.SaveWatch(current); err != nil {
				observe.ActorError("cibridge-watch-observe", current.ID, err)
			}
		}
		return 0
	case domain.VerdictBlocked:
		return b.applyBlocked(ctx, current, obs, now, cycleID)
	case domain.VerdictReady:
		if current.MergePolicy.AutoMerge && b.merger != nil {
			if reason := b.mergePolicyHold(ctx, current); reason != "" {
				return b.publishMergeWait(current, obs, now, cycleID, reason)
			}
			return b.applyMerge(ctx, current, obs, now, cycleID)
		}
		return b.publishActionable(ctx, current, obs, verdict, targets, now, cycleID, "")
	case domain.VerdictMerged:
		return b.applyMerged(ctx, current, obs, now, cycleID)
	case domain.VerdictClosed:
		return b.closeWatch(ctx, current, verdict, now, cycleID)
	default:
		return b.publishActionable(ctx, current, obs, verdict, targets, now, cycleID, "")
	}
}

// mergePolicyHold returns the reason the Watch's merge policy keeps a green
// verdict away from applyMerge, or an empty string when the merge may run.
// requireApproval is a static hold. waitFor and waitForMain need the resolver
// port: a missing resolver or a failed resolution keeps the hold, matching
// the fail-closed rule of the orchestrator gate (ADR 0305).
func (b *verdictApplier) mergePolicyHold(ctx context.Context, w domain.Watch) string {
	if w.MergePolicy.RequireApproval {
		return "require_approval"
	}
	if len(w.MergePolicy.WaitFor) == 0 && len(w.MergePolicy.WaitForMain) == 0 {
		return ""
	}
	resolver := b.dependencyResolver()
	if resolver == nil {
		return "resolver_unavailable"
	}
	for _, ref := range w.MergePolicy.WaitFor {
		ok, err := resolver.Satisfied(ctx, ref)
		if err != nil || !ok {
			return "wait_for:" + ref
		}
	}
	for _, ref := range w.MergePolicy.WaitForMain {
		ok, err := resolver.Satisfied(ctx, ref)
		if err != nil || !ok {
			return "wait_for_main:" + ref
		}
	}
	return ""
}

// publishMergeWait holds a green Watch whose merge policy is not satisfied.
// The reason travels in the persisted verdict, so an operator sees why the PR
// stayed open and the next cycle rechecks the same gate. No merger is called
// and no subscriber gets work: a dependency hold is nobody's fault.
func (b *verdictApplier) publishMergeWait(w domain.Watch, obs ciObservation, now time.Time, cycleID, reason string) int {
	if w.Status == domain.StatusClosed || w.Status == domain.StatusParked || w.Status == domain.StatusBlocked {
		return 0
	}
	if w.Revision == ^uint64(0) {
		return 0
	}
	waitVerdict := domain.Verdict(string(domain.VerdictWaitDependency) + ":" + reason)
	if w.Verdict == waitVerdict && w.Status == domain.StatusNotifying {
		return 0
	}
	next := w
	next.Status = domain.StatusNotifying
	next.Verdict = waitVerdict
	next.Revision = w.Revision + 1
	next.Checks = obs.pr.Checks
	next.Merge = domain.MergeInfo{
		Mergeable:        obs.pr.Mergeable,
		MergeStateStatus: obs.pr.MergeStateStatus,
		ReviewDecision:   obs.pr.ReviewDecision,
	}
	if err := b.store.SaveWatch(next); err != nil {
		observe.ActorError("cibridge-merge-hold", next.ID, err)
		return 0
	}
	for _, sub := range next.Subscribers {
		id := domain.ObligationKey(next.ID, next.Revision, sub.ID)
		if _, err := b.store.Obligation(id); err != nil {
			o := domain.Obligation{
				ID: id, WatchID: next.ID, Revision: next.Revision,
				SubscriberID: sub.ID, VerdictPointer: waitVerdict, StagedAt: now,
			}
			if err := b.store.SaveObligation(o); err != nil {
				observe.ActorError("cibridge-merge-hold-obligation", next.ID, err)
				continue
			}
		}
	}
	observe.VerdictPublished(string(waitVerdict), next.ID, next.PR, next.Revision)
	b.cycleEvent(cycleID, "merge_held", cycleFields{
		watchID: next.ID, revision: next.Revision, verdict: waitVerdict,
	}, observe.Fields{observe.FieldReason: reason})
	return 1
}

// publishActionable saves a non-terminal verdict and wakes the subscriber
// roles named by EvaluatePR. The wake text is role- and verdict-specific so
// the receiver sees a concrete instruction instead of a bare verdict word.
func (b *verdictApplier) publishActionable(ctx context.Context, w domain.Watch, obs ciObservation, verdict domain.Verdict, targets []domain.WakeTarget, now time.Time, cycleID, mergeErr string) int {
	next, _ := domain.Transition(w, domain.VerdictEvent(verdict, now))
	if next.Revision == w.Revision {
		return 0
	}
	if err := b.store.SaveWatch(next); err != nil {
		observe.ActorError("cibridge-publish", next.ID, err)
		return 0
	}

	roles := rolesFromTargets(targets)
	if mergeErr != "" {
		roles[string(domain.RoleExecutor)] = true
		roles[string(domain.RoleFixer)] = true
	}
	text := wakeText(verdict, obs, targets, mergeErr)
	// Every subscriber owns an obligation on the new revision, otherwise
	// get_result cannot project a verdict for them. Wake routing is separate
	// from obligation staging: a ready verdict targets the orchestrator, but
	// the executor still needs a readable pointer.
	for _, sub := range next.Subscribers {
		id := domain.ObligationKey(next.ID, next.Revision, sub.ID)
		if _, err := b.store.Obligation(id); err != nil {
			o := domain.Obligation{
				ID: id, WatchID: next.ID, Revision: next.Revision,
				SubscriberID: sub.ID, VerdictPointer: verdict, StagedAt: now,
			}
			if err := b.store.SaveObligation(o); err != nil {
				observe.ActorError("cibridge-obligation", next.ID, err)
				continue
			}
		}
		if !roles[string(sub.Role)] {
			continue
		}
		b.wakeOne(ctx, next, sub, text, now, cycleID, true)
	}
	observe.VerdictPublishedWithChecks(string(verdict), next.ID, next.PR, next.Revision, realFailedNames(obs.pr.Checks))
	b.cycleEvent(cycleID, "verdict", cycleFields{
		watchID: next.ID, revision: next.Revision, verdict: verdict,
	}, nil)
	log.Printf("cibridge: %s#%d -> %s revision=%d", next.Repo, next.PR, verdict, next.Revision)
	return 1
}

// publishRecheck publishes the recheck verdict produced when the base branch
// moved after the batch snapshot. VerdictRecheck is not an engine verdict, so
// validVerdict rejects a verdict event for it; the Watch state is built here
// instead. The Watch stays live, its revision grows and both subscribers are
// woken to repeat CI against the current main (ADR 0266).
func (b *verdictApplier) publishRecheck(ctx context.Context, w domain.Watch, obs ciObservation, now time.Time, cycleID, reason string) int {
	if w.Status == domain.StatusClosed || w.Status == domain.StatusParked || w.Status == domain.StatusBlocked {
		return 0
	}
	if w.Revision == ^uint64(0) {
		return 0
	}
	next := w
	next.Status = domain.StatusNotifying
	next.Verdict = domain.VerdictRecheck
	next.Revision = w.Revision + 1
	next.Checks = obs.pr.Checks
	next.Merge = domain.MergeInfo{
		Mergeable:        obs.pr.Mergeable,
		MergeStateStatus: obs.pr.MergeStateStatus,
		ReviewDecision:   obs.pr.ReviewDecision,
	}
	if err := b.store.SaveWatch(next); err != nil {
		observe.ActorError("cibridge-recheck", next.ID, err)
		return 0
	}

	text := wakeText(domain.VerdictRecheck, obs, nil, reason)
	roles := map[string]bool{
		string(domain.RoleExecutor):     true,
		string(domain.RoleOrchestrator): true,
	}
	for _, sub := range next.Subscribers {
		id := domain.ObligationKey(next.ID, next.Revision, sub.ID)
		if _, err := b.store.Obligation(id); err != nil {
			o := domain.Obligation{
				ID: id, WatchID: next.ID, Revision: next.Revision,
				SubscriberID: sub.ID, VerdictPointer: domain.VerdictRecheck, StagedAt: now,
			}
			if err := b.store.SaveObligation(o); err != nil {
				observe.ActorError("cibridge-recheck-obligation", next.ID, err)
				continue
			}
		}
		if !roles[string(sub.Role)] {
			continue
		}
		b.wakeOne(ctx, next, sub, text, now, cycleID, true)
	}
	observe.VerdictPublished(string(domain.VerdictRecheck), next.ID, next.PR, next.Revision)
	b.cycleEvent(cycleID, "recheck", cycleFields{
		watchID: next.ID, revision: next.Revision, verdict: domain.VerdictRecheck,
	}, observe.Fields{observe.FieldReason: reason})
	return 1
}

// rolesFromTargets collects the subscriber roles EvaluatePR asked to wake.
// A rest_pin target has no role and therefore wakes nobody.
func rolesFromTargets(targets []domain.WakeTarget) map[string]bool {
	out := make(map[string]bool, len(targets))
	for _, t := range targets {
		if t.Role == "" || t.Kind == domain.WakeRestPin || t.Kind == domain.WakeHeadChanged {
			continue
		}
		out[string(t.Role)] = true
	}
	return out
}

// callMerge performs one merge through the batch-aware path when available.
// obs.pr came from the same batch cycle that produced the ready verdict;
// reusing it avoids a fresh GraphQL read that can report UNKNOWN for a PR the
// batch already resolved to MERGEABLE/CLEAN. The base head from the same cycle
// travels in PRState.BaseHeadSHA so the merge can detect a base that moved.
func (b *verdictApplier) callMerge(ctx context.Context, w domain.Watch, obs ciObservation) (bool, bool, string, error) {
	start := time.Now()
	var merged, held bool
	var reason string
	var err error
	if sm, ok := b.merger.(stateMerger); ok {
		state := obs.pr
		state.BaseHeadSHA = obs.base.HeadSHA
		merged, held, reason, err = sm.MergePRWithState(ctx, w.Repo, w.PR, state)
	} else {
		merged, held, reason, err = b.merger.Merge(ctx, w.Repo, w.PR)
	}
	observe.MergeDuration(time.Since(start))
	return merged, held, reason, err
}

// applyMerge runs the auto-merge step for a ready Watch. A confirmed merge
// closes the Watch with closeReason=merged and finishes every subscriber
// without waking anyone. A hold or error becomes a rebase (conflict/behind)
// or keep_working verdict with the merge error saved on the subscribers. When
// auto-rebase is bound and the first hold is a stale BEHIND base, the branch is
// rebased once and the merge is retried once before the Wake is published.
func (b *verdictApplier) applyMerge(ctx context.Context, w domain.Watch, obs ciObservation, now time.Time, cycleID string) int {
	observe.MergeAttempt(w.ID, w.PR)
	merged, held, reason, err := b.callMerge(ctx, w, obs)
	if merged {
		observe.MergeSuccess(w.ID, w.PR)
		n := b.closeWatch(ctx, w, domain.VerdictMerged, now, cycleID)
		b.cycleEvent(cycleID, "auto_merge_success", cycleFields{
			watchID: w.ID, revision: w.Revision, verdict: domain.VerdictMerged,
		}, observe.Fields{observe.FieldPR: w.PR})
		return n
	}
	observe.MergeFailure(w.ID, w.PR, err)

	// A provider that re-read the base reports a moved main before any write.
	// That is a recheck, not a rebase after a failed merge (ADR 0266).
	if r := isMergeRecheck(reason, err); r != "" {
		return b.publishRecheck(ctx, w, obs, now, cycleID, r)
	}

	retried := false
	if b.autoRebase && b.rebaser != nil && isMergeBehind(reason, err) && b.claimRebase(w) {
		newHead, rbErr := b.rebaser.Rebase(ctx, w.Repo, w.PR)
		if rbErr == nil && strings.TrimSpace(newHead) != "" {
			w.HeadSHA = newHead
			retryObs := obs
			retryObs.pr.HeadRefOid = newHead
			b.cycleEvent(cycleID, "auto_rebase", cycleFields{watchID: w.ID}, observe.Fields{
				observe.FieldPR: w.PR, "headSHA": newHead,
			})
			merged, held, reason, err = b.callMerge(ctx, w, retryObs)
			retried = true
			if merged {
				observe.MergeSuccess(w.ID, w.PR)
				n := b.closeWatch(ctx, w, domain.VerdictMerged, now, cycleID)
				b.cycleEvent(cycleID, "auto_merge_success", cycleFields{
					watchID: w.ID, revision: w.Revision, verdict: domain.VerdictMerged,
				}, observe.Fields{observe.FieldPR: w.PR, "rebase": true})
				return n
			}
			observe.MergeFailure(w.ID, w.PR, err)
			if r := isMergeRecheck(reason, err); r != "" {
				return b.publishRecheck(ctx, w, retryObs, now, cycleID, r)
			}
			b.cycleEvent(cycleID, "auto_rebase_retry_failed", cycleFields{watchID: w.ID}, observe.Fields{
				observe.FieldPR: w.PR, observe.FieldReason: reason,
			})
		} else if rbErr != nil {
			observe.ActorError("cibridge-autorebase", w.ID, rbErr)
		}
	}

	mergeErr := strings.TrimSpace(reason)
	if mergeErr == "" && held {
		mergeErr = "merge held by policy"
	}
	if mergeErr == "" {
		mergeErr = errText(err)
	}
	verdict := domain.VerdictKeepWorking
	// A retried merge that still failed is always a rebase, even when the
	// retry's reason is a policy hold rather than an explicit conflict.
	if isMergeConflict(reason, err) || retried {
		verdict = domain.VerdictRebase
	}
	stored := w
	for i := range stored.Subscribers {
		stored.Subscribers[i].LastError = mergeErr
	}
	targets := []domain.WakeTarget{{Role: domain.RoleExecutor}, {Role: domain.RoleFixer}}
	if verdict == domain.VerdictRebase {
		observe.RebaseRequested(w.ID, w.PR)
	}
	return b.publishActionable(ctx, stored, obs, verdict, targets, now, cycleID, mergeErr)
}

// applyMerged handles a PR GitHub reports as MERGED. It closes the Watch and
// resolves every blocker incident attached to it or to its base branch, which
// unblocks all related Watches in one store transaction.
func (b *verdictApplier) applyMerged(ctx context.Context, w domain.Watch, obs ciObservation, now time.Time, cycleID string) int {
	n := b.closeWatch(ctx, w, domain.VerdictMerged, now, cycleID)
	if b.control != nil {
		for _, id := range []string{w.BlockerID, w.FixBlockerID} {
			if id == "" {
				continue
			}
			unblocked, err := b.control.ResolveBlocker(id, now)
			if err != nil {
				observe.ActorError("cibridge-resolve", w.ID, err)
			} else {
				ids := make([]string, 0, len(unblocked))
				for _, unwound := range unblocked {
					ids = append(ids, unwound.ID)
				}
				b.rotateGenerations(ids...)
			}
		}
		// Resolve by base green only when the provider actually observed the
		// base checks. An empty FailedChecks map from a single-PR provider means
		// "unknown", not "green": treating it as green would close incidents that
		// a different Watch is still waiting on.
		if obs.base.ChecksKnown {
			if _, err := b.control.ResolveBlockersByBaseGreen(w.Repo, baseRefOf(w), obs.base.FailedChecks, now); err != nil {
				observe.ActorError("cibridge-base-green", w.ID, err)
			}
		}
	} else {
		b.unblockFixed(w)
	}
	// A fixer Watch carries its own fixFor link. Resolve it in addition to the
	// incident path so a merged fixer always releases the Watch it repaired.
	if b.watchFixFor(w) != "" {
		b.unblockFixed(w)
	}
	b.cycleEvent(cycleID, "merged_resolve", cycleFields{
		watchID: w.ID, revision: w.Revision, verdict: domain.VerdictMerged,
	}, observe.Fields{
		observe.FieldBlockerID: w.BlockerID, "fixBlockerID": w.FixBlockerID,
	})
	return n
}

// closeLinkedCard marks the card linked to a Watch as done. closeWatch calls it
// only for a merged verdict, so a hold never reaches it. The port owns the
// card_links lookup and the card file write. An unbound port only skips the
// card write, and a port failure is logged without blocking the Watch close or
// blocker resolution. The error is returned so a merged Watch can skip the
// worktree collection when its card was not closed.
func (b *verdictApplier) closeLinkedCard(ctx context.Context, w domain.Watch, at time.Time) error {
	if b == nil || b.cardLinks == nil || w.ID == "" {
		return nil
	}
	if err := b.cardLinks.CloseMerged(ctx, w.ID, w.PR, at); err != nil {
		observe.ActorError("cibridge-card-close", w.ID, err)
		return err
	}
	return nil
}

// collectWorktrees runs the bound GC port after a merged Watch closes its card.
// An unset port is a no-op and an error is logged without changing the verdict.
func (b *verdictApplier) collectWorktrees(ctx context.Context, w domain.Watch, cycleID string) {
	if b == nil || b.gc == nil {
		return
	}
	removed, err := b.gc(ctx)
	if err != nil {
		observe.ActorError("cibridge-worktree-gc", w.ID, err)
		return
	}
	if removed > 0 {
		b.cycleEvent(cycleID, "worktree_gc", cycleFields{
			watchID: w.ID, revision: w.Revision, verdict: domain.VerdictMerged,
		}, observe.Fields{"removed": removed})
	}
}

// closeWatch applies a terminal verdict and marks each subscriber finished.
// It never wakes anyone: a closed Watch has no further work for a subscriber.
func (b *verdictApplier) closeWatch(ctx context.Context, w domain.Watch, verdict domain.Verdict, now time.Time, cycleID string) int {
	next, _ := domain.Transition(w, domain.VerdictEvent(verdict, now))
	if next.Revision == w.Revision {
		return 0
	}
	if verdict == domain.VerdictMerged {
		next.CloseReason = string(domain.CloseReasonMerged)
	}
	for i := range next.Subscribers {
		next.Subscribers[i].Phase = domain.WorkerPhaseFinished
	}
	if err := b.store.SaveWatch(next); err != nil {
		observe.ActorError("cibridge-close", next.ID, err)
		return 0
	}
	if verdict == domain.VerdictMerged {
		if err := b.closeLinkedCard(ctx, w, now); err == nil {
			b.collectWorktrees(ctx, w, cycleID)
		}
	}
	// The terminal verdict is still readable at the new revision, so get_result
	// can confirm the close without a wake.
	for i := range next.Subscribers {
		sub := next.Subscribers[i]
		id := domain.ObligationKey(next.ID, next.Revision, sub.ID)
		if _, err := b.store.Obligation(id); err != nil {
			if err := b.store.SaveObligation(domain.Obligation{
				ID: id, WatchID: next.ID, Revision: next.Revision,
				SubscriberID: sub.ID, VerdictPointer: verdict, StagedAt: now,
			}); err != nil {
				observe.ActorError("cibridge-close-obligation", next.ID, err)
			}
		}
	}
	observe.VerdictPublished(string(verdict), next.ID, next.PR, next.Revision)
	b.cycleEvent(cycleID, "watch_closed", cycleFields{
		watchID: next.ID, revision: next.Revision, verdict: verdict,
	}, nil)
	// A terminal verdict used to be silent: every subscriber went to finished
	// and nobody learned the wave item ended. Push one durable confirmation to
	// the bound project orchestrator so it can start the next wave. Executors
	// stay finished and receive no wake.
	b.notifyOrchestratorTerminal(ctx, next, verdict, now, cycleID)
	return 1
}

// terminalNotice builds the orchestrator's wake text for a terminal verdict.
// Every field is machine-readable so the receiver can branch on it without
// parsing prose. The structured WakePayload fields are duplicated here on
// purpose: the wake text is the envelope the orchestrator actually reads.
func terminalNotice(w domain.Watch, verdict domain.Verdict, revision uint64) string {
	return fmt.Sprintf(
		"terminal_ack: watchID=%s, pr=%s#%d, verdict=%s, closeReason=%s, revision=%d, nextAction: report_to_orchestrator, nextHint: wave item done; proceed to next wave",
		w.ID, w.Repo, w.PR, verdict, w.CloseReason, revision,
	)
}

// notifyOrchestratorTerminal sends exactly one confirmation to the project's
// bound orchestrator after a Watch closes on a terminal verdict. The address
// comes from OrchestratorIDSource (BoundOrchestrator(projectKey)); an unbound
// project sends nothing and never panics. Delivery is remembered on the
// obligation keyed by (watchID, revision, orchestratorSession), so a repeated
// Refresh cannot steer the same wave item twice, even after a restart. There
// is no in-memory flag: SQLite is the only deduplication state.
func (b *verdictApplier) notifyOrchestratorTerminal(ctx context.Context, w domain.Watch, verdict domain.Verdict, now time.Time, cycleID string) {
	if b == nil {
		return
	}
	var session string
	if b.orchestratorID != nil {
		session = strings.TrimSpace(b.orchestratorID())
	}
	if session == "" {
		observe.WakeSkipped("", w.ID, "terminal verdict: no bound orchestrator")
		return
	}
	if b.waker == nil && b.adapterWaker == nil {
		return
	}

	obligationID := domain.ObligationKey(w.ID, w.Revision, session)
	o, err := b.store.Obligation(obligationID)
	if err == nil && !o.DeliveredAt.IsZero() {
		observe.WakeSkipped("", w.ID, "terminal confirmation already delivered for this revision")
		return
	}
	if err != nil {
		o = domain.Obligation{
			ID: obligationID, WatchID: w.ID, Revision: w.Revision,
			SubscriberID: session, VerdictPointer: verdict, StagedAt: now,
		}
		if err := b.store.SaveObligation(o); err != nil {
			observe.ActorError("cibridge-terminal-obligation", w.ID, err)
			return
		}
	}
	// Arm the ladder at delivery, as wakeOne does. A subscriber that only owns
	// a readable pointer never gets here because no wake is addressed to it.
	b.stage(ctx, o, verdict, now)

	p := domain.WakePayload{
		WatchID: w.ID, ObligationID: obligationID,
		Revision: w.Revision, Verdict: terminalNotice(w, verdict, w.Revision),
	}
	adapter := b.blockedAdapter(w, session)
	var deliverErr error
	if b.adapterWaker != nil {
		deliverErr = b.adapterWaker.Deliver(ctx, adapter, session, p)
	} else {
		deliverErr = b.waker.Deliver(ctx, session, p)
	}
	if deliverErr != nil {
		outcome := domain.OutcomeTransportFailed
		if isDeadSessionErr(deliverErr) {
			outcome = domain.OutcomeIndeterminate
		}
		b.recordDelivery(ctx, obligationID, 1, outcome, now)
		observe.WakeFailedOutcome(adapter, w.ID, string(outcome), deliverErr, w.Revision)
		observe.ActorError("wake-terminal", w.ID, deliverErr)
		log.Printf("cibridge: terminal confirmation %s: %v", session, deliverErr)
		return
	}
	observe.WakeDeliveredOutcome(adapter, w.ID, observe.WakeOutcomePosted, w.Revision)
	b.recordDelivery(ctx, obligationID, 1, domain.OutcomePosted, now)
	b.markDelivered(obligationID, now, 1, "")
	b.cycleEvent(cycleID, "terminal_confirmation", cycleFields{
		watchID: w.ID, obligationID: obligationID, revision: w.Revision,
		role: domain.RoleOrchestrator, adapter: adapter, verdict: verdict,
	}, observe.Fields{"sessionID": session})
}

// applyBlocked records a failure already present on the base branch. It moves
// the Watch to StatusBlocked/VerdictBlocked through BlockEvent, folds the
// failed checks into a durable BlockerIncident and steers exactly one
// orchestrator session once per incident.
func (b *verdictApplier) applyBlocked(ctx context.Context, w domain.Watch, obs ciObservation, now time.Time, cycleID string) int {
	base := baseRefOf(w)
	failed := realFailedNames(obs.pr.Checks)
	info := domain.BlockedInfo{
		Reason:       domain.BlockReasonBaseChecksFailed,
		FailedChecks: failed,
		Since:        now,
		BlockedBy:    "cibridge",
	}
	next, _ := domain.Transition(w, domain.BlockEvent(info, now))
	if next.Status != domain.StatusBlocked || next.Blocked == nil {
		return 0
	}
	if err := b.store.SaveWatch(next); err != nil {
		observe.ActorError("cibridge-block", next.ID, err)
		return 0
	}
	dedup := domain.ObligationKey(next.ID, next.Revision, "blocked")
	if b.control != nil {
		incident, _, err := b.control.GetOrCreateOpenBlocker(next.Repo, base, failed, next.ID, now)
		if err != nil {
			observe.ActorError("cibridge-blocker", next.ID, err)
		} else {
			dedup = domain.ObligationKey("blocker:"+incident.ID, 0, "wake")
			if next.BlockerID != incident.ID {
				next.BlockerID = incident.ID
				if err := b.store.SaveWatch(next); err != nil {
					observe.ActorError("cibridge-blocker-id", next.ID, err)
				}
			}
		}
	}
	if b.notifyBlocked(ctx, next, dedup, now, cycleID) {
		observe.VerdictPublishedWithChecks(string(domain.VerdictBlocked), next.ID, next.PR, next.Revision, failed)
	}
	b.cycleEvent(cycleID, "block", cycleFields{
		watchID: next.ID, revision: next.Revision, verdict: domain.VerdictBlocked,
	}, observe.Fields{
		observe.FieldBlockerID: next.BlockerID, observe.FieldFailedChecks: failed,
	})
	return 1
}

// blockedNotice builds the orchestrator's wake text for a blocked Watch. It
// carries the PR, reason, failed checks and watch id and ends with the explicit
// stop-and-wait instruction.
func blockedNotice(w domain.Watch) string {
	info := w.Blocked
	checks := strings.Join(info.FailedChecks, ", ")
	if checks == "" {
		checks = "нет"
	}
	return fmt.Sprintf("нужен FIX-агент: PR %s#%d, причина: %s, failedChecks: %s, watchID: %s; остановись и жди разблокировки",
		w.Repo, w.PR, info.Reason, checks, w.ID)
}

// blockedDefaultAdapter is used when the resolved orchestrator address is not
// a subscriber on the Watch, so no subscriber record carries an adapter.
const blockedDefaultAdapter = "opencode"

// blockedOrchestrator resolves the one session allowed to receive a blocked
// notice: the orchestrator subscriber first, then the Watch's persisted
// OrchestratorID, then the configured fallback. A candidate that also names an
// executor subscriber is skipped.
func (b *verdictApplier) blockedOrchestrator(w domain.Watch) string {
	candidates := make([]string, 0, 2+len(w.Subscribers))
	for _, sub := range w.Subscribers {
		if sub.Role == domain.RoleOrchestrator {
			candidates = append(candidates, strings.TrimSpace(sub.ID))
		}
	}
	candidates = append(candidates, strings.TrimSpace(w.OrchestratorID))
	if b != nil && b.orchestratorID != nil {
		candidates = append(candidates, strings.TrimSpace(b.orchestratorID()))
	}
	for _, id := range candidates {
		if id == "" {
			continue
		}
		isExecutor := false
		for _, sub := range w.Subscribers {
			if sub.Role == domain.RoleExecutor && sub.ID == id {
				isExecutor = true
				break
			}
		}
		if !isExecutor {
			return id
		}
	}
	return ""
}

// blockedAdapter returns the adapter recorded on the resolved session when it
// is a subscriber of w. Other sources fall back to the default adapter.
func (b *verdictApplier) blockedAdapter(w domain.Watch, sessionID string) string {
	for _, sub := range w.Subscribers {
		if sub.ID == sessionID && sub.Adapter != "" {
			return sub.Adapter
		}
	}
	return blockedDefaultAdapter
}

// notifyBlocked stages the subscriber obligations at the block revision and
// steers one orchestrator session. Delivery is remembered in the obligation
// identified by dedup, never in an in-memory map, so a process restart cannot
// steer the same incident twice and a new incident gets a fresh key.
func (b *verdictApplier) notifyBlocked(ctx context.Context, w domain.Watch, dedup string, now time.Time, cycleID string) bool {
	for _, sub := range w.Subscribers {
		id := domain.ObligationKey(w.ID, w.Revision, sub.ID)
		if _, err := b.store.Obligation(id); err != nil {
			if err := b.store.SaveObligation(domain.Obligation{
				ID: id, WatchID: w.ID, Revision: w.Revision,
				SubscriberID: sub.ID, VerdictPointer: domain.VerdictBlocked, StagedAt: now,
			}); err != nil {
				log.Printf("cibridge: blocked obligation %s: %v", id, err)
				return false
			}
		}
	}
	sessionID := b.blockedOrchestrator(w)
	if sessionID == "" {
		observe.WakeSkipped("", w.ID, "blocked Watch has no orchestrator address")
		return false
	}
	if b.waker == nil && b.adapterWaker == nil {
		return false
	}
	if delivered, err := b.store.Obligation(dedup); err == nil && !delivered.DeliveredAt.IsZero() {
		observe.WakeSkipped("", w.ID, "block notice already delivered for this incident")
		return false
	} else if err != nil {
		if err := b.store.SaveObligation(domain.Obligation{
			ID: dedup, WatchID: w.ID, Revision: w.Revision,
			SubscriberID: "wake", VerdictPointer: domain.VerdictBlocked, StagedAt: now,
		}); err != nil {
			log.Printf("cibridge: block dedup obligation %s: %v", dedup, err)
			return false
		}
	}

	obligationID := domain.ObligationKey(w.ID, w.Revision, sessionID)
	if _, err := b.store.Obligation(obligationID); err != nil {
		o := domain.Obligation{
			ID: obligationID, WatchID: w.ID, Revision: w.Revision,
			SubscriberID: sessionID, VerdictPointer: domain.VerdictBlocked, StagedAt: now,
		}
		if err := b.store.SaveObligation(o); err != nil {
			log.Printf("cibridge: blocked obligation %s: %v", obligationID, err)
			return false
		}
		b.stage(ctx, o, domain.VerdictBlocked, now)
	}
	p := domain.WakePayload{
		WatchID: w.ID, ObligationID: obligationID,
		Revision: w.Revision, Verdict: blockedNotice(w),
	}
	adapter := b.blockedAdapter(w, sessionID)
	var deliverErr error
	if b.adapterWaker != nil {
		deliverErr = b.adapterWaker.Deliver(ctx, adapter, sessionID, p)
	} else {
		deliverErr = b.waker.Deliver(ctx, sessionID, p)
	}
	if deliverErr != nil {
		outcome := domain.OutcomeTransportFailed
		if isDeadSessionErr(deliverErr) {
			outcome = domain.OutcomeIndeterminate
		}
		b.recordDelivery(ctx, obligationID, 1, outcome, now)
		observe.WakeFailedOutcome(adapter, w.ID, string(outcome), deliverErr, w.Revision)
		observe.ActorError("wake-blocked", w.ID, deliverErr)
		log.Printf("cibridge: blocked wake %s: %v", sessionID, deliverErr)
		return false
	}
	observe.WakeDeliveredOutcome(adapter, w.ID, observe.WakeOutcomePosted, w.Revision)
	b.recordDelivery(ctx, obligationID, 1, domain.OutcomePosted, now)
	b.markDelivered(dedup, now, 1, "")
	b.markDelivered(obligationID, now, 1, "")
	b.cycleEvent(cycleID, "block_escalation", cycleFields{
		watchID: w.ID, obligationID: obligationID, revision: w.Revision,
		role: domain.RoleOrchestrator, adapter: adapter, verdict: domain.VerdictBlocked,
	}, observe.Fields{"sessionID": sessionID, "incident": dedup})
	return true
}

// handleBlocked observes an already blocked Watch. It re-reads it under the
// single-writer assumption, creates the incident when the control plane is
// bound and stages a blocked obligation for every subscriber before steering
// the orchestrator once.
func (b *verdictApplier) handleBlocked(ctx context.Context, w domain.Watch, cycleID string) {
	if w.Blocked == nil {
		return
	}
	now := b.clock.Now()
	current, err := b.store.Watch(w.ID)
	if err != nil || current.Status == domain.StatusClosed || current.Blocked == nil {
		return
	}
	w = current

	dedup := domain.ObligationKey(w.ID, w.Revision, "blocked")
	if b.control != nil {
		incident, _, err := b.control.GetOrCreateOpenBlocker(w.Repo, baseRefOf(w), w.Blocked.FailedChecks, w.ID, now)
		if err != nil {
			observe.ActorError("cibridge-blocker", w.ID, err)
		} else {
			dedup = domain.ObligationKey("blocker:"+incident.ID, 0, "wake")
			if w.BlockerID != incident.ID {
				w.BlockerID = incident.ID
				if err := b.store.SaveWatch(w); err != nil {
					observe.ActorError("cibridge-blocker-id", w.ID, err)
				}
			}
		}
	}
	b.notifyBlocked(ctx, w, dedup, now, cycleID)
}

// isMergeConflict reports a merge hold or error that a rebase can fix.
func isMergeConflict(reason string, err error) bool {
	s := strings.ToLower(reason + " " + errText(err))
	for _, key := range []string{"conflict", "behind", "dirty", "409", "422"} {
		if strings.Contains(s, key) {
			return true
		}
	}
	return false
}

// isMergeBehind reports the stale-base subset of isMergeConflict. Only this
// case is safe to fix by rebasing: a content conflict, a DIRTY branch or a
// 409/422 response needs a human and must keep waking the executor.
func isMergeBehind(reason string, err error) bool {
	s := strings.ToLower(reason + " " + errText(err))
	return strings.Contains(s, "behind")
}

// isMergeRecheck reports the hold a provider returns when the base branch
// moved after the batch snapshot. The returned string is the reason to record
// on the recheck verdict; an empty string means the merge was not a recheck.
func isMergeRecheck(reason string, err error) string {
	s := strings.ToLower(reason + " " + errText(err))
	for _, key := range []string{mergeHeldRecheck, "base_changed", "base changed", "base moved"} {
		if strings.Contains(s, key) {
			return strings.TrimSpace(reason)
		}
	}
	return ""
}

// fixForSource is an optional capability a Store may implement to expose the
// fixer link of a Watch. It keeps the legacy unblock path working until every
// caller persists FixFor on the Watch itself.
type fixForSource interface {
	FixForOf(watchID string) string
}

// watchFixFor returns the id of the Watch a fixer Watch is assigned to clear.
func (b *verdictApplier) watchFixFor(w domain.Watch) string {
	if s := strings.TrimSpace(w.FixFor); s != "" {
		return s
	}
	if b.store != nil {
		if src, ok := b.store.(fixForSource); ok {
			return strings.TrimSpace(src.FixForOf(w.ID))
		}
	}
	return ""
}

// unblockFixed resumes the Watch a merged fixer Watch points at. With a
// control plane bound it resolves the blocker incident so a later block with
// the same signature gets a fresh wake. Without the port it applies the domain
// unblock transition to the single target Watch.
func (b *verdictApplier) unblockFixed(merged domain.Watch) {
	fixer := merged
	if b.watchFixFor(fixer) == "" {
		watches, err := b.store.AllWatches()
		if err != nil {
			observe.ActorError("cibridge-unblock", merged.ID, err)
			log.Printf("cibridge: unblock lookup %s#%d: %v", merged.Repo, merged.PR, err)
			return
		}
		for _, w := range watches {
			if w.Repo != merged.Repo || w.PR != merged.PR || w.Status == domain.StatusClosed {
				continue
			}
			if b.watchFixFor(w) != "" {
				fixer = w
				break
			}
		}
	}
	fixFor := b.watchFixFor(fixer)
	if fixFor == "" || fixFor == fixer.ID {
		return
	}
	target, err := b.store.Watch(fixFor)
	if err != nil {
		observe.ActorError("cibridge-unblock", fixer.ID, err)
		log.Printf("cibridge: unblock target %s: %v", fixFor, err)
		return
	}
	if target.Status != domain.StatusBlocked || target.Blocked == nil {
		return
	}
	now := b.clock.Now()
	if b.control != nil {
		blockerID := target.BlockerID
		if blockerID == "" {
			// A Watch blocked before its incident link was written still
			// carries the blocked payload. Find its open incident by membership.
			active, err := b.control.ActiveBlockers()
			if err != nil {
				observe.ActorError("cibridge-unblock-blockers", target.ID, err)
			} else {
				for _, incident := range active {
					for _, id := range incident.BlockedWatchIDs {
						if id == target.ID {
							blockerID = incident.ID
							break
						}
					}
					if blockerID != "" {
						break
					}
				}
			}
		}
		if blockerID != "" {
			unblocked, err := b.control.ResolveBlocker(blockerID, now)
			if err != nil {
				observe.ActorError("cibridge-unblock-resolve", target.ID, err)
				log.Printf("cibridge: unblock resolve %s: %v", target.ID, err)
				return
			}
			ids := make([]string, 0, len(unblocked))
			for _, unwound := range unblocked {
				ids = append(ids, unwound.ID)
			}
			b.rotateGenerations(ids...)
			log.Printf("cibridge: %s unblocked by %s#%d via blocker %s", target.ID, fixer.Repo, fixer.PR, blockerID)
			return
		}
	}
	next, _ := domain.Transition(target, domain.UnblockEvent(now))
	if next.Revision == target.Revision || next.Status != domain.StatusWatching {
		return
	}
	if err := b.store.SaveWatch(next); err != nil {
		observe.ActorError("cibridge-unblock", target.ID, err)
		log.Printf("cibridge: unblock save %s: %v", target.ID, err)
		return
	}
	b.rotateGenerations(target.ID)
	log.Printf("cibridge: %s unblocked by %s#%d revision=%d", target.ID, fixer.Repo, fixer.PR, next.Revision)
}
