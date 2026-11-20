// Package store is the SQLite implementation of domain.Store.
//
// One database file, driver modernc.org/sqlite (pure Go, CGO_ENABLED=0), WAL
// and a single writer. Read traffic uses a separate pool so readers do not
// wait for the writer. The domain port lives in internal/domain; this package
// only implements it and knows nothing about OpenCode or the plugin.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	_ "modernc.org/sqlite"
)

const (
	driverName     = "sqlite"
	writeQueueSize = 128

	// metaSchemaVersion mirrors user_version for readers that prefer a row.
	metaSchemaVersion = "schema_version"
)

var (
	errClosed = errors.New("store: closed")

	// The glob picks up every migration file, including 0006_control_plane.sql.
	// migrate applies each version once and skips versions already present.
	//go:embed migrations/*.sql
	migrationFS embed.FS
)

// DB implements domain.Store on one SQLite file. Every mutating call is
// serialized through one writer goroutine that owns one connection.
type DB struct {
	writeDB   *sql.DB
	readDB    *sql.DB
	writeConn *sql.Conn
	writeCh   chan writeJob
	closing   chan struct{}
	done      chan struct{}

	mu        sync.RWMutex
	closed    bool
	closeOnce sync.Once

	version int

	// progress is non-nil only on a DB opened with OpenProduction. It makes
	// every call to Write go through ObservedPersist, so each committed write
	// advances persist_progress.seq and each failure records last_error.
	// Plain Open leaves it nil to keep the existing test fixtures' writer
	// behavior unchanged.
	progress *ObservedPersist
}

type writeJob struct {
	ctx context.Context
	fn  func(*sql.Tx) error
	err chan error
}

var _ domain.Store = (*DB)(nil)

// Open opens path, enables WAL, applies pending migrations and starts the
// single writer. PRAGMAs are set in the DSN so modernc applies them to every
// new connection; busy_timeout comes first (report G §3). Writes reach the
// file directly, as before; use OpenProduction for a store whose writes are
// redacted and observed.
func Open(path string) (*DB, error) {
	return open(path, driverName, false)
}

// OpenProduction opens path with the production wrappers: every bound string
// or byte-slice parameter passes through Redact before it reaches SQLite, and
// every Write goes through ObservedPersist so persist_progress.seq, last_error
// and failed_since stay honest. The redaction filter lives in the SQL driver,
// so callers cannot bypass it by writing with a different statement or by
// using a helper in another package.
func OpenProduction(path string) (*DB, error) {
	return open(path, redactingDriverName, true)
}

func open(path, drv string, production bool) (*DB, error) {
	writeDB, err := openPool(path, 1, drv)
	if err != nil {
		return nil, err
	}
	readDB, err := openPool(path, 4, drv)
	if err != nil {
		writeDB.Close()
		return nil, err
	}

	ctx := context.Background()
	mode, err := journalMode(ctx, writeDB)
	if err != nil {
		writeDB.Close()
		readDB.Close()
		return nil, fmt.Errorf("store: journal mode: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		writeDB.Close()
		readDB.Close()
		return nil, fmt.Errorf("store: journal mode %q, want wal", mode)
	}

	version, err := migrate(ctx, writeDB, migrationFS)
	if err != nil {
		writeDB.Close()
		readDB.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}

	conn, err := writeDB.Conn(ctx)
	if err != nil {
		writeDB.Close()
		readDB.Close()
		return nil, fmt.Errorf("store: writer connection: %w", err)
	}

	d := &DB{
		writeDB:   writeDB,
		readDB:    readDB,
		writeConn: conn,
		writeCh:   make(chan writeJob, writeQueueSize),
		closing:   make(chan struct{}),
		done:      make(chan struct{}),
		version:   version,
	}
	if production {
		// The wrapped writer is the raw one, so recording progress does not
		// re-enter the observation layer and bump seq twice.
		d.progress = NewObservedPersistWithWriter(d, writeFunc(d.rawWrite))
	}
	go d.writeLoop()
	return d, nil
}

func openPool(path string, maxConns int, drv string) (*sql.DB, error) {
	db, err := sql.Open(drv, dsn(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxConns)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func dsn(path string) string {
	return "file:" + path +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
}

// Version returns the schema version the store was opened with.
func (d *DB) Version() int { return d.version }

// JournalMode returns the live journal mode. Used by tests and diagnostics.
func (d *DB) JournalMode(ctx context.Context) (string, error) {
	return journalMode(ctx, d.readDB)
}

func journalMode(ctx context.Context, db *sql.DB) (string, error) {
	var mode string
	err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode)
	return mode, err
}

// Close drains pending writes and releases the pools. Repeated calls are safe.
func (d *DB) Close() error {
	var err error
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		d.mu.Unlock()
		close(d.closing)
		<-d.done
		err = errors.Join(
			d.writeConn.Close(),
			d.readDB.Close(),
			d.writeDB.Close(),
		)
	})
	return err
}

// Write runs fn in one transaction on the single writer. On a production DB
// the call goes through ObservedPersist: a committed write advances
// persist_progress.seq, a failed one records last_error and failed_since. On a
// plain DB the write goes straight to the file.
func (d *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	if d.progress != nil {
		return d.progress.Write(ctx, fn)
	}
	return d.rawWrite(ctx, fn)
}

// rawWrite is the unobserved single-writer path. ObservedPersist uses it for
// both the wrapped data write and its own progress rows, which keeps the
// progress bookkeeping out of the seq it counts.
func (d *DB) rawWrite(ctx context.Context, fn func(*sql.Tx) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.RLock()
	closed := d.closed
	d.mu.RUnlock()
	if closed {
		return errClosed
	}

	job := writeJob{ctx: ctx, fn: fn, err: make(chan error, 1)}
	select {
	case d.writeCh <- job:
	case <-ctx.Done():
		return ctx.Err()
	case <-d.closing:
		return errClosed
	}
	select {
	case err := <-job.err:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-d.closing:
		return errClosed
	}
}

func (d *DB) writeLoop() {
	defer close(d.done)
	for {
		select {
		case job := <-d.writeCh:
			d.runJob(job)
		case <-d.closing:
			for {
				select {
				case job := <-d.writeCh:
					d.runJob(job)
				default:
					return
				}
			}
		}
	}
}

func (d *DB) runJob(job writeJob) {
	tx, err := d.writeConn.BeginTx(job.ctx, nil)
	if err != nil {
		job.err <- err
		return
	}
	defer tx.Rollback()
	if err := job.fn(tx); err != nil {
		job.err <- err
		return
	}
	job.err <- tx.Commit()
}

// migrate applies each migrations/NNNN_name.sql once. Schema version and SQL
// body commit in the same transaction, so a failed migration never advances
// user_version. fsys is an argument to let tests drive a fixed corpus.
func migrate(ctx context.Context, db *sql.DB, fsys fs.FS) (int, error) {
	names, err := fs.Glob(fsys, "migrations/*.sql")
	if err != nil {
		return 0, err
	}
	sort.Strings(names)

	current, err := userVersion(ctx, db)
	if err != nil {
		return 0, err
	}
	for _, name := range names {
		version, err := migrationVersion(name)
		if err != nil {
			return current, err
		}
		if version <= current {
			continue
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return current, err
		}
		if err := applyMigration(ctx, db, string(body), version); err != nil {
			return current, fmt.Errorf("migration %s: %w", name, err)
		}
		current = version
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO meta(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		metaSchemaVersion, current)
	if err != nil {
		return current, fmt.Errorf("store: write schema version: %w", err)
	}
	return current, nil
}

func userVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("store: read user_version: %w", err)
	}
	return version, nil
}

func applyMigration(ctx context.Context, db *sql.DB, body string, version int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	// schema_version belongs to SQLite; only user_version may be set here.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return err
	}
	return tx.Commit()
}

func migrationVersion(name string) (int, error) {
	base := strings.TrimSuffix(path.Base(name), ".sql")
	prefix, _, ok := strings.Cut(base, "_")
	if !ok {
		return 0, fmt.Errorf("migration %q: want NNNN_name.sql", name)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("migration %q: bad version %q", name, prefix)
	}
	return version, nil
}

// SaveWatch upserts a Watch. All writes go through the single writer.
func (d *DB) SaveWatch(w domain.Watch) error {
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		return saveWatchTx(tx, w)
	})
}

// Watch loads one Watch by id.
func (d *DB) Watch(id string) (domain.Watch, error) {
	var payload []byte
	err := d.readDB.QueryRow(`SELECT payload FROM watches WHERE id = ?`, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Watch{}, domain.NewAppError(domain.CodeWatchNotFound, "watch "+id+" not found")
	}
	if err != nil {
		return domain.Watch{}, err
	}
	return decodeWatch(payload)
}

// ActiveWatches returns watches in watching or notifying status. Blocked
// watches are excluded here to keep the existing port contract unchanged;
// control-plane callers that need them use LiveWatches.
func (d *DB) ActiveWatches() ([]domain.Watch, error) {
	return d.queryWatches(`
		SELECT payload FROM watches WHERE status IN (?, ?) ORDER BY id`,
		string(domain.StatusWatching), string(domain.StatusNotifying))
}

// LiveWatches returns every Watch still in the control loop: watching,
// notifying and blocked. Rebind and other control-plane operations must see
// blocked watches, which ActiveWatches intentionally omits.
func (d *DB) LiveWatches() ([]domain.Watch, error) {
	return d.queryWatches(`
		SELECT payload FROM watches WHERE status IN (?, ?, ?) ORDER BY id`,
		string(domain.StatusWatching), string(domain.StatusNotifying),
		string(domain.StatusBlocked))
}

// AllWatches returns every Watch, including closed ones.
func (d *DB) AllWatches() ([]domain.Watch, error) {
	return d.queryWatches(`SELECT payload FROM watches ORDER BY id`)
}

func (d *DB) queryWatches(query string, args ...any) ([]domain.Watch, error) {
	rows, err := d.readDB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var watches []domain.Watch
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		w, err := decodeWatch(payload)
		if err != nil {
			return nil, err
		}
		watches = append(watches, w)
	}
	return watches, rows.Err()
}

func decodeWatch(payload []byte) (domain.Watch, error) {
	var w domain.Watch
	if err := json.Unmarshal(payload, &w); err != nil {
		return domain.Watch{}, err
	}
	return w, nil
}

// SaveObligation inserts an obligation. A repeated (watch_id, revision,
// subscriber_id) triple is a no-op: that is the idempotency key from ADR 0015.
func (d *DB) SaveObligation(o domain.Obligation) error {
	payload, err := json.Marshal(o)
	if err != nil {
		return err
	}
	delivered := 0
	if !o.DeliveredAt.IsZero() {
		delivered = 1
	}
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO obligations
			  (id, watch_id, revision, subscriber_id, verdict_pointer, delivered_at, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			o.ID, o.WatchID, o.Revision, o.SubscriberID, string(o.VerdictPointer), delivered, payload)
		return err
	})
}

