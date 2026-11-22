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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
CREATE INDEX IF NOT EXISTS receipts_by_outcome ON receipts(outcome);
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
	cols, err := tableColumns(tx, "receipts")
	if err != nil {
		return fmt.Errorf("store: receipts: table info: %w", err)
	}
	if cols[strings.ToLower(column)] {
		return nil
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

// ADR 0049 §3. The fence token, the enqueue and the task data change commit in
// one transaction. The token is the one the T3.51 claim issued, carried in by
// the worker. This file does not mint a new monotonic counter. The store keeps
// the highest token it has seen for a task. A worker whose lease was taken
// away presents an older token and the whole write is refused, so it cannot
// publish a receipt or a queue row that belongs to a newer lease.

// createTaskFencesSQL owns the per-task fence token. The table is created on
// first use here, like receipts and the other tables in this package. task_id
// is the primary key, so a task holds at most one token.
const createTaskFencesSQL = `
CREATE TABLE IF NOT EXISTS task_fences (
  task_id       TEXT PRIMARY KEY,
  fencing_token INTEGER NOT NULL,
  owner_id      TEXT NOT NULL,
  updated_at    INTEGER NOT NULL
);
`

// FencedEnqueue is one atomic task mutation. FencingToken is the lease number
// from the T3.51 claim. Receipt and Raw carry the task data change, Message is
// the task put on the durable queue. All three are written in one transaction.
type FencedEnqueue struct {
	TaskID       string
	OwnerID      string
	FencingToken uint64
	Receipt      Receipt
	Raw          []byte
	Message      InboxMessage
}

// FencedEnqueueResult reports what one EnqueueFenced committed. Enqueued and
// Applied are false on an idempotent repeat with a token the task already has.
type FencedEnqueueResult struct {
	FencingToken uint64
	Enqueued     bool
	Applied      bool
}

// FenceTokenStaleError reports an enqueue whose token is older than the token
// the task already holds. The worker lost its lease, so the store refuses the
// write before changing a token, the task data or the queue.
type FenceTokenStaleError struct {
	Presented uint64
	Current   uint64
}

func (e *FenceTokenStaleError) Error() string {
	return fmt.Sprintf("store: fence token %d is stale, task holds token %d", e.Presented, e.Current)
}

// IsStaleFenceToken reports whether err is a stale token rejection.
func IsStaleFenceToken(err error) bool {
	var stale *FenceTokenStaleError
	return errors.As(err, &stale)
}

// EnqueueFenced commits the fence token, the receipt data change and the queue
// row in one transaction (ADR 0049 §3). A token lower than the task's current
// one is refused. A token equal to the current one is an idempotent retry. It
// inserts no second receipt and no second queue row. A higher token becomes the
// task's new fence and the receipt and message are applied with it.
func (d *DB) EnqueueFenced(e FencedEnqueue) (FencedEnqueueResult, error) {
	if strings.TrimSpace(e.TaskID) == "" {
		return FencedEnqueueResult{}, domain.NewAppError(domain.CodeInvalidInput,
			"store: fenced enqueue: empty task id")
	}
	if strings.TrimSpace(e.OwnerID) == "" {
		return FencedEnqueueResult{}, domain.NewAppError(domain.CodeInvalidInput,
			"store: fenced enqueue: empty owner id")
	}
	if e.FencingToken == 0 {
		return FencedEnqueueResult{}, domain.NewAppError(domain.CodeInvalidInput,
			"store: fenced enqueue: fencing token is required")
	}
	if strings.TrimSpace(e.Message.WatchID) == "" || strings.TrimSpace(e.Message.SubscriberID) == "" ||
		strings.TrimSpace(e.Message.PeerName) == "" {
		return FencedEnqueueResult{}, domain.NewAppError(domain.CodeInvalidInput,
			"store: fenced enqueue: message needs watch, subscriber and peer")
	}

	var result FencedEnqueueResult
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createTaskFencesSQL); err != nil {
			return fmt.Errorf("store: fenced enqueue: create fence table: %w", err)
		}

		var current uint64
		if err := tx.QueryRow(`SELECT fencing_token FROM task_fences WHERE task_id = ?`, e.TaskID).Scan(&current); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("store: fenced enqueue: read fence: %w", err)
			}
			current = 0
		}
		if e.FencingToken < current {
			return &FenceTokenStaleError{Presented: e.FencingToken, Current: current}
		}
		result.FencingToken = e.FencingToken

		if e.FencingToken > current {
			if _, err := tx.Exec(`
				INSERT INTO task_fences (task_id, fencing_token, owner_id, updated_at)
				VALUES (?, ?, ?, ?)
				ON CONFLICT(task_id) DO UPDATE SET
				  fencing_token = excluded.fencing_token,
				  owner_id      = excluded.owner_id,
				  updated_at    = excluded.updated_at`,
				e.TaskID, int64(e.FencingToken), e.OwnerID, time.Now().UTC().UnixNano()); err != nil {
				return fmt.Errorf("store: fenced enqueue: write fence: %w", err)
			}
		}

		if e.Receipt.Verified {
			if _, err := tx.Exec(createReceiptsSQL); err != nil {
				return fmt.Errorf("store: fenced enqueue: create receipts: %w", err)
			}
			for _, column := range []string{"source", "version"} {
				if err := ensureReceiptColumn(tx, column); err != nil {
					return err
				}
			}
			inserted, err := insertReceipt(tx, e.Receipt, e.Raw)
			if err != nil {
				return err
			}
			if inserted {
				applied, err := applyReceipt(tx, e.Receipt)
				if err != nil {
					return err
				}
				result.Applied = applied
			}
		}

		if err := ensureInbox(tx); err != nil {
			return err
		}
		key := InboxKey(e.Message.WatchID, e.Message.Revision, e.Message.SubscriberID)
		res, err := tx.Exec(`
			INSERT OR IGNORE INTO inbox_messages
			  (key, watch_id, revision, subscriber_id, peer_name, payload, state, lease_until, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`,
			key, e.Message.WatchID, e.Message.Revision, e.Message.SubscriberID, e.Message.PeerName,
			Redact(e.Message.Payload), inboxStatePending, time.Now().UTC().UnixNano())
		if err != nil {
			return fmt.Errorf("store: fenced enqueue: enqueue: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: fenced enqueue: rows affected: %w", err)
		}
		result.Enqueued = n == 1
		return nil
	})
	if err != nil {
		return FencedEnqueueResult{}, err
	}
	return result, nil
}

