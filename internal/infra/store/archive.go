package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// ClosedWatchRetention is how long a closed Watch stays in the active
	// table before ArchiveClosedWatches moves it to the archive. It is not a
	// TTL: no Watch closes because time passed (ADR 0019).
	ClosedWatchRetention = 7 * 24 * time.Hour

	closeReasonHeadChanged = "head_changed"
	closeReasonCancelled   = "cancelled"
	closeReasonCancel      = "cancel"
	closeReasonMerged      = "merged"
	closeReasonClosed      = "closed"
)

const createWatchArchiveSQL = `
CREATE TABLE IF NOT EXISTS watch_archive (
  id              TEXT PRIMARY KEY,
  repo            TEXT NOT NULL,
  pr              INTEGER NOT NULL,
  head_sha        TEXT NOT NULL,
  base_ref        TEXT NOT NULL,
  issue           TEXT NOT NULL,
  status          TEXT NOT NULL,
  verdict         TEXT NOT NULL,
  revision        INTEGER NOT NULL,
  checks          BLOB NOT NULL,
  merge           BLOB NOT NULL,
  flaky           BLOB NOT NULL,
  policy          BLOB NOT NULL,
  subscribers     BLOB NOT NULL,
  deadline_at     INTEGER NOT NULL,
  no_checks_since INTEGER NOT NULL,
  created_at      INTEGER NOT NULL,
  closed_at       INTEGER NOT NULL,
  close_reason    TEXT NOT NULL,
  payload         BLOB NOT NULL,
  archived_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS watch_archive_closed_at ON watch_archive(closed_at);
`

// ArchiveClosedWatches moves closed Watches that have passed the retention
// window into watch_archive and returns the number moved. The table is created
// on first use because this package owns only archive.go, not the migration
// corpus. All work runs in one transaction on the single writer, so a second
// call finds no source rows and moves nothing.
//
// Obligations are never deleted. They stay in the obligations table keyed by
// watch_id, and WatchObligations reads them after the Watch has moved.
//
// This is the raw, unconditional pass. A cycle should call ArchiveRun, which
// adds the hourly schedule on top of this method.
func (d *DB) ArchiveClosedWatches(now time.Time) (int, error) {
	if now.IsZero() {
		return 0, errors.New("store: archive: zero time")
	}

	moved := 0
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		moved, err = archiveClosedWatchesTx(tx, now)
		return err
	})
	if err != nil {
		return 0, err
	}
	return moved, nil
}

