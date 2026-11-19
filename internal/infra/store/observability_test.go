package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// failingWriter is an artificial write failure: it never runs fn and never
// touches the database, but the observed writer still records the error.
type failingWriter struct {
	err   error
	calls int
}

func (f *failingWriter) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	f.calls++
	return f.err
}

func openObservabilityDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func persistFailed(t *testing.T, err error) *domain.AppError {
	t.Helper()
	if err == nil {
		t.Fatal("mutating call must fail")
	}
	var appErr *domain.AppError
	if !errors.As(err, &appErr) || appErr.Code != domain.CodePersistFailed {
		t.Fatalf("error = %v, want AppError/PERSIST_FAILED", err)
	}
	return appErr
}

func insertRawEvent(t *testing.T, d *DB, watchID string, payload []byte) int64 {
	t.Helper()
	var seq int64
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			INSERT INTO events (watch_id, schema_version, payload, created_at)
			VALUES (?, ?, ?, ?)`, watchID, 2, payload, time.Now().UTC().UnixNano())
		if err != nil {
			return err
		}
		seq, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	return seq
}

func TestObservedPersistFailureRecordsLastErrorAndReturnsPersistFailed(t *testing.T) {
	d := openObservabilityDB(t)
	cause := errors.New("disk full")
	fw := &failingWriter{err: cause}
	op := NewObservedPersistWithWriter(d, fw)

	err := op.Write(context.Background(), func(tx *sql.Tx) error { return nil })
	appErr := persistFailed(t, err)
	if !strings.Contains(appErr.Message, cause.Error()) {
		t.Errorf("message = %q, want it to carry %q", appErr.Message, cause.Error())
	}
	if fw.calls != 1 {
		t.Errorf("writer calls = %d, want 1", fw.calls)
	}

	requireTable(t, d.readDB, "persist_progress")
	state, err := d.PersistState()
	if err != nil {
		t.Fatalf("PersistState: %v", err)
	}
	if state.Seq != 0 {
		t.Errorf("seq = %d, want 0 after a failed write", state.Seq)
	}
	if !strings.Contains(state.LastError, cause.Error()) {
		t.Errorf("lastError = %q, want it to carry %q", state.LastError, cause.Error())
	}
	if state.FailedSince.IsZero() {
		t.Error("failedSince is zero, want the failure timestamp")
	}
}

func TestObservedPersistSeqOnlyAfterCommittedWrites(t *testing.T) {
	d := openObservabilityDB(t)
	op := NewObservedPersist(d)

	// A failure inside the real writer rolls the data transaction back. Seq
	// must not move, and the caller must still see PERSIST_FAILED.
	err := op.Write(context.Background(), func(tx *sql.Tx) error {
		return errors.New("forced rollback")
	})
	persistFailed(t, err)
	state, err := d.PersistState()
	if err != nil {
		t.Fatalf("PersistState after failure: %v", err)
	}
	if state.Seq != 0 || state.LastError == "" || state.FailedSince.IsZero() {
		t.Fatalf("state after failure = %+v, want seq 0 and a recorded error", state)
	}

	const successes = 3
	for i := 0; i < successes; i++ {
		err := op.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO watches
				  (id, repo, pr, head_sha, status, revision, payload)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				fmt.Sprintf("w%d", i), "org/repo", i, "sha", "watching", 1, []byte(`{}`))
			return err
		})
		if err != nil {
			t.Fatalf("Write #%d: %v", i, err)
		}
	}

	state, err = d.PersistState()
	if err != nil {
		t.Fatalf("PersistState after successes: %v", err)
	}
	if state.Seq != successes {
		t.Errorf("seq = %d, want %d", state.Seq, successes)
	}
	if state.LastError != "" {
		t.Errorf("lastError = %q, want cleared after a successful write", state.LastError)
	}
	if !state.FailedSince.IsZero() {
		t.Errorf("failedSince = %v, want zero after a successful write", state.FailedSince)
	}

	var count int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM watches`).Scan(&count); err != nil {
		t.Fatalf("count watches: %v", err)
	}
	if count != successes {
		t.Errorf("watches = %d, want %d committed", count, successes)
	}
}

