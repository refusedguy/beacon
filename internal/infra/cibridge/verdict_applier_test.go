package cibridge

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// countingSaveStore counts SaveWatch calls and delegates every other store
// method to the real DB. It lets the watching test prove that a repeat with the
// same observation performs no write.
type countingSaveStore struct {
	*store.DB
	saves int
}

func (s *countingSaveStore) SaveWatch(w domain.Watch) error {
	s.saves++
	return s.DB.SaveWatch(w)
}

// TestApplyVerdictWatchingPersistsObservationsIdempotently pins the fix for
// watches stuck with verdict=watching and checks=null. A watching verdict now
// persists fresh CI facts without staging an obligation or waking anyone, and a
// repeat with identical facts performs no write.
func TestApplyVerdictWatchingPersistsObservationsIdempotently(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const head = "abc9a799a08f0c90de8675d4dbe74d9628324f8f"
	w := domain.Watch{
		ID:      "watch:refusedguy/beacon:1:" + head,
		Repo:    "refusedguy/beacon",
		PR:      1,
		HeadSHA: head,
		Status:  domain.StatusWatching, Verdict: domain.VerdictWatching,
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("seed SaveWatch: %v", err)
	}

	cs := &countingSaveStore{DB: db}
	b := New(cs, nil, nil, time.Second)
	obs := ciObservation{pr: domain.PRState{
		State: "OPEN", HeadRefOid: head,
		Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		Checks: map[string]domain.Check{
			"ci": {State: "COMPLETED", Conclusion: "SUCCESS", Required: true},
		},
	}}
	now := time.Unix(2, 0)

	if got := b.verdictApplier.applyVerdict(context.Background(), w, obs, domain.VerdictWatching, nil, now, "cycle-1"); got != 0 {
		t.Fatalf("first applyVerdict updated %d watches, want 0", got)
	}
	if cs.saves != 1 {
		t.Fatalf("first applyVerdict SaveWatch calls = %d, want 1", cs.saves)
	}
	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if len(got.Checks) != 1 || got.Checks["ci"].Conclusion != "SUCCESS" {
		t.Fatalf("checks = %+v, want the observed ci success", got.Checks)
	}
	if got.Merge.MergeStateStatus != "CLEAN" || got.Merge.Mergeable != "MERGEABLE" {
		t.Fatalf("merge = %+v, want the observed clean merge state", got.Merge)
	}
	if got.Revision != w.Revision {
		t.Fatalf("revision = %d, want %d (watching must not bump it)", got.Revision, w.Revision)
	}
	if got.Verdict != domain.VerdictWatching || got.Status != domain.StatusWatching {
		t.Fatalf("status/verdict = %q/%q, want watching/watching", got.Status, got.Verdict)
	}
	obligations, err := db.WatchObligations(w.ID)
	if err != nil {
		t.Fatalf("WatchObligations: %v", err)
	}
	if len(obligations) != 0 {
		t.Fatalf("obligations = %d, want 0 for a watching verdict", len(obligations))
	}

	savesAfterFirst := cs.saves
	if got := b.verdictApplier.applyVerdict(context.Background(), w, obs, domain.VerdictWatching, nil, now, "cycle-2"); got != 0 {
		t.Fatalf("second applyVerdict updated %d watches, want 0", got)
	}
	if cs.saves != savesAfterFirst {
		t.Fatalf("second applyVerdict wrote the Watch (%d calls), want no write", cs.saves-savesAfterFirst)
	}
}
