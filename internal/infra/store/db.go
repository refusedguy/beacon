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
}

type writeJob struct {
	ctx context.Context
	fn  func(*sql.Tx) error
	err chan error
}

var _ domain.Store = (*DB)(nil)

// Open opens path, enables WAL, applies pending migrations and starts the
// single writer. PRAGMAs are set in the DSN so modernc applies them to every
// new connection; busy_timeout comes first (report G §3).
func Open(path string) (*DB, error) {
	writeDB, err := openPool(path, 1)
	if err != nil {
		return nil, err
	}
	readDB, err := openPool(path, 4)
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
	go d.writeLoop()
	return d, nil
}

func openPool(path string, maxConns int) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn(path))
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

// Write runs fn in one transaction on the single writer. This is the only
// path that mutates the database, so a redacting wrapper can wrap it later.
func (d *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error {
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
	payload, err := json.Marshal(w)
	if err != nil {
		return err
	}
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO watches (id, repo, pr, head_sha, status, revision, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
			  repo     = excluded.repo,
			  pr       = excluded.pr,
			  head_sha = excluded.head_sha,
			  status   = excluded.status,
			  revision = excluded.revision,
			  payload  = excluded.payload`,
			w.ID, w.Repo, w.PR, w.HeadSHA, string(w.Status), w.Revision, payload)
		return err
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

// ActiveWatches returns watches in watching or notifying status.
func (d *DB) ActiveWatches() ([]domain.Watch, error) {
	return d.queryWatches(`
		SELECT payload FROM watches WHERE status IN (?, ?) ORDER BY id`,
		string(domain.StatusWatching), string(domain.StatusNotifying))
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
