// Package store: redaction and journal schema versioning.
//
// ADR 0012 asks that every write into state pass through one redaction filter
// and that each journal row carry a schema version. ADR 0015 moves the journal
// into SQLite. This file owns that single filter and the journal write path:
// RedactingWriter wraps the one writer from T1.7, removes secrets from a
// payload, stamps the current schema version and inserts one event row.
// Reads select a decoder by the row version, so a row from an older corpus
// fixture stays readable.
package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"modernc.org/sqlite"
)

// redactingDriverName is the SQL driver used by OpenProduction. It wraps
// modernc's driver instead of replacing the global "sqlite" registration, so
// a plain Open keeps its previous byte-for-byte behavior.
const redactingDriverName = "sqlite-redact"

func init() {
	sql.Register(redactingDriverName, redactingDriver{base: &sqlite.Driver{}})
}

// redactingDriver hands out connections whose bound parameters are redacted.
// The filter sits below database/sql, so no write path in this package or in
// any caller can insert a secret into events, watches, obligations,
// blocker_incidents or runtime_stats without passing through Redact.
type redactingDriver struct{ base driver.Driver }

func (d redactingDriver) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &redactingConn{Conn: c}, nil
}

// redactingConn keeps modernc's full interface set by delegating and adds
// database/sql's argument hook. CheckNamedValue runs for Exec, Query and
// prepared statements alike, so a string or byte-slice bound by any write
// reaches SQLite only in redacted form. Reads also pass through the filter;
// they never store the bound value, so the stored data is unaffected.
type redactingConn struct{ driver.Conn }

func (c *redactingConn) CheckNamedValue(nv *driver.NamedValue) error {
	switch v := nv.Value.(type) {
	case string:
		nv.Value = string(Redact([]byte(v)))
	case []byte:
		nv.Value = Redact(v)
	default:
		// Implementing NamedValueChecker replaces database/sql's default
		// conversion. Keep it, or modernc would see raw int/float values it
		// does not accept.
		converted, err := driver.DefaultParameterConverter.ConvertValue(nv.Value)
		if err != nil {
			return err
		}
		nv.Value = converted
	}
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		if err := checker.CheckNamedValue(nv); err != nil && !errors.Is(err, driver.ErrSkip) {
			return err
		}
	}
	return nil
}

func (c *redactingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, query)
	}
	return c.Conn.Prepare(query)
}

func (c *redactingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if bt, ok := c.Conn.(driver.ConnBeginTx); ok {
		return bt.BeginTx(ctx, opts)
	}
	return c.Conn.Begin()
}

func (c *redactingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if execer, ok := c.Conn.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *redactingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if queryer, ok := c.Conn.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *redactingConn) Ping(ctx context.Context) error {
	if pinger, ok := c.Conn.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

func (c *redactingConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c *redactingConn) IsValid() bool {
	if validator, ok := c.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

var (
	_ driver.Driver             = redactingDriver{}
	_ driver.Conn               = (*redactingConn)(nil)
	_ driver.NamedValueChecker  = (*redactingConn)(nil)
	_ driver.ExecerContext      = (*redactingConn)(nil)
	_ driver.QueryerContext     = (*redactingConn)(nil)
	_ driver.ConnBeginTx        = (*redactingConn)(nil)
	_ driver.ConnPrepareContext = (*redactingConn)(nil)
	_ driver.Pinger             = (*redactingConn)(nil)
	_ driver.SessionResetter    = (*redactingConn)(nil)
	_ driver.Validator          = (*redactingConn)(nil)
)

// redacted is the marker written in place of a secret. It is deliberately
// distinct from any real credential so tests can assert its presence.
const redacted = "[REDACTED]"

var (
	// githubTokenRE matches classic tokens (ghp_, gho_, ghu_, ghs_, ghr_) and
	// fine-grained personal access tokens (github_pat_).
	githubTokenRE = regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`)

	// telegramTokenRE matches a Telegram bot token, <digits>:<35 characters>,
	// wherever it appears: a URL path, an error string or a stored payload.
	telegramTokenRE = regexp.MustCompile(`[0-9]+:[A-Za-z0-9_-]{35}`)

	// authHeaderRE matches an Authorization header and its credential, keeping
	// the header name and dropping the scheme plus value.
	authHeaderRE = regexp.MustCompile(`(?i)(authorization["'[:space:]]*[:=]["'[:space:]]*)(?:(?:bearer|token|basic)[[:space:]]+)?[A-Za-z0-9._~+/=-]+`)

	// urlCredsRE matches the user:password part of a URL with a scheme, leaving
	// the host and path intact.
	urlCredsRE = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/@[:space:]:]+:[^/@[:space:]]+@`)
)

// Redact removes GitHub tokens, Telegram bot tokens, Authorization credentials
// and URL credentials from src. It never mutates src and is safe to call on
// arbitrary payload bytes or on a log line before the line leaves the process.
func Redact(src []byte) []byte {
	if len(src) == 0 {
		return append([]byte(nil), src...)
	}
	out := githubTokenRE.ReplaceAll(src, []byte(redacted))
	out = telegramTokenRE.ReplaceAll(out, []byte(redacted))
	out = authHeaderRE.ReplaceAll(out, []byte("${1}"+redacted))
	out = urlCredsRE.ReplaceAll(out, []byte("${1}"+redacted+"@"))
	return out
}

// Writer is the single writer owned by T1.7. The concrete *DB.Write method
// satisfies it. RedactingWriter never opens its own connection: it delegates
// every insert to this writer.
type Writer interface {
	Write(ctx context.Context, fn func(*sql.Tx) error) error
}

// RedactingWriter is the only path for journal writes. It redacts the payload
// and stamps the schema version before handing the insert to the wrapped
// writer, so no journal row can bypass the filter.
type RedactingWriter struct {
	writer  Writer
	version int
}

// NewRedactingWriter wraps w and stamps rows with schemaVersion. It rejects a
// nil writer or a non-positive version: a row without a valid version has no
// decoder.
func NewRedactingWriter(w Writer, schemaVersion int) (*RedactingWriter, error) {
	if w == nil {
		return nil, fmt.Errorf("store: redacting writer: nil writer")
	}
	if schemaVersion <= 0 {
		return nil, fmt.Errorf("store: redacting writer: bad schema version %d", schemaVersion)
	}
	return &RedactingWriter{writer: w, version: schemaVersion}, nil
}

// Version returns the schema version stamped on new rows.
func (rw *RedactingWriter) Version() int { return rw.version }

// WriteEvent redacts payload, stamps the schema version and inserts one event
// row through the single writer. ADR 0015 keeps the journal in SQLite, and the
// events.schema_version column selects the decoder on read.
func (rw *RedactingWriter) WriteEvent(ctx context.Context, watchID string, payload []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	clean := Redact(payload)
	return rw.writer.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO events (watch_id, schema_version, payload, created_at)
			VALUES (?, ?, ?, ?)`,
			watchID, rw.version, clean, time.Now().UnixNano())
		return err
	})
}

