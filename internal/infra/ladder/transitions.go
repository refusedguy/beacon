// Package ladder implements the wake ladder from ADR 0017.
//
// The ladder branches on the delivery outcome of an obligation's pointer:
// posted waits for claimed or read without retries, transport-failed repeats
// the pointer at +5 and +10 minutes with a cap of three attempts and a jitter
// of +/-30 seconds, and indeterminate (including no receipt at all) is never
// retried. Every branch hands control to the evidence check at 15 minutes from
// staging; that check is T3.10's escalation.
//
// The current position is a marker row in ladder_journal, not process memory.
// A new Ladder over the same writer resumes from the last marker, so a restart
// in the middle of the ladder continues at the same step. Outcome, obligation
// attempts and the marker are written in one transaction, which is the
// "verdict, obligation and ladder position in one transaction" rule of ADR 0015.
//
// Time is always supplied by the caller, so the package reads no clock and is
// deterministic under test.
package ladder

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// ADR 0017 timings. These are starting points, not measured optima.
const (
	EvidenceWindow   = 15 * time.Minute
	FirstRetryDelay  = 5 * time.Minute
	SecondRetryDelay = 10 * time.Minute
	MaxAttempts      = 3
	MaxJitter        = 30 * time.Second
)

var retryDelays = []time.Duration{FirstRetryDelay, SecondRetryDelay}

// Status is the lifecycle of a ladder marker.
type Status string

const (
	// StatusActive waits for the next timed action (retry or evidence check).
	StatusActive Status = "active"
	// StatusStopped means read or claimed arrived; all timers are cancelled.
	StatusStopped Status = "stopped"
	// StatusEscalated means the evidence window elapsed without claimed or
	// read; the episode is handed to escalation (T3.10).
	StatusEscalated Status = "escalated"
)

// ErrNotStaged reports a position request for an obligation that never went
// through Stage.
var ErrNotStaged = errors.New("ladder: obligation not staged")

// Marker is one persisted ladder position. Each transition appends a new
// Marker; the last marker for an obligation is its current position.
type Marker struct {
	ObligationID string                `json:"obligationID"`
	WatchID      string                `json:"watchID"`
	Branch       domain.AttemptOutcome `json:"branch"`
	Attempts     int                   `json:"attempts"`
	Status       Status                `json:"status"`
	StagedAt     time.Time             `json:"stagedAt"`
	NextAt       time.Time             `json:"nextAt"`
	EvidenceAt   time.Time             `json:"evidenceAt"`
	Verdict      domain.Verdict        `json:"verdict"`
}

// Receipt is the fact produced by the adapter boundary (T3.6). The wire shape
// is {obligationID, attempt, outcome, at}; a zero Attempt means "next".
type Receipt struct {
	ObligationID string
	Attempt      int
	Outcome      domain.AttemptOutcome
	At           time.Time
}

// ActionKind distinguishes a repeat of the pointer from the evidence check.
type ActionKind string

const (
	// ActionRetry resends the same pointer (obligationID and revision intact).
	ActionRetry ActionKind = "ladder-attempt"
	// ActionEvidence checks for claimed or read and escalates if absent.
	ActionEvidence ActionKind = "ladder-evidence"
)

// Due is one timed action the caller must perform.
type Due struct {
	ObligationID string
	WatchID      string
	Kind         ActionKind
	Attempt      int
	At           time.Time
}

// Action is the result of consuming a Due through Advance. A zero Action
// (empty Kind) means the marker did not move: the call was a no-op.
type Action struct {
	Kind         ActionKind
	ObligationID string
	WatchID      string
	Attempt      int
	At           time.Time
}

// Writer is the single-writer port implemented by *store.DB. Reads also go
// through it so the ladder owns no connection of its own.
type Writer interface {
	Write(ctx context.Context, fn func(*sql.Tx) error) error
}

// Option configures a Ladder.
type Option func(*Ladder)

// WithJitter replaces the +/-30 second retry jitter. Tests inject a constant
// function to make retry times exact.
func WithJitter(fn func(attempt int) time.Duration) Option {
	return func(l *Ladder) {
		if fn != nil {
			l.jitter = fn
		}
	}
}

// Ladder drives the branch and position of obligations through their wake
// lifecycle. It holds no mutable state: a restart is a new Ladder over the
// same writer.
type Ladder struct {
	writer Writer
	jitter func(int) time.Duration
}