// Obligation loads one obligation by its derived id.
func (d *DB) Obligation(id string) (domain.Obligation, error) {
	var payload []byte
	err := d.readDB.QueryRow(`SELECT payload FROM obligations WHERE id = ?`, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Obligation{}, domain.NewAppError(domain.CodeWatchNotFound, "obligation "+id+" not found")
	}
	if err != nil {
		return domain.Obligation{}, err
	}
	var o domain.Obligation
	if err := json.Unmarshal(payload, &o); err != nil {
		return domain.Obligation{}, err
	}
	return o, nil
}

// PendingObligations returns obligations that have no deliveredAt timestamp.
func (d *DB) PendingObligations() ([]domain.Obligation, error) {
	rows, err := d.readDB.Query(`SELECT payload FROM obligations WHERE delivered_at = 0 ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var obligations []domain.Obligation
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var o domain.Obligation
		if err := json.Unmarshal(payload, &o); err != nil {
			return nil, err
		}
		obligations = append(obligations, o)
	}
	return obligations, rows.Err()
}

// createTaskStartsSQL owns the idempotency_key index for task starts. The key
// and the started task's state are written in one transaction (ADR 0015), so a
// repeated start with the same key cannot create a second task. As in
// receipts.go and observability.go, the table is created on first use.
const createTaskStartsSQL = `
CREATE TABLE IF NOT EXISTS task_starts (
  key        TEXT PRIMARY KEY,
  task_id    TEXT NOT NULL,
  payload    BLOB NOT NULL,
  created_at INTEGER NOT NULL
);
`

// StartTaskResult is the outcome of an idempotent task start.
type StartTaskResult struct {
	TaskID   string
	Payload  []byte
	Created  bool // first start with this key wrote the task state
	Conflict bool // key was already used: RejectDuplicate (ADR 0031 §5)
}

// StartTask records an idempotency_key and the started task in one transaction.
// The key is namespaced with domain.StartNamespace so it is never confused with
// a clientRequestId from ADR 0028. A repeat with the same key inserts no second
// task: the stored task is returned, the conflict is recorded in receipts as
// RejectDuplicate, and both commits happen together with the key lookup.
func (d *DB) StartTask(key, taskID string, payload []byte) (StartTaskResult, error) {
	if strings.TrimSpace(key) == "" {
		return StartTaskResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: empty idempotency key")
	}
	if strings.TrimSpace(taskID) == "" {
		return StartTaskResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: empty task id")
	}
	namespaced := domain.IdempotencyKey(domain.StartNamespace, key)
	var result StartTaskResult
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createTaskStartsSQL); err != nil {
			return fmt.Errorf("store: start task: create table: %w", err)
		}
		now := time.Now().UTC().UnixNano()
		res, err := tx.Exec(`
			INSERT OR IGNORE INTO task_starts (key, task_id, payload, created_at)
			VALUES (?, ?, ?, ?)`, namespaced, taskID, payload, now)
		if err != nil {
			return fmt.Errorf("store: start task: insert: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: start task: rows affected: %w", err)
		}
		if n == 1 {
			result = StartTaskResult{TaskID: taskID, Payload: append([]byte(nil), payload...), Created: true}
			return nil
		}
		if err := tx.QueryRow(`SELECT task_id, payload FROM task_starts WHERE key = ?`, namespaced).Scan(&result.TaskID, &result.Payload); err != nil {
			return fmt.Errorf("store: start task: load existing: %w", err)
		}
		result.Conflict = true
		return recordRejectDuplicate(tx, namespaced, result.TaskID)
	})
	if err != nil {
		return StartTaskResult{}, err
	}
	return result, nil
}

// TaskStartCount returns the number of stored task starts. A repeated key must
// not increase it.
func (d *DB) TaskStartCount() (int, error) {
	ok, err := tableExists(context.Background(), d.readDB, "task_starts")
	if err != nil || !ok {
		return 0, err
	}
	var n int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM task_starts`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// createTaskJournalSQL owns the append-only task journal from ADR 0036 §3. As
// with task_starts, the table is created on first use. The two triggers make
// append-only a database rule rather than a convention: no caller, not even a
// raw UPDATE or DELETE through Write, can change or remove a committed row. An
// ack and a new event can only add records, so replaying the journal always
// yields the same bytes (ADR 0012, ADR 0015).
var createTaskJournalSQL = []string{
	`CREATE TABLE IF NOT EXISTS task_journal (
	   seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	   task_id    TEXT NOT NULL,
	   kind       TEXT NOT NULL,
	   revision   INTEGER NOT NULL DEFAULT 0,
	   payload    BLOB NOT NULL,
	   created_at INTEGER NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS task_journal_by_task ON task_journal(task_id, seq)`,
	`CREATE TRIGGER IF NOT EXISTS task_journal_no_update
	 BEFORE UPDATE ON task_journal
	 BEGIN SELECT RAISE(ABORT, 'store: task_journal is append-only'); END`,
	`CREATE TRIGGER IF NOT EXISTS task_journal_no_delete
	 BEFORE DELETE ON task_journal
	 BEGIN SELECT RAISE(ABORT, 'store: task_journal is append-only'); END`,
}

// journalKindAck is the record kind written by AppendAck. It is a journal entry
// like any other; an ack never edits the record it acknowledges.
const journalKindAck = "ack"

// JournalRecord is one committed row of the append-only task journal. Seq is
// assigned by SQLite and never reused. Payload is the exact redacted bytes
// stored at append time, so a reader can verify the row did not change.
type JournalRecord struct {
	Seq       int64
	TaskID    string
	Kind      string
	Revision  int64
	Payload   []byte
	CreatedAt int64
}

// JournalStatus is the current status of a task, served separately from the
// full history as required by ADR 0025. Cursor is the seq of the newest record
// and doubles as the opaque Last-Event-ID for a subscriber that reconnects.
type JournalStatus struct {
	Status   string
	Cursor   int64
	Revision int64
}

// appendJournal inserts one row and returns its seq. This is the only write
// path for the journal: it issues a single INSERT, never reads or rewrites an
// existing row, and redacts the payload (ADR 0012) before storing it.
func (d *DB) appendJournal(ctx context.Context, taskID, kind string, revision int64, payload []byte) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(taskID) == "" {
		return 0, domain.NewAppError(domain.CodeInvalidInput, "store: empty journal task id")
	}
	if strings.TrimSpace(kind) == "" {
		return 0, domain.NewAppError(domain.CodeInvalidInput, "store: empty journal kind")
	}
	clean := Redact(payload)
	var seq int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		for _, stmt := range createTaskJournalSQL {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("store: journal: create table: %w", err)
			}
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO task_journal (task_id, kind, revision, payload, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			taskID, kind, revision, clean, time.Now().UTC().UnixNano())
		if err != nil {
			return fmt.Errorf("store: journal: insert: %w", err)
		}
		seq, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("store: journal: last insert id: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// AppendEvent appends one event record to the task journal and returns its
// seq. It never rewrites an earlier record (ADR 0036 §3).
func (d *DB) AppendEvent(ctx context.Context, taskID, kind, message string) (int64, error) {
	payload, err := json.Marshal(struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}{Kind: kind, Message: message})
	if err != nil {
		return 0, err
	}
	return d.appendJournal(ctx, taskID, kind, 0, payload)
}

// AppendAck appends an acknowledgement record to the task journal and returns
// its seq. It records the revision the ack was given on but does not update the
// acknowledged row: earlier event and ack rows stay byte-for-byte unchanged.
func (d *DB) AppendAck(ctx context.Context, taskID string, revision int64) (int64, error) {
	payload, err := json.Marshal(struct {
		Kind     string `json:"kind"`
		Revision int64  `json:"revision"`
	}{Kind: journalKindAck, Revision: revision})
	if err != nil {
		return 0, err
	}
	return d.appendJournal(ctx, taskID, journalKindAck, revision, payload)
}

// JournalRecords returns every committed journal row for taskID, oldest first.
// Payload is the exact stored bytes, so a caller can compare rows before and
// after later appends to prove the old ones were not rewritten.
func (d *DB) JournalRecords(ctx context.Context, taskID string) ([]JournalRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ok, err := tableExists(ctx, d.readDB, "task_journal")
	if err != nil || !ok {
		return nil, err
	}
	rows, err := d.readDB.QueryContext(ctx, `
		SELECT seq, task_id, kind, revision, payload, created_at
		FROM task_journal WHERE task_id = ? ORDER BY seq`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JournalRecord
	for rows.Next() {
		var r JournalRecord
		if err := rows.Scan(&r.Seq, &r.TaskID, &r.Kind, &r.Revision, &r.Payload, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// inboxStatePending is a stored message that is available for delivery.
const (
	inboxStatePending  = "pending"
	inboxStateInflight = "inflight"
)

// createInboxSQL owns the durable inbox from ADR 0039 §6. Like task_starts and
// task_journal, the table is created on first use: the card owns only db.go and
// db_test.go, so no migration file is added for it.
var createInboxSQL = []string{
	`CREATE TABLE IF NOT EXISTS inbox_messages (
	   key           TEXT PRIMARY KEY,
	   watch_id      TEXT NOT NULL,
	   revision      INTEGER NOT NULL,
	   subscriber_id TEXT NOT NULL,
	   peer_name     TEXT NOT NULL,
	   payload       BLOB NOT NULL,
	   state         TEXT NOT NULL,
	   lease_until   INTEGER NOT NULL DEFAULT 0,
	   created_at    INTEGER NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS inbox_by_peer_state ON inbox_messages(peer_name, state)`,
}

// InboxMessage is one durable message for a called session's inbox. Its
// delivery key is derived from watchID, revision and subscriberID (ADR 0039 §6),
// so a redelivery of the same logical message cannot create a second row.
// State and LeaseUntil are populated by read and claim methods; writers ignore
// them.
type InboxMessage struct {
	WatchID      string
	Revision     int64
	SubscriberID string
	PeerName     string
	Payload      []byte
	State        string
	LeaseUntil   int64
}

// InboxKey builds the delivery key for a message: watchID+revision+subscriberID.
// It is the receiver's deduplication key from ADR 0039 §6, not a sessionID or an
// event.id.
func InboxKey(watchID string, revision int64, subscriberID string) string {
	return watchID + ":" + strconv.FormatInt(revision, 10) + ":" + subscriberID
}

func ensureInbox(tx *sql.Tx) error {
	for _, stmt := range createInboxSQL {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("store: inbox: create table: %w", err)
		}
	}
	return nil
}

// InboxPut stores a message until it is acknowledged. A repeat with the same
// delivery key inserts nothing (INSERT OR IGNORE), so at-least-once redelivery
// does not duplicate held messages. Peer payloads are untrusted input (T3.4),
// so the payload passes through Redact before it is written.
func (d *DB) InboxPut(m InboxMessage) error {
	if strings.TrimSpace(m.WatchID) == "" || strings.TrimSpace(m.SubscriberID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: inbox: empty watch or subscriber id")
	}
	if strings.TrimSpace(m.PeerName) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: inbox: empty peer name")
	}
	clean := Redact(m.Payload)
	key := InboxKey(m.WatchID, m.Revision, m.SubscriberID)
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureInbox(tx); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO inbox_messages
			  (key, watch_id, revision, subscriber_id, peer_name, payload, state, lease_until, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`,
			key, m.WatchID, m.Revision, m.SubscriberID, m.PeerName, clean,
			inboxStatePending, time.Now().UTC().UnixNano())
		return err
	})
}

// reclaimInbox frees messages a dead leaseholder cannot deliver: every
// inflight row held by peerName (a restart with the same PEER_NAME is the same
// owner coming back) and every inflight row whose lease has expired. It runs
// inside the caller's Write transaction, so reclaiming and claiming cannot be
// split by a crash. Returns the number of rows reset.
func reclaimInbox(tx *sql.Tx, peerName string, now time.Time) (int64, error) {
	res, err := tx.Exec(`
		UPDATE inbox_messages
		SET state = ?, lease_until = 0
		WHERE state = ? AND (peer_name = ? OR lease_until <= ?)`,
		inboxStatePending, inboxStateInflight, peerName, now.UnixNano())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// InboxReclaim is the explicit restart hook from ADR 0039 §6. With the same
// PEER_NAME it returns held messages to the live process and clears stale
// leases left by dead holders. It shares the predicate used by InboxClaim.
func (d *DB) InboxReclaim(peerName string, now time.Time) (int, error) {
	if strings.TrimSpace(peerName) == "" {
		return 0, domain.NewAppError(domain.CodeInvalidInput, "store: inbox: empty peer name")
	}
	var n int64
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureInbox(tx); err != nil {
			return err
		}
		var err error
		n, err = reclaimInbox(tx, peerName, now)
		return err
	})
	return int(n), err
}

// InboxClaim reclaims stale leases for peerName and hands the available
// messages to it with a fresh lease, all in one transaction. A message is not
// deleted on claim: it stays in the store until InboxAck, so delivery is
// at-least-once and a restart can return the held message.
func (d *DB) InboxClaim(peerName string, now time.Time, ttl time.Duration) ([]InboxMessage, error) {
	if strings.TrimSpace(peerName) == "" {
		return nil, domain.NewAppError(domain.CodeInvalidInput, "store: inbox: empty peer name")
	}
	leaseUntil := now.Add(ttl).UnixNano()
	var claimed []InboxMessage
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureInbox(tx); err != nil {
			return err
		}
		if _, err := reclaimInbox(tx, peerName, now); err != nil {
			return err
		}
		rows, err := tx.Query(`
			SELECT key, watch_id, revision, subscriber_id, peer_name, payload
			FROM inbox_messages
			WHERE peer_name = ? AND state = ? ORDER BY created_at, key`,
			peerName, inboxStatePending)
		if err != nil {
			return err
		}
		var out []InboxMessage
		for rows.Next() {
			var m InboxMessage
			var key string
			if err := rows.Scan(&key, &m.WatchID, &m.Revision, &m.SubscriberID, &m.PeerName, &m.Payload); err != nil {
				rows.Close()
				return err
			}
			out = append(out, m)
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			return rowsErr
		}
		for _, m := range out {
			if _, err := tx.Exec(`
				UPDATE inbox_messages SET state = ?, lease_until = ? WHERE key = ?`,
				inboxStateInflight, leaseUntil,
				InboxKey(m.WatchID, m.Revision, m.SubscriberID)); err != nil {
				return err
			}
			m.State = inboxStateInflight
			m.LeaseUntil = leaseUntil
			claimed = append(claimed, m)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// InboxAck acknowledges a message by its delivery key and removes it from the
// store. A second ack of the same key is not an error; it reports false because
// the row is already gone.
func (d *DB) InboxAck(key string) (bool, error) {
	var deleted bool
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureInbox(tx); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM inbox_messages WHERE key = ?`, key)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		deleted = n > 0
		return err
	})
	return deleted, err
}

// InboxMessages returns every stored message for peerName with its current
// state and lease deadline, oldest first. Used by tests and diagnostics.
func (d *DB) InboxMessages(peerName string) ([]InboxMessage, error) {
	ok, err := tableExists(context.Background(), d.readDB, "inbox_messages")
	if err != nil || !ok {
		return nil, err
	}
	rows, err := d.readDB.Query(`
		SELECT watch_id, revision, subscriber_id, peer_name, payload, state, lease_until
		FROM inbox_messages WHERE peer_name = ? ORDER BY created_at, key`, peerName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InboxMessage
	for rows.Next() {
		var m InboxMessage
		if err := rows.Scan(&m.WatchID, &m.Revision, &m.SubscriberID, &m.PeerName,
			&m.Payload, &m.State, &m.LeaseUntil); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// JournalStatus returns the current status of taskID without replaying or
// rewriting the journal (ADR 0025). Status and Cursor come from the newest
// record only; an unknown task yields a zero JournalStatus.
func (d *DB) JournalStatus(ctx context.Context, taskID string) (JournalStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ok, err := tableExists(ctx, d.readDB, "task_journal")
	if err != nil || !ok {
		return JournalStatus{}, err
	}
	var s JournalStatus
	err = d.readDB.QueryRowContext(ctx, `
		SELECT kind, seq, revision FROM task_journal
		WHERE task_id = ? ORDER BY seq DESC LIMIT 1`, taskID,
	).Scan(&s.Status, &s.Cursor, &s.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return JournalStatus{}, nil
	}
	return s, err
}

// createWatchOutcomesSQL owns the worker-outcome table from ADR 0041 §4. As
// with task_starts and the inbox, the table is created on first use because
// T3.79 owns only db.go and adds no migration file. One row per worker key is
// enough: the fact of completion and its result live in the file, so every
// process that holds the file open can read them on its own.
const createWatchOutcomesSQL = `
CREATE TABLE IF NOT EXISTS watch_outcomes (
  key        TEXT PRIMARY KEY,
  revision   INTEGER NOT NULL,
  payload    BLOB NOT NULL,
  created_at INTEGER NOT NULL
);
`

// outcomePollInterval is the read cadence of a WatchOutcome watcher. sqliteq
// polls the file about every 50 ms (ADR 0041 §4); the same cadence keeps
// fan-out local to the file, without a broker.
const outcomePollInterval = 50 * time.Millisecond

// WatchOutcome is one completed worker result written to the shared state
// file. Key is the worker key that selects which watchers are woken; Result is
// the redacted result payload.
type WatchOutcome struct {
	Key       string
	Revision  int64
	Result    []byte
	CreatedAt int64
}

// PublishOutcome writes one worker result into the state file. All writes go
// through the single writer and commit in WAL, so every other process with the
// file open sees the committed row on its next read (ADR 0041 §4).
func (d *DB) PublishOutcome(o WatchOutcome) error {
	if strings.TrimSpace(o.Key) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: outcome: empty worker key")
	}
	clean := Redact(o.Result)
	at := o.CreatedAt
	if at == 0 {
		at = time.Now().UTC().UnixNano()
	}
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createWatchOutcomesSQL); err != nil {
			return fmt.Errorf("store: outcome: create table: %w", err)
		}
		_, err := tx.Exec(`
			INSERT INTO watch_outcomes (key, revision, payload, created_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET
			  revision   = excluded.revision,
			  payload    = excluded.payload,
			  created_at = excluded.created_at`,
			o.Key, o.Revision, clean, at)
		return err
	})
}

// WatchOutcome returns a channel that receives the result published for key
// by any process writing the same state file. Each call watches through this
// DB's own read pool: the poll after the writer's commit sees the row, with no
// broker or cross-process channel. The channel yields one value and closes;
// it also closes on ctx cancellation or on Close.
func (d *DB) WatchOutcome(ctx context.Context, key string) (<-chan WatchOutcome, error) {
	if strings.TrimSpace(key) == "" {
		return nil, domain.NewAppError(domain.CodeInvalidInput, "store: outcome: empty worker key")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.RLock()
	closed := d.closed
	d.mu.RUnlock()
	if closed {
		return nil, errClosed
	}

	out := make(chan WatchOutcome, 1)
	go d.watchOutcome(ctx, key, out)
	return out, nil
}

func (d *DB) watchOutcome(ctx context.Context, key string, out chan<- WatchOutcome) {
	defer close(out)
	ticker := time.NewTicker(outcomePollInterval)
	defer ticker.Stop()
	for {
		o, found, err := readWatchOutcome(ctx, d.readDB, key)
		if err != nil {
			return
		}
		if found {
			select {
			case out <- o:
			case <-ctx.Done():
			case <-d.closing:
			}
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		case <-d.closing:
			return
		}
	}
}

func readWatchOutcome(ctx context.Context, db *sql.DB, key string) (WatchOutcome, bool, error) {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, "watch_outcomes",
	).Scan(&n); err != nil {
		return WatchOutcome{}, false, err
	}
	if n == 0 {
		return WatchOutcome{}, false, nil
	}
	var o WatchOutcome
	err := db.QueryRowContext(ctx,
		`SELECT key, revision, payload, created_at FROM watch_outcomes WHERE key = ?`, key,
	).Scan(&o.Key, &o.Revision, &o.Result, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return WatchOutcome{}, false, nil
	}
	if err != nil {
		return WatchOutcome{}, false, err
	}
	return o, true, nil
}

// createReplaysSQL owns the whole-scenario replay table from ADR 0042 §6. As
// with task_starts, the inbox and watch_outcomes, the table is created on first
// use because this card owns only db.go and adds no migration file. One row per
// scenario key is enough: the committed result and the fact that the scenario
// already ran live in the file, so a repeat can return the first result without
// running the side steps again.
const createReplaysSQL = `
CREATE TABLE IF NOT EXISTS scenario_replays (
  key        TEXT PRIMARY KEY,
  result     BLOB NOT NULL,
  created_at INTEGER NOT NULL
);
`

// replayNamespace scopes a whole-scenario key. It is deliberately different
// from a clientRequestId (ADR 0028), which scopes one command, and from the
// task-start namespace (ADR 0031). The three keys never share a storage slot
// even when a caller passes the same raw string.
const replayNamespace = "scenario-replay"

// ReplayKey builds the durable key for a whole scenario. Unlike the
// clientRequestId of ADR 0028, which makes one command idempotent, this key
// makes the entire scenario idempotent, including the side steps between
// commands (ADR 0042 §6).
func ReplayKey(key string) string {
	return domain.IdempotencyKey(replayNamespace, key)
}

// ReplayStep is one side-effecting step of a scenario. Apply runs inside the
// scenario's single transaction, so a step cannot commit a second effect if a
// later step fails. Name is used only in error messages.
type ReplayStep struct {
	Name  string
	Apply func(*sql.Tx) error
}

// ReplayResult is the outcome of a whole-scenario replay. Created marks the
// first run, which wrote the key, the side steps and the result. Replayed marks
// a repeat, which wrote nothing and returned the stored result unchanged.
type ReplayResult struct {
	Result   []byte
	Created  bool
	Replayed bool
}

// Replay runs a whole scenario at most once per key. The key, every side step
// and the result commit in one transaction (ADR 0015): a failed step leaves no
// key and no partial effect, so the scenario can be retried. A repeat with the
// same key inserts no row, runs no step and returns a copy of the stored
// result, so it produces no second task, record, result or notification
// (ADR 0042 §6). This is a scenario key, not the per-command clientRequestId
// from ADR 0028.
func (d *DB) Replay(key string, steps []ReplayStep, result []byte) (ReplayResult, error) {
	if strings.TrimSpace(key) == "" {
		return ReplayResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: empty scenario replay key")
	}
	for i, step := range steps {
		if strings.TrimSpace(step.Name) == "" || step.Apply == nil {
			return ReplayResult{}, domain.NewAppError(domain.CodeInvalidInput,
				fmt.Sprintf("store: replay step %d: empty name or nil apply", i))
		}
	}

	namespaced := ReplayKey(key)
	clean := Redact(result)
	var out ReplayResult
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createReplaysSQL); err != nil {
			return fmt.Errorf("store: replay: create table: %w", err)
		}
		res, err := tx.Exec(`
			INSERT OR IGNORE INTO scenario_replays (key, result, created_at)
			VALUES (?, ?, ?)`, namespaced, clean, time.Now().UTC().UnixNano())
		if err != nil {
			return fmt.Errorf("store: replay: insert key: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: replay: rows affected: %w", err)
		}
		if n == 0 {
			// The same scenario already ran: return the first result and run
			// no step. A second effect is impossible by construction.
			var stored []byte
			if err := tx.QueryRow(`SELECT result FROM scenario_replays WHERE key = ?`, namespaced).Scan(&stored); err != nil {
				return fmt.Errorf("store: replay: load existing: %w", err)
			}
			out = ReplayResult{Result: append([]byte(nil), stored...), Replayed: true}
			return nil
		}
		for _, step := range steps {
			if err := step.Apply(tx); err != nil {
				return fmt.Errorf("store: replay: step %s: %w", step.Name, err)
			}
		}
		out = ReplayResult{Result: append([]byte(nil), clean...), Created: true}
		return nil
	})
	if err != nil {
		return ReplayResult{}, err
	}
	return out, nil
}

