package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func waitlistRows(t *testing.T, ctx context.Context, store *Store) []WaitEntry {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, `SELECT path, task_id, waiting_since FROM waitlist ORDER BY path, task_id`)
	if err != nil {
		t.Fatalf("read waitlist: %v", err)
	}
	defer rows.Close()
	var out []WaitEntry
	for rows.Next() {
		var entry WaitEntry
		var path, taskID string
		if err := rows.Scan(&path, &taskID, &entry.WaitingSince); err != nil {
			t.Fatalf("scan waitlist: %v", err)
		}
		entry.Path = core.ScopePath(path)
		entry.TaskID = core.TaskID(taskID)
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("waitlist rows: %v", err)
	}
	return out
}

func TestAddWaitEntryRefreshesSameRow(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	path := core.ScopeFile("internal/infra/mcp/server.go")

	if err := store.AddWaitEntry(ctx, "T-dup", []core.ScopePath{path}, 100); err != nil {
		t.Fatalf("first AddWaitEntry: %v", err)
	}
	if err := store.AddWaitEntry(ctx, "T-dup", []core.ScopePath{path}, 200); err != nil {
		t.Fatalf("second AddWaitEntry: %v", err)
	}
	rows := waitlistRows(t, ctx, store)
	if len(rows) != 1 || rows[0].Path != path || rows[0].TaskID != "T-dup" || rows[0].WaitingSince != 200 {
		t.Fatalf("rows after duplicate = %+v, want one entry refreshed to 200", rows)
	}
}

func TestLongestWaiterAcrossPaths(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	ci := core.ScopeFile(".github/workflows/ci.yml")
	reconciler := core.ScopeFile("internal/app/reconciler.go")
	unrelated := core.ScopeFile("docs/tasks/T-1.md")

	entries := []struct {
		taskID string
		path   core.ScopePath
		since  int64
	}{
		{"T-young", ci, 400},
		{"T-mid", reconciler, 200},
		{"T-outside", unrelated, 100},
		{"T-old", ci, 300},
	}
	for _, entry := range entries {
		if err := store.AddWaitEntry(ctx, core.TaskID(entry.taskID), []core.ScopePath{entry.path}, entry.since); err != nil {
			t.Fatalf("AddWaitEntry(%s): %v", entry.taskID, err)
		}
	}

	got, err := store.LongestWaiter(ctx, []core.ScopePath{ci, reconciler})
	if err != nil {
		t.Fatalf("LongestWaiter: %v", err)
	}
	if got == nil || got.TaskID != "T-mid" || got.Path != reconciler || got.WaitingSince != 200 {
		t.Fatalf("LongestWaiter = %+v, want T-mid on reconciler at 200", got)
	}

	got, err = store.LongestWaiter(ctx, []core.ScopePath{unrelated})
	if err != nil {
		t.Fatalf("LongestWaiter out of set: %v", err)
	}
	if got == nil || got.TaskID != "T-outside" || got.WaitingSince != 100 {
		t.Fatalf("LongestWaiter out of set = %+v, want T-outside at 100", got)
	}

	for _, paths := range [][]core.ScopePath{nil, {core.ScopeFile("missing.go")}} {
		got, err = store.LongestWaiter(ctx, paths)
		if err != nil || got != nil {
			t.Fatalf("LongestWaiter(%v) = %+v, %v, want nil, nil", paths, got, err)
		}
	}
}

func TestRemoveWaitEntriesAfterRun(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	ci := core.ScopeFile(".github/workflows/ci.yml")
	reconciler := core.ScopeFile("internal/app/reconciler.go")

	if err := store.AddWaitEntry(ctx, "T-run", []core.ScopePath{ci, reconciler}, 100); err != nil {
		t.Fatalf("AddWaitEntry(T-run): %v", err)
	}
	if err := store.AddWaitEntry(ctx, "T-still-waiting", []core.ScopePath{ci}, 200); err != nil {
		t.Fatalf("AddWaitEntry(T-still-waiting): %v", err)
	}
	if err := store.RemoveWaitEntries(ctx, "T-run"); err != nil {
		t.Fatalf("RemoveWaitEntries: %v", err)
	}

	rows := waitlistRows(t, ctx, store)
	if len(rows) != 1 || rows[0].TaskID != "T-still-waiting" || rows[0].Path != ci {
		t.Fatalf("rows after removal = %+v, want only T-still-waiting on ci", rows)
	}
	got, err := store.LongestWaiter(ctx, []core.ScopePath{ci, reconciler})
	if err != nil || got == nil || got.TaskID != "T-still-waiting" {
		t.Fatalf("LongestWaiter after removal = %+v, %v, want T-still-waiting", got, err)
	}
}

func TestWaitEntrySurvivesReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	ci := core.ScopeFile(".github/workflows/ci.yml")
	if err := store.AddWaitEntry(ctx, "T-restart", []core.ScopePath{ci}, 100); err != nil {
		t.Fatalf("AddWaitEntry: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.LongestWaiter(ctx, []core.ScopePath{ci})
	if err != nil || got == nil || got.TaskID != "T-restart" || got.WaitingSince != 100 {
		t.Fatalf("LongestWaiter after reopen = %+v, %v, want T-restart at 100", got, err)
	}
}

func TestWaitlistAddedToPreparedDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE waitlist`); err != nil {
		t.Fatalf("drop waitlist: %v", err)
	}
	// Mark the file prepared without the table, as a database created before
	// this card would be. Open must add the table on top of that marker.
	if err := markSchemaPrepared(db); err != nil {
		t.Fatalf("markSchemaPrepared: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open on prepared database: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	ci := core.ScopeFile(".github/workflows/ci.yml")
	if err := store.AddWaitEntry(ctx, "T-legacy", []core.ScopePath{ci}, 100); err != nil {
		t.Fatalf("AddWaitEntry: %v", err)
	}
	got, err := store.LongestWaiter(ctx, []core.ScopePath{ci})
	if err != nil || got == nil || got.TaskID != "T-legacy" {
		t.Fatalf("LongestWaiter on prepared database = %+v, %v, want T-legacy", got, err)
	}
}