// Event is one raw journal row. SchemaVersion is the version written at insert
// time; it, not the current store version, selects the decoder.
type Event struct {
	Seq           int64  `json:"seq"`
	WatchID       string `json:"watchID"`
	SchemaVersion int    `json:"schemaVersion"`
	Payload       []byte `json:"payload"`
}

// DecodedEvent is the normalized form returned to readers. Kind is empty for
// legacy rows that carried no event type.
type DecodedEvent struct {
	Seq           int64
	WatchID       string
	SchemaVersion int
	Kind          string
	Message       string
}

// decodeHandler turns one raw row into the normalized form.
type decodeHandler func(Event) (DecodedEvent, error)

// currentEventSchemaVersion is the newest journal row version this build can
// decode. It matches the user_version of migration 0004_events.sql, the schema
// version stamped on new rows by RedactingWriter. A row carrying a higher
// version was written by a newer store and must not be guessed at: it has no
// handler until a decoder for that version is added.
const currentEventSchemaVersion = 4

// handlerForVersion maps a row schema version to exactly one decoder. Version
// 1 is the pre-migration corpus format: a bare JSON string or a
// {"message": ...} map. Versions 2 through currentEventSchemaVersion use the
// current {"kind": ..., "message": ...} format. Any other version, including
// one from a newer store, has no handler and fails closed.
func handlerForVersion(version int) decodeHandler {
	switch {
	case version == 1:
		return decodeLegacyV1
	case version >= 2 && version <= currentEventSchemaVersion:
		return decodeCurrent
	default:
		return nil
	}
}

// DecodeEvent selects the handler by the row schema version. The row version,
// not the current store version, decides how the payload is read.
func DecodeEvent(row Event) (DecodedEvent, error) {
	handler := handlerForVersion(row.SchemaVersion)
	if handler == nil {
		return DecodedEvent{}, fmt.Errorf(
			"store: no decoder for event schema version %d", row.SchemaVersion)
	}
	return handler(row)
}

func decodeCurrent(row Event) (DecodedEvent, error) {
	var p struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(row.Payload, &p); err != nil {
		return DecodedEvent{}, fmt.Errorf("decode event v%d: %w", row.SchemaVersion, err)
	}
	return DecodedEvent{
		Seq:           row.Seq,
		WatchID:       row.WatchID,
		SchemaVersion: row.SchemaVersion,
		Kind:          p.Kind,
		Message:       p.Message,
	}, nil
}

func decodeLegacyV1(row Event) (DecodedEvent, error) {
	out := DecodedEvent{
		Seq:           row.Seq,
		WatchID:       row.WatchID,
		SchemaVersion: row.SchemaVersion,
		Kind:          "legacy",
	}
	var message string
	if err := json.Unmarshal(row.Payload, &message); err == nil {
		out.Message = message
		return out, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(row.Payload, &m); err != nil {
		return DecodedEvent{}, fmt.Errorf("decode legacy v1 event: %w", err)
	}
	for _, key := range []string{"message", "msg"} {
		if raw, ok := m[key]; ok {
			if err := json.Unmarshal(raw, &out.Message); err != nil {
				return DecodedEvent{}, fmt.Errorf("decode legacy v1 field %s: %w", key, err)
			}
			break
		}
	}
	return out, nil
}

// Querier is the read side used by ReadEvents. Both *sql.DB and *sql.Conn
// satisfy it.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ReadEvents loads and decodes all journal rows for a watch, oldest first.
// The row version, not the store version, decides how each row is decoded, so
// a corpus of old fixtures reads next to current rows without error.
func ReadEvents(ctx context.Context, q Querier, watchID string) ([]DecodedEvent, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT seq, watch_id, schema_version, payload FROM events
		WHERE watch_id = ? ORDER BY seq`, watchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DecodedEvent
	for rows.Next() {
		var row Event
		if err := rows.Scan(&row.Seq, &row.WatchID, &row.SchemaVersion, &row.Payload); err != nil {
			return nil, err
		}
		decoded, err := DecodeEvent(row)
		if err != nil {
			return nil, err
		}
		out = append(out, decoded)
	}
	return out, rows.Err()
}