// ReplayCount returns the number of stored scenario replays. A repeated key
// must not increase it.
func (d *DB) ReplayCount() (int, error) {
	ok, err := tableExists(context.Background(), d.readDB, "scenario_replays")
	if err != nil || !ok {
		return 0, err
	}
	var n int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM scenario_replays`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ScenarioResult returns the stored result for key and whether it exists.
// It is the direct check that a replay returned the first result unchanged.
func (d *DB) ScenarioResult(key string) ([]byte, bool, error) {
	ok, err := tableExists(context.Background(), d.readDB, "scenario_replays")
	if err != nil || !ok {
		return nil, false, err
	}
	var stored []byte
	err = d.readDB.QueryRow(`SELECT result FROM scenario_replays WHERE key = ?`, ReplayKey(key)).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return stored, true, nil
}

// createWaitCheckpointsSQL owns the wait-progress table from ADR 0043 §1. As
// with task_starts, the inbox and the replay table, it is created on first
// use because this card owns only db.go and adds no migration file. One row
// per wait is enough: the last completed step and the task state at that step
// live in the file, so a restarted wait can resume without polling from the
// beginning.
const createWaitCheckpointsSQL = `
CREATE TABLE IF NOT EXISTS wait_checkpoints (
  key        TEXT PRIMARY KEY,
  task_id    TEXT NOT NULL,
  step       INTEGER NOT NULL,
  state      BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
`

// waitCheckpointNamespace scopes one wait's progress. It is deliberately
// different from a clientRequestId (ADR 0028), a task start (ADR 0031) and a
// scenario replay (ADR 0042 §6): a checkpoint records how far one wait got,
// not whether a command or a whole scenario already ran.
const waitCheckpointNamespace = "wait-checkpoint"

// WaitCheckpointKey builds the durable key for one wait. The same raw string
// can name a request, a task start or a scenario and still address a distinct
// checkpoint.
func WaitCheckpointKey(key string) string {
	return domain.IdempotencyKey(waitCheckpointNamespace, key)
}

// WaitCheckpoint is the recorded progress of one wait. Step is the last
// completed step and State is the redacted task state saved with it.
type WaitCheckpoint struct {
	Key       string
	TaskID    string
	Step      int
	State     []byte
	CreatedAt int64
	UpdatedAt int64
}

// WaitCheckpointResult reports what a CheckpointWait call did. Created marks
// the first checkpoint for the key. Advanced marks a step that moved forward;
// a stale step changes nothing and reports the stored checkpoint instead.
type WaitCheckpointResult struct {
	Checkpoint WaitCheckpoint
	Created    bool
	Advanced   bool
}

// CheckpointWait records the last completed step of one wait together with
// the task state in a single transaction (ADR 0015). saveState, when non-nil,
// runs inside that same transaction and is where the caller persists task
// state outside this row; if it fails, neither the checkpoint nor that state
// is committed. Progress is monotonic: a step not greater than the one
// already recorded writes nothing, so a redelivered or out-of-order callback
// cannot move the wait backwards. State is redacted before it reaches the
// file (ADR 0012). This is progress of one wait, not command deduplication
// (ADR 0028) and not the no-second-effect check of Replay (ADR 0042 §6).
func (d *DB) CheckpointWait(key, taskID string, step int, state []byte, saveState func(*sql.Tx) error) (WaitCheckpointResult, error) {
	if strings.TrimSpace(key) == "" {
		return WaitCheckpointResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: checkpoint: empty wait key")
	}
	if strings.TrimSpace(taskID) == "" {
		return WaitCheckpointResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: checkpoint: empty task id")
	}
	if step < 0 {
		return WaitCheckpointResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: checkpoint: negative step")
	}

	namespaced := WaitCheckpointKey(key)
	clean := Redact(state)
	var out WaitCheckpointResult
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createWaitCheckpointsSQL); err != nil {
			return fmt.Errorf("store: checkpoint: create table: %w", err)
		}
		var prev WaitCheckpoint
		err := tx.QueryRow(`
			SELECT key, task_id, step, state, created_at, updated_at
			FROM wait_checkpoints WHERE key = ?`, namespaced,
		).Scan(&prev.Key, &prev.TaskID, &prev.Step, &prev.State, &prev.CreatedAt, &prev.UpdatedAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: checkpoint: load previous: %w", err)
		}
		found := err == nil
		if found && step <= prev.Step {
			// The wait has already reached this step. Write neither state nor
			// checkpoint and report the stored progress to the caller.
			out = WaitCheckpointResult{Checkpoint: prev}
			return nil
		}
		if saveState != nil {
			if err := saveState(tx); err != nil {
				return fmt.Errorf("store: checkpoint: save state: %w", err)
			}
		}
		now := time.Now().UTC().UnixNano()
		createdAt := now
		if found {
			createdAt = prev.CreatedAt
			_, err = tx.Exec(`
				UPDATE wait_checkpoints
				SET task_id = ?, step = ?, state = ?, updated_at = ?
				WHERE key = ?`, taskID, step, clean, now, namespaced)
		} else {
			_, err = tx.Exec(`
				INSERT INTO wait_checkpoints
				  (key, task_id, step, state, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?)`, namespaced, taskID, step, clean, now, now)
		}
		if err != nil {
			return fmt.Errorf("store: checkpoint: write: %w", err)
		}
		out = WaitCheckpointResult{
			Checkpoint: WaitCheckpoint{
				Key: namespaced, TaskID: taskID, Step: step,
				State: append([]byte(nil), clean...), CreatedAt: createdAt, UpdatedAt: now,
			},
			Created:  !found,
			Advanced: true,
		}
		return nil
	})
	if err != nil {
		return WaitCheckpointResult{}, err
	}
	return out, nil
}

// WaitCheckpoint returns the recorded checkpoint for key and whether one
// exists. Reads use the separate read pool, so a restarted process sees the
// last committed step without waiting on the writer (ADR 0041 §4).
func (d *DB) WaitCheckpoint(key string) (WaitCheckpoint, bool, error) {
	if strings.TrimSpace(key) == "" {
		return WaitCheckpoint{}, false, domain.NewAppError(domain.CodeInvalidInput, "store: checkpoint: empty wait key")
	}
	ok, err := tableExists(context.Background(), d.readDB, "wait_checkpoints")
	if err != nil || !ok {
		return WaitCheckpoint{}, false, err
	}
	var c WaitCheckpoint
	err = d.readDB.QueryRow(`
		SELECT key, task_id, step, state, created_at, updated_at
		FROM wait_checkpoints WHERE key = ?`, WaitCheckpointKey(key),
	).Scan(&c.Key, &c.TaskID, &c.Step, &c.State, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return WaitCheckpoint{}, false, nil
	}
	if err != nil {
		return WaitCheckpoint{}, false, err
	}
	return c, true, nil
}

// ResumeWait returns the last completed step and its task state for key.
// found=false means nothing was checkpointed and the wait starts at step 0;
// otherwise the caller continues at step+1 instead of polling from the start
// (ADR 0043 §1).
func (d *DB) ResumeWait(key string) (int, []byte, bool, error) {
	c, found, err := d.WaitCheckpoint(key)
	if err != nil || !found {
		return 0, nil, found, err
	}
	return c.Step, append([]byte(nil), c.State...), true, nil
}

// WaitCheckpointCount returns the number of stored wait checkpoints. A stale
// step must not increase it.
func (d *DB) WaitCheckpointCount() (int, error) {
	ok, err := tableExists(context.Background(), d.readDB, "wait_checkpoints")
	if err != nil || !ok {
		return 0, err
	}
	var n int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM wait_checkpoints`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// createCompletionNotificationsSQL owns the exactly-once completion notice
// table from ADR 0044 feature 5. As with task_starts, the inbox, the replay and
// wait tables, it is created on first use because this card owns only db.go.
// One row per stable event ID is enough: the redacted notice payload and the
// fact that it was already emitted live in the file, so any process that
// re-delivers the event sees the first notice instead of emitting a second.
const createCompletionNotificationsSQL = `
CREATE TABLE IF NOT EXISTS completion_notifications (
  key        TEXT PRIMARY KEY,
  event_id   TEXT NOT NULL,
  task_id    TEXT NOT NULL,
  payload    BLOB NOT NULL,
  created_at INTEGER NOT NULL
);
`

// completionNotificationNamespace scopes one completion notice. It is
// deliberately distinct from a clientRequestId (ADR 0028), a task start
// (ADR 0031), a scenario replay (ADR 0042) and a wait checkpoint (ADR 0043).
// Those keys deduplicate one command, start, scenario or wait. This key
// deduplicates one "background task finished" notice (ADR 0044 feature 5).
const completionNotificationNamespace = "task-completion"

// CompletionNotificationKey builds the durable key for one completion notice
// from its stable event ID. Re-delivering the same event after a crash yields
// the same key, so the notice cannot be emitted twice.
func CompletionNotificationKey(eventID string) string {
	return domain.IdempotencyKey(completionNotificationNamespace, eventID)
}

// CompletionNotification is one stored background-task completion notice.
// EventID is the stable event identifier from the producer and Payload is the
// redacted notice bytes stored on first delivery.
type CompletionNotification struct {
	EventID   string
	TaskID    string
	Payload   []byte
	CreatedAt int64
}

// NotifyCompletionResult reports what a NotifyCompletion call did. Created
// marks the first delivery, which wrote the key and emitted the notice.
// Duplicate marks a re-delivery, which wrote nothing, emitted nothing and
// returned the first notice unchanged (ADR 0044 feature 5).
type NotifyCompletionResult struct {
	Notification CompletionNotification
	Created      bool
	Duplicate    bool
}

// NotifyCompletion emits a background-task completion notice at most once per
// stable event ID. The key, the emit side effect and the stored notice commit
// in one transaction (ADR 0015): if emit fails, no key is written and a retry
// can still deliver the notice. A repeat with the same event ID inserts no row,
// runs no emit and returns a copy of the first notice, so a re-delivery after a
// crash cannot double the effect. Payload is redacted before it reaches the
// file (ADR 0012). This is the completion notice from ADR 0044 feature 5, not
// the per-command clientRequestId from ADR 0028.
func (d *DB) NotifyCompletion(eventID, taskID string, payload []byte, emit func(*sql.Tx) error) (NotifyCompletionResult, error) {
	if strings.TrimSpace(eventID) == "" {
		return NotifyCompletionResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: completion: empty event id")
	}
	if strings.TrimSpace(taskID) == "" {
		return NotifyCompletionResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: completion: empty task id")
	}
	if emit == nil {
		return NotifyCompletionResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: completion: nil emit")
	}
	key := CompletionNotificationKey(eventID)
	clean := Redact(payload)
	now := time.Now().UTC().UnixNano()
	var out NotifyCompletionResult
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createCompletionNotificationsSQL); err != nil {
			return fmt.Errorf("store: completion: create table: %w", err)
		}
		res, err := tx.Exec(`
			INSERT OR IGNORE INTO completion_notifications
			  (key, event_id, task_id, payload, created_at)
			VALUES (?, ?, ?, ?, ?)`, key, eventID, taskID, clean, now)
		if err != nil {
			return fmt.Errorf("store: completion: insert: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: completion: rows affected: %w", err)
		}
		if n == 0 {
			var stored CompletionNotification
			if err := tx.QueryRow(`
				SELECT event_id, task_id, payload, created_at
				FROM completion_notifications WHERE key = ?`, key,
			).Scan(&stored.EventID, &stored.TaskID, &stored.Payload, &stored.CreatedAt); err != nil {
				return fmt.Errorf("store: completion: load existing: %w", err)
			}
			out = NotifyCompletionResult{Notification: stored, Duplicate: true}
			return nil
		}
		if err := emit(tx); err != nil {
			return fmt.Errorf("store: completion: emit: %w", err)
		}
		out = NotifyCompletionResult{
			Notification: CompletionNotification{
				EventID: eventID, TaskID: taskID,
				Payload: append([]byte(nil), clean...), CreatedAt: now,
			},
			Created: true,
		}
		return nil
	})
	if err != nil {
		return NotifyCompletionResult{}, err
	}
	return out, nil
}

