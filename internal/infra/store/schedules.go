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

// ErrScheduleNotFound and ErrScheduleExists mirror the server port's schedule
// errors. The store layer cannot import server, so they are distinct sentinel
// values; the wiring adapter in cmd/beacon translates them to the port errors.
var (
	ErrScheduleNotFound = errors.New("store: schedules: schedule not found")
	ErrScheduleExists   = errors.New("store: schedules: schedule already exists")
)

// The kinds of schedule the scheduler understands. The port calls a
// mark-triggered schedule "mark"; the store keeps it as "webhook" so reads
// return one canonical value for both spellings.
const (
	ScheduleKindInterval = "interval"
	ScheduleKindCron     = "cron"
	ScheduleKindWebhook  = "webhook"
	ScheduleKindMark     = "mark"
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
// which DueSchedules treats as "nothing is due". The index serves the
// (enabled, next_run) predicate and the next_run, id order of DueSchedules;
// an existing database gets it on the next schedule write.
const createSchedulesSQL = `
CREATE TABLE IF NOT EXISTS schedules (
  id          TEXT PRIMARY KEY,
  kind        TEXT NOT NULL,
  spec        TEXT NOT NULL,
  target_card TEXT NOT NULL,
  next_run    INTEGER NOT NULL,
  enabled     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS schedules_by_enabled_next_run ON schedules(enabled, next_run);
`

// normalizeScheduleKind maps a port-facing kind to its canonical stored form.
// "mark" and "webhook" are two spellings of the same schedule kind, so both
// are stored as "webhook" and reads always return that single value.
func normalizeScheduleKind(kind string) string {
	if kind == ScheduleKindMark {
		return ScheduleKindWebhook
	}
	return kind
}

// validScheduleKind reports whether kind is one of the supported schedule
// kinds, after normalizing the port-facing spelling.
func validScheduleKind(kind string) bool {
	switch normalizeScheduleKind(kind) {
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
	s.Kind = normalizeScheduleKind(s.Kind)
	if err := validateSchedule(s); err != nil {
		return err
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
			s.ID, s.Kind, s.Spec, s.TargetCard, timeToNano(s.NextRun), boolToInt(s.Enabled))
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

// validateSchedule checks the fields shared by every write of a full schedule
// row. Get, Delete and SetEnabled only need an id, so they use validateScheduleID.
func validateSchedule(s Schedule) error {
	if err := validateScheduleID(s.ID); err != nil {
		return err
	}
	if !validScheduleKind(s.Kind) {
		return domain.NewAppError(domain.CodeInvalidInput, "store: schedules: invalid kind "+s.Kind)
	}
	if s.NextRun.IsZero() {
		return domain.NewAppError(domain.CodeInvalidInput, "store: schedules: zero next run")
	}
	return nil
}

func validateScheduleID(id string) error {
	if strings.TrimSpace(id) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: schedules: empty id")
	}
	return nil
}

// normalizeScheduleCtx keeps QueryRowContext and QueryContext safe when a
// caller passes a nil context. DB.Write already does this for writes.
func normalizeScheduleCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// scheduleRowQuerier is satisfied by both *sql.DB and *sql.Tx, so Get and
// SetEnabled share the single-row scan without letting the read escape its
// transaction.
type scheduleRowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const selectScheduleSQL = `SELECT id, kind, spec, target_card, next_run, enabled FROM schedules WHERE id = ?`

func scanScheduleRow(ctx context.Context, q scheduleRowQuerier, id string) (Schedule, error) {
	var s Schedule
	var nextRun int64
	var enabled int
	err := q.QueryRowContext(ctx, selectScheduleSQL, id).Scan(
		&s.ID, &s.Kind, &s.Spec, &s.TargetCard, &nextRun, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, fmt.Errorf("%w: %s", ErrScheduleNotFound, id)
	}
	if err != nil {
		return Schedule{}, err
	}
	s.NextRun = timeFromNano(nextRun)
	s.Enabled = enabled != 0
	return s, nil
}

// CreateSchedule inserts a new schedule. An existing id is refused with
// ErrScheduleExists, so creating is not an upsert: SaveSchedule remains the
// explicit replace operation.
func (d *DB) CreateSchedule(ctx context.Context, s Schedule) error {
	if d == nil {
		return errors.New("store: schedules: nil db")
	}
	s.Kind = normalizeScheduleKind(s.Kind)
	if err := validateSchedule(s); err != nil {
		return err
	}
	ctx = normalizeScheduleCtx(ctx)
	return d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createSchedulesSQL); err != nil {
			return fmt.Errorf("store: schedules: create table: %w", err)
		}
		res, err := tx.Exec(`
			INSERT INTO schedules (id, kind, spec, target_card, next_run, enabled)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO NOTHING`,
			s.ID, s.Kind, s.Spec, s.TargetCard, timeToNano(s.NextRun), boolToInt(s.Enabled))
		if err != nil {
			return fmt.Errorf("store: schedules: create: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: schedules: create rows: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: %s", ErrScheduleExists, s.ID)
		}
		return nil
	})
}

