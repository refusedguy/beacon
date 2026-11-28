package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// withTerminalRunLimit sets the retention boundary for one test. The real
// limit is twenty thousand rows, which a unit test should not have to write.
func withTerminalRunLimit(t *testing.T, limit int) {
	t.Helper()
	previous := terminalRunLimit
	terminalRunLimit = limit
	t.Cleanup(func() { terminalRunLimit = previous })
}

// insertRun writes one run row without recording timeline events, so retention
// tests can place runs at exact timestamps.
func insertRun(t *testing.T, store *Store, id, state string, updatedAt int64) {
	t.Helper()
	_, err := store.db.ExecContext(context.Background(), `
INSERT INTO runs (id, task_id, state, head_sha, created_at, updated_at)
VALUES (?, 'T-1', ?, '', ?, ?)`, id, state, updatedAt-1, updatedAt)
	if err != nil {
		t.Fatalf("insert run %s: %v", id, err)
	}
}

func TestPruneStaleBoundsTerminalRuns(t *testing.T) {
	withTerminalRunLimit(t, 3)
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	states := []string{stateMerged, stateClosed, stateDropped}
	for i := 0; i < 10; i++ {
		insertRun(t, store, fmt.Sprintf("r-%02d", i), states[i%len(states)], int64(1000+i))
	}
	// Active runs are never pruned, even when older than every terminal run.
	insertRun(t, store, "active-1", string(core.RunRunning), 100)
	insertRun(t, store, "active-2", string(core.RunWaitingCI), 101)
	// An incident keeps its fixer run even when that run is past the boundary.
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO base_incidents (id, repo, base_branch, signature, fixer_run_id, created_at)
VALUES ('incident-1', 'o/r', 'main', 'sig', 'r-00', 1)`); err != nil {
		t.Fatalf("insert incident: %v", err)
	}

	removed, err := store.PruneStale(ctx, 0)
	if err != nil {
		t.Fatalf("PruneStale: %v", err)
	}
	// Nine terminal runs are eligible for the limit after the protected one is
	// excluded. The newest three stay, so six go.
	if removed != 6 {
		t.Fatalf("removed = %d, want 6", removed)
	}
	for _, id := range []string{"r-00", "r-07", "r-08", "r-09", "active-1", "active-2"} {
		if got := countStoreRows(t, store, `SELECT COUNT(*) FROM runs WHERE id='`+id+`'`); got != 1 {
			t.Errorf("run %s survived = %d, want 1", id, got)
		}
	}
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("r-%02d", i)
		if got := countStoreRows(t, store, `SELECT COUNT(*) FROM runs WHERE id='`+id+`'`); got != 0 {
			t.Errorf("run %s survived = %d, want 0", id, got)
		}
	}

	removed, err = store.PruneStale(ctx, 0)
	if err != nil || removed != 0 {
		t.Fatalf("second prune = %d, %v; want 0, nil", removed, err)
	}
}

func TestPruneStaleDeletesInBoundedBatches(t *testing.T) {
	if retentionBatchSize != 1000 {
		t.Fatalf("retentionBatchSize = %d, want 1000", retentionBatchSize)
	}
	withTerminalRunLimit(t, 1)
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	// Two full batches plus one partial batch must all go in one pass.
	for i := 0; i < 2*retentionBatchSize+1; i++ {
		insertRun(t, store, fmt.Sprintf("r-%05d", i), stateMerged, int64(1000+i))
	}
	removed, err := store.PruneStale(ctx, 0)
	if err != nil {
		t.Fatalf("PruneStale: %v", err)
	}
	if removed != 2*retentionBatchSize {
		t.Fatalf("removed = %d, want %d", removed, 2*retentionBatchSize)
	}
	if got := countStoreRows(t, store, `SELECT COUNT(*) FROM runs`); got != 1 {
		t.Fatalf("runs = %d, want 1", got)
	}
}

func TestSaveCAS(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	stored := &core.Run{
		ID:        "r-cas",
		TaskID:    "T-1",
		State:     core.RunPending,
		HeadSHA:   "sha-a",
		UpdatedAt: time.Unix(0, 1000).UTC(),
	}
	if err := store.Save(ctx, stored); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	expectedAt, expectedSHA := loaded.UpdatedAt.UnixNano(), loaded.HeadSHA

	next := *loaded
	next.State = core.RunRunning
	next.HeadSHA = "sha-b"
	next.UpdatedAt = loaded.UpdatedAt.Add(time.Second)
	if err := store.SaveCAS(ctx, &next, expectedAt, expectedSHA); err != nil {
		t.Fatalf("SaveCAS with current version: %v", err)
	}
	got, err := store.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get after SaveCAS: %v", err)
	}
	if got.State != core.RunRunning || got.HeadSHA != "sha-b" {
		t.Fatalf("run after SaveCAS = %+v, want running with sha-b", got)
	}

	// The same read no longer matches the row it was taken from.
	stale := *loaded
	stale.State = core.RunBlocked
	if err := store.SaveCAS(ctx, &stale, expectedAt, expectedSHA); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("SaveCAS with stale version = %v, want conflict", err)
	}
	got, err = store.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get after stale SaveCAS: %v", err)
	}
	if got.State != core.RunRunning || got.Reason != "" {
		t.Fatalf("stale save changed the row: %+v", got)
	}

	missing := &core.Run{ID: "r-gone", TaskID: "T-1", State: core.RunPending}
	if err := store.SaveCAS(ctx, missing, expectedAt, expectedSHA); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("SaveCAS on missing run = %v, want not_found", err)
	}
}

func TestSaveCASLetsOneConcurrentWriterWin(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	stored := &core.Run{ID: "r-race", TaskID: "T-1", State: core.RunPending, HeadSHA: "sha-0"}
	if err := store.Save(ctx, stored); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	expectedAt, expectedSHA := loaded.UpdatedAt.UnixNano(), loaded.HeadSHA

	states := []core.RunState{core.RunRunning, core.RunWaitingCI}
	results := make(chan error, len(states))
	var wg sync.WaitGroup
	for _, state := range states {
		wg.Add(1)
		go func(state core.RunState) {
			defer wg.Done()
			next := *loaded
			next.State = state
			next.UpdatedAt = next.UpdatedAt.Add(time.Second)
			results <- store.SaveCAS(ctx, &next, expectedAt, expectedSHA)
		}(state)
	}
	wg.Wait()
	close(results)

	var conflicts int
	for err := range results {
		switch core.CodeOf(err) {
		case "":
		case core.CodeConflict:
			conflicts++
		default:
			t.Fatalf("concurrent SaveCAS = %v", err)
		}
	}
	if conflicts != 1 {
		t.Fatalf("conflicts = %d, want 1", conflicts)
	}
	got, err := store.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get after race: %v", err)
	}
	if got.State != core.RunRunning && got.State != core.RunWaitingCI {
		t.Fatalf("run state = %s, want one of the two writes", got.State)
	}
}

func TestConcurrentOpenPreparesSchemaOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	const openers = 4
	errs := make(chan error, openers)
	var wg sync.WaitGroup
	for i := 0; i < openers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store, err := Open(path)
			if err == nil {
				err = store.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Open: %v", err)
		}
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()
	var marked, current, found int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&marked); err != nil {
		t.Fatalf("PRAGMA user_version: %v", err)
	}
	if err := store.db.QueryRow(`PRAGMA schema_version`).Scan(&current); err != nil {
		t.Fatalf("PRAGMA schema_version: %v", err)
	}
	if marked != current || marked == 0 {
		t.Fatalf("user_version = %d, schema_version = %d; want equal and nonzero", marked, current)
	}
	holders := strings.TrimSuffix(strings.Repeat("?,", len(schemaObjects)), ",")
	args := make([]any, len(schemaObjects))
	for i, name := range schemaObjects {
		args[i] = name
	}
	query := `SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table','index','trigger') AND name IN (` + holders + `)`
	if err := store.db.QueryRow(query, args...).Scan(&found); err != nil {
		t.Fatalf("count schema objects: %v", err)
	}
	if found != len(schemaObjects) {
		t.Fatalf("schema objects = %d, want %d", found, len(schemaObjects))
	}
	insertRun(t, store, "r-1", string(core.RunPending), 1)
}