// New builds a Ladder over w.
func New(w Writer, opts ...Option) *Ladder {
	l := &Ladder{writer: w, jitter: defaultJitter}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

func defaultJitter(int) time.Duration {
	span := int64(2*MaxJitter) + 1
	return time.Duration(rand.Int64N(span) - int64(MaxJitter))
}

const createJournalSQL = `
CREATE TABLE IF NOT EXISTS ladder_journal (
  seq           INTEGER PRIMARY KEY,
  obligation_id TEXT NOT NULL,
  watch_id      TEXT NOT NULL,
  kind          TEXT NOT NULL,
  status        TEXT NOT NULL,
  next_at       INTEGER NOT NULL,
  evidence_at   INTEGER NOT NULL,
  created_at    INTEGER NOT NULL,
  payload       BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS ladder_journal_by_obligation
  ON ladder_journal(obligation_id, seq);
CREATE INDEX IF NOT EXISTS ladder_journal_due
  ON ladder_journal(status, next_at);
`

func ensureJournal(tx *sql.Tx) error {
	_, err := tx.Exec(createJournalSQL)
	if err != nil {
		return fmt.Errorf("ladder: create journal: %w", err)
	}
	return nil
}

func nano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func appendMarker(tx *sql.Tx, m Marker, kind string, at time.Time) error {
	payload, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("ladder: encode marker: %w", err)
	}
	_, err = tx.Exec(`
		INSERT INTO ladder_journal
		  (obligation_id, watch_id, kind, status, next_at, evidence_at, created_at, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ObligationID, m.WatchID, kind, string(m.Status),
		nano(m.NextAt), nano(m.EvidenceAt), at.UnixNano(), payload)
	if err != nil {
		return fmt.Errorf("ladder: append marker: %w", err)
	}
	return nil
}

func currentMarker(tx *sql.Tx, obligationID string) (Marker, error) {
	var payload []byte
	err := tx.QueryRow(`
		SELECT payload FROM ladder_journal
		WHERE obligation_id = ? ORDER BY seq DESC LIMIT 1`, obligationID,
	).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Marker{}, ErrNotStaged
	}
	if err != nil {
		return Marker{}, fmt.Errorf("ladder: read marker %s: %w", obligationID, err)
	}
	var m Marker
	if err := json.Unmarshal(payload, &m); err != nil {
		return Marker{}, fmt.Errorf("ladder: decode marker %s: %w", obligationID, err)
	}
	return m, nil
}

func loadObligation(tx *sql.Tx, id string) (domain.Obligation, bool, error) {
	var payload []byte
	err := tx.QueryRow(`SELECT payload FROM obligations WHERE id = ?`, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Obligation{}, false, nil
	}
	if err != nil {
		return domain.Obligation{}, false, fmt.Errorf("ladder: read obligation %s: %w", id, err)
	}
	var o domain.Obligation
	if err := json.Unmarshal(payload, &o); err != nil {
		return domain.Obligation{}, false, fmt.Errorf("ladder: decode obligation %s: %w", id, err)
	}
	return o, true, nil
}

func saveObligation(tx *sql.Tx, o domain.Obligation) error {
	payload, err := json.Marshal(o)
	if err != nil {
		return fmt.Errorf("ladder: encode obligation %s: %w", o.ID, err)
	}
	delivered := 0
	if !o.DeliveredAt.IsZero() {
		delivered = 1
	}
	_, err = tx.Exec(`
		UPDATE obligations
		SET payload = ?, delivered_at = ?, verdict_pointer = ?
		WHERE id = ?`,
		payload, delivered, string(o.VerdictPointer), o.ID)
	if err != nil {
		return fmt.Errorf("ladder: update obligation %s: %w", o.ID, err)
	}
	return nil
}

// Stage starts the ladder for o at the verdict that woke it. The initial
// pointer counts as attempt one. Repeating Stage for the same obligation is a
// no-op, which keeps an at-least-once caller from resetting the position.
func (l *Ladder) Stage(ctx context.Context, o domain.Obligation, verdict domain.Verdict, at time.Time) error {
	if o.ID == "" || o.WatchID == "" {
		return errors.New("ladder: stage: empty obligation or watch id")
	}
	return l.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureJournal(tx); err != nil {
			return err
		}
		if _, err := currentMarker(tx, o.ID); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotStaged) {
			return err
		}

		o.VerdictPointer = verdict
		if o.StagedAt.IsZero() {
			o.StagedAt = at
		}
		_, exists, err := loadObligation(tx, o.ID)
		if err != nil {
			return err
		}
		if !exists {
			payload, err := json.Marshal(o)
			if err != nil {
				return err
			}
			_, err = tx.Exec(`
				INSERT OR IGNORE INTO obligations
				  (id, watch_id, revision, subscriber_id, verdict_pointer, delivered_at, payload)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				o.ID, o.WatchID, o.Revision, o.SubscriberID,
				string(o.VerdictPointer), 0, payload)
			if err != nil {
				return fmt.Errorf("ladder: insert obligation %s: %w", o.ID, err)
			}
		} else if err := saveObligation(tx, o); err != nil {
			return err
		}

		m := Marker{
			ObligationID: o.ID,
			WatchID:      o.WatchID,
			Branch:       domain.OutcomeIndeterminate,
			Attempts:     1,
			Status:       StatusActive,
			StagedAt:     at,
			NextAt:       at.Add(EvidenceWindow),
			EvidenceAt:   at.Add(EvidenceWindow),
			Verdict:      verdict,
		}
		return appendMarker(tx, m, "staged", at)
	})
}

