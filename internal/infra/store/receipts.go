// Package store: import of adapter receipts from receipts.jsonl.
//
// ADR 0014 keeps the receipt file as the boundary with the OpenCode plugin:
// the plugin appends one line per event and attempt, Beacon reads only whole
// lines. ADR 0015 moves the fact into SQLite in one transaction. This file
// owns the receipts table (the migration corpus belongs to T1.7, so the table
// is created on first use, as in archive.go and observability.go).
//
// Idempotency key is (obligation_id, attempt). A repeated import of the same
// file inserts no rows and applies no second effect to an obligation. Only
// "posted" produces deliveredAt; "transport-failed" and "indeterminate"
// leave the obligation pending.
package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/github"
)

const createReceiptsSQL = `
CREATE TABLE IF NOT EXISTS receipts (
  seq           INTEGER PRIMARY KEY,
  obligation_id TEXT NOT NULL,
  attempt       INTEGER NOT NULL,
  outcome       TEXT NOT NULL,
  at            INTEGER NOT NULL,
  raw           BLOB NOT NULL,
  imported_at   INTEGER NOT NULL,
  source        TEXT NOT NULL DEFAULT '',
  UNIQUE (obligation_id, attempt)
);
CREATE INDEX IF NOT EXISTS receipts_by_obligation ON receipts(obligation_id, seq);
`

// Receipt is one whole line of receipts.jsonl. The wire shape is
// {obligationID, attempt, outcome, at}; at accepts an RFC3339 string or a
// unix timestamp, so a plugin using either convention is read the same way.
type Receipt struct {
	ObligationID string                `json:"obligationID"`
	Attempt      int                   `json:"attempt"`
	Outcome      domain.AttemptOutcome `json:"outcome"`
	At           time.Time             `json:"at"`

	// Source is the explicit, typed place the result was read from: a branch,
	// a PR or a file in a worktree (ADR 0027). It is validated on import, so
	// no private agent log path can enter the receipts table. Older lines
	// without a source keep the zero value and still import.
	Source github.ArtifactSource `json:"source"`
}