// CompletionNotification returns the first notice stored for eventID and
// whether one exists. Reads use the separate read pool, so a restarted process
// sees the committed notice without waiting on the writer.
func (d *DB) CompletionNotification(eventID string) (CompletionNotification, bool, error) {
	if strings.TrimSpace(eventID) == "" {
		return CompletionNotification{}, false, domain.NewAppError(domain.CodeInvalidInput, "store: completion: empty event id")
	}
	ok, err := tableExists(context.Background(), d.readDB, "completion_notifications")
	if err != nil || !ok {
		return CompletionNotification{}, false, err
	}
	var n CompletionNotification
	err = d.readDB.QueryRow(`
		SELECT event_id, task_id, payload, created_at
		FROM completion_notifications WHERE key = ?`, CompletionNotificationKey(eventID),
	).Scan(&n.EventID, &n.TaskID, &n.Payload, &n.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CompletionNotification{}, false, nil
	}
	if err != nil {
		return CompletionNotification{}, false, err
	}
	return n, true, nil
}

// CompletionNotificationCount returns the number of stored completion notices.
// A repeated event ID must not increase it.
func (d *DB) CompletionNotificationCount() (int, error) {
	ok, err := tableExists(context.Background(), d.readDB, "completion_notifications")
	if err != nil || !ok {
		return 0, err
	}
	var n int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM completion_notifications`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// createWebhookEventsSQL owns the webhook delivery journal from ADR 0150. As
// with task_starts and completion_notifications, the table is created on first
// use because this card adds no migration file. delivery_id stores the
// namespaced idempotency key, so one GitHub delivery maps to exactly one row
// and a redelivery cannot insert a second one (ADR 0015).
const createWebhookEventsSQL = `
CREATE TABLE IF NOT EXISTS webhook_events (
  delivery_id  TEXT PRIMARY KEY,
  event        TEXT NOT NULL,
  action       TEXT NOT NULL,
  repo         TEXT NOT NULL,
  pr           INTEGER NOT NULL,
  head_sha     TEXT NOT NULL,
  watch_id     TEXT NOT NULL,
  revision     INTEGER NOT NULL,
  status       TEXT NOT NULL,
  received_at  INTEGER NOT NULL,
  outcome      TEXT NOT NULL
);
`

// createTaskLogSQL owns the three-part task log from ADR 0051 feature 2. As
// with task_starts and the other on-first-use tables, it is created on first
// write because this card owns only db.go and adds no migration file. One row
// per (task_id, part) keeps the request, outcome and report as three separate
// records that each can be read on its own. Live activity lives in a different
// table below, so "what is happening now" never mixes into these three parts.
const createTaskLogSQL = `
CREATE TABLE IF NOT EXISTS task_log (
  task_id    TEXT NOT NULL,
  part       TEXT NOT NULL,
  payload    BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (task_id, part)
);
`

// createTaskActivitySQL owns current live activity, kept apart from the three
// durable log parts (ADR 0051 feature 2). One row per task holds only the
// latest message: activity is what is happening now, not a history record.
const createTaskActivitySQL = `
CREATE TABLE IF NOT EXISTS task_activity (
  task_id    TEXT PRIMARY KEY,
  message    TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
`

// The three durable parts of a task's log (ADR 0051 feature 2). These are
// three separate records, not a status field: the request, outcome and report
// are each written on their own and read independently. This is the journal
// structure from ADR 0051, not the status/cursor of ADR 0025.
const (
	LogPartRequest = "request"
	LogPartOutcome = "outcome"
	LogPartReport  = "report"
)

func validLogPart(part string) bool {
	switch part {
	case LogPartRequest, LogPartOutcome, LogPartReport:
		return true
	}
	return false
}

// TaskLogEntry is one committed part of a task's log.
type TaskLogEntry struct {
	TaskID    string
	Part      string
	Payload   []byte
	CreatedAt int64
}

// SetLogPart writes one of the three parts (request, outcome, report) as a
// separate record. A repeat of the same part is a no-op: the first bytes stay
// stored, so a redelivery cannot overwrite the request with an outcome.
// Payload is redacted before it reaches the file (ADR 0012).
func (d *DB) SetLogPart(taskID, part string, payload []byte) error {
	if strings.TrimSpace(taskID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: log: empty task id")
	}
	if !validLogPart(part) {
		return domain.NewAppError(domain.CodeInvalidInput, "store: log: invalid part "+part)
	}
	clean := Redact(payload)
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createTaskLogSQL); err != nil {
			return fmt.Errorf("store: log: create table: %w", err)
		}
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO task_log (task_id, part, payload, created_at)
			VALUES (?, ?, ?, ?)`, taskID, part, clean, time.Now().UTC().UnixNano())
		return err
	})
}

