package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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

func TestObservedPersistFailureKeepsCauseThroughAppError(t *testing.T) {
	d := openObservabilityDB(t)
	cause := errors.New("disk full")
	op := NewObservedPersistWithWriter(d, &failingWriter{err: cause})

	err := op.Write(context.Background(), func(tx *sql.Tx) error { return nil })
	persistFailed(t, err)
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(%v, cause) = false, want the original cause reachable through *domain.AppError", err)
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

// TestOpenProductionExposesHonestPersistToStats covers the production wiring:
// every d.Write is observed, a failure is recorded without moving seq, and
// PersistStats is the read path beacon_stats.persist uses.
func TestOpenProductionExposesHonestPersistToStats(t *testing.T) {
	d, err := OpenProduction(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenProduction: %v", err)
	}
	defer d.Close()
	ctx := context.Background()

	write := func(fn func(*sql.Tx) error) error { return d.Write(ctx, fn) }
	noop := func(*sql.Tx) error { return nil }
	if err := write(noop); err != nil {
		t.Fatalf("successful write: %v", err)
	}

	before, err := d.PersistState()
	if err != nil {
		t.Fatalf("PersistState before failure: %v", err)
	}
	if before.Seq != 1 {
		t.Fatalf("seq = %d, want 1", before.Seq)
	}

	persistFailed(t, write(func(*sql.Tx) error { return errors.New("disk full") }))
	state, err := d.PersistState()
	if err != nil {
		t.Fatalf("PersistState after failure: %v", err)
	}
	if state.Seq != before.Seq {
		t.Errorf("seq = %d, want it unchanged after failure", state.Seq)
	}
	if !strings.Contains(state.LastError, "disk full") || state.FailedSince.IsZero() {
		t.Errorf("state = %+v, want lastError and failedSince recorded", state)
	}

	stats, loadErrors, err := d.PersistStats()
	if err != nil {
		t.Fatalf("PersistStats: %v", err)
	}
	if stats != state {
		t.Errorf("PersistStats state = %+v, want %+v", stats, state)
	}
	if len(loadErrors) != 0 {
		t.Errorf("loadErrors = %+v, want none", loadErrors)
	}

	if err := write(noop); err != nil {
		t.Fatalf("recovery write: %v", err)
	}
	state, err = d.PersistState()
	if err != nil {
		t.Fatalf("PersistState after recovery: %v", err)
	}
	if state.Seq != 2 || state.LastError != "" || !state.FailedSince.IsZero() {
		t.Errorf("state after recovery = %+v, want seq 2 and a cleared error", state)
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

func TestRecordStreamExposesMissingAndDuplicates(t *testing.T) {
	d := openObservabilityDB(t)
	op := NewObservedPersist(d)

	// A read on a fresh store is zero and does not create the table.
	empty, err := d.StreamHealth()
	if err != nil {
		t.Fatalf("StreamHealth on fresh store: %v", err)
	}
	if empty.Chunks != 0 || empty.LastSeq != 0 || empty.Duplicates != 0 || len(empty.Missing) != 0 {
		t.Errorf("fresh stream health = %+v, want zero", empty)
	}
	ok, err := tableExists(context.Background(), d.readDB, "stream_health")
	if err != nil {
		t.Fatalf("tableExists: %v", err)
	}
	if ok {
		t.Error("a read created stream_health")
	}

	final := domain.StreamFinal{
		Chunks:     5,
		Missing:    []uint64{2, 4},
		Duplicates: 3,
		Streamed:   true,
		Result:     []byte("full result"),
	}
	if err := op.RecordStream(context.Background(), final); err != nil {
		t.Fatalf("RecordStream: %v", err)
	}

	got, err := d.StreamHealth()
	if err != nil {
		t.Fatalf("StreamHealth: %v", err)
	}
	if got.Chunks != 5 || got.LastSeq != 5 || got.Duplicates != 3 {
		t.Errorf("stream health = %+v, want chunks 5, lastSeq 5, duplicates 3", got)
	}
	if !reflect.DeepEqual(got.Missing, []uint64{2, 4}) {
		t.Errorf("missing = %v, want [2 4]", got.Missing)
	}

	// The same read is available on the wrapped writer, and recording stream
	// health must not advance the persist seq.
	viaWriter, err := op.StreamHealth()
	if err != nil {
		t.Fatalf("ObservedPersist.StreamHealth: %v", err)
	}
	if !reflect.DeepEqual(viaWriter, got) {
		t.Errorf("writer view = %+v, db view = %+v", viaWriter, got)
	}
	state, err := d.PersistState()
	if err != nil {
		t.Fatalf("PersistState: %v", err)
	}
	if state.Seq != 0 || state.LastError != "" {
		t.Errorf("persist state = %+v, want unchanged by RecordStream", state)
	}
}

func TestRecordStreamEmptyMissingRoundTrips(t *testing.T) {
	d := openObservabilityDB(t)
	op := NewObservedPersist(d)

	if err := op.RecordStream(context.Background(), domain.StreamFinal{Chunks: 2}); err != nil {
		t.Fatalf("RecordStream: %v", err)
	}
	got, err := d.StreamHealth()
	if err != nil {
		t.Fatalf("StreamHealth: %v", err)
	}
	if len(got.Missing) != 0 {
		t.Errorf("missing = %v, want empty", got.Missing)
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

// TestRecordWakeKeepsSentAndWokeSeparate is the T3.26 acceptance core
// (ADR 0032 p2): a dispatch and a start are different observable facts. A sent
// wake stays woke=false; a start reported on its own stays sent=false; both
// converge on one row when each is observed in turn.
func TestRecordWakeKeepsSentAndWokeSeparate(t *testing.T) {
	d := openObservabilityDB(t)
	op := NewObservedPersist(d)

	empty, err := d.WakeObservations()
	if err != nil {
		t.Fatalf("WakeObservations on fresh store: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("fresh wake observations = %+v, want empty", empty)
	}
	ok, err := tableExists(context.Background(), d.readDB, "wake_observations")
	if err != nil {
		t.Fatalf("tableExists: %v", err)
	}
	if ok {
		t.Error("a read created wake_observations")
	}

	if err := op.RecordWake(context.Background(), WakeObservation{ObligationID: "obl-1", Sent: true}); err != nil {
		t.Fatalf("RecordWake sent: %v", err)
	}
	got, err := d.WakeObservations()
	if err != nil {
		t.Fatalf("WakeObservations after sent: %v", err)
	}
	if len(got) != 1 || got[0].ObligationID != "obl-1" || !got[0].Sent || got[0].Woke {
		t.Fatalf("after sent = %+v, want one row sent=true woke=false (delivery is not a start)", got)
	}

	if err := op.RecordWake(context.Background(), WakeObservation{ObligationID: "obl-1", Woke: true}); err != nil {
		t.Fatalf("RecordWake woke: %v", err)
	}
	if err := op.RecordWake(context.Background(), WakeObservation{ObligationID: "obl-2", Woke: true}); err != nil {
		t.Fatalf("RecordWake woke only: %v", err)
	}
	got, err = d.WakeObservations()
	if err != nil {
		t.Fatalf("WakeObservations after woke: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("wake observations = %+v, want 2 rows", got)
	}
	if got[0].ObligationID != "obl-1" || !got[0].Sent || !got[0].Woke {
		t.Errorf("obl-1 = %+v, want both facts merged", got[0])
	}
	if got[1].ObligationID != "obl-2" || got[1].Sent || !got[1].Woke {
		t.Errorf("obl-2 = %+v, want woke without sent (start is not derived from delivery)", got[1])
	}

	// Repeating a fact is idempotent and does not create extra rows.
	if err := op.RecordWake(context.Background(), WakeObservation{ObligationID: "obl-1", Woke: true}); err != nil {
		t.Fatalf("RecordWake repeat: %v", err)
	}
	got, err = d.WakeObservations()
	if err != nil {
		t.Fatalf("WakeObservations after repeat: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("rows after repeat = %d, want 2", len(got))
	}
}

// TestRecordWakeDoesNotAdvancePersistSeq keeps the new fact out of the persist
// block: wake observations are their own observability surface, like streams.
func TestRecordWakeDoesNotAdvancePersistSeq(t *testing.T) {
	d := openObservabilityDB(t)
	op := NewObservedPersist(d)

	if err := op.RecordWake(context.Background(), WakeObservation{ObligationID: "obl-1", Sent: true, Woke: true}); err != nil {
		t.Fatalf("RecordWake: %v", err)
	}
	viaWriter, err := op.WakeObservations()
	if err != nil {
		t.Fatalf("ObservedPersist.WakeObservations: %v", err)
	}
	viaDB, err := d.WakeObservations()
	if err != nil {
		t.Fatalf("DB.WakeObservations: %v", err)
	}
	if !reflect.DeepEqual(viaWriter, viaDB) {
		t.Errorf("writer view = %+v, db view = %+v", viaWriter, viaDB)
	}
	state, err := d.PersistState()
	if err != nil {
		t.Fatalf("PersistState: %v", err)
	}
	if state.Seq != 0 || state.LastError != "" {
		t.Errorf("persist state = %+v, want unchanged by RecordWake", state)
	}
}

// TestRecordWakeRejectsEmptyObligation keeps the boundary honest: a fact with
// no obligation cannot be attributed to a wake episode.
func TestRecordWakeRejectsEmptyObligation(t *testing.T) {
	d := openObservabilityDB(t)
	op := NewObservedPersist(d)
	if err := op.RecordWake(context.Background(), WakeObservation{Sent: true}); err == nil {
		t.Fatal("RecordWake with empty obligation id: error = nil, want invalid input")
	}
}

// Shared gate acceptance (ADR 0037 p3, T3.49). Fifteen callers on one CI must
// produce one wait, one warning, and no phantom per-agent timers.

func waitForActiveWaiters(t *testing.T, g *SharedGate, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	if runnerDeadline, ok := t.Deadline(); ok && runnerDeadline.Before(deadline) {
		deadline = runnerDeadline
	}
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		if got := g.ActiveWaiters(key); got == want {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("active waiters for %q = %d, want %d", key, g.ActiveWaiters(key), want)
		}
		select {
		case <-poll.C:
		case <-t.Context().Done():
			t.Fatalf("active waiters for %q = %d, want %d (test context ended)", key, g.ActiveWaiters(key), want)
		}
	}
}

// TestSharedGateCoalescesFifteenUnitsOnOneCI is the 15-agents case from the
// card: the expensive wait runs once, every unit sees the shared gate and one
// WARNING is printed, and the gate is empty again afterwards.
func TestSharedGateCoalescesFifteenUnitsOnOneCI(t *testing.T) {
	g := NewSharedGate()
	var mu sync.Mutex
	var warnings []string
	g.SetWarningSink(func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})

	const (
		units = 15
		key   = "ci:org/repo#7"
	)
	release := make(chan struct{})
	var runs atomic.Int32
	fn := func(context.Context) error {
		runs.Add(1)
		<-release
		return nil
	}

	start := make(chan struct{})
	errs := make([]error, units)
	var wg sync.WaitGroup
	for i := 0; i < units; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = g.Wait(context.Background(), key, fn)
		}(i)
	}
	close(start)

	waitForActiveWaiters(t, g, key, units)
	if got := runs.Load(); got != 1 {
		t.Fatalf("expensive wait ran %d times, want 1 (one CI, one wait)", got)
	}

	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("unit %d: Wait = %v, want nil", i, err)
		}
	}
	if got := runs.Load(); got != 1 {
		t.Errorf("expensive wait ran %d times, want 1", got)
	}
	if got := g.ActiveWaiters(key); got != 0 {
		t.Errorf("active waiters after completion = %d, want 0", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d (%q), want 1", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "WARNING") || !strings.Contains(warnings[0], key) {
		t.Errorf("warning = %q, want WARNING with key %q", warnings[0], key)
	}
}

// TestSharedCIWaitCoalescesOneCI is the T3.73 integration check. Fifteen units
// waiting on one CI go through SharedCIWait and run the expensive wait once.
// Removing the SharedGate call from SharedCIWait makes the run count reach 15
// and this test fails.
func TestSharedCIWaitCoalescesOneCI(t *testing.T) {
	g := sharedCIWait()
	const key = "T3.73:ci:org/repo#73"
	g.SetWarningSink(func(string, ...any) {})
	defer g.SetWarningSink(nil)

	const units = 15
	release := make(chan struct{})
	var runs atomic.Int32
	fn := func(context.Context) error {
		runs.Add(1)
		<-release
		return nil
	}

	start := make(chan struct{})
	errs := make([]error, units)
	var wg sync.WaitGroup
	for i := 0; i < units; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = SharedCIWait(context.Background(), key, fn)
		}(i)
	}
	close(start)

	waitForActiveWaiters(t, g, key, units)
	if got := runs.Load(); got != 1 {
		t.Fatalf("SharedCIWait ran the expensive wait %d times, want 1 (one CI, one wait)", got)
	}

	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("unit %d: SharedCIWait = %v, want nil", i, err)
		}
	}
	if got := runs.Load(); got != 1 {
		t.Errorf("expensive wait ran %d times after completion, want 1", got)
	}
	if got := g.ActiveWaiters(key); got != 0 {
		t.Errorf("active waiters after completion = %d, want 0", got)
	}
}

