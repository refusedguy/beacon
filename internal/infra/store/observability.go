// Package store: observability for beacon_stats.
//
// This file owns the persist_progress table and the journal load-error
// collector. The migration corpus belongs to T1.7, so the table is created on
// first use, the same way archive.go creates watch_archive.
//
// ObservedPersist is the only place that advances seq. It does so after the
// wrapped write has committed, mirroring "meta.json with seq is written last"
// from WORKER-PROMPT §4.2. A failed write keeps seq where it was and records
// the reason in last_error, so the caller hears PERSIST_FAILED and the stats
// reader sees an honest progress row.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const createPersistProgressSQL = `
CREATE TABLE IF NOT EXISTS persist_progress (
  id           INTEGER PRIMARY KEY CHECK (id = 1),
  seq          INTEGER NOT NULL DEFAULT 0,
  last_error   TEXT NOT NULL DEFAULT '',
  failed_since INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO persist_progress (id, seq, last_error, failed_since)
VALUES (1, 0, '', 0);
`

// updateProgressSuccessSQL runs inside the data transaction. Keeping the seq
// bump in the same transaction as the data is what makes a successful Write a
// durable ack of both facts (ADR 0067).
const updateProgressSuccessSQL = `
UPDATE persist_progress
SET seq = seq + 1, last_error = '', failed_since = 0
WHERE id = 1`

const createStreamHealthSQL = `
CREATE TABLE IF NOT EXISTS stream_health (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  chunks     INTEGER NOT NULL DEFAULT 0,
  missing    TEXT NOT NULL DEFAULT '[]',
  duplicates INTEGER NOT NULL DEFAULT 0,
  last_seq   INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO stream_health (id, chunks, missing, duplicates, last_seq)
VALUES (1, 0, '[]', 0, 0);
`

// ADR 0032 p2 keeps "the follow-up was sent" and "the agent started work" as
// two separate facts. They live in their own table, not in persist_progress or
// stream_health, and are not the delivered/claimed/read timestamps of an
// obligation (ADR 0014). One row per obligation; each fact can be recorded on
// its own and the two are merged with OR, so observing a wake then a start, or
// only a start, both converge on one honest row.
const createWakeObservationsSQL = `
CREATE TABLE IF NOT EXISTS wake_observations (
  obligation_id TEXT PRIMARY KEY,
  sent          INTEGER NOT NULL DEFAULT 0,
  woke          INTEGER NOT NULL DEFAULT 0
);
`

// PersistState is the persist{seq,lastError} block of beacon_stats. FailedSince
// is when the last failed write was recorded; it is zero while the last write
// succeeded. The JSON names match the stats contract.
type PersistState struct {
	Seq         uint64    `json:"seq"`
	LastError   string    `json:"lastError"`
	FailedSince time.Time `json:"failedSince,omitempty"`
}

// StreamHealth is the observability view of the last completed stream: how
// many chunks the producer emitted, which seq positions were missing at the
// client and how many duplicates arrived. Missing is always a sorted list of
// 1-based positions. The counters are kept apart from persist_progress so the
// beacon_stats persist block keeps its seq and lastError contract.
type StreamHealth struct {
	Chunks     uint64   `json:"chunks"`
	Missing    []uint64 `json:"missing"`
	Duplicates uint64   `json:"duplicates"`
	LastSeq    uint64   `json:"lastSeq"`
}

// WakeObservation is the observability view of one wake episode (ADR 0032 p2).
// Sent means a follow-up was dispatched; Woke means the confirmation that the
// agent began work arrived. They are independent facts: Sent never implies
// Woke, and Woke is never inferred from delivered, claimed or read. The JSON
// names are separate so a manager can tell "отправил" from "начал работу".
type WakeObservation struct {
	ObligationID string `json:"obligationID"`
	Sent         bool   `json:"sent"`
	Woke         bool   `json:"woke"`
}

// LoadError is one damaged journal row. Line is the event seq, the SQLite
// analogue of a JSONL line number. Untrusted payload bytes are never copied
// into Reason; only the decoder error is kept.
type LoadError struct {
	Line   int64  `json:"line"`
	Reason string `json:"reason"`
}

// SingleWriter is the minimal write port. *DB and *ObservedPersist both
// satisfy it, so writers can be layered, for example a redacting writer around
// the observed one.
type SingleWriter interface {
	Write(ctx context.Context, fn func(*sql.Tx) error) error
}

// writeFunc adapts a function to SingleWriter. OpenProduction uses it to wrap
// the DB's raw writer so recorded progress does not re-enter the observer.
type writeFunc func(ctx context.Context, fn func(*sql.Tx) error) error

func (f writeFunc) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	return f(ctx, fn)
}

var (
	_ SingleWriter = (*DB)(nil)
	_ SingleWriter = (*ObservedPersist)(nil)
	_ SingleWriter = writeFunc(nil)
)

// ObservedPersist wraps a single writer and keeps persist_progress honest.
type ObservedPersist struct {
	mu     sync.Mutex
	db     *DB
	writer SingleWriter

	// initOnce creates the progress table on first use for observers built
	// outside open(). Production DBs create it at Open, so this path is a
	// fallback and never runs on the hot write loop after the first call.
	initOnce sync.Once
	initErr  error
}

// Observed returns a progress-tracking writer bound to d's own writer.
func (d *DB) Observed() *ObservedPersist { return NewObservedPersist(d) }

// NewObservedPersist wraps db's own writer.
func NewObservedPersist(db *DB) *ObservedPersist {
	return NewObservedPersistWithWriter(db, db)
}

// NewObservedPersistWithWriter wraps w and stores progress in db. Splitting
// the two lets tests inject a failing writer while keeping a healthy progress
// DB.
func NewObservedPersistWithWriter(db *DB, w SingleWriter) *ObservedPersist {
	if w == nil {
		w = db
	}
	return &ObservedPersist{db: db, writer: w}
}

// ensureProgressTable creates the progress table once, outside the hot write
// path. Production DBs already did this at Open; observers created directly in
// tests still need it before their first write.
func (p *ObservedPersist) ensureProgressTable() error {
	p.initOnce.Do(func() {
		ok, err := tableExists(context.Background(), p.db.readDB, "persist_progress")
		if err != nil {
			p.initErr = err
			return
		}
		if ok {
			return
		}
		p.initErr = p.db.rawWrite(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(createPersistProgressSQL)
			return err
		})
	})
	return p.initErr
}

// Write runs fn on the wrapped writer and records the outcome. Data and the
// success counter commit in one transaction, so a crash between them cannot
// leave seq behind the data it counts. A failed write rolls the data back and
// records last_error in its own transaction; the caller still sees
// PERSIST_FAILED (ADR 0067: only a durable commit is an ack).
func (p *ObservedPersist) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}

	if err := p.ensureProgressTable(); err != nil {
		return domain.NewAppError(domain.CodePersistFailed, err.Error())
	}

	withProgress := func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		_, err := tx.Exec(updateProgressSuccessSQL)
		return err
	}
	if err := p.writer.Write(ctx, withProgress); err != nil {
		if recErr := p.recordFailure(ctx, err); recErr != nil {
			err = errors.Join(err, recErr)
		}
		return domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	return nil
}

func (p *ObservedPersist) recordFailure(ctx context.Context, cause error) error {
	return p.db.rawWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			UPDATE persist_progress
			SET last_error = ?, failed_since = ?
			WHERE id = 1`, cause.Error(), time.Now().UTC().UnixNano())
		return err
	})
}