// Record consumes one receipt. The outcome is appended to the obligation's
// attempts and the new branch and position are appended as a marker in the
// same transaction as the obligation (and therefore its verdict). A repeated
// receipt for the same attempt number is deduplicated. Read stops everything,
// so a receipt arriving after read is ignored.
func (l *Ladder) Record(ctx context.Context, r Receipt) error {
	switch r.Outcome {
	case domain.OutcomePosted, domain.OutcomeTransportFailed, domain.OutcomeIndeterminate:
	default:
		return fmt.Errorf("ladder: unknown outcome %q", r.Outcome)
	}
	return l.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureJournal(tx); err != nil {
			return err
		}
		m, err := currentMarker(tx, r.ObligationID)
		if err != nil {
			return err
		}
		if m.Status == StatusStopped {
			return nil
		}

		o, exists, err := loadObligation(tx, r.ObligationID)
		if err != nil {
			return err
		}
		n := r.Attempt
		if n <= 0 {
			n = m.Attempts + 1
		}
		seen := false
		for _, a := range o.Attempts {
			if a.N == n {
				seen = true
				break
			}
		}
		if !seen {
			o.Attempts = append(o.Attempts, domain.Attempt{
				N: n, At: r.At, Outcome: r.Outcome,
			})
			sort.Slice(o.Attempts, func(i, j int) bool {
				return o.Attempts[i].N < o.Attempts[j].N
			})
		}

		switch r.Outcome {
		case domain.OutcomePosted:
			// No retries: wait for claimed or read until the evidence window.
			m.Branch = domain.OutcomePosted
			m.NextAt = m.EvidenceAt
		case domain.OutcomeTransportFailed:
			m.Branch = domain.OutcomeTransportFailed
			idx := m.Attempts - 1
			if idx < 0 {
				idx = 0
			}
			if idx >= len(retryDelays) {
				m.NextAt = m.EvidenceAt
			} else {
				m.NextAt = m.StagedAt.Add(retryDelays[idx] + l.jitter(m.Attempts+1))
			}
		case domain.OutcomeIndeterminate:
			// Never retry. Only the evidence check remains.
			m.Branch = domain.OutcomeIndeterminate
			m.NextAt = m.EvidenceAt
		}

		if exists {
			if err := saveObligation(tx, o); err != nil {
				return err
			}
		}
		return appendMarker(tx, m, "receipt", r.At)
	})
}