// TestSharedGateDifferentKeysDoNotCoalesce keeps two independent CIs
// independent: no shared gate, no warning, two waits.
func TestSharedGateDifferentKeysDoNotCoalesce(t *testing.T) {
	g := NewSharedGate()
	var mu sync.Mutex
	var warnings []string
	g.SetWarningSink(func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})

	release := make(chan struct{})
	var runs atomic.Int32
	fn := func(context.Context) error {
		runs.Add(1)
		<-release
		return nil
	}

	keys := []string{"ci:org/repo#7", "ci:org/repo#8"}
	var wg sync.WaitGroup
	for _, key := range keys {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_ = g.Wait(context.Background(), key, fn)
		}(key)
	}
	waitForActiveWaiters(t, g, keys[0], 1)
	waitForActiveWaiters(t, g, keys[1], 1)

	if got := runs.Load(); got != 2 {
		t.Fatalf("expensive wait ran %d times, want 2 for two different keys", got)
	}
	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want none: distinct keys are not a shared gate", warnings)
	}
}

// TestSharedGateSharesLeaderError: one CI result is shared by all waiters,
// success or failure. A failed shared wait is still one wait.
func TestSharedGateSharesLeaderError(t *testing.T) {
	g := NewSharedGate()
	g.SetWarningSink(func(string, ...any) {})

	boom := errors.New("ci red")
	release := make(chan struct{})
	var runs atomic.Int32
	fn := func(context.Context) error {
		runs.Add(1)
		<-release
		return boom
	}

	const units = 5
	const key = "ci:org/repo#9"
	start := make(chan struct{})
	errs := make([]error, units)
	var wg sync.WaitGroup
	for i := 0; i < units; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = g.Wait(context.Background(), key, fn)
		}(i)
	}
	close(start)
	waitForActiveWaiters(t, g, key, units)

	close(release)
	wg.Wait()

	if got := runs.Load(); got != 1 {
		t.Errorf("expensive wait ran %d times, want 1", got)
	}
	for i, err := range errs {
		if !errors.Is(err, boom) {
			t.Errorf("unit %d: Wait = %v, want the shared leader error %v", i, err, boom)
		}
	}
}