// archiveClosedWatchesTx is the unconditional pass inside a caller-owned
// transaction. ArchiveRun reuses it so the move and the schedule marker commit
// together; a crash between the two cannot leave a "completed" pass that never
// archived, or an archived pass that will be repeated on the next tick.
func archiveClosedWatchesTx(tx *sql.Tx, now time.Time) (int, error) {
	if _, err := tx.Exec(createWatchArchiveSQL); err != nil {
		return 0, fmt.Errorf("store: archive: create table: %w", err)
	}

	rows, err := tx.Query(`
		SELECT id, payload FROM watches WHERE status = ? ORDER BY id`,
		string(domain.StatusClosed))
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id string
		w  domain.Watch
	}
	var candidates []candidate
	for rows.Next() {
		var id string
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			rows.Close()
			return 0, err
		}
		w, err := decodeWatch(payload)
		if err != nil {
			rows.Close()
			return 0, err
		}
		if archiveEligible(w, now) {
			candidates = append(candidates, candidate{id: id, w: w})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	moved := 0
	for _, c := range candidates {
		if err := insertArchivedWatch(tx, c.w, now); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM watches WHERE id = ?`, c.id); err != nil {
			return 0, err
		}
		moved++
	}
	return moved, nil
}

// ArchiveScheduleInterval is how often the hourly archive pass runs. A cycle
// may call ArchiveRun on every tick; it archives at most once per interval.
const ArchiveScheduleInterval = time.Hour

const archiveScheduleKey = "archive"

// createArchiveScheduleSQL is the durable marker for the last archive pass.
// It is created on first use, like the other tables owned by this package, so
// a process restart does not re-run an hour's work or forget a pass that is
// due. One row, keyed by a fixed name.
const createArchiveScheduleSQL = `
CREATE TABLE IF NOT EXISTS archive_schedule (
  id          TEXT PRIMARY KEY,
  last_run_at INTEGER NOT NULL
);
`

// ArchiveRunResult is what one scheduled pass did. Ran is false when the
// interval has not elapsed; Moved is zero in that case and also when the pass
// ran but nothing had passed the retention window.
type ArchiveRunResult struct {
	Ran   bool
	Moved int
}

// ArchiveDue reports whether the next hourly pass is due by now. A store with
// no marker yet is due, so the first cycle archives immediately on startup.
// It only reads; a caller that wants to perform the pass calls ArchiveRun.
func (d *DB) ArchiveDue(ctx context.Context, now time.Time) (bool, error) {
	if now.IsZero() {
		return false, errors.New("store: archive: zero time")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ok, err := tableExists(ctx, d.readDB, "archive_schedule")
	if err != nil || !ok {
		return err == nil, err
	}
	last, ok, err := readLastArchiveRun(ctx, d.readDB)
	if err != nil || !ok {
		return err == nil, err
	}
	return !now.Before(last.Add(ArchiveScheduleInterval)), nil
}

// ArchiveRun performs the archive pass when it is due and records the run in
// the same transaction as the move. A pass that is not due is a no-op with
// Ran=false. Repeated or concurrent calls in the same interval commit at most
// one pass, so the watches table stays compact without the cycle tracking any
// schedule state of its own.
func (d *DB) ArchiveRun(ctx context.Context, now time.Time) (ArchiveRunResult, error) {
	if now.IsZero() {
		return ArchiveRunResult{}, errors.New("store: archive: zero time")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result ArchiveRunResult
	err := d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createArchiveScheduleSQL); err != nil {
			return fmt.Errorf("store: archive: create schedule: %w", err)
		}
		last, ok, err := readLastArchiveRun(ctx, tx)
		if err != nil {
			return err
		}
		if ok && now.Before(last.Add(ArchiveScheduleInterval)) {
			return nil
		}
		moved, err := archiveClosedWatchesTx(tx, now)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO archive_schedule (id, last_run_at) VALUES (?, ?)
			ON CONFLICT(id) DO UPDATE SET last_run_at = excluded.last_run_at`,
			archiveScheduleKey, timeToNano(now)); err != nil {
			return fmt.Errorf("store: archive: record run: %w", err)
		}
		result = ArchiveRunResult{Ran: true, Moved: moved}
		return nil
	})
	if err != nil {
		return ArchiveRunResult{}, err
	}
	return result, nil
}

// LastArchiveRun returns the time of the last committed pass and whether one
// has been recorded. W6-B can log it when it wires the scheduler into its
// cycle; tests use it to assert a pass was persisted across a reopen.
func (d *DB) LastArchiveRun(ctx context.Context) (time.Time, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ok, err := tableExists(ctx, d.readDB, "archive_schedule")
	if err != nil || !ok {
		return time.Time{}, false, err
	}
	return readLastArchiveRun(ctx, d.readDB)
}

// queryer is the narrow read surface shared by *sql.DB and *sql.Tx. Both the
// read pool and the writer transaction can answer "when did we last run".
type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readLastArchiveRun(ctx context.Context, q queryer) (time.Time, bool, error) {
	var nano int64
	err := q.QueryRowContext(ctx,
		`SELECT last_run_at FROM archive_schedule WHERE id = ?`, archiveScheduleKey,
	).Scan(&nano)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return time.Unix(0, nano).UTC(), true, nil
}

