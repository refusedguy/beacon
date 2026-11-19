package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

func openArchiveDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		_ = d.Close()
	})
	return d
}

func newArchiveWatch(id string, status domain.Status, verdict domain.Verdict, closedAt time.Time, reason string) domain.Watch {
	return domain.Watch{
		ID:       id,
		Repo:     "org/repo",
		PR:       7,
		HeadSHA:  "sha-" + id,
		BaseRef:  "main",
		Issue:    "org/repo#7",
		Status:   status,
		Verdict:  verdict,
		Revision: 3,
		Checks: map[string]domain.Check{
			"build": {State: "completed", Conclusion: "success", Required: true, DetailsURL: "https://ci/build"},
		},
		Merge: domain.MergeInfo{
			Mergeable:        "MERGEABLE",
			MergeStateStatus: "CLEAN",
			ReviewDecision:   "APPROVED",
		},
		Flaky: domain.FlakyConfig{
			Names:      []string{"lint"},
			MaxRetries: 2,
			Used:       map[string]int{"lint": 1},
		},
		Policy: domain.Policy{RequireUpToDate: true},
		Subscribers: []domain.Subscriber{
			{ID: "s1", Role: domain.RoleExecutor, Adapter: "opencode", Target: "worker", AddedAt: closedAt.Add(-48 * time.Hour), LastActivity: closedAt},
		},
		DeadlineAt:  closedAt.Add(-12 * time.Hour),
		CreatedAt:   closedAt.Add(-72 * time.Hour),
		ClosedAt:    closedAt,
		CloseReason: reason,
	}
}

