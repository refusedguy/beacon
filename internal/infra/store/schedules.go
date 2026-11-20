// Package store: durable schedules (BS.33, ADR 0192).
//
// A schedule survives a restart as one row in the schedules table. The table
// is created on first use, like the other tables in this package, so no
// migration file is needed. next_run is stored as Unix nanoseconds, the same
// representation the rest of the store uses for time. A schedule is "due"
// when it is enabled and its next_run has passed. MarkTriggered moves
// next_run forward so an overdue schedule is performed once per period rather
// than on every cycle that observes the overdue row.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// The three kinds of schedule the scheduler understands.
const (
	ScheduleKindInterval = "interval"
	ScheduleKindCron     = "cron"
	ScheduleKindWebhook  = "webhook"
)

// Schedule is one persisted schedule row. NextRun is the absolute time at
// which the schedule next becomes due; zero is never stored.
type Schedule struct {
	ID         string
	Kind       string // interval, cron or webhook
	Spec       string
	TargetCard string
	NextRun    time.Time
	Enabled    bool
}

// createSchedulesSQL is issued on first use by every method that can create
// the table. A store that has never written a schedule simply has no table,
// which DueSchedules treats as "nothing is due".
const createSchedulesSQL = `
CREATE TABLE IF NOT EXISTS schedules (
  id          TEXT PRIMARY KEY,
  kind        TEXT NOT NULL,
  spec        TEXT NOT NULL,
  target_card TEXT NOT NULL,
  next_run    INTEGER NOT NULL,
  enabled     INTEGER NOT NULL
);
`

// validScheduleKind reports whether kind is one of the supported schedule
// kinds.
func validScheduleKind(kind string) bool {
	switch kind {
	case ScheduleKindInterval, ScheduleKindCron, ScheduleKindWebhook:
		return true
	default:
		return false
	}
}

// SaveSchedule inserts s, or replaces the row with the same id when one
// already exists. The whole row is overwritten, including NextRun and
// Enabled, so a caller can both create and re-arm or disable a schedule
// through this method.
func (d *DB) SaveSchedule(s Schedule) error {
	if d == nil {
		return errors.New("store: schedules: nil db")
	}
	if strings.TrimSpace(s.ID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: schedules: empty id")
	}
	if !validScheduleKind(s.Kind) {
		return domain.NewAppError(domain.CodeInvalidInput, "store: schedules: invalid kind "+s.Kind)
	}
	if s.NextRun.IsZero() {
		return domain.NewAppError(domain.CodeInvalidInput, "store: schedules: zero next run")
	}
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createSchedulesSQL); err != nil {
			return fmt.Errorf("store: schedules: create table: %w", err)
		}
		_, err := tx.Exec(`
			INSERT INTO schedules (id, kind, spec, target_card, next_run, enabled)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
			  kind = excluded.kind,
			  spec = excluded.spec,
			  target_card = excluded.target_card,
			  next_run = excluded.next_run,
			  enabled = excluded.enabled`,
			s.ID, s.Kind, s.Spec, s.TargetCard, timeToNano(s.NextRun), scheduleBoolToInt(s.Enabled))
		if err != nil {
			return fmt.Errorf("store: schedules: save: %w", err)
		}
		return nil
	})
}

// DueSchedules returns every enabled schedule whose next_run is at or before
// now, ordered by next_run then id so the caller can process them in a stable
// order. An empty store, or a store with no due schedules, returns an empty
// slice and no error.
func (d *DB) DueSchedules(now time.Time) ([]Schedule, error) {
	if d == nil {
		return nil, errors.New("store: schedules: nil db")
	}
	if now.IsZero() {
		return nil, errors.New("store: schedules: zero now")
	}
	ctx := context.Background()
	ok, err := tableExists(ctx, d.readDB, "schedules")
	if err != nil || !ok {
		return nil, err
	}
	rows, err := d.readDB.QueryContext(ctx, `
		SELECT id, kind, spec, target_card, next_run, enabled FROM schedules
		WHERE enabled = 1 AND next_run <= ?
		ORDER BY next_run, id`, timeToNano(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		var s Schedule
		var nextRun int64
		var enabled int
		if err := rows.Scan(&s.ID, &s.Kind, &s.Spec, &s.TargetCard, &nextRun, &enabled); err != nil {
			return nil, err
		}
		s.NextRun = timeFromNano(nextRun)
		s.Enabled = enabled != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// MarkTriggered moves id's next_run to nextRun. It is the acknowledgement a
// caller writes after performing a due schedule: once next_run is in the
// future the same overdue row is no longer returned by DueSchedules, so it is
// performed once per period. It returns an error when no schedule has that id.
func (d *DB) MarkTriggered(id string, nextRun time.Time) error {
	if d == nil {
		return errors.New("store: schedules: nil db")
	}
	if strings.TrimSpace(id) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: schedules: empty id")
	}
	if nextRun.IsZero() {
		return domain.NewAppError(domain.CodeInvalidInput, "store: schedules: zero next run")
	}
	ctx := context.Background()
	return d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createSchedulesSQL); err != nil {
			return fmt.Errorf("store: schedules: create table: %w", err)
		}
		res, err := tx.Exec(`UPDATE schedules SET next_run = ? WHERE id = ?`,
			timeToNano(nextRun), id)
		if err != nil {
			return fmt.Errorf("store: schedules: mark triggered: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: schedules: mark triggered rows: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("store: schedules: unknown id %q", id)
		}
		return nil
	})
}

// DisableSchedule sets id's enabled flag to false, taking it out of the due
// list without deleting its configuration or next_run. It returns an error
// when no schedule has that id.
func (d *DB) DisableSchedule(id string) error {
	if d == nil {
		return errors.New("store: schedules: nil db")
	}
	if strings.TrimSpace(id) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: schedules: empty id")
	}
	ctx := context.Background()
	return d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createSchedulesSQL); err != nil {
			return fmt.Errorf("store: schedules: create table: %w", err)
		}
		res, err := tx.Exec(`UPDATE schedules SET enabled = 0 WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: schedules: disable: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: schedules: disable rows: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("store: schedules: unknown id %q", id)
		}
		return nil
	})
}

func scheduleBoolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func timeFromNano(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}
