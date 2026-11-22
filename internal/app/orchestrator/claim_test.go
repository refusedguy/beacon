package orchestrator

import (
	"errors"
	"sync"
	"testing"
)

// memGenerationStore is a test store whose state lives outside the ledger, so
// dropping one ledger and building another from the same store models a process
// restart. The mutex keeps the fake safe for the ledger's own locking.
type memGenerationStore struct {
	mu      sync.Mutex
	state   GenerationState
	loadErr error
	saveErr error
}

func (s *memGenerationStore) Load() (GenerationState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return GenerationState{}, s.loadErr
	}
	return cloneGenerationState(s.state), nil
}

func (s *memGenerationStore) Save(state GenerationState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.state = cloneGenerationState(state)
	return nil
}

func cloneGenerationState(in GenerationState) GenerationState {
	out := GenerationState{
		Seq:    in.Seq,
		Rows:   make(map[string]ClaimRow, len(in.Rows)),
		Owners: make(map[string]Generation, len(in.Owners)),
	}
	for taskID, row := range in.Rows {
		out.Rows[taskID] = row
	}
	for ownerID, generation := range in.Owners {
		out.Owners[ownerID] = generation
	}
	return out
}

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

// TestClaimGenerationSurvivesReopen is the INTG-02 acceptance check. It proves
// a ledger rebuilt from a store restores its state, that a rotation performed
// after the reopen invalidates a generation issued before it, and that the
// rotation itself is persisted for the next reopen.
func TestClaimGenerationSurvivesReopen(t *testing.T) {
	store := &memGenerationStore{}
	first, err := NewClaimGenerationWithStore(store)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	row, err := first.EnterInProgress("watch:R1", "owner-A")
	if err != nil {
		t.Fatalf("enter in_progress: %v", err)
	}
	issued := row.Generation
	if issued == "" {
		t.Fatal("no generation issued")
	}

	// Reopen from the same store and discard the old ledger. This is the
	// restart the audit found missing: state must come back, not reset.
	second, err := NewClaimGenerationWithStore(store)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	if got := second.GenerationFor("owner-A"); got != issued {
		t.Fatalf("owner generation after reopen = %q, want %q", got, issued)
	}
	restored, ok := second.Snapshot("watch:R1")
	if !ok || restored.Generation != issued || restored.Status != ClaimInProgress {
		t.Fatalf("row after reopen = %+v, want in_progress at %q", restored, issued)
	}
	if _, err := second.Claim("watch:R1", issued); err != nil {
		t.Fatalf("restored live claim: %v", err)
	}

	// Rotate after the reopen. The generation issued before the recreate is no
	// longer accepted, and the row returns to pending at the fresh generation.
	rotated, applied := second.ResolveBlocked("watch:R1")
	if !applied || rotated == "" || rotated == issued {
		t.Fatalf("rotation after reopen = %q/%v, want a fresh generation", rotated, applied)
	}
	if _, err := second.Claim("watch:R1", issued); !IsStaleClaim(err) {
		t.Fatalf("pre-restart generation accepted after rotation: %v", err)
	}
	if second.Valid("watch:R1", issued) {
		t.Fatal("pre-restart generation still valid after rotation")
	}
	pending, _ := second.Snapshot("watch:R1")
	if pending.Status != ClaimPending || pending.ClaimedBy != "" || pending.Generation != rotated {
		t.Fatalf("row after rotation = %+v, want pending at %q", pending, rotated)
	}

	// A third reopen must see the rotation, so durability is not just in memory.
	third, err := NewClaimGenerationWithStore(store)
	if err != nil {
		t.Fatalf("second reopen: %v", err)
	}
	if _, err := third.Claim("watch:R1", issued); !IsStaleClaim(err) {
		t.Fatalf("pre-restart generation accepted after second reopen: %v", err)
	}
	if got := third.GenerationFor("owner-A"); got != rotated {
		t.Fatalf("owner generation after second reopen = %q, want %q", got, rotated)
	}
}

// TestClaimGenerationNoDoubleRotationAfterReopen proves ResolveBlocked stays
// idempotent across a reopen: a pending row is left alone and the owner's
// generation is not rotated a second time.
func TestClaimGenerationNoDoubleRotationAfterReopen(t *testing.T) {
	store := &memGenerationStore{}
	first, err := NewClaimGenerationWithStore(store)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	if _, err := first.EnterInProgress("watch:R2", "owner-B"); err != nil {
		t.Fatalf("enter in_progress: %v", err)
	}
	rotated, applied := first.ResolveBlocked("watch:R2")
	if !applied || rotated == "" {
		t.Fatalf("first resolve = %q/%v", rotated, applied)
	}

	reopened, err := NewClaimGenerationWithStore(store)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	again, reapplied := reopened.ResolveBlocked("watch:R2")
	if reapplied || again != "" {
		t.Fatalf("repeat resolve after reopen = %q/%v, want no-op", again, reapplied)
	}
	row, _ := reopened.Snapshot("watch:R2")
	if row.Generation != rotated || row.Status != ClaimPending {
		t.Fatalf("repeat resolve moved row: %+v, want pending at %q", row, rotated)
	}
	if got := reopened.GenerationFor("owner-B"); got != rotated {
		t.Fatalf("repeat rotate changed owner generation: %q, want %q", got, rotated)
	}
}

// TestClaimGenerationWithStoreLoadError proves a broken store fails the
// constructor instead of quietly starting with an empty ledger.
func TestClaimGenerationWithStoreLoadError(t *testing.T) {
	store := &memGenerationStore{loadErr: errors.New("disk gone")}
	ledger, err := NewClaimGenerationWithStore(store)
	if err == nil || ledger != nil {
		t.Fatalf("new ledger with failing store = %v/%v, want error", ledger, err)
	}
}

// TestClaimGenerationSaveErrSurfacesPersistenceFailure proves a failed write is
// observable even though the generation-port methods cannot return an error.
func TestClaimGenerationSaveErrSurfacesPersistenceFailure(t *testing.T) {
	store := &memGenerationStore{saveErr: errors.New("read-only")}
	ledger, err := NewClaimGenerationWithStore(store)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	ledger.IssueFor("owner-C")
	if got := ledger.SaveErr(); got == nil {
		t.Fatal("SaveErr is nil after a failed save")
	}
}
