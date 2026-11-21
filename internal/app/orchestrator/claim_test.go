package orchestrator

import (
	"errors"
	"testing"
)

// TestClaimGenerationResolveRotatesAndStalesOldClaim is the acceptance check
// for T3.457: a blocked:resolved must change the row's generation and make the
// claim held by the previous owner fail. The row returns to pending, so the
// following swap has to stamp a fresh generation before work can continue.
func TestClaimGenerationResolveRotatesAndStalesOldClaim(t *testing.T) {
	ledger := NewClaimGeneration()

	row, err := ledger.EnterInProgress("watch:D1", "owner-A")
	if err != nil {
		t.Fatalf("enter in_progress: %v", err)
	}
	old := row.Generation
	if old == "" || row.ClaimedBy != old || row.Status != ClaimInProgress {
		t.Fatalf("stamped row = %+v, want in_progress with matching generation", row)
	}
	if got := ledger.GenerationFor("owner-A"); got != old {
		t.Fatalf("owner generation = %q, want %q", got, old)
	}
	if got, err := ledger.Claim("watch:D1", old); err != nil || got.TaskID != "watch:D1" {
		t.Fatalf("live claim = %+v, err = %v, want the stamped row", got, err)
	}

	rotated, applied := ledger.ResolveBlocked("watch:D1")
	if !applied || rotated == "" || rotated == old {
		t.Fatalf("resolve = %q/%v, want a fresh generation and applied", rotated, applied)
	}
	pending, ok := ledger.Snapshot("watch:D1")
	if !ok || pending.Status != ClaimPending || pending.ClaimedBy != "" || pending.Generation != rotated {
		t.Fatalf("row after resolve = %+v, want pending at %q with no claim stamp", pending, rotated)
	}

	if _, err := ledger.Claim("watch:D1", old); !IsStaleClaim(err) {
		t.Fatalf("old claim after resolve: err = %v, want StaleClaimError", err)
	}
	if ledger.Valid("watch:D1", old) {
		t.Fatal("old generation is still valid after resolve")
	}

	again, reapplied := ledger.ResolveBlocked("watch:D1")
	if reapplied || again != "" {
		t.Fatalf("repeat resolve = %q/%v, want no-op", again, reapplied)
	}
	if current, _ := ledger.Snapshot("watch:D1"); current.Generation != rotated {
		t.Fatalf("repeat resolve moved generation: %q, want %q", current.Generation, rotated)
	}
}

// TestClaimGenerationSwapUsesAssigneeNotActor proves the generation on the row
// belongs to the assignee the row is handed to, not to the fixer that resolved
// the block. The actor's generation is issued to the actor and is never
// accepted as a claim on the Watch.
func TestClaimGenerationSwapUsesAssigneeNotActor(t *testing.T) {
	ledger := NewClaimGeneration()

	actorGeneration := ledger.IssueFor("fixer")
	first, err := ledger.EnterInProgress("watch:D1", "owner-A")
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := ledger.Claim("watch:D1", actorGeneration); !IsStaleClaim(err) {
		t.Fatalf("actor generation accepted before resolve: %v", err)
	}

	if _, applied := ledger.ResolveBlocked("watch:D1"); !applied {
		t.Fatal("resolve did not rotate the generation")
	}
	swapped, err := ledger.EnterInProgress("watch:D1", "owner-B")
	if err != nil {
		t.Fatalf("swap: %v", err)
	}
	if swapped.Generation != ledger.GenerationFor("owner-B") {
		t.Fatalf("swap generation = %q, want assignee's %q", swapped.Generation, ledger.GenerationFor("owner-B"))
	}
	if swapped.Generation == first.Generation || swapped.Generation == actorGeneration {
		t.Fatalf("swap reused a stale generation: %q", swapped.Generation)
	}
	if _, err := ledger.Claim("watch:D1", first.Generation); !IsStaleClaim(err) {
		t.Fatalf("previous owner still passes after swap: %v", err)
	}
	if _, err := ledger.Claim("watch:D1", actorGeneration); !IsStaleClaim(err) {
		t.Fatalf("actor still passes after swap: %v", err)
	}
	if got, err := ledger.Claim("watch:D1", swapped.Generation); err != nil || got.AssigneeID != "owner-B" {
		t.Fatalf("new owner claim = %+v, err = %v, want owner-B", got, err)
	}
}

// TestClaimGenerationStaleClaimErrorShape checks the error carries both sides
// of the comparison, so a caller can log why the claim was refused.
func TestClaimGenerationStaleClaimErrorShape(t *testing.T) {
	ledger := NewClaimGeneration()
	row, err := ledger.EnterInProgress("watch:D1", "owner-A")
	if err != nil {
		t.Fatalf("enter in_progress: %v", err)
	}
	ledger.ResolveBlocked("watch:D1")

	_, err = ledger.Claim("watch:D1", row.Generation)
	var stale *StaleClaimError
	if !IsStaleClaim(err) || !errors.As(err, &stale) {
		t.Fatalf("err = %v, want StaleClaimError", err)
	}
	if stale.TaskID != "watch:D1" || stale.Presented != row.Generation || stale.Current == "" {
		t.Fatalf("stale error = %+v, want task, presented and current generation", stale)
	}
}

func TestClaimGenerationRejectsEmptyIdentity(t *testing.T) {
	ledger := NewClaimGeneration()
	for _, tc := range []struct{ task, owner string }{
		{task: "", owner: "owner-A"},
		{task: "watch:D1", owner: ""},
	} {
		if _, err := ledger.EnterInProgress(tc.task, tc.owner); err == nil {
			t.Fatalf("EnterInProgress(%q, %q) succeeded, want an error", tc.task, tc.owner)
		}
	}
	if g := ledger.IssueFor("  "); g != "" {
		t.Fatalf("IssueFor(blank) = %q, want empty", g)
	}
	if ledger.Valid("watch:D1", "generation-1") {
		t.Fatal("unissued generation must not validate")
	}
}

func TestClaimGenerationSnapshotIsCopy(t *testing.T) {
	ledger := NewClaimGeneration()
	row, err := ledger.EnterInProgress("watch:D1", "owner-A")
	if err != nil {
		t.Fatalf("enter in_progress: %v", err)
	}
	snap, ok := ledger.Snapshot("watch:D1")
	if !ok {
		t.Fatal("snapshot missing")
	}
	snap.Status = ClaimPending
	snap.ClaimedBy = "forged"

	current, _ := ledger.Snapshot("watch:D1")
	if current.Status != row.Status || current.ClaimedBy != row.ClaimedBy {
		t.Fatalf("ledger mutated through snapshot: %+v", current)
	}
}
