package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

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
	d := openTestDB(t)
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
	d := openTestDB(t)
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
	d := openTestDB(t)
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

// W6-C: the cycle calls Run on every tick; it must archive once per hour, and
// the marker must survive a restart so a fresh process does not re-run the
// last hour's work or forget a pass that is due.
func TestArchiveRunHourlySchedule(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

	first := newArchiveWatch("scheduled-1", domain.StatusClosed, domain.VerdictMerged,
		now.Add(-ClosedWatchRetention-time.Hour), "")
	recent := newArchiveWatch("scheduled-recent", domain.StatusClosed, domain.VerdictMerged,
		now.Add(-24*time.Hour), "")
	for _, w := range []domain.Watch{first, recent} {
		if err := d.SaveWatch(w); err != nil {
			t.Fatalf("SaveWatch(%s): %v", w.ID, err)
		}
	}
	if err := d.SaveObligation(domain.Obligation{
		ID: "scheduled-1:3:s1", WatchID: "scheduled-1", Revision: 3, SubscriberID: "s1",
		Attempts: []domain.Attempt{{N: 1, At: now, Outcome: domain.OutcomePosted}},
	}); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}

	// A store with no marker is due at cycle start.
	due, err := d.ArchiveDue(ctx, now)
	if err != nil || !due {
		t.Fatalf("ArchiveDue before first run = %v, %v; want true", due, err)
	}
	ran, err := d.ArchiveRun(ctx, now)
	if err != nil {
		t.Fatalf("first ArchiveRun: %v", err)
	}
	if !ran.Ran || ran.Moved != 1 {
		t.Fatalf("first ArchiveRun = %+v, want Ran=true Moved=1", ran)
	}
	if got := strings.Join(activeWatchIDs(t, d), ","); got != "scheduled-recent" {
		t.Errorf("active watches after first run = %q, want scheduled-recent", got)
	}
	last, ok, err := d.LastArchiveRun(ctx)
	if err != nil || !ok || !last.Equal(now) {
		t.Fatalf("LastArchiveRun = %v, %v, %v; want %v, true", last, ok, err, now)
	}

	// Half an hour later the cycle is a no-op.
	soon := now.Add(30 * time.Minute)
	due, err = d.ArchiveDue(ctx, soon)
	if err != nil || due {
		t.Fatalf("ArchiveDue at +30m = %v, %v; want false", due, err)
	}
	ran, err = d.ArchiveRun(ctx, soon)
	if err != nil || ran.Ran || ran.Moved != 0 {
		t.Fatalf("ArchiveRun at +30m = %+v, %v; want no-op", ran, err)
	}

	// A watch that crossed the boundary in the meantime is archived on the
	// next hourly pass, and its obligation history survives.
	later := newArchiveWatch("scheduled-2", domain.StatusClosed, domain.VerdictMerged,
		soon.Add(-ClosedWatchRetention), "")
	if err := d.SaveWatch(later); err != nil {
		t.Fatalf("SaveWatch(scheduled-2): %v", err)
	}
	hour := now.Add(ArchiveScheduleInterval)
	due, err = d.ArchiveDue(ctx, hour)
	if err != nil || !due {
		t.Fatalf("ArchiveDue at +1h = %v, %v; want true", due, err)
	}
	ran, err = d.ArchiveRun(ctx, hour)
	if err != nil || !ran.Ran || ran.Moved != 1 {
		t.Fatalf("ArchiveRun at +1h = %+v, %v; want Ran=true Moved=1", ran, err)
	}
	if got := strings.Join(activeWatchIDs(t, d), ","); got != "scheduled-recent" {
		t.Errorf("active watches after +1h = %q, want scheduled-recent", got)
	}
	if got := strings.Join(archivedWatchIDs(t, d), ","); got != "scheduled-1,scheduled-2" {
		t.Errorf("archived watches = %q, want scheduled-1,scheduled-2", got)
	}
	history, err := d.WatchObligations("scheduled-1")
	if err != nil || len(history) != 1 || len(history[0].Attempts) != 1 {
		t.Fatalf("obligation history after scheduled move = %+v, %v", history, err)
	}
}

func TestArchiveRunMarkerSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := d.ArchiveRun(ctx, now); err != nil {
		t.Fatalf("ArchiveRun: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	last, ok, err := reopened.LastArchiveRun(ctx)
	if err != nil || !ok || !last.Equal(now) {
		t.Fatalf("LastArchiveRun after reopen = %v, %v, %v; want %v", last, ok, err, now)
	}
	due, err := reopened.ArchiveDue(ctx, now.Add(ArchiveScheduleInterval-time.Minute))
	if err != nil || due {
		t.Fatalf("ArchiveDue after reopen = %v, %v; want false", due, err)
	}
}

// Two cycles can tick at the same time. The single writer and the marker in
// the same transaction must let exactly one of them perform the pass.
func TestArchiveRunConcurrentCyclesArchiveOnce(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"race-1", "race-2", "race-3"} {
		w := newArchiveWatch(id, domain.StatusClosed, domain.VerdictMerged,
			now.Add(-ClosedWatchRetention-time.Hour), "")
		if err := d.SaveWatch(w); err != nil {
			t.Fatalf("SaveWatch(%s): %v", id, err)
		}
	}

	const cycles = 8
	var wg sync.WaitGroup
	results := make(chan ArchiveRunResult, cycles)
	errs := make(chan error, cycles)
	for i := 0; i < cycles; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := d.ArchiveRun(ctx, now)
			if err != nil {
				errs <- err
				return
			}
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("ArchiveRun: %v", err)
	}
	runs, moved := 0, 0
	for r := range results {
		if r.Ran {
			runs++
		}
		moved += r.Moved
	}
	if runs != 1 || moved != 3 {
		t.Fatalf("runs = %d, moved = %d; want 1 and 3", runs, moved)
	}
}

func TestArchiveRunRejectsZeroTime(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if _, err := d.ArchiveRun(ctx, time.Time{}); err == nil {
		t.Error("ArchiveRun with zero time succeeded")
	}
	if _, err := d.ArchiveDue(ctx, time.Time{}); err == nil {
		t.Error("ArchiveDue with zero time succeeded")
	}
}

func TestWatchArchiveSchemaHasAllWatchFields(t *testing.T) {
	d := openTestDB(t)
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