// RecordStream persists the final counters of a completed stream. The missing
// list is stored as JSON so an empty and a populated list round-trip the same
// way. It writes directly through the underlying DB, not through Write, so
// recording stream health does not advance the persist seq.
func (p *ObservedPersist) RecordStream(ctx context.Context, final domain.StreamFinal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	positions := final.Missing
	if positions == nil {
		positions = []uint64{}
	}
	missing, err := json.Marshal(positions)
	if err != nil {
		return domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	err = p.db.rawWrite(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createStreamHealthSQL); err != nil {
			return fmt.Errorf("store: stream health: create table: %w", err)
		}
		_, err := tx.Exec(`
			UPDATE stream_health
			SET chunks = ?, missing = ?, duplicates = ?, last_seq = ?
			WHERE id = 1`, final.Chunks, string(missing), final.Duplicates, final.Chunks)
		return err
	})
	if err != nil {
		return domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	return nil
}

// StreamHealth reads the last completed stream's counters without writing. A
// store that has never recorded a stream reports a zero state with no missing
// positions, and does not create the stream_health table.
func (d *DB) StreamHealth() (StreamHealth, error) {
	ok, err := tableExists(context.Background(), d.readDB, "stream_health")
	if err != nil || !ok {
		return StreamHealth{}, err
	}
	var (
		health StreamHealth
		raw    string
	)
	err = d.readDB.QueryRow(`
		SELECT chunks, missing, duplicates, last_seq FROM stream_health WHERE id = 1`,
	).Scan(&health.Chunks, &raw, &health.Duplicates, &health.LastSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return StreamHealth{}, nil
	}
	if err != nil {
		return StreamHealth{}, err
	}
	if err := json.Unmarshal([]byte(raw), &health.Missing); err != nil {
		return StreamHealth{}, err
	}
	return health, nil
}

