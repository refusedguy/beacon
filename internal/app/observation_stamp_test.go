package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestPendingChecksLeaveObservationUnstamped(t *testing.T) {
	ctx := context.Background()
	prior := core.Observation{
		PRNumber: 42, PRState: "open", HeadSHA: "old-head", BaseSHA: "old-base",
	}
	obs := core.Observation{
		PRNumber: 42, PRState: "open", HeadSHA: "new-head", BaseSHA: "new-base", Behind: true,
		Checks: []core.CheckResult{{Name: "ci", State: core.CheckPending}},
	}
	store := newFakeStore()
	run := makeRun("r-pending", 42, core.RunWaitingCI)
	run.HeadSHA = prior.HeadSHA
	run.NoteObservationIdentity(prior.HeadSHA, prior.BaseSHA, observationIdentity(prior))
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf(`store.Save(ctx, run): %v`, err)
	}
	wantIdentity := run.ObservedIdentity
	r := &Reconciler{
		Store: store, SCM: &fakeSCM{obs: obs}, Policy: behindAwarePolicy{},
		Harness: &fakeHarness{}, Source: &fakeSource{}, Workspace: &fakeWorkspace{}, AutoRebase: true,
	}

	for i := 0; i < 3; i++ {
		decision, err := r.Reconcile(ctx, 42)
		if err != nil {
			t.Fatalf("tick %d: r.Reconcile(ctx, 42): %v", i, err)
		}
		if decision.Verdict != core.VerdictNeedsRebase {
			t.Fatalf("tick %d: verdict = %s, want %s", i, decision.Verdict, core.VerdictNeedsRebase)
		}
	}

	got := store.runs[run.ID]
	if got.ObservedSHA != prior.HeadSHA || got.ObservedBaseSHA != prior.BaseSHA || got.ObservedIdentity != wantIdentity {
		t.Fatalf("observed identity = %q/%q/%q, want %q/%q/%q",
			got.ObservedSHA, got.ObservedBaseSHA, got.ObservedIdentity,
			prior.HeadSHA, prior.BaseSHA, wantIdentity)
	}
	if got.HeadSHA != prior.HeadSHA {
		t.Fatalf("head = %q, want %q", got.HeadSHA, prior.HeadSHA)
	}
	if got.RebaseAttempts != 0 || got.FixAttempts != 0 {
		t.Fatalf("rebaseAttempts=%d fixAttempts=%d, want 0/0", got.RebaseAttempts, got.FixAttempts)
	}
}

// baseAwareSCM reports the current head and base and advances the head only
// when a branch update succeeds.
type baseAwareSCM struct {
	*fakeSCM
	head, base string
	updates    int
}

func (s *baseAwareSCM) Observe(_ context.Context, pr int) (core.Observation, error) {
	return core.Observation{PRNumber: pr, PRState: "open", HeadSHA: s.head, BaseSHA: s.base, Behind: true}, nil
}

func (s *baseAwareSCM) UpdateBranch(_ context.Context, _ int, sha string) (string, error) {
	s.updates++
	s.head = "new-" + sha
	return s.head, nil
}

func TestCommittedDecisionStampsIdentityOnce(t *testing.T) {
	ctx := context.Background()
	obs := core.Observation{
		PRNumber: 42, PRState: "open", HeadSHA: "new-head", BaseSHA: "new-base", Behind: true,
	}
	store := newObservationSpyStore()
	run := makeRun("r-committed", 42, core.RunWaitingCI)
	run.ObservedSHA = "old-head"
	run.ObservedBaseSHA = "old-base"
	run.UpdatedAt = time.Now().UTC().Add(-time.Hour)
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf(`store.Save(ctx, run): %v`, err)
	}
	scm := &baseAwareSCM{fakeSCM: &fakeSCM{}, head: obs.HeadSHA, base: obs.BaseSHA}
	r := &Reconciler{
		Store: store, SCM: scm, Policy: fakePolicy{decision: core.MergeDecision{Verdict: core.VerdictNeedsRebase}},
		Harness: &fakeHarness{}, Source: &fakeSource{}, Workspace: &fakeWorkspace{}, AutoRebase: true,
	}

	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatalf(`r.Reconcile(ctx, 42): %v`, err)
	}
	first := store.runs[run.ID]
	if first.ObservedIdentity == "" {
		t.Fatal("committed decision left the observation identity empty")
	}

	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatalf(`r.Reconcile(ctx, 42): %v`, err)
	}
	got := store.runs[run.ID]
	if got.State != core.RunWaitingCI || scm.updates != 1 {
		t.Fatalf("state=%s branch updates=%d, want waiting_ci/1", got.State, scm.updates)
	}
	if got.ObservedSHA != "new-new-head" || got.ObservedBaseSHA != obs.BaseSHA {
		t.Fatalf("observed pair = %q/%q, want new-new-head/%s",
			got.ObservedSHA, got.ObservedBaseSHA, obs.BaseSHA)
	}
	if got.ObservedIdentity != first.ObservedIdentity {
		t.Fatal("a repeated tick stamped the identity again")
	}
	if store.recorded != 1 {
		t.Fatalf("observation saves = %d, want 1", store.recorded)
	}
}