// TestSharedGateCancelledFollowerLeavesRound: an agent that times out on its
// own context leaves with ctx.Err() and does not abort the shared wait or
// create a second one for the others.
func TestSharedGateCancelledFollowerLeavesRound(t *testing.T) {
	g := NewSharedGate()
	g.SetWarningSink(func(string, ...any) {})

	release := make(chan struct{})
	var runs atomic.Int32
	fn := func(context.Context) error {
		runs.Add(1)
		<-release
		return nil
	}

	const key = "ci:org/repo#11"
	cancelCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var leaderErr, followerErr, cancelledErr error
	cancelledReturned := make(chan struct{})
	var wg sync.WaitGroup

	// Start the leader alone so it definitely owns the round. If the
	// cancellable caller won the race to become leader, it would block in fn
	// and the wait below for its return would deadlock.
	wg.Add(1)
	go func() { defer wg.Done(); leaderErr = g.Wait(context.Background(), key, fn) }()
	waitForActiveWaiters(t, g, key, 1)

	wg.Add(1)
	go func() { defer wg.Done(); followerErr = g.Wait(context.Background(), key, fn) }()
	wg.Add(1)
	go func() {
		defer wg.Done()
		cancelledErr = g.Wait(cancelCtx, key, fn)
		close(cancelledReturned)
	}()

	waitForActiveWaiters(t, g, key, 3)
	cancel()
	// Let the cancelled unit leave before the leader finishes, so the only
	// ready case in its select is ctx.Done(), not the shared gate's done.
	<-cancelledReturned

	close(release)
	wg.Wait()

	if !errors.Is(cancelledErr, context.Canceled) {
		t.Errorf("cancelled follower = %v, want context.Canceled", cancelledErr)
	}
	if leaderErr != nil || followerErr != nil {
		t.Errorf("leader = %v, follower = %v, want both nil", leaderErr, followerErr)
	}
	if got := runs.Load(); got != 1 {
		t.Errorf("expensive wait ran %d times, want 1", got)
	}
	if got := g.ActiveWaiters(key); got != 0 {
		t.Errorf("active waiters after completion = %d, want 0", got)
	}
}

// TestSharedGateWarnsOnEverySharedRound: detection resets per round, so a
// later shared wait on the same key is reported again.
func TestSharedGateWarnsOnEverySharedRound(t *testing.T) {
	g := NewSharedGate()
	var mu sync.Mutex
	warnings := 0
	g.SetWarningSink(func(string, ...any) {
		mu.Lock()
		defer mu.Unlock()
		warnings++
	})

	const key = "ci:org/repo#12"
	for round := 0; round < 2; round++ {
		release := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		for i := 0; i < 2; i++ {
			go func() {
				defer wg.Done()
				_ = g.Wait(context.Background(), key, func(context.Context) error {
					<-release
					return nil
				})
			}()
		}
		waitForActiveWaiters(t, g, key, 2)
		close(release)
		wg.Wait()
	}

	mu.Lock()
	defer mu.Unlock()
	if warnings != 2 {
		t.Errorf("warnings = %d, want one per shared round (2)", warnings)
	}
}
