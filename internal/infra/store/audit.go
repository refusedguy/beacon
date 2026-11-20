// Package store: audit log of privileged commands (BS.22, ADR 0188).
//
// Every privileged command carries the actor who issued it and the ingress it
// arrived on. The three ingresses are mcp, tui and webhook. An audit row is
// written through RecordAuditTx in the caller's transaction, so it commits or
// rolls back together with the command it describes. The HTTP reader of the
// log belongs to another card; this file owns only the store side.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// AuditIngress is the transport a privileged command arrived on. An unknown
// value is rejected on write instead of being stored as an anonymous entry.
type AuditIngress string

const (
	AuditIngressMCP     AuditIngress = "mcp"
	AuditIngressTUI     AuditIngress = "tui"
	AuditIngressWebhook AuditIngress = "webhook"
)

// AuditEntry is one privileged command with its actor and ingress. At is set
// by the caller when it is known; a zero value is filled with the current UTC
// time at insert. A merge or a wake is an action, not a special row type: each
// is recorded with the actor that triggered it.
type AuditEntry struct {
	Actor     string       `json:"actor"`
	Ingress   AuditIngress `json:"ingress"`
	CommandID string       `json:"commandID"`
	Action    string       `json:"action"`
	At        time.Time    `json:"at"`
}

// AuditFilter narrows ListAudit. Empty fields mean any: an empty Actor and an
// empty Ingress list every entry, and the two set fields combine with AND.
// Time bounds and pagination belong to the API layer, not to the store.
type AuditFilter struct {
	Actor   string
	Ingress AuditIngress
}

// createAuditSQL owns the audit table. Like the other tables owned by a
// single card, it is created on first use instead of in the migration corpus.
// The indexes serve the two required filters. The TIMESTAMP column stores the
// UTC instant as Unix nanoseconds, as the other timestamp columns in this
// package do.
const createAuditSQL = `
CREATE TABLE IF NOT EXISTS audit (
  actor      TEXT      NOT NULL,
  ingress    TEXT      NOT NULL,
  command_id TEXT      NOT NULL,
  action     TEXT      NOT NULL,
  at         TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_by_actor ON audit(actor, at);
CREATE INDEX IF NOT EXISTS audit_by_ingress ON audit(ingress, at);
`

// validAuditIngress reports whether in is one of the three known ingresses.
func validAuditIngress(in AuditIngress) bool {
	switch in {
	case AuditIngressMCP, AuditIngressTUI, AuditIngressWebhook:
		return true
	default:
		return false
	}
}

// validateAuditEntry enforces the card's rule that every privileged command
// has an author. It also requires a command id, an action and a known
// ingress, so an anonymous or unclassified entry can never enter the log.
func validateAuditEntry(e AuditEntry) error {
	if strings.TrimSpace(e.Actor) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: audit: empty actor")
	}
	if !validAuditIngress(e.Ingress) {
		return domain.NewAppError(domain.CodeInvalidInput,
			fmt.Sprintf("store: audit: unknown ingress %q", e.Ingress))
	}
	if strings.TrimSpace(e.CommandID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: audit: empty command id")
	}
	if strings.TrimSpace(e.Action) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: audit: empty action")
	}
	return nil
}

// ensureAudit creates the audit table and its indexes inside tx. Writes that
// already hold a transaction call this through RecordAuditTx, so the DDL and
// the row are part of the same commit as the command being audited.
func ensureAudit(tx *sql.Tx) error {
	if _, err := tx.Exec(createAuditSQL); err != nil {
		return fmt.Errorf("store: audit: create table: %w", err)
	}
	return nil
}

// RecordAuditTx inserts one audit entry into tx. This is the primitive that
// makes the log transactional: a caller records a merge or a wake beside the
// state change it performs, and if the transaction rolls back, no audit row
// survives. A zero At is replaced with the current UTC time.
func RecordAuditTx(tx *sql.Tx, e AuditEntry) error {
	if tx == nil {
		return domain.NewAppError(domain.CodeInvalidInput, "store: audit: nil transaction")
	}
	if err := validateAuditEntry(e); err != nil {
		return err
	}
	if err := ensureAudit(tx); err != nil {
		return err
	}
	at := e.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if _, err := tx.Exec(`
		INSERT INTO audit (actor, ingress, command_id, action, at)
		VALUES (?, ?, ?, ?, ?)`,
		e.Actor, string(e.Ingress), e.CommandID, e.Action, at.UTC().UnixNano(),
	); err != nil {
		return fmt.Errorf("store: audit: insert: %w", err)
	}
	return nil
}

// RecordAudit writes one entry in its own transaction through the single
// writer. Callers that need the row to commit with another change use
// RecordAuditTx instead.
func (d *DB) RecordAudit(e AuditEntry) error {
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		return RecordAuditTx(tx, e)
	})
}

// ListAudit returns the committed entries matching f, oldest first. A store
// that never recorded an entry reports an empty list and does not create the
// table: an audit read never writes.
func (d *DB) ListAudit(f AuditFilter) ([]AuditEntry, error) {
	ok, err := tableExists(context.Background(), d.readDB, "audit")
	if err != nil || !ok {
		return []AuditEntry{}, err
	}

	var (
		query string
		args  []any
	)
	switch {
	case f.Actor != "" && f.Ingress != "":
		query = `SELECT actor, ingress, command_id, action, at FROM audit
			WHERE actor = ? AND ingress = ? ORDER BY at, rowid`
		args = []any{f.Actor, string(f.Ingress)}
	case f.Actor != "":
		query = `SELECT actor, ingress, command_id, action, at FROM audit
			WHERE actor = ? ORDER BY at, rowid`
		args = []any{f.Actor}
	case f.Ingress != "":
		query = `SELECT actor, ingress, command_id, action, at FROM audit
			WHERE ingress = ? ORDER BY at, rowid`
		args = []any{string(f.Ingress)}
	default:
		query = `SELECT actor, ingress, command_id, action, at FROM audit ORDER BY at, rowid`
	}

	rows, err := d.readDB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AuditEntry{}
	for rows.Next() {
		var (
			e       AuditEntry
			ingress string
			atNano  int64
		)
		if err := rows.Scan(&e.Actor, &ingress, &e.CommandID, &e.Action, &atNano); err != nil {
			return nil, err
		}
		e.Ingress = AuditIngress(ingress)
		e.At = time.Unix(0, atNano).UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// AuditCount returns the number of stored entries. It is the direct check
// that a rolled-back transaction left no row behind.
func (d *DB) AuditCount() (int, error) {
	ok, err := tableExists(context.Background(), d.readDB, "audit")
	if err != nil || !ok {
		return 0, err
	}
	var n int
	err = d.readDB.QueryRow(`SELECT count(*) FROM audit`).Scan(&n)
	return n, err
}
