// Package store: durable queue of wiring work deferred past a Watch handoff.
//
// A deferred row records that a Watch still needs a follow-up wiring change:
// which PR it belongs to, a human-readable title and reason, and the owner
// files the follow-up must touch. The (watch_id, reason) pair is the identity
// of one deferred item, guarded by a unique index, so a replayed wake or a
// retried defer cannot create a second row. The first write wins; a repeat
// returns the stored row unchanged.
//
// The table normally arrives with a migration. This file also creates it on
// first use, so a tree without that migration still compiles and runs. No
// change to db.go is needed: writes ensure the table inside the single-writer
// transaction and reads return an empty result when it does not exist yet.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// Deferred statuses. todo is the initial state, claimed means a worker has
// taken the item, done means the wiring was applied.
const (
	DeferredStatusTodo    = "todo"
	DeferredStatusClaimed = "claimed"
	DeferredStatusDone    = "done"
)

// Errors returned by the status transitions. ErrDeferredNotFound means no row
// has the given (watch_id, reason); ErrDeferredWrongStatus means the row exists
// but is not in a state that permits the requested transition. A repeated
// transition into the target status is idempotent and returns the row.
var (
	ErrDeferredNotFound    = errors.New("store: deferred wiring: not found")
	ErrDeferredWrongStatus = errors.New("store: deferred wiring: wrong status")
)

const createDeferredWiringTableSQL = `
CREATE TABLE IF NOT EXISTS deferred_wiring (
  id          INTEGER PRIMARY KEY,
  watch_id    TEXT NOT NULL,
  pr          INTEGER NOT NULL,
  title       TEXT NOT NULL DEFAULT '',
  reason      TEXT NOT NULL,
  owner_files TEXT NOT NULL DEFAULT '[]',
  status      TEXT NOT NULL DEFAULT 'todo',
  created_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS deferred_wiring_by_watch_reason
  ON deferred_wiring(watch_id, reason);
CREATE INDEX IF NOT EXISTS deferred_wiring_by_status
  ON deferred_wiring(status);
`

// DeferredWiring is one stored deferred wiring item. OwnerFiles is stored as a
// JSON array of strings in owner_files and decoded back on read.
type DeferredWiring struct {
	ID         int64
	WatchID    string
	PR         int
	Title      string
	Reason     string
	OwnerFiles []string
	Status     string
	CreatedAt  int64
}

func validateDeferredWiring(in DeferredWiring, status string) error {
	if strings.TrimSpace(in.WatchID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: deferred wiring: empty watch id")
	}
	if strings.TrimSpace(in.Reason) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: deferred wiring: empty reason")
	}
	return validateDeferredStatus(status)
}

func validateDeferredStatus(status string) error {
	switch status {
	case "", DeferredStatusTodo, DeferredStatusClaimed, DeferredStatusDone:
		return nil
	default:
		return domain.NewAppError(domain.CodeInvalidInput,
			"store: deferred wiring: unknown status "+status)
	}
}

func ensureDeferredWiringTable(tx *sql.Tx) error {
	if _, err := tx.Exec(createDeferredWiringTableSQL); err != nil {
		return fmt.Errorf("store: deferred wiring: create table: %w", err)
	}
	return nil
}

// encodeOwnerFiles normalizes a nil slice to [] so the stored cell is always a
// JSON array, never null.
func encodeOwnerFiles(files []string) (string, error) {
	if files == nil {
		files = []string{}
	}
	raw, err := json.Marshal(files)
	if err != nil {
		return "", fmt.Errorf("store: deferred wiring: encode owner_files: %w", err)
	}
	return string(raw), nil
}

func decodeOwnerFiles(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{}, nil
	}
	var files []string
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		return nil, fmt.Errorf("store: deferred wiring: decode owner_files: %w", err)
	}
	if files == nil {
		files = []string{}
	}
	return files, nil
}

// loadDeferred reads one row by its (watch_id, reason) identity. It works with
// both the write transaction and the read pool through rowQuerier.
func loadDeferred(q rowQuerier, watchID, reason string) (DeferredWiring, error) {
	var row DeferredWiring
	var raw string
	err := q.QueryRow(`
		SELECT id, watch_id, pr, title, reason, owner_files, status, created_at
		FROM deferred_wiring WHERE watch_id = ? AND reason = ?`, watchID, reason,
	).Scan(&row.ID, &row.WatchID, &row.PR, &row.Title, &row.Reason,
		&raw, &row.Status, &row.CreatedAt)
	if err != nil {
		return DeferredWiring{}, err
	}
	files, err := decodeOwnerFiles(raw)
	if err != nil {
		return DeferredWiring{}, err
	}
	row.OwnerFiles = files
	return row, nil
}

