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
	// StatusPaused means the episode is parked at a human gate. Timers are
	// suspended until a human ack decides the next step.
	StatusPaused Status = "paused"
	// StatusApproved is terminal approval at the gate. It is distinct from
	// stopped (read or claimed) and escalated so a human sign-off is never
	// confused with a delivery outcome or a timeout (ADR 0025).
	StatusApproved Status = "approved"
)

var (
	// ErrNotStaged reports a position request for an obligation that never
	// went through Stage.
	ErrNotStaged = errors.New("ladder: obligation not staged")
	// ErrGateNotPaused reports an ack applied to an obligation that is not
	// currently parked at a gate.
	ErrGateNotPaused = errors.New("ladder: obligation not paused at a gate")
	// ErrInvalidAckOutcome reports an ack outcome outside the three defined by
	// ADR 0031 section 6.
	ErrInvalidAckOutcome = errors.New("ladder: invalid ack outcome")
	// ErrInvalidGateStep reports a rework target outside the ladder's steps.
	ErrInvalidGateStep = errors.New("ladder: invalid gate rework step")
)

// AckOutcome is the human decision at a gate. The three values are deliberately
// distinct: approval ends the episode, context resumes it from the pause point,
// and rework rewinds the ladder to a chosen step (ADR 0031 section 6).
type AckOutcome string

const (
	// AckApprove is a human sign-off. It is terminal and triggers no rework.
	AckApprove AckOutcome = "approve"
	// AckContext records added context and resumes from where the gate paused.
	AckContext AckOutcome = "context"
	// AckRework sends the work back to TargetStep, an attempt in
	// [1, MaxAttempts]. It is the "ack with rework" of ADR 0031.
	AckRework AckOutcome = "rework"
)

// GateDecision is one human ack applied at a paused gate. TargetStep is used
// only by AckRework; Context is recorded for every outcome and is required in
// practice for AckContext. At is supplied by the caller so the package stays
// deterministic.
type GateDecision struct {
	Outcome    AckOutcome
	TargetStep int
	Context    string
	Reviewer   string
	At         time.Time
}

// gateHold is the ladder position captured when a gate pauses an episode. An
// "add context" ack restores it so the ladder continues from the exact point
// the human interrupted instead of restarting.
type gateHold struct {
	Branch   domain.AttemptOutcome `json:"branch"`
	Attempts int                   `json:"attempts"`
	NextAt   time.Time             `json:"nextAt"`
}

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

	// Gate state. It is appended to the marker so the persisted state, not
	// process memory, is what distinguishes approval from rework. Old markers
	// decode with zero values, so this is backward compatible.
	AckOutcome  AckOutcome `json:"ackOutcome,omitempty"`
	AckStep     int        `json:"ackStep,omitempty"`
	AckContext  string     `json:"ackContext,omitempty"`
	AckReviewer string     `json:"ackReviewer,omitempty"`
	Held        *gateHold  `json:"held,omitempty"`
	// HumanAlerted records that the episode already notified a human. The
	// first revoke sets it; later revokes of the same episode do not issue a
	// second alert. It lives on the marker so a restart cannot reset it.
	HumanAlerted bool `json:"humanAlerted,omitempty"`
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
		// Rung 1 is the initial pointer and no retry is armed yet. These live
		// on Obligation so saveObligation persists them with the verdict.
		o.DeliveryRung = 1
		o.DeliveryAttempts = 1
		o.NextRetryAt = time.Time{}
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
		// A paused gate is authoritative: no delivery evidence is recorded
		// while a human decides. Approval is terminal. Both extend the
		// existing "read stops everything" rule without changing the
		// ADR 0017 branches for active episodes.
		if m.Status == StatusStopped || m.Status == StatusPaused || m.Status == StatusApproved {
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

		// Keep the delivery markers on the obligation so beacon inspect shows
		// the current rung, the attempts seen and the armed retry time.
		o.DeliveryRung = m.Attempts
		o.DeliveryAttempts = len(o.Attempts)
		if m.Branch == domain.OutcomeTransportFailed {
			o.NextRetryAt = m.NextAt
		} else {
			o.NextRetryAt = time.Time{}
		}

		if exists {
			if err := saveObligation(tx, o); err != nil {
				return err
			}
		}
		return appendMarker(tx, m, "receipt", r.At)
	})
}

