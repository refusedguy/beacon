package cibridge

import (
	"context"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// baseGreenBatch records the Watches the refresh layer sampled and returns a
// green base observation for each of them.
type baseGreenBatch struct {
	seen []string
}

func (b *baseGreenBatch) FetchBatch(_ context.Context, watches []domain.Watch) (map[string]domain.PRSnapshot, error) {
	out := make(map[string]domain.PRSnapshot, len(watches))
	for _, w := range watches {
		b.seen = append(b.seen, w.ID)
		out[w.ID] = domain.PRSnapshot{
			PR:   domain.PRState{State: "OPEN", HeadRefOid: w.HeadSHA},
			Base: domain.BaseBranchState{BaseRef: "main", ChecksKnown: true},
		}
	}
	return out, nil
}

func (b *baseGreenBatch) PointStats() (int, int) { return len(b.seen), 1000 }

// TestFetchSnapshotsIncludesBlockedWatch pins the base-green fix. The resolver
// runs after the Watch loop over the snapshot map, so a blocked Watch missing
// from that map can never release its own incident.
func TestFetchSnapshotsIncludesBlockedWatch(t *testing.T) {
	batch := &baseGreenBatch{}
	b := New(nil, nil, fixedClock{at: time.Unix(1, 0)}, time.Second).WithBatchCI(batch)

	watches := []domain.Watch{
		{ID: "watching", Status: domain.StatusWatching, HeadSHA: "sha-watching"},
		{ID: "blocked", Status: domain.StatusBlocked, HeadSHA: "sha-blocked"},
		{ID: "needs-input", Status: domain.StatusNeedsInput, HeadSHA: "sha-input"},
		{ID: "closed", Status: domain.StatusClosed, HeadSHA: "sha-closed"},
	}
	snaps, err := b.fetchSnapshots(context.Background(), watches, "cycle-test")
	if err != nil {
		t.Fatalf("fetchSnapshots: %v", err)
	}
	for _, id := range []string{"watching", "blocked"} {
		if _, ok := snaps[id]; !ok {
			t.Errorf("snapshot for %q missing; got %v", id, snaps)
		}
	}
	for _, id := range []string{"needs-input", "closed"} {
		if _, ok := snaps[id]; ok {
			t.Errorf("snapshot for %q present; it must stay parked", id)
		}
	}
	found := false
	for _, id := range batch.seen {
		if id == "blocked" {
			found = true
		}
	}
	if !found {
		t.Errorf("blocked Watch was not polled; batch saw %v", batch.seen)
	}
}