// RecentClosedWatches returns the completed Watches closed at or after since,
// newest first and no more than limit. The history window is shorter than
// ClosedWatchRetention, so the rows still live in the active watches table;
// watch_archive holds only Watches older than the retention window.
//
// The SQL filter narrows the rows by the closedAt field in the payload. Go
// repeats the boundary check on decoded times, because the SQL timestamp
// comparison has coarser precision than time.Time.
func (d *DB) RecentClosedWatches(ctx context.Context, since time.Time, limit int) ([]domain.Watch, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		return []domain.Watch{}, nil
	}
	rows, err := d.readDB.QueryContext(ctx, `
		SELECT payload FROM watches
		WHERE status = ?
		  AND julianday(json_extract(payload, '$.closedAt')) >= julianday(?)
		ORDER BY julianday(json_extract(payload, '$.closedAt')) DESC
		LIMIT ?`,
		string(domain.StatusClosed), since.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	watches := make([]domain.Watch, 0, limit)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		w, err := decodeWatch(payload)
		if err != nil {
			return nil, err
		}
		if w.ClosedAt.Before(since) {
			continue
		}
		watches = append(watches, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(watches, func(i, j int) bool {
		return watches[i].ClosedAt.After(watches[j].ClosedAt)
	})
	return watches, nil
}

// archiveEligible accepts a closed Watch only when it was closed by a
// terminal verdict, a cancel or a head change and has been closed for the
// retention window. A Watch that is merely old is never eligible, and time
// never closes a Watch.
func archiveEligible(w domain.Watch, now time.Time) bool {
	if w.Status != domain.StatusClosed || w.ClosedAt.IsZero() {
		return false
	}
	if now.Sub(w.ClosedAt) < ClosedWatchRetention {
		return false
	}
	switch w.CloseReason {
	case closeReasonHeadChanged, closeReasonCancelled, closeReasonCancel,
		closeReasonMerged, closeReasonClosed:
		return true
	}
	return terminalVerdict(w.Verdict)
}

func terminalVerdict(v domain.Verdict) bool {
	return v == domain.VerdictMerged || v == domain.VerdictClosed
}

func insertArchivedWatch(tx *sql.Tx, w domain.Watch, archivedAt time.Time) error {
	checks, err := json.Marshal(w.Checks)
	if err != nil {
		return err
	}
	merge, err := json.Marshal(w.Merge)
	if err != nil {
		return err
	}
	flaky, err := json.Marshal(w.Flaky)
	if err != nil {
		return err
	}
	policy, err := json.Marshal(w.Policy)
	if err != nil {
		return err
	}
	subscribers, err := json.Marshal(w.Subscribers)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(w)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`
		INSERT OR REPLACE INTO watch_archive
		  (id, repo, pr, head_sha, base_ref, issue, status, verdict, revision,
		   checks, merge, flaky, policy, subscribers, deadline_at,
		   no_checks_since, created_at, closed_at, close_reason, payload, archived_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.Repo, w.PR, w.HeadSHA, w.BaseRef, w.Issue,
		string(w.Status), string(w.Verdict), int64(w.Revision),
		checks, merge, flaky, policy, subscribers,
		timeToNano(w.DeadlineAt), timeToNano(w.NoChecksSince),
		timeToNano(w.CreatedAt), timeToNano(w.ClosedAt), w.CloseReason,
		payload, timeToNano(archivedAt))
	return err
}

func timeToNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func (d *DB) ensureWatchArchive() error {
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(createWatchArchiveSQL)
		return err
	})
}

// ArchivedWatch loads one archived Watch by its watchID.
func (d *DB) ArchivedWatch(id string) (domain.Watch, error) {
	if err := d.ensureWatchArchive(); err != nil {
		return domain.Watch{}, err
	}
	var payload []byte
	err := d.readDB.QueryRow(`SELECT payload FROM watch_archive WHERE id = ?`, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Watch{}, domain.NewAppError(domain.CodeWatchNotFound, "archived watch "+id+" not found")
	}
	if err != nil {
		return domain.Watch{}, err
	}
	return decodeWatch(payload)
}

// ArchivedWatches returns every Watch in the archive, ordered by id.
func (d *DB) ArchivedWatches() ([]domain.Watch, error) {
	if err := d.ensureWatchArchive(); err != nil {
		return nil, err
	}
	return d.queryWatches(`SELECT payload FROM watch_archive ORDER BY id`)
}

// WatchObligations returns the obligation history for a watchID. The rows
// stay in the obligations table when the Watch is archived, so the history
// survives the move.
func (d *DB) WatchObligations(watchID string) ([]domain.Obligation, error) {
	rows, err := d.readDB.Query(`
		SELECT payload FROM obligations WHERE watch_id = ? ORDER BY seq`, watchID)
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

// createObligationEraseLedgerSQL owns the audit trail for erased obligations.
// The table is created on first use, like the watch archive and the other
// package-owned tables. A row keeps the erased id, who removed it, when and
// why, together with a copy of the removed payload. The copy lets an operator
// check the erase after a restore, when the source row is gone.
const createObligationEraseLedgerSQL = `
CREATE TABLE IF NOT EXISTS obligation_erase_ledger (
  id        TEXT PRIMARY KEY,
  actor     TEXT NOT NULL,
  reason    TEXT NOT NULL,
  erased_at INTEGER NOT NULL,
  payload   BLOB
);
`

// ObligationEraseRecord is one ledger row. Erased is true only for the call
// that wrote the row; a repeat of the same erase returns the stored record
// with Erased=false.
type ObligationEraseRecord struct {
	ObligationID string    `json:"obligationID"`
	Actor        string    `json:"actor"`
	Reason       string    `json:"reason"`
	ErasedAt     time.Time `json:"erasedAt"`
	Payload      []byte    `json:"payload,omitempty"`
	Erased       bool      `json:"erased"`
}

// EraseObligation removes one obligation and records the removal in the same
// transaction. The ledger row is written before the delete, so a crash cannot
// leave an erased obligation without its audit trail. A repeated call for the
// same id returns the original record and writes nothing.
func (d *DB) EraseObligation(ctx context.Context, id, actor, reason string, at time.Time) (ObligationEraseRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		return ObligationEraseRecord{}, errors.New("store: erase: empty obligation id")
	}
	if at.IsZero() {
		return ObligationEraseRecord{}, errors.New("store: erase: zero time")
	}

	var rec ObligationEraseRecord
	err := d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createObligationEraseLedgerSQL); err != nil {
			return fmt.Errorf("store: erase: create ledger: %w", err)
		}

		var erasedAt int64
		err := tx.QueryRowContext(ctx, `
			SELECT actor, reason, erased_at, payload
			FROM obligation_erase_ledger WHERE id = ?`, id,
		).Scan(&rec.Actor, &rec.Reason, &erasedAt, &rec.Payload)
		if err == nil {
			rec.ObligationID = id
			rec.ErasedAt = time.Unix(0, erasedAt).UTC()
			rec.Erased = false
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		var payload []byte
		err = tx.QueryRowContext(ctx, `SELECT payload FROM obligations WHERE id = ?`, id).Scan(&payload)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		rec = ObligationEraseRecord{
			ObligationID: id,
			Actor:        actor,
			Reason:       reason,
			ErasedAt:     at.UTC(),
			Payload:      payload,
			Erased:       true,
		}
		if _, err := tx.Exec(`
			INSERT INTO obligation_erase_ledger (id, actor, reason, erased_at, payload)
			VALUES (?, ?, ?, ?, ?)`, id, actor, reason, timeToNano(at), payload); err != nil {
			return fmt.Errorf("store: erase: record ledger: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM obligations WHERE id = ?`, id); err != nil {
			return fmt.Errorf("store: erase: delete obligation: %w", err)
		}
		return nil
	})
	return rec, err
}

// ObligationErase returns the ledger row for an erased obligation.
func (d *DB) ObligationErase(id string) (ObligationEraseRecord, error) {
	if err := d.ensureObligationEraseLedger(); err != nil {
		return ObligationEraseRecord{}, err
	}
	var rec ObligationEraseRecord
	var erasedAt int64
	err := d.readDB.QueryRow(`
		SELECT actor, reason, erased_at, payload
		FROM obligation_erase_ledger WHERE id = ?`, id,
	).Scan(&rec.Actor, &rec.Reason, &erasedAt, &rec.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return ObligationEraseRecord{}, domain.NewAppError(domain.CodeWatchNotFound, "erase record "+id+" not found")
	}
	if err != nil {
		return ObligationEraseRecord{}, err
	}
	rec.ObligationID = id
	rec.ErasedAt = time.Unix(0, erasedAt).UTC()
	return rec, nil
}

func (d *DB) ensureObligationEraseLedger() error {
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(createObligationEraseLedgerSQL)
		return err
	})
}