// rebaseThenFixPolicy asks for a branch update on the first tick and a fix on
// the ones after it. A provider failure moves the run to needs_agent_fix, so
// the next tick exercises the wake path with the same observation.
type rebaseThenFixPolicy struct{ calls int }

func (p *rebaseThenFixPolicy) Evaluate(core.Observation) core.MergeDecision {
	p.calls++
	if p.calls == 1 {
		return core.MergeDecision{Verdict: core.VerdictNeedsRebase, Reason: "branch is behind base"}
	}
	return core.MergeDecision{Verdict: core.VerdictNeedsAgentFix, Reason: "required check failed: ci"}
}

func (p *rebaseThenFixPolicy) ReviewRequired(core.ReviewMode, core.Observation) bool { return true }

func TestFailedRebaseWakesFixerOnFirstAttempt(t *testing.T) {
	ctx := context.Background()
	prior := core.Observation{
		PRNumber: 42, PRState: "open", HeadSHA: "old-head", BaseSHA: "old-base",
	}
	obs := core.Observation{
		PRNumber: 42, PRState: "open", HeadSHA: "new-head", BaseSHA: "new-base", Behind: true,
		Checks:         []core.CheckResult{{Name: "ci", State: core.CheckFailure}},
		FailureExcerpt: "ci reported a failure",
	}
	store := newFakeStore()
	run := makeRun("r-rebase-failed", 42, core.RunWaitingCI)
	run.HeadSHA = prior.HeadSHA
	run.NoteObservationIdentity(prior.HeadSHA, prior.BaseSHA, observationIdentity(prior))
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf(`store.Save(ctx, run): %v`, err)
	}
	scm := &fakeSCM{obs: obs, updateErr: errors.New("expectedHeadOid mismatch")}
	h := &fakeHarness{}
	r := &Reconciler{
		Store: store, SCM: scm, Policy: &rebaseThenFixPolicy{},
		Harness: h, Source: &fakeSource{}, Workspace: &fakeWorkspace{}, AutoRebase: true,
	}

	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatalf(`r.Reconcile(ctx, 42): %v`, err)
	}
	failed := store.runs[run.ID]
	if failed.State != core.RunNeedsAgentFix {
		t.Fatalf("state after failed rebase = %s, want %s", failed.State, core.RunNeedsAgentFix)
	}
	if len(h.wakes) != 0 {
		t.Fatalf("wakes after failed rebase = %d, want 0", len(h.wakes))
	}
	if failed.ObservedSHA != obs.HeadSHA || failed.ObservedBaseSHA != obs.BaseSHA {
		t.Fatalf("observed pair after failed rebase = %q/%q, want %q/%q",
			failed.ObservedSHA, failed.ObservedBaseSHA, obs.HeadSHA, obs.BaseSHA)
	}
	if failed.ObservedIdentity == observationIdentity(obs) {
		t.Fatal("failed rebase stamped the wake identity and would drop the wake")
	}

	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatalf(`r.Reconcile(ctx, 42): %v`, err)
	}
	woke := store.runs[run.ID]
	if len(h.wakes) != 1 || woke.FixAttempts != 1 {
		t.Fatalf("wakes=%d fixAttempts=%d, want 1/1", len(h.wakes), woke.FixAttempts)
	}
	if h.wakes[0].HeadSHA != obs.HeadSHA {
		t.Fatalf("wake head = %q, want %q", h.wakes[0].HeadSHA, obs.HeadSHA)
	}
	if woke.ObservedIdentity != observationIdentity(obs) {
		t.Fatal("delivered wake did not stamp the full observation identity")
	}

	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatalf(`r.Reconcile(ctx, 42): %v`, err)
	}
	if len(h.wakes) != 1 || store.runs[run.ID].FixAttempts != 1 {
		t.Fatalf("repeated tick woke the fixer again: wakes=%d fixAttempts=%d",
			len(h.wakes), store.runs[run.ID].FixAttempts)
	}
}
