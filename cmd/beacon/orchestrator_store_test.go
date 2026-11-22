package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
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

	s := newDBOrchestratorStore(nil, db)
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

// TestDBStoreMergedPRReadsLoadedWatch proves a PR dependency is resolved from
// the SQLite Watch rows the process already has. There is no CLI runner in this
// store, so a gh process cannot be started on this path.
func TestDBStoreMergedPRReadsLoadedWatch(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if err := db.SaveWatch(domain.Watch{
		ID: "watch:merged:77", Repo: "refusedguy/beacon", PR: 77,
		Status: domain.StatusClosed, Verdict: domain.VerdictMerged, CloseReason: "merged",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveWatch(domain.Watch{
		ID: "watch:open:78", Repo: "refusedguy/beacon", PR: 78,
		Status: domain.StatusWatching, Verdict: domain.VerdictReady,
	}); err != nil {
		t.Fatal(err)
	}

	s := newDBOrchestratorStore(nil, db)
	merged, err := s.MergedPR(context.Background(), 77)
	if err != nil || !merged {
		t.Fatalf("MergedPR(77) = %t, %v, want true", merged, err)
	}
	merged, err = s.MergedPR(context.Background(), 78)
	if err != nil || merged {
		t.Fatalf("MergedPR(78) = %t, %v, want false", merged, err)
	}
}

// TestDBStoreHoldWatchPersistsWaitReason proves the hold is visible through the
// same store beacon_list_running and beacon inspect read.
func TestDBStoreHoldWatchPersistsWaitReason(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	w := domain.Watch{
		ID: "watch:hold:1", Repo: "refusedguy/beacon", PR: 42,
		Status: domain.StatusWatching, Verdict: domain.VerdictReady,
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatal(err)
	}

	s := newDBOrchestratorStore(nil, db)
	verdict := orchestrator.WaitDependencyVerdict("wait_for:#77")
	if err := s.HoldWatch(context.Background(), w.ID, verdict); err != nil {
		t.Fatal(err)
	}
	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Verdict) != verdict {
		t.Fatalf("verdict = %q, want %q", got.Verdict, verdict)
	}
	if got.Status != domain.StatusWatching {
		t.Fatalf("hold must keep the Watch in the control loop: %s", got.Status)
	}
}

// TestDBStoreCloseWatchAtMarksMerged proves a merge at the revision and head
// it decided on still closes the Watch with the merged reason.
func TestDBStoreCloseWatchAtMarksMerged(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	w := domain.Watch{
		ID: "watch:closeat:1", Repo: "refusedguy/beacon", PR: 1, HeadSHA: "sha1",
		Status: domain.StatusWatching, Verdict: domain.VerdictReady, Revision: 3,
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatal(err)
	}

	s := newDBOrchestratorStore(nil, db)
	if err := s.CloseWatchAt(context.Background(), w.ID, w.Revision, w.HeadSHA); err != nil {
		t.Fatal(err)
	}
	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusClosed || got.Verdict != domain.VerdictMerged || got.CloseReason != "merged" {
		t.Fatalf("closed watch = %+v, want status=closed verdict=merged closeReason=merged", got)
	}
}

// TestDBStoreCloseWatchAtRejectsStaleDecision proves a mismatch on revision or
// head returns a typed conflict and leaves the Watch untouched.
func TestDBStoreCloseWatchAtRejectsStaleDecision(t *testing.T) {
	cases := []struct {
		name     string
		revision uint64
		headSHA  string
	}{
		{name: "revision", revision: 2, headSHA: "sha4"},
		{name: "head", revision: 3, headSHA: "shaOld"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "beacon.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()

			w := domain.Watch{
				ID: "watch:closeat:stale", Repo: "refusedguy/beacon", PR: 1, HeadSHA: "sha4",
				Status: domain.StatusWatching, Verdict: domain.VerdictReady, Revision: 3,
			}
			if err := db.SaveWatch(w); err != nil {
				t.Fatal(err)
			}

			s := newDBOrchestratorStore(nil, db)
			err = s.CloseWatchAt(context.Background(), w.ID, tc.revision, tc.headSHA)
			var conflict *orchestrator.WatchCloseConflict
			if !errors.As(err, &conflict) {
				t.Fatalf("error = %v, want *orchestrator.WatchCloseConflict", err)
			}
			got, err := db.Watch(w.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != domain.StatusWatching || got.Verdict != domain.VerdictReady || got.CloseReason != "" {
				t.Fatalf("stale close changed watch: %+v", got)
			}
		})
	}
}

// TestDBStoreCloseWatchAtKeepsHeadChangedReason proves a late merge cannot
// overwrite the reason the bridge wrote when the head changed.
func TestDBStoreCloseWatchAtKeepsHeadChangedReason(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	w := domain.Watch{
		ID: "watch:closeat:race", Repo: "refusedguy/beacon", PR: 1, HeadSHA: "sha1",
		Status: domain.StatusWatching, Verdict: domain.VerdictReady, Revision: 1,
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatal(err)
	}

	// The bridge closes the Watch after the merge decision and before the
	// confirmation reaches the store. Revision and head are unchanged.
	closed := w
	closed.Status = domain.StatusClosed
	closed.CloseReason = string(domain.CloseReasonHeadChanged)
	if err := db.SaveWatch(closed); err != nil {
		t.Fatal(err)
	}

	s := newDBOrchestratorStore(nil, db)
	err = s.CloseWatchAt(context.Background(), w.ID, w.Revision, w.HeadSHA)
	var conflict *orchestrator.WatchCloseConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want *orchestrator.WatchCloseConflict", err)
	}
	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CloseReason != string(domain.CloseReasonHeadChanged) {
		t.Fatalf("closeReason = %q, want %q", got.CloseReason, domain.CloseReasonHeadChanged)
	}
	if got.Verdict == domain.VerdictMerged {
		t.Fatalf("late merge rewrote the verdict of a head-changed watch: %+v", got)
	}
}
