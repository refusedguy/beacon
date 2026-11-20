package orchestrator

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// TestDBStoreCloseWatchMarksMerged proves a merged Watch is closed with a
// merged reason, not the "cancelled" reason the cancel watcher would write.
func TestDBStoreCloseWatchMarksMerged(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	w := domain.Watch{
		ID: "watch:close:1", Repo: "refusedguy/beacon", PR: 1, HeadSHA: "sha1",
		Status: domain.StatusWatching, Verdict: domain.VerdictReady,
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatal(err)
	}

	s := NewDBStore(nil, db)
	if err := s.CloseWatch(context.Background(), w.ID); err != nil {
		t.Fatal(err)
	}

	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusClosed || got.Verdict != domain.VerdictMerged || got.CloseReason != "merged" {
		t.Fatalf("closed watch = %+v, want status=closed verdict=merged closeReason=merged", got)
	}

	// An already closed watch is terminal: a repeat must not rewrite it.
	if err := s.CloseWatch(context.Background(), w.ID); err != nil {
		t.Fatalf("repeat close: %v", err)
	}
	again, err := db.Watch(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != got.Status || again.Verdict != got.Verdict || again.CloseReason != got.CloseReason {
		t.Fatalf("repeat close changed watch: %+v -> %+v", got, again)
	}
}
