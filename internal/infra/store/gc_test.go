package store

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

func gcObligationTestDB(t *testing.T) (*DB, time.Time) {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
}

func gcObligationTestWatch(id string, status domain.Status, closedAt time.Time, reason string) domain.Watch {
	return domain.Watch{
		ID:          id,
		Repo:        "refusedguy/beacon",
		PR:          1,
		Status:      status,
		Revision:    1,
		ClosedAt:    closedAt,
		CloseReason: reason,
	}
}

func gcObligationTestObligation(id, watchID string, stagedAt time.Time) domain.Obligation {
	return domain.Obligation{
		ID:             id,
		WatchID:        watchID,
		Revision:       1,
		SubscriberID:   "s1",
		VerdictPointer: domain.VerdictReady,
		StagedAt:       stagedAt,
	}
}

func gcObligationPlanIDs(items []ObligationGCItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return ids
}

// TestGCObligationsKeepsLiveAndRecent verifies the two gates of the collector.
// A live Watch always protects its obligation, and a closed Watch keeps its
// obligation for the replay window. Only old obligations of closed, archived
// or missing Watches are selected.
func TestGCObligationsKeepsLiveAndRecent(t *testing.T) {
	d, now := gcObligationTestDB(t)
	recent := now.Add(-2 * time.Hour)
	old := now.Add(-72 * time.Hour)
	ancient := now.Add(-8 * 24 * time.Hour)

	watches := []domain.Watch{
		gcObligationTestWatch("live-watching", domain.StatusWatching, time.Time{}, ""),
		gcObligationTestWatch("live-blocked", domain.StatusBlocked, time.Time{}, ""),
		gcObligationTestWatch("closed-old", domain.StatusClosed, old, closeReasonMerged),
		gcObligationTestWatch("closed-recent", domain.StatusClosed, recent, closeReasonMerged),
		gcObligationTestWatch("archived-old", domain.StatusClosed, ancient, closeReasonMerged),
	}
	for _, w := range watches {
		if err := d.SaveWatch(w); err != nil {
			t.Fatalf("SaveWatch(%s): %v", w.ID, err)
		}
	}
	moved, err := d.ArchiveClosedWatches(now)
	if err != nil || moved != 1 {
		t.Fatalf("ArchiveClosedWatches = %d, %v; want 1", moved, err)
	}

	obligations := []domain.Obligation{
		gcObligationTestObligation("o-live", "live-watching", ancient),
		gcObligationTestObligation("o-blocked", "live-blocked", ancient),
		gcObligationTestObligation("o-closed-old", "closed-old", ancient),
		gcObligationTestObligation("o-closed-recent", "closed-recent", ancient),
		gcObligationTestObligation("o-archived", "archived-old", ancient),
		gcObligationTestObligation("o-missing-old", "never-existed", old),
		gcObligationTestObligation("o-missing-recent", "never-existed-2", recent),
		gcObligationTestObligation("o-missing-untimed", "never-existed-3", time.Time{}),
	}
	for _, o := range obligations {
		if err := d.SaveObligation(o); err != nil {
			t.Fatalf("SaveObligation(%s): %v", o.ID, err)
		}
	}

	plan, err := d.GCObligations(context.Background(), now, ObligationGCRetention, false)
	if err != nil {
		t.Fatalf("GCObligations dry run: %v", err)
	}
	want := []string{"o-archived", "o-closed-old", "o-missing-old"}
	if got := gcObligationPlanIDs(plan); !equalStrings(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	reasons := make(map[string]string, len(plan))
	for _, item := range plan {
		reasons[item.ID] = item.Reason
	}
	if reasons["o-archived"] != "archived" || reasons["o-closed-old"] != "closed" || reasons["o-missing-old"] != "missing" {
		t.Fatalf("reasons = %+v, want archived/closed/missing", reasons)
	}

	// The dry run must not delete anything.
	for _, o := range obligations {
		if _, err := d.Obligation(o.ID); err != nil {
			t.Fatalf("dry run removed %s: %v", o.ID, err)
		}
	}

	applied, err := d.GCObligations(context.Background(), now, ObligationGCRetention, true)
	if err != nil {
		t.Fatalf("GCObligations apply: %v", err)
	}
	if got := gcObligationPlanIDs(applied); !equalStrings(got, want) {
		t.Fatalf("applied = %v, want %v", got, want)
	}
	for _, id := range want {
		if _, err := d.Obligation(id); err == nil {
			t.Errorf("%s still present after apply", id)
		}
		rec, err := d.ObligationErase(id)
		if err != nil || !rec.ErasedAt.Equal(now) {
			t.Errorf("audit for %s = %+v, %v", id, rec, err)
		}
	}
	for _, o := range obligations {
		switch o.ID {
		case "o-archived", "o-closed-old", "o-missing-old":
			continue
		}
		if _, err := d.Obligation(o.ID); err != nil {
			t.Errorf("collector removed protected %s: %v", o.ID, err)
		}
	}

	again, err := d.GCObligations(context.Background(), now, ObligationGCRetention, false)
	if err != nil {
		t.Fatalf("second GCObligations: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second pass = %v, want empty", gcObligationPlanIDs(again))
	}
}

// TestGCObligationsKeepsLiveRetry checks that retry data protects an obligation
// whose Watch row is gone and whose StagedAt is old. A future NextRetryAt and a
// recent attempt must both survive collection; a row with neither is erased.
func TestGCObligationsKeepsLiveRetry(t *testing.T) {
	d, now := gcObligationTestDB(t)
	old := now.Add(-72 * time.Hour)

	live := gcObligationTestObligation("o-live-retry", "gone-watch", old)
	live.NextRetryAt = now.Add(2 * time.Hour)
	retried := gcObligationTestObligation("o-recent-retry", "gone-watch-2", old)
	retried.NextRetryAt = now.Add(-time.Hour)
	attempted := gcObligationTestObligation("o-recent-attempt", "gone-watch-3", old)
	attempted.Attempts = []domain.Attempt{{N: 1, At: now.Add(-2 * time.Hour), Channel: "s1", Outcome: domain.OutcomePosted}}
	stale := gcObligationTestObligation("o-stale", "gone-watch-4", old)

	for _, o := range []domain.Obligation{live, retried, attempted, stale} {
		if err := d.SaveObligation(o); err != nil {
			t.Fatalf("SaveObligation(%s): %v", o.ID, err)
		}
	}

	plan, err := d.GCObligations(context.Background(), now, ObligationGCRetention, false)
	if err != nil {
		t.Fatalf("GCObligations dry run: %v", err)
	}
	if got := gcObligationPlanIDs(plan); !equalStrings(got, []string{"o-stale"}) {
		t.Fatalf("plan = %v, want [o-stale]", got)
	}

	applied, err := d.GCObligations(context.Background(), now, ObligationGCRetention, true)
	if err != nil {
		t.Fatalf("GCObligations apply: %v", err)
	}
	if got := gcObligationPlanIDs(applied); !equalStrings(got, []string{"o-stale"}) {
		t.Fatalf("applied = %v, want [o-stale]", got)
	}
	for _, o := range []domain.Obligation{live, retried, attempted} {
		if _, err := d.Obligation(o.ID); err != nil {
			t.Errorf("collector removed protected %s: %v", o.ID, err)
		}
	}
	if _, err := d.Obligation(stale.ID); err == nil {
		t.Error("o-stale still present after apply")
	}

	again, err := d.GCObligations(context.Background(), now, ObligationGCRetention, false)
	if err != nil {
		t.Fatalf("second GCObligations: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second pass = %v, want empty", gcObligationPlanIDs(again))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