// TaskFence returns the fence token the task currently holds and whether a
// fence was recorded. A task without a fence has never had a fenced enqueue.
func (d *DB) TaskFence(taskID string) (uint64, bool, error) {
	ok, err := tableExists(context.Background(), d.readDB, "task_fences")
	if err != nil || !ok {
		return 0, false, err
	}
	var token int64
	if err := d.readDB.QueryRow(`SELECT fencing_token FROM task_fences WHERE task_id = ?`, taskID).Scan(&token); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return uint64(token), true, nil
}

// ADR 0058 §3, feature 3. Checking the agent's own claims against git, test
// reports and CI. This is a different object from T3.98's external verdict in
// internal/domain/verdict.go: T3.98 evaluates checks an external verifier ran
// on the result, while here the thing under check is a statement the agent
// made. The evidence is stored beside the claim and the verdict links to the
// exact observation that decided it, so a client sees the basis, not the words.

// ClaimSourceKind is the external source that can confirm or refute a claim.
// The three kinds are the ones named by ADR 0058 §3: a test, a CI run or a git
// fact. An agent's private log is deliberately not a source.
type ClaimSourceKind string

const (
	ClaimSourceTest    ClaimSourceKind = "test"
	ClaimSourceCIRun   ClaimSourceKind = "ci_run"
	ClaimSourceGitFact ClaimSourceKind = "git_fact"
)

// ClaimEvidence is one recorded observation from an external source. An adapter
// fills it from a real test report, CI run or git fact; the agent's own account
// never enters these fields. Observed says the fact was actually captured and
// Passed says it supports the claim: a green test or CI run, or a git fact that
// holds. Ref names the concrete observation, for example the test name, the CI
// run id or the commit sha, so the verdict can point at it.
type ClaimEvidence struct {
	Kind     ClaimSourceKind `json:"kind"`
	Ref      string          `json:"ref"`
	Observed bool            `json:"observed"`
	Passed   bool            `json:"passed"`
}

// AgentClaim is a statement the agent made together with the evidence recorded
// beside it. ID names the claim so a verdict can reference it.
type AgentClaim struct {
	ID       string          `json:"id"`
	Text     string          `json:"text"`
	Evidence []ClaimEvidence `json:"evidence"`
}