// TaskLogParts returns only the three durable parts for taskID, in request,
// outcome, report order. Live activity is never included: it lives in
// task_activity and is read by Activity. Reading the parts does not touch the
// activity row or the ADR 0025 status/cursor.
func (d *DB) TaskLogParts(taskID string) ([]TaskLogEntry, error) {
	if strings.TrimSpace(taskID) == "" {
		return nil, domain.NewAppError(domain.CodeInvalidInput, "store: log: empty task id")
	}
	ok, err := tableExists(context.Background(), d.readDB, "task_log")
	if err != nil || !ok {
		return nil, err
	}
	rows, err := d.readDB.Query(`
		SELECT task_id, part, payload, created_at FROM task_log WHERE task_id = ?
		ORDER BY CASE part WHEN ? THEN 0 WHEN ? THEN 1 WHEN ? THEN 2 ELSE 3 END`,
		taskID, LogPartRequest, LogPartOutcome, LogPartReport)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskLogEntry
	for rows.Next() {
		var e TaskLogEntry
		if err := rows.Scan(&e.TaskID, &e.Part, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// TaskActivity is the current live activity of a task. Message is the latest
// "what is happening now" text; it is not one of the three durable log parts.
type TaskActivity struct {
	TaskID    string
	Message   string
	UpdatedAt int64
}

// SetActivity records what is happening now for taskID. Only the latest
// message is kept: activity is current state, not a history record, and it is
// stored separately from the three durable log parts (ADR 0051 feature 2).
// The message is redacted before it reaches the file (ADR 0012).
func (d *DB) SetActivity(taskID, message string) error {
	if strings.TrimSpace(taskID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: activity: empty task id")
	}
	clean := Redact([]byte(message))
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createTaskActivitySQL); err != nil {
			return fmt.Errorf("store: activity: create table: %w", err)
		}
		_, err := tx.Exec(`
			INSERT INTO task_activity (task_id, message, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT(task_id) DO UPDATE SET
			  message = excluded.message,
			  updated_at = excluded.updated_at`,
			taskID, string(clean), time.Now().UTC().UnixNano())
		return err
	})
}