func TestLoadEventsSkipsCorruptedRowsWithoutStopping(t *testing.T) {
	d := openObservabilityDB(t)
	insertRawEvent(t, d, "good-1", []byte(`{"kind":"wake-intent","obligationID":"w1:1:s1"}`))
	badSeq := insertRawEvent(t, d, "bad", []byte(`{"kind":`))
	insertRawEvent(t, d, "good-2", []byte(`{"ok":true}`))

	events, loadErrors, err := d.LoadEvents()
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 good rows: %+v", len(events), events)
	}
	if events[0].WatchID != "good-1" || events[1].WatchID != "good-2" {
		t.Errorf("event watch ids = %q,%q, want good-1,good-2", events[0].WatchID, events[1].WatchID)
	}
	if len(loadErrors) != 1 {
		t.Fatalf("loadErrors = %d, want 1: %+v", len(loadErrors), loadErrors)
	}
	if loadErrors[0].Line != badSeq {
		t.Errorf("load error line = %d, want %d", loadErrors[0].Line, badSeq)
	}
	if loadErrors[0].Reason == "" {
		t.Error("load error reason is empty")
	}
}

func TestPersistStatsIsReadOnly(t *testing.T) {
	d := openObservabilityDB(t)
	op := NewObservedPersist(d)

	err := op.Write(context.Background(), func(tx *sql.Tx) error {
		return errors.New("forced failure")
	})
	persistFailed(t, err)
	insertRawEvent(t, d, "bad", []byte(`not json at all`))

	stateBefore, errorsBefore, err := d.PersistStats()
	if err != nil {
		t.Fatalf("PersistStats #1: %v", err)
	}
	stateAfter, errorsAfter, err := d.PersistStats()
	if err != nil {
		t.Fatalf("PersistStats #2: %v", err)
	}
	if !reflect.DeepEqual(stateBefore, stateAfter) {
		t.Errorf("persist changed across reads: %+v -> %+v", stateBefore, stateAfter)
	}
	if !reflect.DeepEqual(errorsBefore, errorsAfter) {
		t.Errorf("loadErrors changed across reads: %+v -> %+v", errorsBefore, errorsAfter)
	}
	if len(errorsBefore) != 1 {
		t.Errorf("loadErrors = %d, want 1", len(errorsBefore))
	}
	if stateBefore.LastError == "" || stateBefore.Seq != 0 {
		t.Errorf("state = %+v, want a recorded error and seq 0", stateBefore)
	}

	var progressRows, eventRows int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM persist_progress`).Scan(&progressRows); err != nil {
		t.Fatalf("count persist_progress: %v", err)
	}
	if err := d.readDB.QueryRow(`SELECT count(*) FROM events`).Scan(&eventRows); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if progressRows != 1 || eventRows != 1 {
		t.Errorf("rows after reads = progress %d, events %d, want 1,1", progressRows, eventRows)
	}
}

func TestPersistStatsOnEmptyStoreDoesNotCreateState(t *testing.T) {
	d := openObservabilityDB(t)
	state, loadErrors, err := d.PersistStats()
	if err != nil {
		t.Fatalf("PersistStats: %v", err)
	}
	if state.Seq != 0 || state.LastError != "" || !state.FailedSince.IsZero() {
		t.Errorf("state = %+v, want zero", state)
	}
	if len(loadErrors) != 0 {
		t.Errorf("loadErrors = %+v, want empty", loadErrors)
	}
	ok, err := tableExists(context.Background(), d.readDB, "persist_progress")
	if err != nil {
		t.Fatalf("tableExists: %v", err)
	}
	if ok {
		t.Error("a read created persist_progress")
	}
}