// DeferWiring inserts one deferred item, or returns the stored one when a row
// with the same (watch_id, reason) already exists. The insert and the read
// share one transaction, so concurrent defers of the same key converge on a
// single row and every caller receives that row. Status defaults to todo;
// CreatedAt defaults to the current UTC time in nanoseconds.
func (d *DB) DeferWiring(in DeferredWiring) (DeferredWiring, error) {
	status := in.Status
	if status == "" {
		status = DeferredStatusTodo
	}
	if err := validateDeferredWiring(in, status); err != nil {
		return DeferredWiring{}, err
	}
	raw, err := encodeOwnerFiles(in.OwnerFiles)
	if err != nil {
		return DeferredWiring{}, err
	}
	at := in.CreatedAt
	if at == 0 {
		at = time.Now().UTC().UnixNano()
	}

	var out DeferredWiring
	err = d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureDeferredWiringTable(tx); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO deferred_wiring
			  (watch_id, pr, title, reason, owner_files, status, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			in.WatchID, in.PR, in.Title, in.Reason, raw, status, at)
		if err != nil {
			return fmt.Errorf("store: deferred wiring: insert: %w", err)
		}
		out, err = loadDeferred(tx, in.WatchID, in.Reason)
		if err != nil {
			return fmt.Errorf("store: deferred wiring: load: %w", err)
		}
		return nil
	})
	if err != nil {
		return DeferredWiring{}, err
	}
	return out, nil
}

// ListDeferred returns the deferred items in id order. An empty status lists
// every row; otherwise only rows with that status are returned. A database
// that has never stored a deferred item returns an empty result.
func (d *DB) ListDeferred(status string) ([]DeferredWiring, error) {
	if err := validateDeferredStatus(status); err != nil {
		return nil, err
	}
	ok, err := tableExists(context.Background(), d.readDB, "deferred_wiring")
	if err != nil || !ok {
		return nil, err
	}
	query := `
		SELECT id, watch_id, pr, title, reason, owner_files, status, created_at
		FROM deferred_wiring`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY id`

	rows, err := d.readDB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: deferred wiring: list: %w", err)
	}
	defer rows.Close()

	var out []DeferredWiring
	for rows.Next() {
		var row DeferredWiring
		var raw string
		if err := rows.Scan(&row.ID, &row.WatchID, &row.PR, &row.Title,
			&row.Reason, &raw, &row.Status, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: deferred wiring: scan: %w", err)
		}
		files, err := decodeOwnerFiles(raw)
		if err != nil {
			return nil, err
		}
		row.OwnerFiles = files
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: deferred wiring: iterate: %w", err)
	}
	return out, nil
}

// setDeferredStatus moves one item into target when its current status is in
// allowed. The condition is part of the UPDATE, so two racers cannot both win:
// only the first transition changes a row. Repeating a transition that already
// reached target is a no-op that returns the stored row.
func (d *DB) setDeferredStatus(watchID, reason, target string, allowed []string) (DeferredWiring, error) {
	if strings.TrimSpace(watchID) == "" || strings.TrimSpace(reason) == "" {
		return DeferredWiring{}, domain.NewAppError(
			domain.CodeInvalidInput, "store: deferred wiring: empty watch id or reason")
	}
	if len(allowed) == 0 {
		return DeferredWiring{}, domain.NewAppError(
			domain.CodeInvalidInput, "store: deferred wiring: no source statuses")
	}

	placeholders := make([]string, len(allowed))
	args := make([]any, 0, len(allowed)+3)
	args = append(args, target)
	for i, s := range allowed {
		placeholders[i] = "?"
		args = append(args, s)
	}
	args = append(args, watchID, reason)
	query := fmt.Sprintf(`
		UPDATE deferred_wiring SET status = ?
		WHERE status IN (%s) AND watch_id = ? AND reason = ?`,
		strings.Join(placeholders, ","))

	var out DeferredWiring
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureDeferredWiringTable(tx); err != nil {
			return err
		}
		res, err := tx.Exec(query, args...)
		if err != nil {
			return fmt.Errorf("store: deferred wiring: update: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: deferred wiring: rows affected: %w", err)
		}
		if n == 0 {
			existing, err := loadDeferred(tx, watchID, reason)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrDeferredNotFound
			}
			if err != nil {
				return err
			}
			if existing.Status == target {
				out = existing
				return nil
			}
			return fmt.Errorf("%w: watch %s reason %q status %q, want one of %v",
				ErrDeferredWrongStatus, watchID, reason, existing.Status, allowed)
		}
		out, err = loadDeferred(tx, watchID, reason)
		return err
	})
	if err != nil {
		return DeferredWiring{}, err
	}
	return out, nil
}

// ClaimDeferred moves a todo item to claimed. A repeat on an already claimed
// item returns it unchanged; a missing item or one past todo is an error.
func (d *DB) ClaimDeferred(watchID, reason string) (DeferredWiring, error) {
	return d.setDeferredStatus(watchID, reason, DeferredStatusClaimed,
		[]string{DeferredStatusTodo})
}

// MarkDeferredDone moves a todo or claimed item to done. A repeat on an
// already done item returns it unchanged.
func (d *DB) MarkDeferredDone(watchID, reason string) (DeferredWiring, error) {
	return d.setDeferredStatus(watchID, reason, DeferredStatusDone,
		[]string{DeferredStatusTodo, DeferredStatusClaimed})
}
