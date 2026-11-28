package app

import (
	"context"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

// saveCountStore counts Save calls while delegating to the shared fake store.
type saveCountStore struct {
	*fakeStore
	saves int
}

func (s *saveCountStore) Save(ctx context.Context, run *core.Run) error {
	s.saves++
	return s.fakeStore.Save(ctx, run)
}

// reasonRun stores a run whose recorded observation matches obs, with a stale
// reason from an earlier decision. It clears the save count so a test measures
// only the call under test.
func reasonRun(t *testing.T, store *saveCountStore, obs core.Observation, reason string) *core.Run {
	t.Helper()
	run := makeRun("r1", 42, core.RunWaitingCI)
	run.HeadSHA = obs.HeadSHA
	run.Reason = reason
	run.ObservedSHA = obs.HeadSHA
	run.ObservedBaseSHA = obs.BaseSHA
	run.ObservedIdentity = observationIdentity(obs)
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatalf("store.Save: %v", err)
	}
	store.saves = 0
	return run
}

// assertObservationUntouched checks that no NoteObservation or
// NoteObservationIdentity call stamped the run during a reason update.
func assertObservationUntouched(t *testing.T, before, after *core.Run) {
	t.Helper()
	if after.ObservedSHA != before.ObservedSHA ||
		after.ObservedBaseSHA != before.ObservedBaseSHA ||
		after.ObservedIdentity != before.ObservedIdentity {
		t.Fatalf("observation stamp changed: before %q/%q/%q, after %q/%q/%q",
			before.ObservedSHA, before.ObservedBaseSHA, before.ObservedIdentity,
			after.ObservedSHA, after.ObservedBaseSHA, after.ObservedIdentity)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("UpdatedAt changed from %v to %v", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestChangedDecisionReasonReplacesStaleReason(t *testing.T) {
	ctx := context.Background()
	obs := core.Observation{
		PRNumber: 42, PRState: "open", HeadSHA: "sha42", BaseSHA: "base1",
		Checks: []core.CheckResult{{Name: "test-modules", State: core.CheckFailure}},
	}
	const stale = "unavailable: update branch: Variable $sha of type GitObjectID! was provided invalid value"
	const fresh = "required check failed: test-modules (root infra)"

	store := &saveCountStore{fakeStore: newFakeStore()}
	run := reasonRun(t, store, obs, stale)
	before := *store.runs[run.ID]
	h := &fakeHarness{}
	r := &Reconciler{Store: store, SCM: &fakeSCM{obs: obs}, Harness: h, Source: &fakeSource{}, Workspace: &fakeWorkspace{}}

	decision := core.MergeDecision{Verdict: core.VerdictNeedsAgentFix, Reason: fresh}
	got, err := r.handleNeedsAgentFix(ctx, run, obs, decision)
	if err != nil {
		t.Fatalf("handleNeedsAgentFix: %v", err)
	}
	if got != decision {
		t.Fatalf("returned decision = %+v, want %+v", got, decision)
	}
	if store.saves != 1 {
		t.Fatalf("Save calls = %d, want 1", store.saves)
	}
	after := store.runs[run.ID]
	if after.Reason != fresh {
		t.Fatalf("stored reason = %q, want %q", after.Reason, fresh)
	}
	if run.Reason != fresh {
		t.Fatalf("in-memory reason = %q, want %q", run.Reason, fresh)
	}
	if after.State != core.RunWaitingCI {
		t.Fatalf("state = %s, want %s so no check-failed event was applied", after.State, core.RunWaitingCI)
	}
	assertObservationUntouched(t, &before, after)
	if len(h.wakes) != 0 {
		t.Fatalf("wakes = %d, want 0", len(h.wakes))
	}
}

func TestUnchangedDecisionReasonSkipsSave(t *testing.T) {
	ctx := context.Background()
	obs := core.Observation{
		PRNumber: 42, PRState: "open", HeadSHA: "sha42", BaseSHA: "base1",
		Checks: []core.CheckResult{{Name: "test-modules", State: core.CheckFailure}},
	}
	const reason = "required check failed: test-modules (root infra)"

	store := &saveCountStore{fakeStore: newFakeStore()}
	run := reasonRun(t, store, obs, reason)
	before := *store.runs[run.ID]
	h := &fakeHarness{}
	r := &Reconciler{Store: store, SCM: &fakeSCM{obs: obs}, Harness: h, Source: &fakeSource{}, Workspace: &fakeWorkspace{}}

	decision := core.MergeDecision{Verdict: core.VerdictNeedsAgentFix, Reason: reason}
	got, err := r.handleNeedsAgentFix(ctx, run, obs, decision)
	if err != nil {
		t.Fatalf("handleNeedsAgentFix: %v", err)
	}
	if got != decision {
		t.Fatalf("returned decision = %+v, want %+v", got, decision)
	}
	if store.saves != 0 {
		t.Fatalf("Save calls = %d, want 0", store.saves)
	}
	after := store.runs[run.ID]
	if after.Reason != reason {
		t.Fatalf("stored reason = %q, want %q", after.Reason, reason)
	}
	if after.State != core.RunWaitingCI {
		t.Fatalf("state = %s, want %s so no check-failed event was applied", after.State, core.RunWaitingCI)
	}
	assertObservationUntouched(t, &before, after)
	if len(h.wakes) != 0 {
		t.Fatalf("wakes = %d, want 0", len(h.wakes))
	}
}
