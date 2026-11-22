// Package store: atomic commit of a verdict, its obligation and a wake intent.
//
// ADR 0015 requires the verdict, the obligation and the ladder position to be
// written in one transaction. WORKER-PROMPT §4.2 п.7 and §4.3 require the wake
// intent to travel in the same write as the verdict, and require every mutation
// to reach disk before the client is answered. In SQLite this is one COMMIT on
// the single writer: a reader on another connection either sees all three
// components or sees none of them.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// wakeSchemaVersion is the version of the reader-visible wake-intent payload.
type wakeSchemaVersion int

const (
	// wakeIntentKind marks the journal row that carries the staged wake request.
	wakeIntentKind = "wake-intent"

	// wakeSchemaV2 is the payload format from ADR 0012 that readers decode today.
	wakeSchemaV2 wakeSchemaVersion = 2

	// defaultWakeSchemaVersion is stamped on rows written by CommitVerdict. It
	// is the journal row version, not the SQLite user_version. A caller can
	// override it when it commits with a newer decoder.
	defaultWakeSchemaVersion = wakeSchemaV2
)

// wakeReaderFields lists the payload fields a reader consumes. DecodeEvent in
// redaction.go reads kind and message. Every other payload field is raw: it is
// stored for diagnostics and does not select a decoder.
type wakeReaderFields struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// wakeSchemaByReaderFields is the bump ledger. Each reader format has one
// version. When a reader starts using a new field, add a higher version here
// and a golden fixture for that version. Do not change an existing entry,
// because old readers still decode rows written with that version.
var wakeSchemaByReaderFields = map[wakeReaderFields]wakeSchemaVersion{
	{Kind: wakeIntentKind}: defaultWakeSchemaVersion,
}

// wakeSchemaVersionForPayload derives the row version from the fields a reader
// can observe. A raw field left for diagnostics leaves the version unchanged.
// A reader-visible change without a ledger entry fails closed.
func wakeSchemaVersionForPayload(payload []byte) (wakeSchemaVersion, error) {
	var fields wakeReaderFields
	if err := json.Unmarshal(payload, &fields); err != nil {
		return 0, fmt.Errorf("store: wake schema: decode reader fields: %w", err)
	}
	version, ok := wakeSchemaByReaderFields[fields]
	if !ok {
		return 0, fmt.Errorf("store: wake schema: no version for reader fields %+v", fields)
	}
	return version, nil
}

// WakeIntent is the staged marker: the request to wake the subscriber of one
// obligation. It is written with the verdict, never after it (ADR 0017).
type WakeIntent struct {
	WatchID      string    `json:"watchID"`
	ObligationID string    `json:"obligationID"`
	Revision     uint64    `json:"revision"`
	StagedAt     time.Time `json:"stagedAt"`
}

// VerdictCommit is the triple that becomes visible to a client only together.
// Obligation.ID must be domain.ObligationKey(Watch.ID, Revision,
// Obligation.SubscriberID): that key is the idempotency rule from ADR 0015.
type VerdictCommit struct {
	Watch         domain.Watch
	Obligation    domain.Obligation
	Wake          WakeIntent
	SchemaVersion int // zero selects defaultWakeSchemaVersion
}

// VerdictWriter is the single-writer port used by CommitVerdict. Both *DB and
// a redacting writer that wraps it satisfy this interface.
type VerdictWriter interface {
	Write(ctx context.Context, fn func(*sql.Tx) error) error
}