// Activity returns the current live activity for taskID. found=false means
// nothing is recorded right now. This read never returns the three log parts.
func (d *DB) Activity(taskID string) (TaskActivity, bool, error) {
	if strings.TrimSpace(taskID) == "" {
		return TaskActivity{}, false, domain.NewAppError(domain.CodeInvalidInput, "store: activity: empty task id")
	}
	ok, err := tableExists(context.Background(), d.readDB, "task_activity")
	if err != nil || !ok {
		return TaskActivity{}, false, err
	}
	var a TaskActivity
	err = d.readDB.QueryRow(`
		SELECT task_id, message, updated_at FROM task_activity WHERE task_id = ?`, taskID,
	).Scan(&a.TaskID, &a.Message, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskActivity{}, false, nil
	}
	if err != nil {
		return TaskActivity{}, false, err
	}
	return a, true, nil
}

// ClearActivity removes the current activity for taskID, for example when the
// task reaches its result. A second clear is not an error. It never touches
// the three durable log parts.
func (d *DB) ClearActivity(taskID string) error {
	if strings.TrimSpace(taskID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: activity: empty task id")
	}
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createTaskActivitySQL); err != nil {
			return fmt.Errorf("store: activity: create table: %w", err)
		}
		_, err := tx.Exec(`DELETE FROM task_activity WHERE task_id = ?`, taskID)
		return err
	})
}

// The two CLI display states derived from a task's log (ADR 0051 feature 2).
// They are computed from the parts, not stored as a status or cursor.
const (
	TaskStateRunning = "running"      // «работа идёт»
	TaskStateReady   = "result_ready" // «результат готов»
)

// TaskView is what the CLI shows. It carries a display state plus the three
// durable parts and the current activity, each read from its own store. The
// state is running until the report part exists, then result_ready. This is a
// view over the three-part log, not a replacement for ADR 0025 status/cursor.
type TaskView struct {
	TaskID      string
	State       string
	Parts       []TaskLogEntry
	Activity    TaskActivity
	HasActivity bool
}

// TaskView assembles the CLI view from independent reads. It does not write
// anything and does not consult the ADR 0025 status/cursor.
func (d *DB) TaskView(taskID string) (TaskView, error) {
	parts, err := d.TaskLogParts(taskID)
	if err != nil {
		return TaskView{}, err
	}
	view := TaskView{TaskID: taskID, State: TaskStateRunning, Parts: parts}
	for _, p := range parts {
		if p.Part == LogPartReport {
			view.State = TaskStateReady
			break
		}
	}
	act, found, err := d.Activity(taskID)
	if err != nil {
		return TaskView{}, err
	}
	view.Activity = act
	view.HasActivity = found
	return view, nil
}

// ---- WAL before the "accepted" reply (ADR 0056 feature 2, T3.123) ----
//
// T1.8 (CommitVerdict) makes the verdict, its obligation and the wake intent a
// single atomic commit and checks the three components on the same DB after
// CommitVerdict returns. T3.123 is a different guarantee: it orders the durable
// write of a task's state against the moment the CLI tells the client
// "accepted". The reply is a callback this store invokes only after the write
// has committed to the WAL, never inside the transaction and never before the
// commit. A client that polls the issued task id on a fresh connection after a
// restart therefore always finds the state on disk.

// createAcceptedTasksSQL owns the WAL-before-reply task state table. As with
// the other on-first-use tables in this file, the card adds no migration file;
// the table is created inside the same write transaction as the first state.
// One row per task id is enough: the current state and the fact that the task
// was accepted live in the file, so a poll after restart finds it by id.
const createAcceptedTasksSQL = `
CREATE TABLE IF NOT EXISTS accepted_tasks (
  task_id    TEXT PRIMARY KEY,
  payload    BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
`

// AcceptTask writes taskID's state to the WAL, then invokes reply exactly once.
// reply is the "accepted" response the CLI sends to the client. It runs after
// d.Write has committed and returned, so the committed row is already visible
// to any other connection, including a restarted process polling the issued
// id, before the client is told the task was accepted. reply is never called
// inside the write transaction and is not called at all when the write fails.
// A repeat with the same task id updates the row in place and never creates a
// second task. Payload is redacted before it reaches the file (ADR 0012).
func (d *DB) AcceptTask(ctx context.Context, taskID string, state []byte, reply func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(taskID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: accept: empty task id")
	}
	if reply == nil {
		return domain.NewAppError(domain.CodeInvalidInput, "store: accept: nil reply")
	}
	clean := Redact(state)
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, createAcceptedTasksSQL); err != nil {
			return fmt.Errorf("store: accept: create table: %w", err)
		}
		now := time.Now().UTC().UnixNano()
		_, err := tx.ExecContext(ctx, `
			INSERT INTO accepted_tasks (task_id, payload, created_at, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(task_id) DO UPDATE SET
			  payload    = excluded.payload,
			  updated_at = excluded.updated_at`,
			taskID, clean, now, now)
		if err != nil {
			return fmt.Errorf("store: accept: write state: %w", err)
		}
		return nil
	}); err != nil {
		// The WAL did not commit. No "accepted" reply may reach the client.
		return err
	}
	// WAL committed. Only now may the client be told the task was accepted.
	return reply()
}

// AcceptedState is one committed task state read from the WAL. Payload is the
// redacted bytes stored by AcceptTask; UpdatedAt is the last commit time.
type AcceptedState struct {
	TaskID    string
	Payload   []byte
	CreatedAt int64
	UpdatedAt int64
}

// AcceptedTask reads a task's state by the id issued to the client. It uses
// the separate read pool, so a restarted process finds the committed state
// without waiting on the writer (ADR 0041 §4). found=false means the id is not
// on disk, so the client must not treat the task as accepted.
func (d *DB) AcceptedTask(taskID string) (AcceptedState, bool, error) {
	return acceptedTask(context.Background(), d.readDB, taskID)
}

// acceptedTask reads through an arbitrary connection. Tests pass a fresh pool
// on the same file to model a restart without opening a second full DB.
func acceptedTask(ctx context.Context, db *sql.DB, taskID string) (AcceptedState, bool, error) {
	if strings.TrimSpace(taskID) == "" {
		return AcceptedState{}, false, domain.NewAppError(domain.CodeInvalidInput, "store: accept: empty task id")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ok, err := tableExists(ctx, db, "accepted_tasks")
	if err != nil || !ok {
		return AcceptedState{}, false, err
	}
	var s AcceptedState
	err = db.QueryRowContext(ctx, `
		SELECT task_id, payload, created_at, updated_at FROM accepted_tasks WHERE task_id = ?`, taskID,
	).Scan(&s.TaskID, &s.Payload, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AcceptedState{}, false, nil
	}
	if err != nil {
		return AcceptedState{}, false, err
	}
	return s, true, nil
}

// AcceptedTaskCount returns the number of committed accepted tasks. A repeat
// of the same id must not increase it.
func (d *DB) AcceptedTaskCount() (int, error) {
	ok, err := tableExists(context.Background(), d.readDB, "accepted_tasks")
	if err != nil || !ok {
		return 0, err
	}
	var n int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM accepted_tasks`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ---- Named queues with fenced leases and atomic continuations (ADR 0058 feature 5, T3.132) ----
//
// A named queue is a durable FIFO list of tasks identified only by a name.
// Leasing the oldest pending task hands one worker a fencing token that is
// strictly higher than every token this store has issued before. Completing a
// leased task and enqueueing its continuation happen in ONE transaction (ADR
// 0015), so the next step can never be lost in the gap between two commits.
//
// This is a different object from the T3.97 worktree lease (an in-memory lease
// of a directory with a heartbeat) and from the T3.116 fenced enqueue (which
// stores a token minted elsewhere together with a receipt and an inbox
// message). Here the queue mints the token when it leases the task, and the
// continuation is another task in a named queue written by the same
// transaction that completes the current one.

const (
	queueStatePending = "pending"
	queueStateLeased  = "leased"
	queueStateDone    = "done"
)

// createQueueTablesSQL owns the named-queue tables. As with the other tables
// in this file, this card owns no migration file, so they are created on first
// use inside the caller's Write transaction. queue_tasks keeps one row per task
// with its current lease; queue_fence_counters keeps the monotonic token source
// so tokens keep increasing across restarts.
var createQueueTablesSQL = []string{
	`CREATE TABLE IF NOT EXISTS queue_tasks (
	   id            TEXT PRIMARY KEY,
	   queue         TEXT NOT NULL,
	   payload       BLOB NOT NULL,
	   state         TEXT NOT NULL,
	   owner_id      TEXT NOT NULL DEFAULT '',
	   fencing_token INTEGER NOT NULL DEFAULT 0,
	   lease_until   INTEGER NOT NULL DEFAULT 0,
	   created_at    INTEGER NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS queue_tasks_by_queue ON queue_tasks(queue, state, created_at, id)`,
	`CREATE TABLE IF NOT EXISTS queue_fence_counters (
	   id         INTEGER PRIMARY KEY CHECK (id = 1),
	   last_token INTEGER NOT NULL)`,
}

func ensureQueueTables(tx *sql.Tx) error {
	for _, stmt := range createQueueTablesSQL {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("store: queue: create table: %w", err)
		}
	}
	return nil
}

// nextQueueFenceToken returns the next strictly increasing lease number. It is
// called inside a Write transaction, so the single writer serialises every
// lease; the counter is durable, so a restart cannot reuse a token.
func nextQueueFenceToken(tx *sql.Tx) (uint64, error) {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO queue_fence_counters (id, last_token) VALUES (1, 0)`); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE queue_fence_counters SET last_token = last_token + 1 WHERE id = 1`); err != nil {
		return 0, err
	}
	var token int64
	if err := tx.QueryRow(`SELECT last_token FROM queue_fence_counters WHERE id = 1`).Scan(&token); err != nil {
		return 0, err
	}
	return uint64(token), nil
}

// QueueTask is one task stored in a named queue. State is one of pending,
// leased or done. OwnerID, FencingToken and LeaseUntil describe the lease and
// are populated by LeaseQueueTask; writers of a new task leave them empty.
type QueueTask struct {
	ID           string
	Queue        string
	Payload      []byte
	State        string
	OwnerID      string
	FencingToken uint64
	LeaseUntil   int64
	CreatedAt    int64
}

// QueueLease is the task a worker got from a named queue. FencingToken is the
// lease number it must present to complete the task; a later lease of the same
// task carries a higher token and makes this one stale.
type QueueLease struct {
	TaskID       string
	Queue        string
	OwnerID      string
	Payload      []byte
	FencingToken uint64
	LeaseUntil   int64
}

// QueueContinuation is the next step to enqueue atomically with a completion.
// It may name a different queue from the task that produced it.
type QueueContinuation struct {
	Queue   string
	TaskID  string
	Payload []byte
}

// QueueCompletionResult reports what one CompleteQueueTask committed.
// Completed marks the current task's transition to done. Continued marks a
// newly enqueued continuation; it is false on an idempotent repeat.
type QueueCompletionResult struct {
	TaskID       string
	FencingToken uint64
	Completed    bool
	Continued    bool
}

// QueueFenceStaleError reports a completion whose token is not the one the task
// currently holds. The lease was reclaimed and handed to another worker, so
// the store refuses the whole write: no completion, no continuation.
type QueueFenceStaleError struct {
	TaskID    string
	Presented uint64
	Current   uint64
}

func (e *QueueFenceStaleError) Error() string {
	return fmt.Sprintf("store: queue: task %q fencing token %d is stale, task holds token %d",
		e.TaskID, e.Presented, e.Current)
}

// IsQueueFenceStale reports whether err is a stale queue-fence rejection.
func IsQueueFenceStale(err error) bool {
	var stale *QueueFenceStaleError
	return errors.As(err, &stale)
}

func scanQueueTask(scanner interface{ Scan(...any) error }) (QueueTask, error) {
	var t QueueTask
	var token int64
	err := scanner.Scan(&t.ID, &t.Queue, &t.Payload, &t.State, &t.OwnerID,
		&token, &t.LeaseUntil, &t.CreatedAt)
	t.FencingToken = uint64(token)
	return t, err
}

// EnqueueQueueTask puts a task into a named queue in pending state. A repeat
// with the same task id inserts nothing, so a redelivered enqueue cannot
// create a second task. Payload is redacted before it reaches the file (ADR
// 0012).
func (d *DB) EnqueueQueueTask(queue, taskID string, payload []byte) error {
	if strings.TrimSpace(queue) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: queue: empty queue name")
	}
	if strings.TrimSpace(taskID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: queue: empty task id")
	}
	clean := Redact(payload)
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureQueueTables(tx); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO queue_tasks
			  (id, queue, payload, state, owner_id, fencing_token, lease_until, created_at)
			VALUES (?, ?, ?, ?, '', 0, 0, ?)`,
			taskID, queue, clean, queueStatePending, time.Now().UTC().UnixNano())
		return err
	})
}