// ClaimVerdict is the result of checking one claim against its evidence.
type ClaimVerdict string

const (
	// ClaimConfirmed means an observed source supports the claim.
	ClaimConfirmed ClaimVerdict = "CONFIRMED"
	// ClaimRefuted means an observed source contradicts the claim. A single
	// failed observation refutes even when other observations pass.
	ClaimRefuted ClaimVerdict = "REFUTED"
	// ClaimUnverifiable means no usable observation was recorded. The gate
	// fails closed: a claim without evidence is not a confirmed claim.
	ClaimUnverifiable ClaimVerdict = "UNVERIFIABLE"
)

// ClaimResult is the verdict for one claim and the exact evidence that decided
// it. Evidence is the zero value only for UNVERIFIABLE, where no usable
// observation existed.
type ClaimResult struct {
	ClaimID  string        `json:"claim_id"`
	Verdict  ClaimVerdict  `json:"verdict"`
	Evidence ClaimEvidence `json:"evidence"`
}

// validClaimSource reports whether k is one of the three ADR 0058 §3 sources.
// An unknown kind is not evidence and cannot move a verdict.
func validClaimSource(k ClaimSourceKind) bool {
	switch k {
	case ClaimSourceTest, ClaimSourceCIRun, ClaimSourceGitFact:
		return true
	default:
		return false
	}
}

// CheckClaim checks one claim against the evidence recorded with it. It is a
// pure function: it reads no clock, runs no process and mutates the claim.
//
// Rules, in order:
//
//  1. An observed, failed observation from a known source refutes the claim;
//     that observation is the deciding evidence.
//  2. Otherwise an observed, passing observation from a known source confirms
//     the claim; the first such observation is the deciding evidence.
//  3. With no usable observation the claim is unverifiable, fail closed.
//
// Only recorded observations decide. The claim text is carried for the client
// but never affects the verdict, so an agent cannot argue its way to a pass.
func CheckClaim(claim AgentClaim) ClaimResult {
	result := ClaimResult{ClaimID: claim.ID}
	for _, e := range claim.Evidence {
		if e.Observed && validClaimSource(e.Kind) && !e.Passed {
			result.Verdict = ClaimRefuted
			result.Evidence = e
			return result
		}
	}
	for _, e := range claim.Evidence {
		if e.Observed && validClaimSource(e.Kind) && e.Passed {
			result.Verdict = ClaimConfirmed
			result.Evidence = e
			return result
		}
	}
	result.Verdict = ClaimUnverifiable
	return result
}

// CheckClaims checks a slice of claims and returns one verdict each, in the
// input order. It is the pure counterpart of SaveClaims plus ClaimResults.
func CheckClaims(claims []AgentClaim) []ClaimResult {
	out := make([]ClaimResult, 0, len(claims))
	for _, c := range claims {
		out = append(out, CheckClaim(c))
	}
	return out
}

// createClaimsSQL owns the per-claim evidence table. Like receipts and the
// other tables here, it is created on first use instead of in the migration
// corpus. One row per claim id; the whole claim, text and evidence, is the
// payload.
const createClaimsSQL = `
CREATE TABLE IF NOT EXISTS agent_claims (
  id          TEXT PRIMARY KEY,
  payload     BLOB NOT NULL,
  created_at  INTEGER NOT NULL
);
`

// SaveClaims upserts claims by ID in one transaction. The payload stores the
// text and the evidence together so a verdict is always recomputed from what
// was recorded at save time. A missing ID is rejected at this boundary; the
// payload passes through the one redaction filter before it reaches SQLite.
func (d *DB) SaveClaims(claims []AgentClaim) error {
	for _, c := range claims {
		if strings.TrimSpace(c.ID) == "" {
			return domain.NewAppError(domain.CodeInvalidInput,
				"store: save claims: empty claim id")
		}
	}
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createClaimsSQL); err != nil {
			return fmt.Errorf("store: claims: create table: %w", err)
		}
		for _, c := range claims {
			payload, err := json.Marshal(c)
			if err != nil {
				return fmt.Errorf("store: claims: encode %s: %w", c.ID, err)
			}
			if _, err := tx.Exec(`
				INSERT INTO agent_claims (id, payload, created_at)
				VALUES (?, ?, ?)
				ON CONFLICT(id) DO UPDATE SET payload = excluded.payload`,
				c.ID, Redact(payload), time.Now().UTC().UnixNano()); err != nil {
				return fmt.Errorf("store: claims: save %s: %w", c.ID, err)
			}
		}
		return nil
	})
}

