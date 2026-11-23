package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// ensureEventLog gives the events table from migration 0004 the scoped-log
// shape used by Append and ReadAfter: a scope column, a type column and the
// single working index on (scope, seq). Migration 0004 owns the original table
// and other store components keep writing it with (watch_id, schema_version,
// payload, created_at), so this adds columns instead of replacing the table. It
// runs at every Open and is idempotent: SQLite has no ADD COLUMN IF NOT EXISTS,
// so the existing columns are read from PRAGMA table_info first. It also drops
// the legacy events_by_watch index that migration 0004 used to create; dropping
// it keeps older databases on one index as well (BCX-38).
func ensureEventLog(ctx context.Context, db *sql.DB) error {
	cols, err := eventLogColumns(ctx, db)
	if err != nil {
		return err
	}
	if _, ok := cols["scope"]; !ok {
		if _, err := db.ExecContext(ctx, `ALTER TABLE events ADD COLUMN scope TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add scope column: %w", err)
		}
	}
	if _, ok := cols["type"]; !ok {
		if _, err := db.ExecContext(ctx, `ALTER TABLE events ADD COLUMN type TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add type column: %w", err)
		}
	}
	// Append writes watch_id = scope, so the old events_by_watch(watch_id, seq)
	// from migration 0004 led with the same value as events_by_scope and made
	// every insert write two indexes. Fresh databases no longer get it from the
	// migration; drop it from databases created by an older build (BCX-38).
	if _, err := db.ExecContext(ctx, `DROP INDEX IF EXISTS events_by_watch`); err != nil {
		return fmt.Errorf("drop legacy watch index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS events_by_scope ON events(scope, seq)`); err != nil {
		return fmt.Errorf("create replay index: %w", err)
	}
	return nil
}

// eventLogColumns returns the column names of the events table as seen through
// db. Used at initialization to make the schema change repeatable.
func eventLogColumns(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	return tableColumns(db, "events")
}

// LogEvent is one committed row of the scoped event log. Seq is assigned by
// SQLite at insert time and grows on every append, so the last seq a reader
// saw doubles as a replay cursor. SchemaVersion is stamped on the row at
// append time and selects the decoder for Payload; it is never rewritten.
type LogEvent struct {
	Seq           int64
	Scope         string
	Type          string
	SchemaVersion int
	Payload       []byte
	CreatedAt     int64
}

// Append writes one event to scope and returns its seq. Payload is redacted
// before it reaches the file and is stamped with the current schema version,
// so a later replay can decode it without guessing (ADR 0012). Two appends in
// a row get consecutive seq values because the table is append-only.
func (d *DB) Append(scope, eventType string, payload []byte) (int64, error) {
	if strings.TrimSpace(scope) == "" {
		return 0, domain.NewAppError(domain.CodeInvalidInput, "store: event: empty scope")
	}
	if strings.TrimSpace(eventType) == "" {
		return 0, domain.NewAppError(domain.CodeInvalidInput, "store: event: empty type")
	}
	clean := Redact(payload)
	var seq int64
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			INSERT INTO events (scope, type, watch_id, schema_version, payload, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			scope, eventType, scope, currentEventSchemaVersion, clean, time.Now().UTC().UnixNano())
		if err != nil {
			return fmt.Errorf("store: event: insert: %w", err)
		}
		seq, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("store: event: last insert id: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// ReadAfter returns every event in scope with a seq greater than cursor, in
// seq order. Cursor 0 reads the whole scope; a cursor past the last seq yields
// an empty slice, which is how a new subscriber starts and a reconnecting one
// resumes without redelivering events it already saw.
func (d *DB) ReadAfter(scope string, cursor int64) ([]LogEvent, error) {
	if strings.TrimSpace(scope) == "" {
		return nil, domain.NewAppError(domain.CodeInvalidInput, "store: event: empty scope")
	}
	rows, err := d.readDB.Query(`
		SELECT seq, scope, type, schema_version, payload, created_at
		FROM events WHERE scope = ? AND seq > ? ORDER BY seq`, scope, cursor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogEvent
	for rows.Next() {
		var e LogEvent
		if err := rows.Scan(&e.Seq, &e.Scope, &e.Type, &e.SchemaVersion, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