// LeaseQueueTask reclaims expired leases in queue and hands the oldest pending
// task to ownerID for ttl, all in one transaction. Every successful lease mints
// a fresh fencing token, so a worker whose lease was reclaimed can no longer
// complete the task with its old token. found=false means the queue has no
// available task. The task is not deleted on lease: it stays until completion,
// so a reclaim can hand the same work to a new owner.
func (d *DB) LeaseQueueTask(queue, ownerID string, now time.Time, ttl time.Duration) (QueueLease, bool, error) {
	if strings.TrimSpace(queue) == "" {
		return QueueLease{}, false, domain.NewAppError(domain.CodeInvalidInput, "store: queue: empty queue name")
	}
	if strings.TrimSpace(ownerID) == "" {
		return QueueLease{}, false, domain.NewAppError(domain.CodeInvalidInput, "store: queue: empty owner id")
	}
	if ttl <= 0 {
		return QueueLease{}, false, domain.NewAppError(domain.CodeInvalidInput, "store: queue: non-positive ttl")
	}
	leaseUntil := now.Add(ttl).UnixNano()
	var out QueueLease
	found := false
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureQueueTables(tx); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			UPDATE queue_tasks
			SET state = ?, owner_id = '', fencing_token = 0, lease_until = 0
			WHERE queue = ? AND state = ? AND lease_until <= ?`,
			queueStatePending, queue, queueStateLeased, now.UnixNano()); err != nil {
			return fmt.Errorf("store: queue: reclaim: %w", err)
		}
		row := tx.QueryRow(`
			SELECT id, queue, payload, state, owner_id, fencing_token, lease_until, created_at
			FROM queue_tasks
			WHERE queue = ? AND state = ? ORDER BY created_at, id LIMIT 1`,
			queue, queueStatePending)
		task, err := scanQueueTask(row)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("store: queue: select pending: %w", err)
		}
		token, err := nextQueueFenceToken(tx)
		if err != nil {
			return fmt.Errorf("store: queue: fence token: %w", err)
		}
		if _, err := tx.Exec(`
			UPDATE queue_tasks
			SET state = ?, owner_id = ?, fencing_token = ?, lease_until = ?
			WHERE id = ?`,
			queueStateLeased, ownerID, int64(token), leaseUntil, task.ID); err != nil {
			return fmt.Errorf("store: queue: lease: %w", err)
		}
		out = QueueLease{
			TaskID: task.ID, Queue: task.Queue, OwnerID: ownerID,
			Payload:      append([]byte(nil), task.Payload...),
			FencingToken: token, LeaseUntil: leaseUntil,
		}
		found = true
		return nil
	})
	if err != nil {
		return QueueLease{}, false, err
	}
	return out, found, nil
}

// CompleteQueueTask marks a leased task done and enqueues its continuation in
// ONE transaction (ADR 0058 feature 5). finish, when non-nil, runs inside that
// transaction as the completion side effect; if it fails, neither the
// completion nor the continuation is committed. A token other than the one the
// task currently holds is refused before any row changes, so a worker whose
// lease was reclaimed cannot finish the task twice. A repeat with the current
// token after the task is already done writes nothing and reports no effect.
func (d *DB) CompleteQueueTask(taskID, ownerID string, fencingToken uint64, next *QueueContinuation, finish func(*sql.Tx) error) (QueueCompletionResult, error) {
	if strings.TrimSpace(taskID) == "" {
		return QueueCompletionResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: queue: empty task id")
	}
	if strings.TrimSpace(ownerID) == "" {
		return QueueCompletionResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: queue: empty owner id")
	}
	if fencingToken == 0 {
		return QueueCompletionResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: queue: fencing token is required")
	}
	if next != nil {
		if strings.TrimSpace(next.Queue) == "" {
			return QueueCompletionResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: queue: empty continuation queue")
		}
		if strings.TrimSpace(next.TaskID) == "" {
			return QueueCompletionResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: queue: empty continuation task id")
		}
	}

	var out QueueCompletionResult
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureQueueTables(tx); err != nil {
			return err
		}
		var state, holder string
		var current int64
		err := tx.QueryRow(`
			SELECT state, owner_id, fencing_token FROM queue_tasks WHERE id = ?`, taskID,
		).Scan(&state, &holder, &current)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.NewAppError(domain.CodeInvalidInput, "store: queue: unknown task "+taskID)
		}
		if err != nil {
			return fmt.Errorf("store: queue: load task: %w", err)
		}
		if uint64(current) != fencingToken {
			return &QueueFenceStaleError{TaskID: taskID, Presented: fencingToken, Current: uint64(current)}
		}
		if state == queueStateDone {
			// Idempotent retry of a completed task: no second continuation.
			out = QueueCompletionResult{TaskID: taskID, FencingToken: fencingToken}
			return nil
		}
		if state != queueStateLeased {
			return domain.NewAppError(domain.CodeRejectDuplicate, "store: queue: task "+taskID+" is not leased")
		}
		if holder != ownerID {
			return domain.NewAppError(domain.CodeRejectDuplicate,
				"store: queue: task "+taskID+" is held by "+holder+", not "+ownerID)
		}

		if finish != nil {
			if err := finish(tx); err != nil {
				return fmt.Errorf("store: queue: finish: %w", err)
			}
		}
		if _, err := tx.Exec(`
			UPDATE queue_tasks SET state = ?, owner_id = '', lease_until = 0 WHERE id = ?`,
			queueStateDone, taskID); err != nil {
			return fmt.Errorf("store: queue: complete: %w", err)
		}
		out.Completed = true

		if next != nil {
			res, err := tx.Exec(`
				INSERT OR IGNORE INTO queue_tasks
				  (id, queue, payload, state, owner_id, fencing_token, lease_until, created_at)
				VALUES (?, ?, ?, ?, '', 0, 0, ?)`,
				next.TaskID, next.Queue, Redact(next.Payload), queueStatePending,
				time.Now().UTC().UnixNano())
			if err != nil {
				return fmt.Errorf("store: queue: continuation: %w", err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("store: queue: continuation rows: %w", err)
			}
			out.Continued = n == 1
		}
		out.TaskID = taskID
		out.FencingToken = fencingToken
		return nil
	})
	if err != nil {
		return QueueCompletionResult{}, err
	}
	return out, nil
}

// QueueTask returns one task by id with its current state and lease. found=false
// means no task with that id is stored. Reads use the separate pool, so a
// restarted process sees the committed completion and continuation.
func (d *DB) QueueTask(taskID string) (QueueTask, bool, error) {
	ok, err := tableExists(context.Background(), d.readDB, "queue_tasks")
	if err != nil || !ok {
		return QueueTask{}, false, err
	}
	t, err := scanQueueTask(d.readDB.QueryRow(`
		SELECT id, queue, payload, state, owner_id, fencing_token, lease_until, created_at
		FROM queue_tasks WHERE id = ?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return QueueTask{}, false, nil
	}
	if err != nil {
		return QueueTask{}, false, err
	}
	return t, true, nil
}