// Due returns the active markers whose next timer has fired by now. It does
// not mutate anything; consume each entry with Advance.
func (l *Ladder) Due(ctx context.Context, now time.Time) ([]Due, error) {
	var out []Due
	err := l.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureJournal(tx); err != nil {
			return err
		}
		rows, err := tx.Query(`
			SELECT j.payload FROM ladder_journal j
			JOIN (
			  SELECT obligation_id, MAX(seq) AS seq FROM ladder_journal
			  GROUP BY obligation_id
			) latest ON j.seq = latest.seq
			WHERE j.status = ? AND j.next_at > 0 AND j.next_at <= ?
			ORDER BY j.next_at, j.obligation_id`,
			string(StatusActive), now.UnixNano())
		if err != nil {
			return fmt.Errorf("ladder: due query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var payload []byte
			if err := rows.Scan(&payload); err != nil {
				return fmt.Errorf("ladder: due scan: %w", err)
			}
			var m Marker
			if err := json.Unmarshal(payload, &m); err != nil {
				return fmt.Errorf("ladder: due decode: %w", err)
			}
			kind := ActionRetry
			if !m.NextAt.Before(m.EvidenceAt) {
				kind = ActionEvidence
			}
			out = append(out, Due{
				ObligationID: m.ObligationID,
				WatchID:      m.WatchID,
				Kind:         kind,
				Attempt:      m.Attempts + 1,
				At:           m.NextAt,
			})
		}
		return rows.Err()
	})
	return out, err
}

// Advance consumes the timer that fired for id at at. It appends the new
// position to the journal and returns the action the caller should perform.
// A call before the next timer, on a stopped marker, or on a marker with no
// timer is a no-op (zero Action).
func (l *Ladder) Advance(ctx context.Context, id string, at time.Time) (Action, error) {
	var action Action
	err := l.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureJournal(tx); err != nil {
			return err
		}
		m, err := currentMarker(tx, id)
		if err != nil {
			return err
		}
		if m.Status != StatusActive || m.NextAt.IsZero() || at.Before(m.NextAt) {
			return nil
		}

		if !m.NextAt.Before(m.EvidenceAt) {
			// Evidence window elapsed without claimed or read. Hand off to
			// escalation; this package never closes a Watch (ADR 0019).
			m.Status = StatusEscalated
			m.NextAt = time.Time{}
			if err := appendMarker(tx, m, "evidence", at); err != nil {
				return err
			}
			action = Action{
				Kind: ActionEvidence, ObligationID: id,
				WatchID: m.WatchID, At: at,
			}
			return nil
		}

		if m.Branch != domain.OutcomeTransportFailed || m.Attempts >= MaxAttempts {
			m.NextAt = m.EvidenceAt
			return appendMarker(tx, m, "wait", at)
		}

		next := m.Attempts + 1
		m.Attempts = next
		if next < MaxAttempts {
			idx := next - 1
			m.NextAt = m.StagedAt.Add(retryDelays[idx] + l.jitter(next))
		} else {
			m.NextAt = m.EvidenceAt
		}
		if err := appendMarker(tx, m, "retry", at); err != nil {
			return err
		}
		action = Action{
			Kind: ActionRetry, ObligationID: id,
			WatchID: m.WatchID, Attempt: next, At: at,
		}
		return nil
	})
	return action, err
}

// MarkRead stops every timer for an obligation. read terminates the ladder.
func (l *Ladder) MarkRead(ctx context.Context, id string, at time.Time) error {
	return l.stop(ctx, id, "read", at)
}

// MarkClaimed stops every timer for an obligation. claimed is the other
// evidence that makes an evidence check unnecessary.
func (l *Ladder) MarkClaimed(ctx context.Context, id string, at time.Time) error {
	return l.stop(ctx, id, "claimed", at)
}

func (l *Ladder) stop(ctx context.Context, id, kind string, at time.Time) error {
	return l.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureJournal(tx); err != nil {
			return err
		}
		m, err := currentMarker(tx, id)
		if err != nil {
			return err
		}
		if m.Status == StatusStopped {
			return nil
		}
		m.Status = StatusStopped
		m.NextAt = time.Time{}
		m.EvidenceAt = time.Time{}

		o, exists, err := loadObligation(tx, id)
		if err != nil {
			return err
		}
		if exists {
			if kind == "read" {
				o.ReadAt = at
			} else {
				o.ClaimedAt = at
			}
			if err := saveObligation(tx, o); err != nil {
				return err
			}
		}
		return appendMarker(tx, m, kind, at)
	})
}

// Position returns the current marker for id, read from the journal.
func (l *Ladder) Position(ctx context.Context, id string) (Marker, error) {
	var m Marker
	err := l.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureJournal(tx); err != nil {
			return err
		}
		var readErr error
		m, readErr = currentMarker(tx, id)
		return readErr
	})
	return m, err
}
