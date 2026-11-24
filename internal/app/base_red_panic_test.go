package app

import (
	"context"
	"errors"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

// nilIncidentStore reproduces the sqlite behaviour when an incident with the
// same signature is already resolved: the conflict insert is a no-op and the
// open-incident lookup returns (nil, nil).
type nilIncidentStore struct {
	*fakeStore
	calls int
}

func (s *nilIncidentStore) CreateIncident(ctx context.Context, in *core.BaseIncident) (*core.BaseIncident, error) {
	s.calls++
	if s.calls == 1 {
		return nil, nil
	}
	return s.fakeStore.CreateIncident(ctx, in)
}

type saveErrorStore struct {
	*fakeStore
	failFor core.RunID
}

func (s *saveErrorStore) Save(ctx context.Context, run *core.Run) error {
	if run.ID == s.failFor {
		return errors.New("simulated save failure")
	}
	return s.fakeStore.Save(ctx, run)
}

type panicObserveSCM struct {
	*fakeSCM
	panicFor int
}

func (s *panicObserveSCM) Observe(ctx context.Context, pr int) (core.Observation, error) {
	if pr == s.panicFor {
		panic("simulated observation panic")
	}
	return s.fakeSCM.Observe(ctx, pr)
}

func addFailingRun(t *testing.T, store *fakeStore, scm *fakeSCM, id core.RunID, pr int) {
	t.Helper()
	if err := store.Save(context.Background(), makeRun(id, pr, core.RunWaitingCI)); err != nil {
		t.Fatal(err)
	}
	if scm.obsByPR == nil {
		scm.obsByPR = map[int]core.Observation{}
	}
	scm.obsByPR[pr] = core.Observation{
		PRNumber: pr, HeadSHA: "sha2", PRState: "open",
		Checks: []core.CheckResult{{Name: "ci", State: core.CheckFailure}},
	}
}

func TestCheckBaseRedNilIncidentDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	r, store, scm, _, _ := redReconciler(t)
	addFailingRun(t, store, scm, "good", 44)

	wrapped := &nilIncidentStore{fakeStore: store}
	r.Store = wrapped

	// The first tick used to panic at `other.BlockedIncidentID = in.ID`.
	if err := r.checkBaseRed(ctx, store.runs["r1"], scm.obs, "ci failed"); err != nil {
		t.Fatal(err)
	}
	if wrapped.calls != 1 {
		t.Fatalf("CreateIncident calls = %d, want 1", wrapped.calls)
	}
	for _, id := range []core.RunID{"r1", "good"} {
		if got := store.runs[id]; got.State == core.RunBlocked {
			t.Fatalf("%s blocked without an incident", id)
		}
	}

	// A later tick with a real incident still blocks every valid run.
	if err := r.checkBaseRed(ctx, store.runs["r1"], scm.obs, "ci failed"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []core.RunID{"r1", "good"} {
		got := store.runs[id]
		if got.State != core.RunBlocked {
			t.Fatalf("%s state = %s, want blocked", id, got.State)
		}
		if got.BlockedIncidentID == "" {
			t.Fatalf("%s has no incident ID", id)
		}
	}
}

func TestCheckBaseRedContinuesAfterSaveError(t *testing.T) {
	ctx := context.Background()
	r, store, scm, _, _ := redReconciler(t)
	addFailingRun(t, store, scm, "bad", 43)
	addFailingRun(t, store, scm, "good", 44)
	r.Store = &saveErrorStore{fakeStore: store, failFor: "bad"}

	if err := r.checkBaseRed(ctx, store.runs["r1"], scm.obs, "ci failed"); err != nil {
		t.Fatalf("Save error escaped the incident loop: %v", err)
	}
	got := store.runs["good"]
	if got.State != core.RunBlocked || got.BlockedIncidentID == "" {
		t.Fatalf("valid run not blocked after bad run failed: %+v", got)
	}
}

func TestSafeReconcileRecoversPanicAndContinues(t *testing.T) {
	ctx := context.Background()
	r, store, scm, _, _ := redReconciler(t)
	addFailingRun(t, store, scm, "good", 44)

	r.SCM = &panicObserveSCM{fakeSCM: scm, panicFor: 42}
	// An unrecovered panic would kill the test process.
	r.safeReconcile(ctx, 42)

	// The daemon keeps reconciling other PRs after the panic.
	r.SCM = scm
	r.safeReconcile(ctx, 42)
	for _, id := range []core.RunID{"r1", "good"} {
		got := store.runs[id]
		if got.State != core.RunBlocked {
			t.Fatalf("%s state = %s, want blocked", id, got.State)
		}
	}
}
