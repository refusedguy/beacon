package app

import (
	"context"
	"errors"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

// changedWorkspace reports pending changes so an idle event takes the nudge
// path instead of blocking the run.
type changedWorkspace struct {
	*fakeWorkspace
	changed bool
}

func (w *changedWorkspace) HasChanges(context.Context, core.Workspace, string) (bool, error) {
	return w.changed, nil
}

// lookupSCM returns a fixed FindOpenPRByBranch result without changing the
// rest of the fake SCM.
type lookupSCM struct {
	*fakeSCM
	err error
}

func (s *lookupSCM) FindOpenPRByBranch(context.Context, string) (int, error) {
	return 0, s.err
}

func idleRunWithSCM(t *testing.T, scm core.ScmGateway) (*Reconciler, *fakeStore) {
	t.Helper()
	ctx := context.Background()
	store := newFakeStore()
	run := makeRun("r1", 0, core.RunRunning)
	run.Branch = "beacon/T-1"
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{
		Store: store, SCM: scm,
		Workspace:  &changedWorkspace{fakeWorkspace: &fakeWorkspace{}},
		BaseBranch: "main",
	}
	return r, store
}

func TestSecondIdleEventNudgesAgain(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := makeRun("r1", 0, core.RunRunning)
	run.Branch = "beacon/T-1"
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	h := &fakeHarness{}
	r := &Reconciler{
		Store: store, SCM: &fakeSCM{}, Harness: h,
		Workspace:  &changedWorkspace{fakeWorkspace: &fakeWorkspace{}, changed: true},
		BaseBranch: "main",
	}

	r.HandleHarnessEvent(ctx, core.HarnessEvent{RunID: "r1", Kind: "idle"})
	r.HandleHarnessEvent(ctx, core.HarnessEvent{RunID: "r1", Kind: "idle"})

	if len(h.wakes) != 2 {
		t.Fatalf("wakes = %d, want one per idle event", len(h.wakes))
	}
	if got := store.runs["r1"].State; got != core.RunRunning {
		t.Fatalf("run state = %s, want running after both nudges", got)
	}
	if len(store.deliveries) != 0 {
		t.Fatalf("deliveries = %v, want the idle nudge claim released", store.deliveries)
	}
}

func TestIdleWithoutPRDoesNotLogLookupFailure(t *testing.T) {
	ctx := context.Background()
	logs := captureDecisions(t)
	scm := &lookupSCM{
		fakeSCM: &fakeSCM{},
		err:     core.Errorf(core.CodeNotFound, "no open PR for branch beacon/T-1"),
	}
	r, _ := idleRunWithSCM(t, scm)

	r.HandleHarnessEvent(ctx, core.HarnessEvent{RunID: "r1", Kind: "idle"})

	for _, record := range logs.snapshot() {
		if record.Message == "idle PR lookup" {
			t.Fatalf("missing PR logged as a lookup failure: %v", record)
		}
	}
}

func TestIdleLookupOutageIsLogged(t *testing.T) {
	ctx := context.Background()
	logs := captureDecisions(t)
	want := errors.New("scm outage")
	r, _ := idleRunWithSCM(t, &lookupSCM{fakeSCM: &fakeSCM{}, err: want})

	r.HandleHarnessEvent(ctx, core.HarnessEvent{RunID: "r1", Kind: "idle"})

	record := decisionRecord(t, logs, "idle PR lookup")
	if got := decisionFields(record)["error"]; got != want {
		t.Fatalf("logged error = %v, want %v", got, want)
	}
}
