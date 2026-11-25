package app

import (
	"context"
	"errors"
	"testing"

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