// QueueTasks returns every task in a named queue in FIFO order, including
// leased and done rows. Used by tests and diagnostics.
func (d *DB) QueueTasks(queue string) ([]QueueTask, error) {
	ok, err := tableExists(context.Background(), d.readDB, "queue_tasks")
	if err != nil || !ok {
		return nil, err
	}
	rows, err := d.readDB.Query(`
		SELECT id, queue, payload, state, owner_id, fencing_token, lease_until, created_at
		FROM queue_tasks WHERE queue = ? ORDER BY created_at, id`, queue)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueTask
	for rows.Next() {
		t, err := scanQueueTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---- FIFO with pause, resume, cancel and crash-safe resume (ADR 0059 feature 5, T3.138) ----
//
// This is ONE FIFO queue in the same state file, not T3.132's named queues.
// T3.132 mints fencing tokens, leases by name and enqueues a continuation in
// the completing transaction. Here the contract is order and control commands:
// tasks are claimed strictly in enqueue order, the whole queue can be paused
// and resumed, any task can be cancelled, and after a process restart the
// queue continues from the file. An item claimed by a process that died stays
// inflight in the file; FIFORecover puts every inflight item back at its
// original FIFO position, so the next claim resumes where the crash left off.
// No fencing token or lease TTL is used: recovery is one durable transition.

const (
	fifoStatePending  = "pending"
	fifoStateInflight = "inflight"
)

// createFIFOQueueSQL owns the single FIFO queue and its control row. As with
// the other tables in this file, this card owns no migration file, so they are
// created on first use inside the caller's Write transaction. fifo_queue keeps
// enqueue order in seq; fifo_control keeps the pause command as one durable
// flag. Both live in the state file, so a restart reads the same order and the
// same commands back.
var createFIFOQueueSQL = []string{
	`CREATE TABLE IF NOT EXISTS fifo_queue (
	   seq         INTEGER PRIMARY KEY AUTOINCREMENT,
	   id          TEXT NOT NULL UNIQUE,
	   payload     BLOB NOT NULL,
	   state       TEXT NOT NULL,
	   enqueued_at INTEGER NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS fifo_queue_by_state ON fifo_queue(state, seq)`,
	`CREATE TABLE IF NOT EXISTS fifo_control (
	   id         INTEGER PRIMARY KEY CHECK (id = 1),
	   paused     INTEGER NOT NULL DEFAULT 0,
	   updated_at INTEGER NOT NULL)`,
}

func ensureFIFOQueue(tx *sql.Tx) error {
	for _, stmt := range createFIFOQueueSQL {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("store: fifo: create table: %w", err)
		}
	}
	return nil
}

// fifoEnsureControl makes the single control row exist so pause state can be
// read and written. It is called inside a Write transaction.
func fifoEnsureControl(tx *sql.Tx) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO fifo_control (id, paused, updated_at) VALUES (1, 0, 0)`)
	return err
}

func fifoPaused(tx *sql.Tx) (bool, error) {
	if err := fifoEnsureControl(tx); err != nil {
		return false, err
	}
	var paused int
	if err := tx.QueryRow(`SELECT paused FROM fifo_control WHERE id = 1`).Scan(&paused); err != nil {
		return false, err
	}
	return paused != 0, nil
}

func setFIFOPaused(tx *sql.Tx, paused bool) error {
	if err := fifoEnsureControl(tx); err != nil {
		return err
	}
	v := 0
	if paused {
		v = 1
	}
	_, err := tx.Exec(`
		UPDATE fifo_control SET paused = ?, updated_at = ? WHERE id = 1`,
		v, time.Now().UTC().UnixNano())
	return err
}

// FIFOItem is one task in the single FIFO queue. Seq is the enqueue order
// assigned by SQLite and never reused, so a recovered item keeps its position.
// State is pending while waiting to be claimed and inflight while a process
// holds it.
type FIFOItem struct {
	ID         string
	Payload    []byte
	State      string
	Seq        int64
	EnqueuedAt int64
}

// FIFOState is the whole queue as stored in the file: the pause flag and the
// items in FIFO order. Used by tests and diagnostics.
type FIFOState struct {
	Paused bool
	Items  []FIFOItem
}

func scanFIFOItem(scanner interface{ Scan(...any) error }) (FIFOItem, error) {
	var it FIFOItem
	err := scanner.Scan(&it.Seq, &it.ID, &it.Payload, &it.State, &it.EnqueuedAt)
	return it, err
}

// FIFOEnqueue appends a task to the tail of the single FIFO queue in pending
// state. A repeat with the same id inserts nothing, so a redelivered enqueue
// cannot put the same task twice. Payload is redacted before it reaches the
// file (ADR 0012).
func (d *DB) FIFOEnqueue(id string, payload []byte) error {
	if strings.TrimSpace(id) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: fifo: empty task id")
	}
	clean := Redact(payload)
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureFIFOQueue(tx); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO fifo_queue (id, payload, state, enqueued_at)
			VALUES (?, ?, ?, ?)`, id, clean, fifoStatePending, time.Now().UTC().UnixNano())
		return err
	})
}

// FIFOClaim hands the oldest pending task to the caller and marks it inflight.
// A paused queue hands out nothing; the pause flag is read in the same
// transaction as the claim, so a pause cannot race with a dispatch. found=false
// means either the queue is paused or no task is pending. An inflight item is
// not deleted, so a crash can put it back with FIFORecover.
func (d *DB) FIFOClaim() (FIFOItem, bool, error) {
	var out FIFOItem
	found := false
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureFIFOQueue(tx); err != nil {
			return err
		}
		paused, err := fifoPaused(tx)
		if err != nil {
			return err
		}
		if paused {
			return nil
		}
		it, err := scanFIFOItem(tx.QueryRow(`
			SELECT seq, id, payload, state, enqueued_at FROM fifo_queue
			WHERE state = ? ORDER BY seq LIMIT 1`, fifoStatePending))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("store: fifo: select pending: %w", err)
		}
		if _, err := tx.Exec(`UPDATE fifo_queue SET state = ? WHERE seq = ?`, fifoStateInflight, it.Seq); err != nil {
			return fmt.Errorf("store: fifo: mark inflight: %w", err)
		}
		it.State = fifoStateInflight
		out = it
		found = true
		return nil
	})
	if err != nil {
		return FIFOItem{}, false, err
	}
	return out, found, nil
}

// FIFOPause stops the queue from handing out new tasks. The command is stored
// in the file, so a process that restarts while paused stays paused until an
// explicit FIFOResume. Repeated calls are safe.
func (d *DB) FIFOPause() error {
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureFIFOQueue(tx); err != nil {
			return err
		}
		return setFIFOPaused(tx, true)
	})
}

// FIFOResume restarts dispatch. It only clears the pause flag; it does not
// touch inflight items, so a worker that survived a restart keeps its task.
// FIFORecover is the separate hook for items left by a dead worker.
func (d *DB) FIFOResume() error {
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureFIFOQueue(tx); err != nil {
			return err
		}
		return setFIFOPaused(tx, false)
	})
}

// FIFOCancel removes a task from the queue whether it is pending or inflight.
// It reports whether a row was removed, so a repeated cancel is not an error.
func (d *DB) FIFOCancel(id string) (bool, error) {
	if strings.TrimSpace(id) == "" {
		return false, domain.NewAppError(domain.CodeInvalidInput, "store: fifo: empty task id")
	}
	removed := false
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureFIFOQueue(tx); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM fifo_queue WHERE id = ?`, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		removed = n > 0
		return err
	})
	return removed, err
}

// FIFOComplete finishes an inflight task and removes it from the queue. A task
// that is still pending cannot be completed, and a repeated complete reports
// false without changing anything.
func (d *DB) FIFOComplete(id string) (bool, error) {
	if strings.TrimSpace(id) == "" {
		return false, domain.NewAppError(domain.CodeInvalidInput, "store: fifo: empty task id")
	}
	removed := false
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureFIFOQueue(tx); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM fifo_queue WHERE id = ? AND state = ?`, id, fifoStateInflight)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		removed = n > 0
		return err
	})
	return removed, err
}

// FIFORecover is the crash-safe resume hook. Every inflight item is put back
// to pending in one transaction, keeping its original seq so the next claim
// hands tasks out in the same order as before the crash. It returns the number
// of items recovered. Call it once after reopening the state file.
func (d *DB) FIFORecover() (int, error) {
	var n int64
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureFIFOQueue(tx); err != nil {
			return err
		}
		res, err := tx.Exec(`
			UPDATE fifo_queue SET state = ? WHERE state = ?`,
			fifoStatePending, fifoStateInflight)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

// FIFOState returns the pause flag and every queued item in FIFO order. Reads
// use the separate pool, so a restarted process sees the same order and
// commands the previous process committed.
func (d *DB) FIFOState() (FIFOState, error) {
	ctx := context.Background()
	ok, err := tableExists(ctx, d.readDB, "fifo_queue")
	if err != nil || !ok {
		return FIFOState{}, err
	}
	var state FIFOState
	okCtrl, err := tableExists(ctx, d.readDB, "fifo_control")
	if err != nil {
		return FIFOState{}, err
	}
	if okCtrl {
		var paused int
		err := d.readDB.QueryRow(`SELECT paused FROM fifo_control WHERE id = 1`).Scan(&paused)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return FIFOState{}, err
		}
		state.Paused = paused != 0
	}
	rows, err := d.readDB.Query(`
		SELECT seq, id, payload, state, enqueued_at FROM fifo_queue ORDER BY seq`)
	if err != nil {
		return FIFOState{}, err
	}
	defer rows.Close()
	for rows.Next() {
		it, err := scanFIFOItem(rows)
		if err != nil {
			return FIFOState{}, err
		}
		state.Items = append(state.Items, it)
	}
	return state, rows.Err()
}

// FIFOPaused reports the persisted pause command. A fresh file is not paused.
func (d *DB) FIFOPaused() (bool, error) {
	ok, err := tableExists(context.Background(), d.readDB, "fifo_control")
	if err != nil || !ok {
		return false, err
	}
	var paused int
	err = d.readDB.QueryRow(`SELECT paused FROM fifo_control WHERE id = 1`).Scan(&paused)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return paused != 0, err
}

// ---- Batched enqueue of the durable FIFO queue (ADR 0063 фича 3, T3.155) ----
//
// FIFOEnqueue opens one write transaction per task, so a burst of enqueues
// pays for one SQLite commit per task against the single writer. A batch is
// one Write: a single prepared INSERT executed N times inside ONE transaction,
// so the burst costs one commit. This batches only the enqueue path. Ordering,
// pause, resume, cancel, claim, completion and crash recovery stay with
// T3.138 and are not duplicated here.

// FIFOEnqueueBatchResult reports what one batched enqueue committed. Inserted
// counts new rows; Ignored counts ids already in the queue, whose stored
// payload was left untouched.
type FIFOEnqueueBatchResult struct {
	Inserted int
	Ignored  int
}

// FIFOEnqueueBatch appends several tasks to the tail of the single FIFO queue
// in one transaction. All inserts share one commit, so enqueueing N tasks does
// not serialize N writers (ADR 0063 фича 3). Tasks receive their seq in the
// order given, so FIFOClaim still hands them out in that order. A repeat with
// an existing id inserts nothing and keeps the first payload (INSERT OR
// IGNORE, the same rule as FIFOEnqueue). Every payload is redacted before it
// reaches the file (ADR 0012). Only ID and Payload of an item are used; Seq,
// State and EnqueuedAt are assigned by the queue. Ids are validated before the
// transaction, so a batch holding a blank id commits nothing.
func (d *DB) FIFOEnqueueBatch(items []FIFOItem) (FIFOEnqueueBatchResult, error) {
	for i, it := range items {
		if strings.TrimSpace(it.ID) == "" {
			return FIFOEnqueueBatchResult{}, domain.NewAppError(domain.CodeInvalidInput,
				fmt.Sprintf("store: fifo: batch item %d: empty task id", i))
		}
	}
	if len(items) == 0 {
		return FIFOEnqueueBatchResult{}, nil
	}

	now := time.Now().UTC().UnixNano()
	var out FIFOEnqueueBatchResult
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureFIFOQueue(tx); err != nil {
			return err
		}
		stmt, err := tx.Prepare(`
			INSERT OR IGNORE INTO fifo_queue (id, payload, state, enqueued_at)
			VALUES (?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("store: fifo: batch prepare: %w", err)
		}
		defer stmt.Close()
		inserted := 0
		for _, it := range items {
			res, err := stmt.Exec(it.ID, Redact(it.Payload), fifoStatePending, now)
			if err != nil {
				return fmt.Errorf("store: fifo: batch insert %s: %w", it.ID, err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("store: fifo: batch rows affected %s: %w", it.ID, err)
			}
			inserted += int(n)
		}
		out = FIFOEnqueueBatchResult{Inserted: inserted, Ignored: len(items) - inserted}
		return nil
	})
	if err != nil {
		return FIFOEnqueueBatchResult{}, err
	}
	return out, nil
}