// RevokeResult is the work a caller must do after a revoke. HumanAlert is true
// only for the first revoke of an episode, so one incident never notifies a
// human twice. Retry is true when the revoke armed the next delivery.
type RevokeResult struct {
	ObligationID string
	WatchID      string
	HumanAlert   bool
	Retry        bool
	Rung         int
	Attempts     int
	NextAt       time.Time
}

// Revoke consumes a failed delivery reported by a domain revoke event. It
// records the failure on the obligation, advances the delivery rung and arms
// the next retry from the rung it replaces. The first revoke of an episode
// emits one human alert; later revokes of the same episode do not. A retry is
// armed only when a revoke arrives, so retries stay tied to revokes. Revokes
// on a stopped, paused, approved or escalated episode are no-ops.
func (l *Ladder) Revoke(ctx context.Context, r domain.DeliveryRevoke, at time.Time) (RevokeResult, error) {
	var out RevokeResult
	if r.ObligationID == "" {
		return out, errors.New("ladder: revoke: empty obligation id")
	}
	outcome := r.Outcome
	if outcome == "" {
		outcome = domain.OutcomeTransportFailed
	}
	switch outcome {
	case domain.OutcomePosted, domain.OutcomeTransportFailed, domain.OutcomeIndeterminate:
	default:
		return out, fmt.Errorf("ladder: revoke: unknown outcome %q", r.Outcome)
	}

	err := l.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureJournal(tx); err != nil {
			return err
		}
		m, err := currentMarker(tx, r.ObligationID)
		if err != nil {
			return err
		}
		if m.Status != StatusActive {
			return nil
		}
		o, exists, err := loadObligation(tx, r.ObligationID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotStaged
		}

		// One alert per episode. The flag is on the marker, so a restart or a
		// repeated revoke cannot open a second alert.
		out.HumanAlert = !m.HumanAlerted
		m.HumanAlerted = true

		// Every revoke is a failure report, so it counts as a delivery try
		// even after the retry cap is spent. The rung stays capped.
		o.DeliveryAttempts++

		nextRung := m.Attempts + 1
		if nextRung <= MaxAttempts {
			idx := nextRung - 2
			nextAt := m.StagedAt.Add(retryDelays[idx] + l.jitter(nextRung))
			m.Attempts = nextRung
			m.NextAt = nextAt
			o.DeliveryRung = nextRung
			o.NextRetryAt = nextAt
			out.Retry = true
			out.NextAt = nextAt
		} else {
			// The retry cap is spent. The evidence check owns the rest and no
			// further retry is armed.
			m.NextAt = m.EvidenceAt
			o.DeliveryRung = MaxAttempts
			o.NextRetryAt = time.Time{}
		}

		if err := saveObligation(tx, o); err != nil {
			return err
		}
		if err := appendMarker(tx, m, "revoke", at); err != nil {
			return err
		}
		out.ObligationID = m.ObligationID
		out.WatchID = m.WatchID
		out.Rung = o.DeliveryRung
		out.Attempts = o.DeliveryAttempts
		return nil
	})
	if err != nil {
		return RevokeResult{}, err
	}
	return out, nil
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

