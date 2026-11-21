package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func openProductionObservabilityDB(t *testing.T) *DB {
	t.Helper()
	d, err := OpenProduction(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenProduction: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestOpenProductionCreatesProgressTableAtOpen pins the first half of the fix:
// the progress table exists before any write, so CREATE TABLE IF NOT EXISTS
// never runs on the hot path.
func TestOpenProductionCreatesProgressTableAtOpen(t *testing.T) {
	d := openProductionObservabilityDB(t)
	ok, err := tableExists(context.Background(), d.readDB, "persist_progress")
	if err != nil {
		t.Fatalf("tableExists: %v", err)
	}
	if !ok {
		t.Fatal("OpenProduction left persist_progress missing; it must be created at Open")
	}
	state, err := d.PersistState()
	if err != nil {
		t.Fatalf("PersistState: %v", err)
	}
	if state.Seq != 0 || state.LastError != "" || !state.FailedSince.IsZero() {
		t.Errorf("fresh state = %+v, want a seeded zero row", state)
	}
}

// TestObservedPersistUsesOneTransactionPerWrite pins the second half: a
// successful observed Write commits data and seq in one writer transaction.
// The old code committed data and then opened a second transaction for the
// counter, so the transaction count was twice the number of writes.
func TestObservedPersistUsesOneTransactionPerWrite(t *testing.T) {
	d := openProductionObservabilityDB(t)
	ctx := context.Background()
	before := d.WriteCount()

	const writes = 5
	for i := 0; i < writes; i++ {
		if err := d.Write(ctx, func(*sql.Tx) error { return nil }); err != nil {
			t.Fatalf("Write #%d: %v", i, err)
		}
	}
	if got := d.WriteCount() - before; got != writes {
		t.Fatalf("writer transactions = %d, want %d (one per Write)", got, writes)
	}
	state, err := d.PersistState()
	if err != nil {
		t.Fatalf("PersistState: %v", err)
	}
	if state.Seq != writes {
		t.Errorf("seq = %d, want %d", state.Seq, writes)
	}
	if state.LastError != "" || !state.FailedSince.IsZero() {
		t.Errorf("state = %+v, want a clean lastError and failedSince after successes", state)
	}
}

// commitThenFailWriter commits fn through the real DB, then reports a failure.
// It models the old crash window: the data transaction committed, but the
// progress write that used to follow it never ran.
type commitThenFailWriter struct {
	db    *DB
	cause error
}

func (w *commitThenFailWriter) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	if err := w.db.rawWrite(ctx, fn); err != nil {
		return err
	}
	return w.cause
}

// TestObservedPersistProgressDoesNotLagAfterCrashBetweenOperations checks that
// the counter cannot fall behind committed data. With progress inside the data
// transaction, a writer that fails after committing still leaves seq matching
// the rows on disk. The old two-transaction path left seq at zero here.
func TestObservedPersistProgressDoesNotLagAfterCrashBetweenOperations(t *testing.T) {
	d := openProductionObservabilityDB(t)
	ctx := context.Background()
	cause := errors.New("crash before the old progress transaction")
	op := NewObservedPersistWithWriter(d, &commitThenFailWriter{db: d, cause: cause})

	err := op.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO watches (id, repo, pr, head_sha, status, revision, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, "crash-1", "org/repo", 1, "sha", "watching", 1, []byte(`{}`))
		return err
	})
	persistFailed(t, err)

	var watches int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM watches`).Scan(&watches); err != nil {
		t.Fatalf("count watches: %v", err)
	}
	if watches != 1 {
		t.Fatalf("watches = %d, want the committed row", watches)
	}
	state, err := d.PersistState()
	if err != nil {
		t.Fatalf("PersistState: %v", err)
	}
	if state.Seq != 1 {
		t.Errorf("seq = %d, want 1 to match the committed data", state.Seq)
	}
	if !strings.Contains(state.LastError, cause.Error()) || state.FailedSince.IsZero() {
		t.Errorf("state = %+v, want the crash recorded in lastError and failedSince", state)
	}
}
