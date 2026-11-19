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
  version       TEXT NOT NULL DEFAULT '',
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
	Source ArtifactSource `json:"source"`

	// Version is the label of the agent that saved the receipt (ADR 0034 §3).
	// It travels with the receipt so a long-pending approval is read with the
	// configuration that produced it, not with today's configuration. Older
	// lines without a version keep the zero value and still import.
	Version AgentVersion `json:"version"`

	// Verified reports whether the result passed its CI check (ADR 0040 §5,
	// ADR 0022). Only a verified result is stored in results and published as
	// delivered. An explicit false keeps the result inside the task: import
	// stores no row and applies no deliveredAt, so wait_result stays pending.
	// A receipt written before this flag existed has no verified field and is
	// treated as verified, so an older receipts.jsonl still imports unchanged.
	Verified bool `json:"verified"`
}

// AgentVersion is the version label of the agent that saved a receipt. It
// fixes the model, prompt and tool set at save time so a snapshot behind a
// long-pending approval can be read after the model, prompt or tools change
// (ADR 0034 §3). An empty label marks a receipt saved before versions were
// recorded. It describes the agent, not the corpus version of ADR 0012.
type AgentVersion struct {
	Model  string   `json:"model"`
	Prompt string   `json:"prompt"`
	Tools  []string `json:"tools,omitempty"`
}