// PendingCount returns the number of obligations whose latest ladder marker is
// still active, i.e. waiting for a retry or the evidence check. It is the
// pending half of beacon_stats.ladder and reads no process-local state.
func (l *Ladder) PendingCount(ctx context.Context) (int, error) {
	n := 0
	if ctx == nil {
		ctx = context.Background()
	}
	err := l.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureJournal(tx); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM ladder_journal j
			JOIN (
			  SELECT obligation_id, MAX(seq) AS seq FROM ladder_journal
			  GROUP BY obligation_id
			) latest ON j.seq = latest.seq
			WHERE j.status = ?`, string(StatusActive)).Scan(&n)
	})
	if err != nil {
		return 0, fmt.Errorf("ladder: pending count: %w", err)
	}
	return n, nil
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

// Pause parks an active obligation at a human gate. Its timers are suspended:
// Due stops returning it and Advance is a no-op until a human ack decides. The
// position at pause is held so an "add context" ack can resume from it. Only an
// active episode can be paused; any other status is a no-op, which keeps an
// at-least-once caller from disturbing a stopped or escalated episode.
func (l *Ladder) Pause(ctx context.Context, id string, at time.Time) error {
	return l.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureJournal(tx); err != nil {
			return err
		}
		m, err := currentMarker(tx, id)
		if err != nil {
			return err
		}
		if m.Status != StatusActive {
			return nil
		}
		m.Held = &gateHold{Branch: m.Branch, Attempts: m.Attempts, NextAt: m.NextAt}
		m.Status = StatusPaused
		m.NextAt = time.Time{}
		// EvidenceAt is absolute from staged and is kept, so a resumed or
		// reworked ladder still honours the 15-minute proof window (ADR 0017).
		return appendMarker(tx, m, "gate-pause", at)
	})
}

// Ack applies a human decision at a paused gate. The three outcomes differ in
// the persisted marker and in the journal kind, so the ladder state records
// which one happened:
//
//   - AckApprove is terminal (StatusApproved), cancels every timer and writes
//     no rework.
//   - AckContext keeps the context, clears the pause and resumes the ladder
//     from the exact position captured at Pause.
//   - AckRework rewinds the ladder to TargetStep, an attempt in
//     [1, MaxAttempts]. It re-arms the retry branch so the pointer for that
//     step is delivered again, then normal ADR 0017 timing continues.
//
// Ack requires a paused marker; otherwise it returns ErrGateNotPaused.
func (l *Ladder) Ack(ctx context.Context, id string, d GateDecision) error {
	switch d.Outcome {
	case AckApprove, AckContext, AckRework:
	default:
		return ErrInvalidAckOutcome
	}
	if d.Outcome == AckRework && (d.TargetStep < 1 || d.TargetStep > MaxAttempts) {
		return ErrInvalidGateStep
	}
	return l.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureJournal(tx); err != nil {
			return err
		}
		m, err := currentMarker(tx, id)
		if err != nil {
			return err
		}
		if m.Status != StatusPaused {
			return ErrGateNotPaused
		}

		m.AckOutcome = d.Outcome
		m.AckStep = d.TargetStep
		m.AckContext = d.Context
		m.AckReviewer = d.Reviewer

		switch d.Outcome {
		case AckApprove:
			m.Status = StatusApproved
			m.NextAt = time.Time{}
			m.EvidenceAt = time.Time{}
			m.Held = nil
			return appendMarker(tx, m, "gate-approve", d.At)
		case AckContext:
			if m.Held != nil {
				m.Status = StatusActive
				m.Branch = m.Held.Branch
				m.Attempts = m.Held.Attempts
				m.NextAt = m.Held.NextAt
				m.Held = nil
			}
			return appendMarker(tx, m, "gate-context", d.At)
		case AckRework:
			m.Status = StatusActive
			// Re-delivery is a retry of the same pointer, so it re-enters the
			// ADR 0017 retry branch. Attempts is one behind the target because
			// Due and Advance report the next attempt as Attempts+1.
			m.Branch = domain.OutcomeTransportFailed
			m.Attempts = d.TargetStep - 1
			m.NextAt = l.reworkTime(m, d.TargetStep, d.At)
			m.Held = nil
			return appendMarker(tx, m, "gate-rework", d.At)
		}
		return nil
	})
}

// reworkTime is when the retry for step becomes due. Step 1 is the initial
// pointer and is re-sent immediately; later steps use the ADR 0017 retry delay
// for that attempt, measured from the original stage (as Record and Advance do).
func (l *Ladder) reworkTime(m Marker, step int, at time.Time) time.Time {
	if step <= 1 {
		return at
	}
	idx := step - 2
	if idx >= len(retryDelays) {
		return m.EvidenceAt
	}
	return m.StagedAt.Add(retryDelays[idx] + l.jitter(step))
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

// ---------------------------------------------------------------------------
// Quorum from completed reviews (ADR 0038 section 5).
//
// A decision is adopted only after the required number of approvals comes from
// reviews that reached the end and issued a verdict. Reviews that were
// interrupted, never finished, or ended in error produce no approval marker and
// shrink the pool of approvals still possible. When that pool falls below the
// threshold the quorum is unreachable and is rejected at once instead of
// waiting forever. The SHIP verdict of the independent review gate (ADR 0038
// section 4) is one source of an approval marker.
//
// This is a separate concern from the ADR 0017 delivery ladder above: it reads
// and writes no ladder marker and changes no ladder timing. Review markers live
// in their own table in the same store, written through the same Writer.
// ---------------------------------------------------------------------------

// ReviewVerdict is the outcome a completed review issued. The three values
// mirror the independent review gate of ADR 0038 section 4. Only ReviewShip is
// an approval marker for the quorum.
type ReviewVerdict string

const (
	ReviewShip     ReviewVerdict = "SHIP"
	ReviewFixFirst ReviewVerdict = "FIX-FIRST"
	ReviewBlocked  ReviewVerdict = "BLOCKED"
)

func validReviewVerdict(v ReviewVerdict) bool {
	switch v {
	case ReviewShip, ReviewFixFirst, ReviewBlocked:
		return true
	}
	return false
}

// ReviewState is how far a review got. Only ReviewDone can carry an approval,
// and only when its verdict is ReviewShip. ReviewRunning may still produce an
// approval. ReviewAborted (interrupted, incomplete, or errored) cannot.
type ReviewState string

const (
	ReviewRunning ReviewState = "running"
	ReviewDone    ReviewState = "done"
	ReviewAborted ReviewState = "aborted"
)

// QuorumResult is the decision of a quorum evaluation.
type QuorumResult string

const (
	// QuorumPending means the threshold is not met yet but is still reachable.
	QuorumPending QuorumResult = "pending"
	// QuorumReached means completed approvals meet or exceed the threshold.
	QuorumReached QuorumResult = "reached"
	// QuorumUnreachable means the most approvals still possible is below the
	// threshold, so the quorum must be rejected instead of waiting.
	QuorumUnreachable QuorumResult = "unreachable"
)

// QuorumDecision reports a quorum evaluation with the counts behind it, so a
// caller can tell approvals from markers that were still running or discarded.
type QuorumDecision struct {
	Result      QuorumResult
	Approvals   int // completed reviews that issued ReviewShip
	MaxPossible int // approvals plus reviews still running
	Threshold   int
}

// ReviewMarker is one review's contribution to a quorum evaluation.
type ReviewMarker struct {
	ReviewID string
	State    ReviewState
	Verdict  ReviewVerdict
}

var (
	// ErrInvalidThreshold reports a quorum threshold below one.
	ErrInvalidThreshold = errors.New("ladder: invalid quorum threshold")
	// ErrInvalidReviewVerdict reports a verdict outside the three review gate values.
	ErrInvalidReviewVerdict = errors.New("ladder: invalid review verdict")
	// ErrReviewNotRunning reports a transition for a review that is unknown or
	// already terminal.
	ErrReviewNotRunning = errors.New("ladder: review not running")
)

const createQuorumSQL = `
CREATE TABLE IF NOT EXISTS quorum_reviews (
  task_id   TEXT NOT NULL,
  review_id TEXT NOT NULL,
  state     TEXT NOT NULL,
  verdict   TEXT NOT NULL,
  PRIMARY KEY (task_id, review_id)
);
`

func ensureQuorum(tx *sql.Tx) error {
	if _, err := tx.Exec(createQuorumSQL); err != nil {
		return fmt.Errorf("ladder: create quorum reviews: %w", err)
	}
	return nil
}

// AddReview registers a review as running for taskID. Re-adding an existing
// review is a no-op so an at-least-once caller cannot reset a terminal review.
func (l *Ladder) AddReview(ctx context.Context, taskID, reviewID string) error {
	if taskID == "" || reviewID == "" {
		return errors.New("ladder: add review: empty task or review id")
	}
	return l.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureQuorum(tx); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO quorum_reviews (task_id, review_id, state, verdict)
			VALUES (?, ?, ?, ?)`, taskID, reviewID, string(ReviewRunning), "")
		if err != nil {
			return fmt.Errorf("ladder: add review: %w", err)
		}
		return nil
	})
}

