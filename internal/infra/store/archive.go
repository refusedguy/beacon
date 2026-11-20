package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