// UnmarshalJSON decodes a version label at the boundary. It rejects trailing
// data and trims the strings, so whitespace cannot make two equal labels
// differ. It is deliberately permissive about which fields are present: older
// and future agents may carry fewer or more details than this build knows.
func (v *AgentVersion) UnmarshalJSON(data []byte) error {
	var wire struct {
		Model  string   `json:"model"`
		Prompt string   `json:"prompt"`
		Tools  []string `json:"tools"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&wire); err != nil {
		return fmt.Errorf("agent version: decode: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("agent version: trailing data")
	}
	*v = AgentVersion{Model: strings.TrimSpace(wire.Model), Prompt: strings.TrimSpace(wire.Prompt)}
	for _, tool := range wire.Tools {
		if tool = strings.TrimSpace(tool); tool != "" {
			v.Tools = append(v.Tools, tool)
		}
	}
	return nil
}

// Artifact source, ADR 0027. A result is read from a git branch, a pull
// request or a file in an isolated worktree. There is deliberately no kind for
// an agent's private state: logs, transcripts and databases are not a public
// contract and are never a Beacon artifact source.

// ArtifactKind is the explicit kind of an artifact source.
type ArtifactKind string

const (
	ArtifactBranch   ArtifactKind = "branch"
	ArtifactPR       ArtifactKind = "pr"
	ArtifactWorktree ArtifactKind = "worktree"
)

// ArtifactSource is a validated, typed locator of a result. Branch and PR
// carry no filesystem path. A worktree file is a slash-separated path relative
// to the worktree root; absolute paths, home-relative paths, parent traversal
// and hidden path segments are rejected so no private agent state can be
// passed as an artifact.
type ArtifactSource struct {
	Kind         ArtifactKind `json:"kind"`
	Branch       string       `json:"branch,omitempty"`
	Repo         string       `json:"repo,omitempty"`
	PR           int          `json:"pr,omitempty"`
	State        string       `json:"state,omitempty"`
	WorktreeFile string       `json:"worktreeFile,omitempty"`
}

// UnmarshalJSON validates the source at the boundary. An unknown kind, a
// missing locator or a path outside a worktree is an error rather than a
// guess, so a private agent log can never become a public artifact source.
func (s *ArtifactSource) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind         string `json:"kind"`
		Branch       string `json:"branch"`
		Repo         string `json:"repo"`
		PR           int    `json:"pr"`
		State        string `json:"state"`
		WorktreeFile string `json:"worktreeFile"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&wire); err != nil {
		return fmt.Errorf("artifact source: decode: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("artifact source: trailing data")
	}

	kind := ArtifactKind(strings.ToLower(strings.TrimSpace(wire.Kind)))
	*s = ArtifactSource{Kind: kind, State: strings.ToUpper(strings.TrimSpace(wire.State))}
	switch kind {
	case ArtifactBranch:
		if wire.Repo != "" || wire.PR != 0 || wire.WorktreeFile != "" || wire.State != "" {
			return errors.New("artifact source: branch carries only a branch name")
		}
		branch := strings.TrimSpace(wire.Branch)
		if !validArtifactName(branch) {
			return fmt.Errorf("artifact source: bad branch %q", wire.Branch)
		}
		s.Branch = branch
	case ArtifactPR:
		if wire.Branch != "" || wire.WorktreeFile != "" {
			return errors.New("artifact source: pr carries only repo, pr and state")
		}
		repo := strings.TrimSpace(wire.Repo)
		if wire.PR <= 0 || !validArtifactRepo(repo) {
			return fmt.Errorf("artifact source: bad pr locator repo=%q pr=%d", wire.Repo, wire.PR)
		}
		s.Repo, s.PR = repo, wire.PR
	case ArtifactWorktree:
		if wire.Branch != "" || wire.Repo != "" || wire.PR != 0 || wire.State != "" {
			return errors.New("artifact source: worktree carries only a worktree file")
		}
		file := strings.TrimSpace(wire.WorktreeFile)
		if !validWorktreeFile(file) {
			return fmt.Errorf("artifact source: bad worktree file %q", wire.WorktreeFile)
		}
		s.WorktreeFile = file
	default:
		return fmt.Errorf("artifact source: unknown kind %q", wire.Kind)
	}
	return nil
}

// Verdict returns the domain verdict implied by the source. Only a PR source
// carries an observed state. A closed or merged PR is terminal, as ADR 0027
// requires, and the result comes from that state alone: no log, transcript or
// database is read.
func (s ArtifactSource) Verdict() domain.Verdict {
	if s.Kind != ArtifactPR {
		return domain.VerdictWatching
	}
	v, _ := domain.Evaluate(domain.Snapshot{State: s.State}, domain.Policy{})
	return v
}

func validArtifactName(v string) bool {
	if v == "" || strings.ContainsAny(v, " \t\r\n\\~") || strings.HasPrefix(v, "-") {
		return false
	}
	for _, seg := range strings.Split(v, "/") {
		if !validSegment(seg) {
			return false
		}
	}
	return true
}

func validArtifactRepo(v string) bool {
	parts := strings.Split(v, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if !validSegment(part) {
			return false
		}
	}
	return true
}

func validWorktreeFile(v string) bool {
	if v == "" || strings.HasPrefix(v, "/") || strings.ContainsAny(v, "\\\x00") {
		return false
	}
	for _, seg := range strings.Split(v, "/") {
		if !validSegment(seg) {
			return false
		}
	}
	return true
}

// validSegment rejects hidden, parent and home-relative path pieces. A hidden
// segment is where agent products keep their private session state, so no
// artifact locator may use one.
func validSegment(seg string) bool {
	return seg != "" && seg != "." && seg != ".." && !strings.HasPrefix(seg, ".") &&
		!strings.ContainsAny(seg, " \t\r\n\\~")
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
		Version      json.RawMessage `json:"version"`
		Verified     json.RawMessage `json:"verified"`
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
	if len(wire.Version) > 0 && !bytes.Equal(bytes.TrimSpace(wire.Version), []byte("null")) {
		if err := json.Unmarshal(wire.Version, &r.Version); err != nil {
			return fmt.Errorf("receipt: bad version: %w", err)
		}
	}
	// Absent or null means a legacy line from before verification was
	// recorded; those results were accepted, so they stay verified. Only an
	// explicit boolean can mark a result unverified.
	r.Verified = true
	if len(wire.Verified) > 0 && !bytes.Equal(bytes.TrimSpace(wire.Verified), []byte("null")) {
		if err := json.Unmarshal(wire.Verified, &r.Verified); err != nil {
			return fmt.Errorf("receipt: bad verified: %w", err)
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
	Unverified  int  // valid results that failed CI verification; not stored or published
	PartialTail bool // trailing bytes without a newline were ignored
}

// ImportReceipts reads path, then inserts every whole line and updates the
// matching obligations in one transaction. A missing file is a no-op: no
// receipt means indeterminate, not failed. A final line without a newline is
// ignored, as are blank and malformed lines; neither stops the other lines.
// Only a verified result is stored. An explicit verified=false line is counted
// and skipped, keeping the result inside its task until a verified line arrives.
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
		if !receipt.Verified {
			// A failed or missing CI check leaves the result inside the task
			// with verified=false as its reason. It is not written to results
			// and sets no deliveredAt, so the call keeps waiting (ADR 0040 §5).
			result.Unverified++
			continue
		}
		entries = append(entries, entry{receipt: receipt, raw: line})
	}

	err = d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createReceiptsSQL); err != nil {
			return fmt.Errorf("store: receipts: create table: %w", err)
		}
		for _, column := range []string{"source", "version"} {
			if err := ensureReceiptColumn(tx, column); err != nil {
				return err
			}
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

// ensureReceiptColumn adds column to a receipts table made by an earlier
// build. The table is created on first use here, so additive upgrades belong
// here too instead of in the migration corpus. column is one of our own fixed
// names ("source", "version"), never external input.
func ensureReceiptColumn(tx *sql.Tx, column string) error {
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
		if strings.EqualFold(name, column) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: receipts: table info rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: receipts: close table info: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`ALTER TABLE receipts ADD COLUMN %s TEXT NOT NULL DEFAULT ''`, column)); err != nil {
		return fmt.Errorf("store: receipts: add %s column: %w", column, err)
	}
	return nil
}