// UnmarshalJSON parses and validates one receipt line. An unrecognized
// outcome is rejected rather than guessed: an unknown outcome must never be
// turned into a delivery fact.
func (r *Receipt) UnmarshalJSON(data []byte) error {
	var wire struct {
		ObligationID string          `json:"obligationID"`
		Attempt      json.Number     `json:"attempt"`
		Outcome      string          `json:"outcome"`
		At           json.RawMessage `json:"at"`
		Source       json.RawMessage `json:"source"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&wire); err != nil {
		return fmt.Errorf("receipt: decode: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("receipt: trailing data")
	}

	r.ObligationID = strings.TrimSpace(wire.ObligationID)
	if r.ObligationID == "" {
		return errors.New("receipt: empty obligationID")
	}
	if wire.Attempt.String() == "" {
		return errors.New("receipt: missing attempt")
	}
	attempt, err := strconv.Atoi(wire.Attempt.String())
	if err != nil || attempt < 0 {
		return fmt.Errorf("receipt: bad attempt %q", wire.Attempt.String())
	}
	r.Attempt = attempt

	outcome := domain.AttemptOutcome(strings.ToLower(strings.TrimSpace(wire.Outcome)))
	switch outcome {
	case domain.OutcomePosted, domain.OutcomeTransportFailed, domain.OutcomeIndeterminate:
		r.Outcome = outcome
	default:
		return fmt.Errorf("receipt: unknown outcome %q", wire.Outcome)
	}

	r.At, err = parseReceiptTime(wire.At)
	if err != nil {
		return err
	}
	if len(wire.Source) > 0 && !bytes.Equal(bytes.TrimSpace(wire.Source), []byte("null")) {
		if err := json.Unmarshal(wire.Source, &r.Source); err != nil {
			return fmt.Errorf("receipt: bad source: %w", err)
		}
	}
	return nil
}

// parseReceiptTime accepts the two timestamp shapes the plugin may write.
// Empty or invalid values are an error: a fact without a time is not a fact.
func parseReceiptTime(raw json.RawMessage) (time.Time, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return time.Time{}, errors.New("receipt: missing at")
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return time.Time{}, fmt.Errorf("receipt: bad at: %w", err)
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return time.Time{}, fmt.Errorf("receipt: bad at %q: %w", s, err)
		}
		return t.UTC(), nil
	}
	var n int64
	if err := json.Unmarshal(trimmed, &n); err != nil {
		return time.Time{}, fmt.Errorf("receipt: bad at: %w", err)
	}
	switch {
	case n >= 1_000_000_000_000_000_000:
		return time.Unix(0, n).UTC(), nil
	case n >= 1_000_000_000_000:
		return time.UnixMilli(n).UTC(), nil
	default:
		return time.Unix(n, 0).UTC(), nil
	}
}

// ImportReceiptsResult reports one import pass over a receipts file.
type ImportReceiptsResult struct {
	Lines       int  // complete, non-empty lines considered
	Imported    int  // new (obligationID, attempt) pairs stored
	Duplicates  int  // pairs already stored, no second effect applied
	Malformed   int  // complete lines that failed validation
	Missing     int  // receipts with no matching obligation
	PartialTail bool // trailing bytes without a newline were ignored
}

// ImportReceipts reads path, then inserts every whole line and updates the
// matching obligations in one transaction. A missing file is a no-op: no
// receipt means indeterminate, not failed. A final line without a newline is
// ignored, as are blank and malformed lines; neither stops the other lines.
func (d *DB) ImportReceipts(path string) (ImportReceiptsResult, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ImportReceiptsResult{}, nil
	}
	if err != nil {
		return ImportReceiptsResult{}, fmt.Errorf("store: receipts: read %s: %w", path, err)
	}

	rawLines, partial := splitCompleteLines(data)
	result := ImportReceiptsResult{PartialTail: partial}

	type entry struct {
		receipt Receipt
		raw     []byte
	}
	entries := make([]entry, 0, len(rawLines))
	for _, raw := range rawLines {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		result.Lines++
		var receipt Receipt
		if err := json.Unmarshal(line, &receipt); err != nil {
			result.Malformed++
			continue
		}
		entries = append(entries, entry{receipt: receipt, raw: line})
	}

	err = d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createReceiptsSQL); err != nil {
			return fmt.Errorf("store: receipts: create table: %w", err)
		}
		if err := ensureReceiptSourceColumn(tx); err != nil {
			return err
		}
		for _, e := range entries {
			inserted, err := insertReceipt(tx, e.receipt, e.raw)
			if err != nil {
				return err
			}
			if !inserted {
				result.Duplicates++
				continue
			}
			result.Imported++
			updated, err := applyReceipt(tx, e.receipt)
			if err != nil {
				return err
			}
			if !updated {
				result.Missing++
			}
		}
		return nil
	})
	if err != nil {
		return ImportReceiptsResult{}, err
	}
	return result, nil
}

// splitCompleteLines returns only lines terminated by a newline. The trailing
// fragment is reported separately and never parsed.
func splitCompleteLines(data []byte) ([][]byte, bool) {
	if len(data) == 0 {
		return nil, false
	}
	rest := data
	partial := false
	if rest[len(rest)-1] != '\n' {
		i := bytes.LastIndexByte(rest, '\n')
		if i < 0 {
			return nil, true
		}
		rest = rest[:i+1]
		partial = true
	}
	var lines [][]byte
	for _, line := range bytes.Split(rest, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) == 0 {
			continue
		}
		lines = append(lines, line)
	}
	return lines, partial
}

// ensureReceiptSourceColumn adds the source column to a receipts table made
// by an earlier build. The table is created on first use here, so its additive
// upgrade belongs here too instead of in the migration corpus.
func ensureReceiptSourceColumn(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA table_info(receipts)`)
	if err != nil {
		return fmt.Errorf("store: receipts: table info: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid      int
			name     string
			typ      string
			notNull  int
			defaultV any
			primary  int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultV, &primary); err != nil {
			return fmt.Errorf("store: receipts: scan table info: %w", err)
		}
		if strings.EqualFold(name, "source") {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: receipts: table info rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: receipts: close table info: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE receipts ADD COLUMN source TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("store: receipts: add source column: %w", err)
	}
	return nil
}

// insertReceipt stores a receipt only when the idempotency pair is new. The
// RowsAffected result decides whether the obligation gets a second effect.
// Only a validated artifact source is stored; no agent log path reaches here.
func insertReceipt(tx *sql.Tx, r Receipt, raw []byte) (bool, error) {
	source := ""
	if r.Source.Kind != "" {
		encoded, err := json.Marshal(r.Source)
		if err != nil {
			return false, fmt.Errorf("store: receipts: encode source: %w", err)
		}
		source = string(encoded)
	}
	res, err := tx.Exec(`
		INSERT OR IGNORE INTO receipts
		  (obligation_id, attempt, outcome, at, raw, imported_at, source)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ObligationID, r.Attempt, string(r.Outcome), r.At.UnixNano(),
		Redact(raw), time.Now().UTC().UnixNano(), source)
	if err != nil {
		return false, fmt.Errorf("store: receipts: insert: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: receipts: rows affected: %w", err)
	}
	return n == 1, nil
}

// applyReceipt updates the obligation named by the receipt. Posted sets
// deliveredAt once; other outcomes only record the attempt. The attempt list
// is deduplicated by number, so a repeated pair cannot grow it.
func applyReceipt(tx *sql.Tx, r Receipt) (bool, error) {
	var payload []byte
	err := tx.QueryRow(`SELECT payload FROM obligations WHERE id = ?`, r.ObligationID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: receipts: obligation %s: %w", r.ObligationID, err)
	}
	var o domain.Obligation
	if err := json.Unmarshal(payload, &o); err != nil {
		return false, fmt.Errorf("store: receipts: decode obligation %s: %w", r.ObligationID, err)
	}

	if r.Outcome == domain.OutcomePosted && o.DeliveredAt.IsZero() {
		o.DeliveredAt = r.At
	}
	seen := false
	for _, a := range o.Attempts {
		if a.N == r.Attempt {
			seen = true
			break
		}
	}
	if !seen {
		o.Attempts = append(o.Attempts, domain.Attempt{
			N:       r.Attempt,
			At:      r.At,
			Outcome: r.Outcome,
		})
	}

	updated, err := json.Marshal(o)
	if err != nil {
		return false, fmt.Errorf("store: receipts: encode obligation %s: %w", r.ObligationID, err)
	}
	delivered := 0
	if !o.DeliveredAt.IsZero() {
		delivered = 1
	}
	if _, err := tx.Exec(`
		UPDATE obligations SET delivered_at = ?, payload = ? WHERE id = ?`,
		delivered, updated, r.ObligationID); err != nil {
		return false, fmt.Errorf("store: receipts: update obligation %s: %w", r.ObligationID, err)
	}
	return true, nil
}

// Receipts returns the stored receipts for one obligation, oldest first.
func (d *DB) Receipts(obligationID string) ([]Receipt, error) {
	ok, err := tableExists(context.Background(), d.readDB, "receipts")
	if err != nil || !ok {
		return nil, err
	}
	rows, err := d.readDB.Query(`
		SELECT obligation_id, attempt, outcome, at, source FROM receipts
		WHERE obligation_id = ? ORDER BY seq`, obligationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Receipt
	for rows.Next() {
		var (
			r      Receipt
			atNano int64
			source string
		)
		if err := rows.Scan(&r.ObligationID, &r.Attempt, &r.Outcome, &atNano, &source); err != nil {
			return nil, err
		}
		r.At = time.Unix(0, atNano).UTC()
		if source != "" {
			if err := json.Unmarshal([]byte(source), &r.Source); err != nil {
				return nil, fmt.Errorf("store: receipts: decode source: %w", err)
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReceiptCount returns the number of stored receipts. It is the direct check
// that a repeated import did not duplicate rows.
func (d *DB) ReceiptCount() (int, error) {
	ok, err := tableExists(context.Background(), d.readDB, "receipts")
	if err != nil || !ok {
		return 0, err
	}
	var n int
	err = d.readDB.QueryRow(`SELECT count(*) FROM receipts`).Scan(&n)
	return n, err
}
