package app

import (
	"context"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestCheckBaseRedSkipsFixerAndUnrelatedPRs(t *testing.T) {
	r, store, scm, _, _ := redReconciler(t)
	ctx := context.Background()

	fixer := makeRun("fix-1", 99, core.RunWaitingCI)
	fixer.IsFixer = true
	if err := store.Save(ctx, fixer); err != nil {
		t.Fatal(err)
	}
	unrelated := makeRun("r2", 43, core.RunWaitingCI)
	if err := store.Save(ctx, unrelated); err != nil {
		t.Fatal(err)
	}
	scm.obsByPR = map[int]core.Observation{
		43: {PRNumber: 43, Checks: []core.CheckResult{{Name: "lint", State: core.CheckSuccess}}},
	}

	if err := r.checkBaseRed(ctx, store.runs["r1"], scm.obs, "ci failed"); err != nil {
		t.Fatal(err)
	}
	if store.runs["fix-1"].State != core.RunWaitingCI {
		t.Fatalf("fixer was blocked: %s", store.runs["fix-1"].State)
	}
	if store.runs["r2"].State != core.RunWaitingCI {
		t.Fatalf("unrelated PR was blocked: %s", store.runs["r2"].State)
	}
	if store.runs["r1"].State != core.RunBlocked {
		t.Fatalf("matching PR was not blocked: %s", store.runs["r1"].State)
	}
}