// rejectDuplicateOutcome is the receipts outcome for a repeated task start.
// The store writes it directly; it is never accepted from receipts.jsonl, so
// the plugin's wire vocabulary of outcomes is unchanged.
const rejectDuplicateOutcome = "reject-duplicate"

// recordRejectDuplicate stores the conflict of a repeated idempotent start in
// the same transaction as the key lookup (ADR 0015). obligation_id is the
// namespaced idempotency key and attempt is 0, so UNIQUE(obligation_id,
// attempt) gives at most one conflict row per key.
func recordRejectDuplicate(tx *sql.Tx, namespacedKey, taskID string) error {
	if _, err := tx.Exec(createReceiptsSQL); err != nil {
		return fmt.Errorf("store: receipts: create table: %w", err)
	}
	for _, column := range []string{"source", "version"} {
		if err := ensureReceiptColumn(tx, column); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(struct {
		IdempotencyKey string `json:"idempotencyKey"`
		TaskID         string `json:"taskID"`
	}{IdempotencyKey: namespacedKey, TaskID: taskID})
	if err != nil {
		return fmt.Errorf("store: receipts: encode conflict: %w", err)
	}
	at := time.Now().UTC().UnixNano()
	_, err = tx.Exec(`
		INSERT OR IGNORE INTO receipts
		  (obligation_id, attempt, outcome, at, raw, imported_at, source)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		namespacedKey, 0, rejectDuplicateOutcome, at, Redact(raw), at, "")
	if err != nil {
		return fmt.Errorf("store: receipts: record reject duplicate: %w", err)
	}
	return nil
}

// RejectDuplicateCount returns the number of recorded duplicate-start
// conflicts. A repeated start with the same key records at most one.
func (d *DB) RejectDuplicateCount() (int, error) {
	ok, err := tableExists(context.Background(), d.readDB, "receipts")
	if err != nil || !ok {
		return 0, err
	}
	var n int
	err = d.readDB.QueryRow(`SELECT count(*) FROM receipts WHERE outcome = ?`, rejectDuplicateOutcome).Scan(&n)
	return n, err
}

// encodeReceiptVersion returns the stored form of an agent version label.
// An empty label is stored as "", so receipts saved before versions were
// recorded keep the column empty and need no backfill (ADR 0034 §3).
func encodeReceiptVersion(v AgentVersion) (string, error) {
	if v.Model == "" && v.Prompt == "" && len(v.Tools) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("store: receipts: encode version: %w", err)
	}
	return string(encoded), nil
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
	version, err := encodeReceiptVersion(r.Version)
	if err != nil {
		return false, err
	}
	res, err := tx.Exec(`
		INSERT OR IGNORE INTO receipts
		  (obligation_id, attempt, outcome, at, raw, imported_at, source, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ObligationID, r.Attempt, string(r.Outcome), r.At.UnixNano(),
		Redact(raw), time.Now().UTC().UnixNano(), source, version)
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
		SELECT obligation_id, attempt, outcome, at, source, version FROM receipts
		WHERE obligation_id = ? ORDER BY seq`, obligationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Receipt
	for rows.Next() {
		var (
			r       Receipt
			atNano  int64
			source  string
			version string
		)
		if err := rows.Scan(&r.ObligationID, &r.Attempt, &r.Outcome, &atNano, &source, &version); err != nil {
			return nil, err
		}
		r.At = time.Unix(0, atNano).UTC()
		// Only verified results reach the table, so a stored receipt is
		// verified by construction (ADR 0040 §5).
		r.Verified = true
		if source != "" {
			if err := json.Unmarshal([]byte(source), &r.Source); err != nil {
				return nil, fmt.Errorf("store: receipts: decode source: %w", err)
			}
		}
		if version != "" {
			if err := json.Unmarshal([]byte(version), &r.Version); err != nil {
				return nil, fmt.Errorf("store: receipts: decode version: %w", err)
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