func activeWatchIDs(t *testing.T, d *DB) []string {
	t.Helper()
	rows, err := d.readDB.Query(`SELECT id FROM watches ORDER BY id`)
	if err != nil {
		t.Fatalf("query watches: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan watch id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("watch rows: %v", err)
	}
	return ids
}

func archivedWatchIDs(t *testing.T, d *DB) []string {
	t.Helper()
	archived, err := d.ArchivedWatches()
	if err != nil {
		t.Fatalf("ArchivedWatches: %v", err)
	}
	ids := make([]string, 0, len(archived))
	for _, w := range archived {
		ids = append(ids, w.ID)
	}
	return ids
}

func TestArchiveClosedWatchesMovesOnlyEligible(t *testing.T) {
	d := openArchiveDB(t)
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	old := now.Add(-ClosedWatchRetention - time.Hour)
	recent := now.Add(-24 * time.Hour)

	watches := []domain.Watch{
		newArchiveWatch("merged", domain.StatusClosed, domain.VerdictMerged, old, ""),
		newArchiveWatch("head", domain.StatusClosed, domain.VerdictWatching, old, closeReasonHeadChanged),
		newArchiveWatch("cancelled", domain.StatusClosed, domain.VerdictReady, old, closeReasonCancelled),
		newArchiveWatch("recent", domain.StatusClosed, domain.VerdictMerged, recent, ""),
		newArchiveWatch("watching", domain.StatusWatching, domain.VerdictWatching, time.Time{}, ""),
		newArchiveWatch("notifying", domain.StatusNotifying, domain.VerdictReady, old, ""),
		newArchiveWatch("no-terminal", domain.StatusClosed, domain.VerdictWatching, old, ""),
		newArchiveWatch("timeout", domain.StatusClosed, domain.VerdictTimeout, old, "timeout"),
	}
	for _, w := range watches {
		if err := d.SaveWatch(w); err != nil {
			t.Fatalf("SaveWatch(%s): %v", w.ID, err)
		}
	}

	obligations := []domain.Obligation{
		{
			ID: "merged:3:s1", WatchID: "merged", Revision: 3, SubscriberID: "s1",
			VerdictPointer: domain.VerdictMerged,
			Attempts:       []domain.Attempt{{N: 1, At: old, Channel: "test", Outcome: domain.OutcomePosted}},
		},
		{
			ID: "head:3:s1", WatchID: "head", Revision: 3, SubscriberID: "s1",
			VerdictPointer: domain.VerdictWatching,
		},
	}
	for _, o := range obligations {
		if err := d.SaveObligation(o); err != nil {
			t.Fatalf("SaveObligation(%s): %v", o.ID, err)
		}
	}

	moved, err := d.ArchiveClosedWatches(now)
	if err != nil {
		t.Fatalf("ArchiveClosedWatches: %v", err)
	}
	if moved != 3 {
		t.Errorf("moved = %d, want 3", moved)
	}

	wantActive := "no-terminal,notifying,recent,timeout,watching"
	if got := strings.Join(activeWatchIDs(t, d), ","); got != wantActive {
		t.Errorf("active watches = %q, want %q", got, wantActive)
	}
	wantArchived := "cancelled,head,merged"
	if got := strings.Join(archivedWatchIDs(t, d), ","); got != wantArchived {
		t.Errorf("archived watches = %q, want %q", got, wantArchived)
	}

	got, err := d.ArchivedWatch("merged")
	if err != nil {
		t.Fatalf("ArchivedWatch(merged): %v", err)
	}
	if got.Status != domain.StatusClosed || got.Verdict != domain.VerdictMerged {
		t.Errorf("archived status/verdict = %s/%s, want closed/merged", got.Status, got.Verdict)
	}
	if !got.ClosedAt.Equal(old) {
		t.Errorf("archived ClosedAt = %v, want %v", got.ClosedAt, old)
	}
	if got.Merge.Mergeable != "MERGEABLE" || !got.Policy.RequireUpToDate {
		t.Errorf("archived nested fields lost: %+v", got)
	}
	if len(got.Subscribers) != 1 || len(got.Checks) != 1 {
		t.Errorf("archived history lost: subscribers=%d checks=%d", len(got.Subscribers), len(got.Checks))
	}

	history, err := d.WatchObligations("merged")
	if err != nil {
		t.Fatalf("WatchObligations(merged): %v", err)
	}
	if len(history) != 1 || history[0].ID != "merged:3:s1" || len(history[0].Attempts) != 1 {
		t.Fatalf("obligation history after move = %+v", history)
	}
	other, err := d.Obligation("head:3:s1")
	if err != nil || other.WatchID != "head" {
		t.Fatalf("Obligation(head:3:s1): %v, %+v", err, other)
	}

	movedAgain, err := d.ArchiveClosedWatches(now)
	if err != nil {
		t.Fatalf("second ArchiveClosedWatches: %v", err)
	}
	if movedAgain != 0 {
		t.Errorf("second run moved %d, want 0", movedAgain)
	}
	if got := strings.Join(activeWatchIDs(t, d), ","); got != wantActive {
		t.Errorf("active watches after second run = %q, want %q", got, wantActive)
	}
	if got := strings.Join(archivedWatchIDs(t, d), ","); got != wantArchived {
		t.Errorf("archived watches after second run = %q, want %q", got, wantArchived)
	}
}

func TestArchiveClosedWatchesRetentionBoundary(t *testing.T) {
	d := openArchiveDB(t)
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	boundary := now.Add(-ClosedWatchRetention)
	watches := []domain.Watch{
		newArchiveWatch("exact", domain.StatusClosed, domain.VerdictMerged, boundary, ""),
		newArchiveWatch("younger", domain.StatusClosed, domain.VerdictMerged, boundary.Add(time.Nanosecond), ""),
	}
	for _, w := range watches {
		if err := d.SaveWatch(w); err != nil {
			t.Fatalf("SaveWatch(%s): %v", w.ID, err)
		}
	}

	moved, err := d.ArchiveClosedWatches(now)
	if err != nil {
		t.Fatalf("ArchiveClosedWatches: %v", err)
	}
	if moved != 1 {
		t.Errorf("moved = %d, want 1", moved)
	}
	if got := strings.Join(activeWatchIDs(t, d), ","); got != "younger" {
		t.Errorf("active watches = %q, want younger", got)
	}
	if got := strings.Join(archivedWatchIDs(t, d), ","); got != "exact" {
		t.Errorf("archived watches = %q, want exact", got)
	}
}

func TestArchiveClosedWatchesSkipsCloseWithoutTerminalReason(t *testing.T) {
	d := openArchiveDB(t)
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	old := now.Add(-ClosedWatchRetention - time.Hour)
	watches := []domain.Watch{
		newArchiveWatch("no-reason", domain.StatusClosed, domain.VerdictWatching, old, ""),
		newArchiveWatch("no-closed-at", domain.StatusClosed, domain.VerdictMerged, time.Time{}, ""),
	}
	for _, w := range watches {
		if err := d.SaveWatch(w); err != nil {
			t.Fatalf("SaveWatch(%s): %v", w.ID, err)
		}
	}

	moved, err := d.ArchiveClosedWatches(now)
	if err != nil {
		t.Fatalf("ArchiveClosedWatches: %v", err)
	}
	if moved != 0 {
		t.Errorf("moved = %d, want 0", moved)
	}
	if got := strings.Join(activeWatchIDs(t, d), ","); got != "no-closed-at,no-reason" {
		t.Errorf("active watches = %q, want no-closed-at,no-reason", got)
	}
}

func TestWatchArchiveSchemaHasAllWatchFields(t *testing.T) {
	d := openArchiveDB(t)
	if err := d.ensureWatchArchive(); err != nil {
		t.Fatalf("ensureWatchArchive: %v", err)
	}
	requireTable(t, d.readDB, "watch_archive")

	rows, err := d.readDB.Query(`PRAGMA table_info(watch_archive)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("column rows: %v", err)
	}

	want := []string{
		"id", "repo", "pr", "head_sha", "base_ref", "issue", "status", "verdict",
		"revision", "checks", "merge", "flaky", "policy", "subscribers",
		"deadline_at", "no_checks_since", "created_at", "closed_at", "close_reason",
		"payload", "archived_at",
	}
	for _, name := range want {
		if !columns[name] {
			t.Errorf("watch_archive column %s missing", name)
		}
	}
}