// GetSchedule returns the row with id. An unknown id reports ErrScheduleNotFound.
func (d *DB) GetSchedule(ctx context.Context, id string) (Schedule, error) {
	if d == nil {
		return Schedule{}, errors.New("store: schedules: nil db")
	}
	if err := validateScheduleID(id); err != nil {
		return Schedule{}, err
	}
	ctx = normalizeScheduleCtx(ctx)
	ok, err := tableExists(ctx, d.readDB, "schedules")
	if err != nil {
		return Schedule{}, err
	}
	if !ok {
		return Schedule{}, fmt.Errorf("%w: %s", ErrScheduleNotFound, id)
	}
	return scanScheduleRow(ctx, d.readDB, id)
}

// ListSchedules returns every stored row ordered by id. A store with no
// schedules table returns an empty slice, like DueSchedules.
func (d *DB) ListSchedules(ctx context.Context) ([]Schedule, error) {
	if d == nil {
		return nil, errors.New("store: schedules: nil db")
	}
	ctx = normalizeScheduleCtx(ctx)
	ok, err := tableExists(ctx, d.readDB, "schedules")
	if err != nil || !ok {
		return []Schedule{}, err
	}
	rows, err := d.readDB.QueryContext(ctx, `
		SELECT id, kind, spec, target_card, next_run, enabled FROM schedules
		ORDER BY id`)
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

// UpdateSchedule replaces the whole row for an existing id. An unknown id
// reports ErrScheduleNotFound, so an update never inserts a new row.
func (d *DB) UpdateSchedule(ctx context.Context, s Schedule) error {
	if d == nil {
		return errors.New("store: schedules: nil db")
	}
	s.Kind = normalizeScheduleKind(s.Kind)
	if err := validateSchedule(s); err != nil {
		return err
	}
	ctx = normalizeScheduleCtx(ctx)
	return d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createSchedulesSQL); err != nil {
			return fmt.Errorf("store: schedules: create table: %w", err)
		}
		res, err := tx.Exec(`
			UPDATE schedules SET kind = ?, spec = ?, target_card = ?, next_run = ?, enabled = ?
			WHERE id = ?`,
			s.Kind, s.Spec, s.TargetCard, timeToNano(s.NextRun), boolToInt(s.Enabled), s.ID)
		if err != nil {
			return fmt.Errorf("store: schedules: update: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: schedules: update rows: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: %s", ErrScheduleNotFound, s.ID)
		}
		return nil
	})
}

// DeleteSchedule removes id permanently. A second delete reports
// ErrScheduleNotFound, so deletion is not an idempotent no-op.
func (d *DB) DeleteSchedule(ctx context.Context, id string) error {
	if d == nil {
		return errors.New("store: schedules: nil db")
	}
	if err := validateScheduleID(id); err != nil {
		return err
	}
	ctx = normalizeScheduleCtx(ctx)
	return d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createSchedulesSQL); err != nil {
			return fmt.Errorf("store: schedules: create table: %w", err)
		}
		res, err := tx.Exec(`DELETE FROM schedules WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: schedules: delete: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: schedules: delete rows: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: %s", ErrScheduleNotFound, id)
		}
		return nil
	})
}

// SetScheduleEnabled flips id's Enabled flag and returns the updated row in one
// write, so a concurrent Pause and Resume cannot read the same row and write it
// back over each other. An unknown id reports ErrScheduleNotFound.
func (d *DB) SetScheduleEnabled(ctx context.Context, id string, enabled bool) (Schedule, error) {
	if d == nil {
		return Schedule{}, errors.New("store: schedules: nil db")
	}
	if err := validateScheduleID(id); err != nil {
		return Schedule{}, err
	}
	ctx = normalizeScheduleCtx(ctx)
	var out Schedule
	err := d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createSchedulesSQL); err != nil {
			return fmt.Errorf("store: schedules: create table: %w", err)
		}
		res, err := tx.Exec(`UPDATE schedules SET enabled = ? WHERE id = ?`,
			boolToInt(enabled), id)
		if err != nil {
			return fmt.Errorf("store: schedules: set enabled: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: schedules: set enabled rows: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: %s", ErrScheduleNotFound, id)
		}
		row, err := scanScheduleRow(ctx, tx, id)
		if err != nil {
			return err
		}
		out = row
		return nil
	})
	if err != nil {
		return Schedule{}, err
	}
	return out, nil
}

func timeFromNano(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}