// ClaimResults returns one verdict per stored claim, ordered by id. Each is
// recomputed by CheckClaim from the evidence on disk, so the result reflects
// the recorded basis and never a verdict the agent wrote into its claim.
func (d *DB) ClaimResults() ([]ClaimResult, error) {
	ok, err := tableExists(context.Background(), d.readDB, "agent_claims")
	if err != nil || !ok {
		return nil, err
	}
	rows, err := d.readDB.Query(`SELECT payload FROM agent_claims ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ClaimResult
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var claim AgentClaim
		if err := json.Unmarshal(payload, &claim); err != nil {
			return nil, fmt.Errorf("store: claims: decode: %w", err)
		}
		out = append(out, CheckClaim(claim))
	}
	return out, rows.Err()
}

// BS.13 / ADR 0103. A receipt records a committed intent, not a finished
// effect. The client_receipts table keeps the two apart: intent_committed says
// the command was accepted and stored, while effect_status tracks the separate
// progress of the effect. clientRequestId (ADR 0028) is the idempotency key, so
// two calls with the same key write one row and the repeat returns the first
// result. The table lives beside the existing receipts table and is created on
// first use, as with the other tables in this package, so no existing call is
// changed and no migration file is needed.

// createClientReceiptsSQL owns the per-clientRequestId receipt store. The
// client_request_id column is UNIQUE, so a repeat cannot create a second row.
// receipt_json, intent_committed and effect_status are separate columns: the
// receipt body and the intent are fixed on first write, the effect status is
// free to advance independently (ADR 0103).
const createClientReceiptsSQL = `
CREATE TABLE IF NOT EXISTS client_receipts (
  seq               INTEGER PRIMARY KEY AUTOINCREMENT,
  client_request_id TEXT NOT NULL,
  receipt_json      BLOB NOT NULL DEFAULT '',
  intent_committed  INTEGER NOT NULL DEFAULT 0,
  effect_status     TEXT NOT NULL DEFAULT '',
  created_at        INTEGER NOT NULL DEFAULT 0,
  UNIQUE (client_request_id)
);
`

// Effect status values kept in the separate effect_status column. A receipt is
// recorded with intent_committed=true while its effect is still pending; none
// of these states is derived from the presence of the receipt itself.
const (
	EffectStatusPending    = "pending"
	EffectStatusInProgress = "in_progress"
	EffectStatusComplete   = "complete"
	EffectStatusFailed     = "failed"
)

// clientReceiptColumns is the minimal additive schema a partially created
// client_receipts table is extended with. Keys are lowercase to match
// PRAGMA table_info; values are column definitions for ALTER TABLE. column is
// one of our fixed names, never external input.
var clientReceiptColumns = map[string]string{
	"client_request_id": "TEXT NOT NULL DEFAULT ''",
	"receipt_json":      "BLOB NOT NULL DEFAULT ''",
	"intent_committed":  "INTEGER NOT NULL DEFAULT 0",
	"effect_status":     "TEXT NOT NULL DEFAULT ''",
	"created_at":        "INTEGER NOT NULL DEFAULT 0",
}

// ensureClientReceipts creates the table on first use and extends a table made
// by an earlier build with any missing column. It runs inside the caller's
// transaction, so the insert that follows cannot see a half-upgraded schema.
func ensureClientReceipts(tx *sql.Tx) error {
	if _, err := tx.Exec(createClientReceiptsSQL); err != nil {
		return fmt.Errorf("store: client receipts: create table: %w", err)
	}
	present, err := tableColumns(tx, "client_receipts")
	if err != nil {
		return fmt.Errorf("store: client receipts: table info: %w", err)
	}
	for column, def := range clientReceiptColumns {
		if present[column] {
			continue
		}
		if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE client_receipts ADD COLUMN %s %s", column, def)); err != nil {
			return fmt.Errorf("store: client receipts: add %s: %w", column, err)
		}
	}
	// A partial table may lack the UNIQUE constraint. The named index makes
	// clientRequestId unique either way, so the idempotency key holds.
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS client_receipts_by_request ON client_receipts(client_request_id)`); err != nil {
		return fmt.Errorf("store: client receipts: unique index: %w", err)
	}
	return nil
}