// StreamHealth is the same read method on the wrapped writer, for callers that
// hold only the ObservedPersist.
func (p *ObservedPersist) StreamHealth() (StreamHealth, error) {
	return p.db.StreamHealth()
}

// RecordWake stores one wake fact for an obligation (ADR 0032 p2). Sent and
// Woke are independent observations: each may be true without the other, and a
// later call merges them with OR so observing a dispatch then a start, or only
// a start, converges on one honest row. It writes through the underlying DB,
// not through Write, so recording wake facts does not advance the persist seq.
func (p *ObservedPersist) RecordWake(ctx context.Context, obs WakeObservation) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if obs.ObligationID == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: wake observation requires an obligation id")
	}
	sent, woke := 0, 0
	if obs.Sent {
		sent = 1
	}
	if obs.Woke {
		woke = 1
	}
	err := p.db.rawWrite(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createWakeObservationsSQL); err != nil {
			return fmt.Errorf("store: wake observations: create table: %w", err)
		}
		_, err := tx.Exec(`
			INSERT INTO wake_observations (obligation_id, sent, woke)
			VALUES (?, ?, ?)
			ON CONFLICT(obligation_id) DO UPDATE SET
			  sent = wake_observations.sent OR excluded.sent,
			  woke = wake_observations.woke OR excluded.woke`, obs.ObligationID, sent, woke)
		return err
	})
	if err != nil {
		return domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	return nil
}