// CompleteReview records that a review reached the end and issued v. Only a
// running review can be completed.
func (l *Ladder) CompleteReview(ctx context.Context, taskID, reviewID string, v ReviewVerdict) error {
	if !validReviewVerdict(v) {
		return ErrInvalidReviewVerdict
	}
	return l.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureQuorum(tx); err != nil {
			return err
		}
		res, err := tx.Exec(`
			UPDATE quorum_reviews SET state = ?, verdict = ?
			WHERE task_id = ? AND review_id = ? AND state = ?`,
			string(ReviewDone), string(v), taskID, reviewID, string(ReviewRunning))
		if err != nil {
			return fmt.Errorf("ladder: complete review: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("ladder: complete review rows: %w", err)
		}
		if n == 0 {
			return ErrReviewNotRunning
		}
		return nil
	})
}

// AbortReview records that a review was interrupted, left incomplete, or ended
// in error. An aborted review yields no approval marker.
func (l *Ladder) AbortReview(ctx context.Context, taskID, reviewID string) error {
	return l.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureQuorum(tx); err != nil {
			return err
		}
		res, err := tx.Exec(`
			UPDATE quorum_reviews SET state = ?, verdict = ''
			WHERE task_id = ? AND review_id = ? AND state = ?`,
			string(ReviewAborted), taskID, reviewID, string(ReviewRunning))
		if err != nil {
			return fmt.Errorf("ladder: abort review: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("ladder: abort review rows: %w", err)
		}
		if n == 0 {
			return ErrReviewNotRunning
		}
		return nil
	})
}