// ClientReceipt is the stored receipt for one clientRequestId. IntentCommitted
// reports that the command was stored; EffectStatus tracks the effect on its
// own timeline and is not implied by the receipt (ADR 0103).
type ClientReceipt struct {
	ClientRequestID string
	ReceiptJSON     []byte
	IntentCommitted bool
	EffectStatus    string
	CreatedAt       int64
}

// RecordClientReceiptResult reports what one RecordClientReceipt call did.
// Created marks the first write. Duplicate marks a repeat: nothing was written
// and the first row is returned. Conflict marks a repeat whose receipt body or
// intent differs from the first; the stored row still wins, so a repeat cannot
// roll the recorded intent back. A different effect_status alone is not a
// conflict, because the effect advances independently of the receipt.
type RecordClientReceiptResult struct {
	Receipt   ClientReceipt
	Created   bool
	Duplicate bool
	Conflict  bool
}

// loadClientReceipt reads one row by clientRequestId. q is satisfied by both
// *sql.DB and *sql.Tx, so the same scan serves the writer and the read pool.
func loadClientReceipt(q rowQuerier, clientRequestID string) (ClientReceipt, error) {
	var r ClientReceipt
	var intent int
	err := q.QueryRow(`
		SELECT client_request_id, receipt_json, intent_committed, effect_status, created_at
		FROM client_receipts WHERE client_request_id = ?`, clientRequestID,
	).Scan(&r.ClientRequestID, &r.ReceiptJSON, &intent, &r.EffectStatus, &r.CreatedAt)
	r.IntentCommitted = intent == 1
	return r, err
}