// WakeObservations reads all recorded wake facts (ADR 0032 p2). A store that
// never recorded a wake reports an empty list and does not create the table.
func (d *DB) WakeObservations() ([]WakeObservation, error) {
	ok, err := tableExists(context.Background(), d.readDB, "wake_observations")
	if err != nil || !ok {
		return []WakeObservation{}, err
	}
	rows, err := d.readDB.Query(`
		SELECT obligation_id, sent, woke FROM wake_observations ORDER BY obligation_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []WakeObservation{}
	for rows.Next() {
		var (
			obs        WakeObservation
			sent, woke int
		)
		if err := rows.Scan(&obs.ObligationID, &sent, &woke); err != nil {
			return nil, err
		}
		obs.Sent = sent != 0
		obs.Woke = woke != 0
		out = append(out, obs)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// WakeObservations is the same read method on the wrapped writer, for callers
// that hold only the ObservedPersist.
func (p *ObservedPersist) WakeObservations() ([]WakeObservation, error) {
	return p.db.WakeObservations()
}

// PersistState reads persist{seq,lastError} without writing. A store that has
// never had an observed write reports a zero state.
func (d *DB) PersistState() (PersistState, error) {
	ok, err := tableExists(context.Background(), d.readDB, "persist_progress")
	if err != nil || !ok {
		return PersistState{}, err
	}
	var (
		state       PersistState
		failedSince int64
	)
	err = d.readDB.QueryRow(`
		SELECT seq, last_error, failed_since FROM persist_progress WHERE id = 1`,
	).Scan(&state.Seq, &state.LastError, &failedSince)
	if errors.Is(err, sql.ErrNoRows) {
		return PersistState{}, nil
	}
	if err != nil {
		return PersistState{}, err
	}
	if failedSince > 0 {
		state.FailedSince = time.Unix(0, failedSince).UTC()
	}
	return state, nil
}

func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name,
	).Scan(&n)
	return n == 1, err
}

// JournalEvent is one well-formed row of the event journal.
type JournalEvent struct {
	Seq           int64           `json:"seq"`
	WatchID       string          `json:"watchID"`
	SchemaVersion int             `json:"schemaVersion"`
	Payload       json.RawMessage `json:"payload"`
	CreatedAt     int64           `json:"createdAt"`
}

// LoadEvents reads the journal in seq order. Malformed rows are returned as
// LoadError and skipped; one damaged row never stops the load or the start.
func (d *DB) LoadEvents() ([]JournalEvent, []LoadError, error) {
	rows, err := d.readDB.Query(`
		SELECT seq, watch_id, schema_version, payload, created_at
		FROM events ORDER BY seq`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var (
		events     []JournalEvent
		loadErrors []LoadError
	)
	for rows.Next() {
		var (
			seq           int64
			watchID       string
			schemaVersion int
			payload       []byte
			createdAt     int64
		)
		if err := rows.Scan(&seq, &watchID, &schemaVersion, &payload, &createdAt); err != nil {
			return nil, nil, err
		}
		var probe any
		if err := json.Unmarshal(payload, &probe); err != nil {
			loadErrors = append(loadErrors, LoadError{Line: seq, Reason: err.Error()})
			continue
		}
		events = append(events, JournalEvent{
			Seq:           seq,
			WatchID:       watchID,
			SchemaVersion: schemaVersion,
			Payload:       json.RawMessage(payload),
			CreatedAt:     createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return events, loadErrors, nil
}

// PersistStats is the read method for beacon_stats. It forms persist and
// loadErrors and never writes: no read path touches persist_progress or
// events.
func (d *DB) PersistStats() (PersistState, []LoadError, error) {
	state, err := d.PersistState()
	if err != nil {
		return PersistState{}, nil, err
	}
	_, loadErrors, err := d.LoadEvents()
	if err != nil {
		return PersistState{}, nil, err
	}
	return state, loadErrors, nil
}

// PersistStats is the same read method on the wrapped writer, for callers that
// hold only the ObservedPersist.
func (p *ObservedPersist) PersistStats() (PersistState, []LoadError, error) {
	return p.db.PersistStats()
}

// Shared gate (ADR 0037 p3, T3.49).
//
// Fifteen agents waiting on one CI must not produce fifteen independent
// timers against it. The phantom timeouts that follow look like a code defect
// even though the code is honest. SharedGate is the single point where units
// that declare the same expensive command meet. The first unit to arrive runs
// the wait; the others join it and receive the same result instead of starting
// their own. When a second distinct unit arrives on a key, the gate prints a
// WARNING so an operator can see the shared gate.
//
// Unit here means one active caller, not one session name: the key is the
// command being awaited, so renaming a session does not create a new gate.

// SharedGateWait is the expensive operation run once per active wait round, for
// example one CI check. Callers re-read the state they care about after Wait
// returns; the gate only guarantees that the wait happened once.
type SharedGateWait func(ctx context.Context) error

// sharedGateCall is one in-flight round for a key. done is closed by the leader
// after err is set, which publishes err to every joined caller.
type sharedGateCall struct {
	done    chan struct{}
	err     error
	waiters int
}

// SharedGate coalesces concurrent waits on the same key into one wait and
// warns when a key turns out to be shared. It is safe for concurrent use.
type SharedGate struct {
	mu    sync.Mutex
	calls map[string]*sharedGateCall
	warnf func(string, ...any)
}

// NewSharedGate returns an empty gate whose warnings go to the standard log.
func NewSharedGate() *SharedGate {
	return &SharedGate{
		calls: make(map[string]*sharedGateCall),
		warnf: log.Printf,
	}
}

// SetWarningSink replaces the WARNING sink. A nil sink restores log.Printf.
// It exists so tests can capture warnings without touching the global logger.
func (g *SharedGate) SetWarningSink(f func(string, ...any)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if f == nil {
		f = log.Printf
	}
	g.warnf = f
}

// Wait runs fn once for key and shares the outcome with every caller that
// arrives while that round is active. The first caller leads the round; later
// callers join it and block on the same done channel rather than starting a
// second wait. A caller that is cancelled or times out leaves with ctx.Err()
// and does not disturb the round for the others.
//
// The first moment a key has more than one active caller is reported through
// the warning sink, once per round.
func (g *SharedGate) Wait(ctx context.Context, key string, fn SharedGateWait) error {
	if ctx == nil {
		ctx = context.Background()
	}

	g.mu.Lock()
	if call, ok := g.calls[key]; ok {
		call.waiters++
		waiters := call.waiters
		warnf := g.warnf
		done := call.done
		g.mu.Unlock()

		if waiters == 2 {
			warnf("WARNING: shared gate %q: multiple active units wait on the same expensive command; serialized into one wait", key)
		}
		select {
		case <-done:
			return call.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	call := &sharedGateCall{done: make(chan struct{}), waiters: 1}
	g.calls[key] = call
	g.mu.Unlock()

	err := fn(ctx)

	g.mu.Lock()
	call.err = err
	delete(g.calls, key)
	close(call.done)
	g.mu.Unlock()
	return err
}

// ActiveWaiters reports how many callers are currently waiting on key. It
// makes the shared gate observable: two or more means the key is shared and
// its wait is being serialized (ADR 0037 p3).
func (g *SharedGate) ActiveWaiters(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if call, ok := g.calls[key]; ok {
		return call.waiters
	}
	return 0
}

// Shared CI wait (ADR 0037 p3, T3.73).
//
// SharedGate on its own is only a mechanism. The process-wide gate below is
// the point a wait path calls. It is package level, not per caller, so units
// in one daemon coalesce even when they hold different store handles.

var (
	sharedCIWaitOnce sync.Once
	sharedCIWaitGate *SharedGate
)

// sharedCIWait returns the process-wide gate for expensive CI waits.
func sharedCIWait() *SharedGate {
	sharedCIWaitOnce.Do(func() { sharedCIWaitGate = NewSharedGate() })
	return sharedCIWaitGate
}

// SharedCIWait runs fn once for the active wait round on key. Callers that
// arrive while the round is active join it and receive the same result instead
// of starting a second wait on the same CI. A second distinct unit on the key
// makes the gate print its WARNING (ADR 0037 p3).
//
// Callers re-read the state they care about after the wait returns; the gate
// only guarantees that the wait happened once. A nil ctx becomes Background.
func SharedCIWait(ctx context.Context, key string, fn SharedGateWait) error {
	return sharedCIWait().Wait(ctx, key, fn)
}
