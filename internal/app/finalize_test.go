package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// failingOpenPRSCM makes epic PR publication fail after the delivery key is claimed.
type failingOpenPRSCM struct {
	*fakeSCM
	err error
}

func (f *failingOpenPRSCM) OpenPR(context.Context, string, string, string) (int, error) {
	return 0, f.err
}

func TestBaseBranchFollowsTaskParentNotIDShape(t *testing.T) {
	ctx := context.Background()
	r := &Reconciler{
		Source: &fakeSource{tasks: []core.Task{
			{ID: "T-EPIC", Kind: core.TaskKindEpic, Status: core.TaskAvailable},
			{ID: "T-CHILD", Parent: "T-EPIC", Status: core.TaskAvailable},
		}},
		BaseBranch: "main",
	}
	run := core.NewRun("r1", "T-CHILD", time.Now().UTC())

	if got := r.baseBranchFor(ctx, run); got != "beacon/T-EPIC" {
		t.Fatalf("base branch = %q, want beacon/T-EPIC", got)
	}
}

func TestEpicPRDeliveryReleasedWhenOpenFails(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.ready = []core.Task{{ID: "T-EPIC", Title: "Big epic", Kind: core.TaskKindEpic}}
	r := &Reconciler{
		Store: store, SCM: &failingOpenPRSCM{fakeSCM: &fakeSCM{}, err: errors.New("open failed")},
		Harness: &fakeHarness{}, Source: epicSource(core.ReviewModeNone),
		Workspace: &fakeWorkspace{}, BaseBranch: "main",
	}

	if err := r.reconcileEpics(ctx); err != nil {
		t.Fatal(err)
	}
	key := "epic-pr:T-EPIC"
	if store.deliveries[key] {
		t.Fatal("epic delivery claim survived a failed PR open")
	}
	ok, err := store.ClaimDelivery(ctx, key)
	if err != nil || !ok {
		t.Fatalf("later tick cannot reclaim epic delivery: ok=%v err=%v", ok, err)
	}
}