// RecordClientReceipt stores one receipt per clientRequestId at most once. An
// empty key is rejected at this boundary. The insert, the schema setup and the
// repeat lookup share one transaction (ADR 0015), so two concurrent calls with
// the same key cannot both create a row. An empty effect status defaults to
// pending: recording the receipt never claims the effect is done. The payload
// passes through Redact before it reaches SQLite.
func (d *DB) RecordClientReceipt(in ClientReceipt) (RecordClientReceiptResult, error) {
	if strings.TrimSpace(in.ClientRequestID) == "" {
		return RecordClientReceiptResult{}, domain.NewAppError(domain.CodeInvalidInput,
			"store: client receipt: empty clientRequestId")
	}
	if in.EffectStatus == "" {
		in.EffectStatus = EffectStatusPending
	}
	clean := Redact(in.ReceiptJSON)
	at := in.CreatedAt
	if at == 0 {
		at = time.Now().UTC().UnixNano()
	}
	intent := 0
	if in.IntentCommitted {
		intent = 1
	}
	var out RecordClientReceiptResult
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureClientReceipts(tx); err != nil {
			return err
		}
		res, err := tx.Exec(`
			INSERT OR IGNORE INTO client_receipts
			  (client_request_id, receipt_json, intent_committed, effect_status, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			in.ClientRequestID, clean, intent, in.EffectStatus, at)
		if err != nil {
			return fmt.Errorf("store: client receipt: insert: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: client receipt: rows affected: %w", err)
		}
		if n == 1 {
			out = RecordClientReceiptResult{
				Receipt: ClientReceipt{
					ClientRequestID: in.ClientRequestID,
					ReceiptJSON:     append([]byte(nil), clean...),
					IntentCommitted: in.IntentCommitted,
					EffectStatus:    in.EffectStatus,
					CreatedAt:       at,
				},
				Created: true,
			}
			return nil
		}
		stored, err := loadClientReceipt(tx, in.ClientRequestID)
		if err != nil {
			return fmt.Errorf("store: client receipt: load existing: %w", err)
		}
		out = RecordClientReceiptResult{
			Receipt:   stored,
			Duplicate: true,
			Conflict: string(stored.ReceiptJSON) != string(clean) ||
				stored.IntentCommitted != in.IntentCommitted,
		}
		return nil
	})
	if err != nil {
		return RecordClientReceiptResult{}, err
	}
	return out, nil
}

// ClientReceipt returns the stored receipt for clientRequestId and whether one
// exists. Reads use the separate pool, so a process that reopens the same file
// sees the first result without waiting on the writer. An empty file has no
// phantom row.
func (d *DB) ClientReceipt(clientRequestID string) (ClientReceipt, bool, error) {
	if strings.TrimSpace(clientRequestID) == "" {
		return ClientReceipt{}, false, domain.NewAppError(domain.CodeInvalidInput,
			"store: client receipt: empty clientRequestId")
	}
	ok, err := tableExists(context.Background(), d.readDB, "client_receipts")
	if err != nil || !ok {
		return ClientReceipt{}, false, err
	}
	r, err := loadClientReceipt(d.readDB, clientRequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return ClientReceipt{}, false, nil
	}
	if err != nil {
		return ClientReceipt{}, false, err
	}
	return r, true, nil
}

// SetEffectStatus advances the effect of a recorded receipt without touching
// its intent or receipt body. It returns false when no receipt has that key.
// This is the only state a receipt's effect can change: the receipt itself
// never claims the effect finished (ADR 0103).
func (d *DB) SetEffectStatus(clientRequestID, status string) (bool, error) {
	if strings.TrimSpace(clientRequestID) == "" {
		return false, domain.NewAppError(domain.CodeInvalidInput,
			"store: client receipt: empty clientRequestId")
	}
	if strings.TrimSpace(status) == "" {
		return false, domain.NewAppError(domain.CodeInvalidInput,
			"store: client receipt: empty effect status")
	}
	var updated bool
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureClientReceipts(tx); err != nil {
			return err
		}
		res, err := tx.Exec(`
			UPDATE client_receipts SET effect_status = ? WHERE client_request_id = ?`,
			status, clientRequestID)
		if err != nil {
			return fmt.Errorf("store: client receipt: set effect status: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: client receipt: rows affected: %w", err)
		}
		updated = n == 1
		return nil
	})
	return updated, err
}

// ClientReceiptCount returns the number of stored client receipts. A repeated
// clientRequestId must not increase it, and an empty file has count zero.
func (d *DB) ClientReceiptCount() (int, error) {
	ok, err := tableExists(context.Background(), d.readDB, "client_receipts")
	if err != nil || !ok {
		return 0, err
	}
	var n int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM client_receipts`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CA-25. Local model-message journal.
//
// aider writes a local .aider.llm.history.md with a `ROLE timestamp` header
// and the message text beneath it (aider/io.py:754-765). A write failure warns
// and switches the journal off without interrupting the turn. Beacon had no
// such local artifact: agent logs and transcripts are private state and never
// become public receipts (ADR 0027). This journal stays a plain local file
// outside the receipts table, so no entry can be mistaken for a proof.

const defaultMessageJournalMaxBytes int64 = 1 << 20 // 1 MiB

// MessageRole names the side of a model exchange recorded in the journal. The
// role is written verbatim as the first field of an entry header.
type MessageRole string

const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleSystem    MessageRole = "system"
	MessageRoleTool      MessageRole = "tool"
)

// JournalEntry is one message read back from a ModelJournal. At is the UTC
// time stored in the entry header.
type JournalEntry struct {
	Role MessageRole
	At   time.Time
	Text string
}

// ModelJournal is a local append-only journal of model messages (CA-25). Each
// message occupies a header line `ROLE RFC3339Nano` followed by its text. The
// file is a debug artifact: it never enters receipts, results or any public
// proof. A write failure disables the journal for the rest of the process and
// returns no error to the caller, so a bad path cannot break a turn. Older
// entries are dropped whole when a new message would cross the size cap.
type ModelJournal struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	disabled bool
}

// MessageJournal is the name used by callers that describe the artifact by
// its contents rather than by the model that produced it.
type MessageJournal = ModelJournal

// NewModelJournal returns a journal writing to path. An optional maxBytes sets
// the size cap; zero or no argument selects the 1 MiB default. The parent
// directory is created on the first append.
func NewModelJournal(path string, maxBytes ...int64) *ModelJournal {
	limit := defaultMessageJournalMaxBytes
	if len(maxBytes) > 0 && maxBytes[0] > 0 {
		limit = maxBytes[0]
	}
	return &ModelJournal{path: path, maxBytes: limit}
}

// NewMessageJournal is an alias for NewModelJournal.
func NewMessageJournal(path string, maxBytes ...int64) *MessageJournal {
	return NewModelJournal(path, maxBytes...)
}

// Path returns the journal file path.
func (j *ModelJournal) Path() string {
	if j == nil {
		return ""
	}
	return j.path
}

