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
func (d *DB) ArchiveClosedWatches(now time.Time) (int, error) {
	if now.IsZero() {
		return 0, errors.New("store: archive: zero time")
	}

	moved := 0
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createWatchArchiveSQL); err != nil {
			return fmt.Errorf("store: archive: create table: %w", err)
		}

		rows, err := tx.Query(`
			SELECT id, payload FROM watches WHERE status = ? ORDER BY id`,
			string(domain.StatusClosed))
		if err != nil {
			return err
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
				return err
			}
			w, err := decodeWatch(payload)
			if err != nil {
				rows.Close()
				return err
			}
			if archiveEligible(w, now) {
				candidates = append(candidates, candidate{id: id, w: w})
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}

		for _, c := range candidates {
			if err := insertArchivedWatch(tx, c.w, now); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM watches WHERE id = ?`, c.id); err != nil {
				return err
			}
			moved++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return moved, nil
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