// classifyQuorum counts approval markers from completed reviews and decides
// whether the quorum is reached, still pending, or unreachable. Only a review
// that is done with the SHIP verdict counts as an approval. A running review can
// still become an approval, so it adds to the maximum possible; a done review
// with a non-SHIP verdict and an aborted review cannot.
func classifyQuorum(reviews []ReviewMarker, threshold int) (QuorumDecision, error) {
	if threshold < 1 {
		return QuorumDecision{}, ErrInvalidThreshold
	}
	d := QuorumDecision{Threshold: threshold}
	for _, r := range reviews {
		switch {
		case r.State == ReviewDone && r.Verdict == ReviewShip:
			d.Approvals++
		case r.State == ReviewRunning:
			d.MaxPossible++
		}
	}
	d.MaxPossible += d.Approvals
	switch {
	case d.Approvals >= threshold:
		d.Result = QuorumReached
	case d.MaxPossible < threshold:
		d.Result = QuorumUnreachable
	default:
		d.Result = QuorumPending
	}
	return d, nil
}

// Quorum evaluates the decision for taskID against threshold. It counts only
// approval markers from reviews that reached the end with the SHIP verdict.
func (l *Ladder) Quorum(ctx context.Context, taskID string, threshold int) (QuorumDecision, error) {
	if threshold < 1 {
		return QuorumDecision{}, ErrInvalidThreshold
	}
	var reviews []ReviewMarker
	err := l.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureQuorum(tx); err != nil {
			return err
		}
		rows, err := tx.Query(`
			SELECT review_id, state, verdict FROM quorum_reviews
			WHERE task_id = ? ORDER BY review_id`, taskID)
		if err != nil {
			return fmt.Errorf("ladder: quorum query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var m ReviewMarker
			var state, verdict string
			if err := rows.Scan(&m.ReviewID, &state, &verdict); err != nil {
				return fmt.Errorf("ladder: quorum scan: %w", err)
			}
			m.State = ReviewState(state)
			m.Verdict = ReviewVerdict(verdict)
			reviews = append(reviews, m)
		}
		return rows.Err()
	})
	if err != nil {
		return QuorumDecision{}, err
	}
	return classifyQuorum(reviews, threshold)
}