// CommitVerdictTx inserts the verdict, the obligation and the wake intent into
// one transaction. It never commits or rolls back: the caller owns tx, which
// is what makes a forced rollback in the middle of the write testable.
//
// Repeating the same triple adds no second row. The obligation is protected by
// UNIQUE(watch_id, revision, subscriber_id) and ON CONFLICT DO NOTHING; the
// verdict is an upsert on the watch id; the wake intent is inserted only when
// no row for the same obligation id exists yet.
func CommitVerdictTx(tx *sql.Tx, c VerdictCommit) error {
	if tx == nil {
		return errors.New("store: commit verdict: nil transaction")
	}
	if err := validateVerdictCommit(c); err != nil {
		return err
	}

	watchPayload, err := json.Marshal(c.Watch)
	if err != nil {
		return fmt.Errorf("store: commit verdict: marshal watch: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO watches
		  (id, repo, pr, head_sha, status, revision, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  repo     = excluded.repo,
		  pr       = excluded.pr,
		  head_sha = excluded.head_sha,
		  status   = excluded.status,
		  revision = excluded.revision,
		  payload  = excluded.payload`,
		c.Watch.ID, c.Watch.Repo, c.Watch.PR, c.Watch.HeadSHA,
		string(c.Watch.Status), c.Watch.Revision, watchPayload); err != nil {
		return fmt.Errorf("store: commit verdict: write watch: %w", err)
	}

	obligationPayload, err := json.Marshal(c.Obligation)
	if err != nil {
		return fmt.Errorf("store: commit verdict: marshal obligation: %w", err)
	}
	delivered := 0
	if !c.Obligation.DeliveredAt.IsZero() {
		delivered = 1
	}
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO obligations
		  (id, watch_id, revision, subscriber_id, verdict_pointer, delivered_at, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.Obligation.ID, c.Obligation.WatchID, c.Obligation.Revision,
		c.Obligation.SubscriberID, string(c.Obligation.VerdictPointer),
		delivered, obligationPayload); err != nil {
		return fmt.Errorf("store: commit verdict: write obligation: %w", err)
	}

	wakePayload, err := json.Marshal(struct {
		Kind string `json:"kind"`
		WakeIntent
	}{Kind: wakeIntentKind, WakeIntent: c.Wake})
	if err != nil {
		return fmt.Errorf("store: commit verdict: marshal wake intent: %w", err)
	}
	version := c.SchemaVersion
	if version == 0 {
		derived, err := wakeSchemaVersionForPayload(wakePayload)
		if err != nil {
			return err
		}
		version = int(derived)
	}
	// json_extract reads only the rows this function writes. Legacy rows from
	// the migration corpus may carry arbitrary bytes, which compare as NULL.
	if _, err := tx.Exec(`
		INSERT INTO events (watch_id, schema_version, payload, created_at)
		SELECT ?, ?, ?, ?
		WHERE NOT EXISTS (
		  SELECT 1 FROM events
		  WHERE watch_id = ?
		    AND json_extract(payload, '$.kind') = ?
		    AND json_extract(payload, '$.obligationID') = ?)`,
		c.Wake.WatchID, version, wakePayload, c.Wake.StagedAt.UnixNano(),
		c.Wake.WatchID, wakeIntentKind, c.Wake.ObligationID); err != nil {
		return fmt.Errorf("store: commit verdict: write wake intent: %w", err)
	}
	return nil
}

// CommitVerdict writes the triple through w and returns only after w has
// committed it. The caller may pass *DB directly or a writer that redacts
// payloads before delegating to *DB.
func CommitVerdict(ctx context.Context, w VerdictWriter, c VerdictCommit) error {
	if w == nil {
		return errors.New("store: commit verdict: nil writer")
	}
	if err := validateVerdictCommit(c); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return w.Write(ctx, func(tx *sql.Tx) error { return CommitVerdictTx(tx, c) })
}

// CommitVerdict is the convenience form bound to the store's single writer.
func (d *DB) CommitVerdict(ctx context.Context, c VerdictCommit) error {
	return CommitVerdict(ctx, d, c)
}

func validateVerdictCommit(c VerdictCommit) error {
	if c.Watch.ID == "" || c.Obligation.ID == "" || c.Obligation.SubscriberID == "" {
		return errors.New("store: commit verdict: watch, obligation and subscriber id are required")
	}
	if c.Obligation.WatchID != c.Watch.ID ||
		c.Obligation.Revision != c.Watch.Revision ||
		c.Wake.Revision != c.Watch.Revision {
		return errors.New("store: commit verdict: watch, obligation and wake revision mismatch")
	}
	if want := domain.ObligationKey(c.Watch.ID, c.Watch.Revision, c.Obligation.SubscriberID); c.Obligation.ID != want {
		return fmt.Errorf("store: commit verdict: obligation id %q, want %q", c.Obligation.ID, want)
	}
	if c.Wake.WatchID != c.Watch.ID || c.Wake.ObligationID != c.Obligation.ID {
		return errors.New("store: commit verdict: wake intent points at a different watch or obligation")
	}
	if c.Watch.Verdict == "" || c.Obligation.VerdictPointer == "" {
		return errors.New("store: commit verdict: verdict and verdict pointer are required")
	}
	if c.Obligation.VerdictPointer != c.Watch.Verdict {
		return errors.New("store: commit verdict: verdict pointer does not match the verdict")
	}
	return nil
}