// Enabled reports whether the journal still accepts writes.
func (j *ModelJournal) Enabled() bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return !j.disabled
}

// Disabled reports whether a write failure has switched the journal off.
func (j *ModelJournal) Disabled() bool { return !j.Enabled() }

// Append records one message with the current UTC timestamp. It returns nil
// even on a filesystem error: the journal disables itself and the caller's
// turn continues (CA-25).
func (j *ModelJournal) Append(role, text string) error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.disabled {
		return nil
	}
	if err := j.appendLocked(role, text); err != nil {
		j.disabled = true
	}
	return nil
}

// AppendMessage is a role-typed form of Append.
func (j *ModelJournal) AppendMessage(role MessageRole, text string) error {
	return j.Append(string(role), text)
}

// Entries reads the journal back in write order. A missing file has no
// entries. Reading does not change the disabled state.
func (j *ModelJournal) Entries() ([]JournalEntry, error) {
	if j == nil {
		return nil, nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	data, err := os.ReadFile(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: model journal: read: %w", err)
	}
	blocks := splitJournalBlocks(data)
	out := make([]JournalEntry, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, JournalEntry{Role: b.role, At: b.at, Text: b.text})
	}
	return out, nil
}

func (j *ModelJournal) appendLocked(role, text string) error {
	if strings.TrimSpace(j.path) == "" {
		return errors.New("store: model journal: empty path")
	}
	header := strings.TrimSpace(role) + " " + time.Now().UTC().Format(time.RFC3339Nano)
	entry := []byte(header + "\n" + strings.TrimRight(text, "\n") + "\n")

	if dir := filepath.Dir(j.path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("store: model journal: create dir: %w", err)
		}
	}
	data, err := os.ReadFile(j.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: model journal: read: %w", err)
	}
	if int64(len(data)+len(entry)) > j.maxBytes {
		data = trimJournal(data, int64(len(entry)), j.maxBytes)
		if err := os.WriteFile(j.path, data, 0o644); err != nil {
			return fmt.Errorf("store: model journal: trim: %w", err)
		}
	}
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("store: model journal: open: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(entry); err != nil {
		return fmt.Errorf("store: model journal: append: %w", err)
	}
	return nil
}

// journalBlock is one stored message together with its raw bytes, so an old
// entry can be dropped whole when the file is trimmed.
type journalBlock struct {
	raw  []byte
	role MessageRole
	at   time.Time
	text string
}

// splitJournalBlocks reads a journal file into messages. A header is a line
// with exactly two fields whose second field parses as RFC3339Nano; every
// line until the next header belongs to the same message. Text before the
// first header is ignored.
func splitJournalBlocks(data []byte) []journalBlock {
	type header struct {
		start int
		role  MessageRole
		at    time.Time
	}
	var headers []header
	for offset := 0; offset < len(data); {
		end := bytes.IndexByte(data[offset:], '\n')
		if end < 0 {
			break
		}
		line := string(data[offset : offset+end])
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			if at, err := time.Parse(time.RFC3339Nano, fields[len(fields)-1]); err == nil {
				role := strings.Join(fields[:len(fields)-1], " ")
				headers = append(headers, header{start: offset, role: MessageRole(role), at: at})
			}
		}
		offset += end + 1
	}

	blocks := make([]journalBlock, 0, len(headers))
	for i, h := range headers {
		stop := len(data)
		if i+1 < len(headers) {
			stop = headers[i+1].start
		}
		raw := data[h.start:stop]
		text := ""
		if nl := bytes.IndexByte(raw, '\n'); nl >= 0 {
			text = string(raw[nl+1:])
		}
		blocks = append(blocks, journalBlock{
			raw:  raw,
			role: h.role,
			at:   h.at,
			text: strings.TrimSuffix(text, "\n"),
		})
	}
	return blocks
}

// trimJournal drops the oldest complete messages until a new entry of add
// bytes fits under max. It returns data unchanged when the file already fits.
func trimJournal(data []byte, add, max int64) []byte {
	if int64(len(data))+add <= max {
		return data
	}
	blocks := splitJournalBlocks(data)
	out := append([]byte(nil), data...)
	for int64(len(out))+add > max && len(blocks) > 0 {
		out = out[len(blocks[0].raw):]
		blocks = blocks[1:]
	}
	return out
}
