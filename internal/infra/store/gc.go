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

// ObligationGCRetention is the replay window for obligations of closed Watches.
// A short replay can still need the pointer after the Watch closes, so the
// collector keeps recent rows and erases only the ones older than this window.
const ObligationGCRetention = 24 * time.Hour

const gcObligationActor = "beacon-gc"

// gcBetweenPlanAndApply runs once after the collector builds its plan and
// before the apply phase. Production keeps the empty default; the concurrency
// test installs a hook that changes the store through a second handle.
var gcBetweenPlanAndApply = func() {}

// ObligationGCItem is one obligation the collector selected. Reason names the
// state of the Watch: "closed", "archived" or "missing". Skipped is set on
// apply when the candidate changed after the plan and was left in place.
type ObligationGCItem struct {
	ID      string `json:"id"`
	WatchID string `json:"watchID"`
	Reason  string `json:"reason"`
	Skipped bool   `json:"skipped,omitempty"`
}

// gcWatchState is what the collector knew about a Watch when it built the
// plan. known distinguishes a Watch still in the active table from one that
// is gone; a non-live Watch with no close time is known but not collectible.
type gcWatchState struct {
	known    bool
	status   domain.Status
	closedAt time.Time
	archived bool
}

// gcCandidate holds a plan row together with the snapshot facts that the apply
// phase rechecks inside the delete transaction. The snapshot keeps a second
// process from making a fresh Watch or obligation look collectible.
type gcCandidate struct {
	item               ObligationGCItem
	watchKnown         bool
	watchArchived      bool
	watchStatus        domain.Status
	watchClosedAt      time.Time
	obligationRevision uint64
}

// GCObligations selects obligations whose Watch is no longer live and whose
// close time has passed the retention window, then erases them when apply is
// true. The caller gets the same items for a dry run and for an apply, so the
// report stays honest.
//
// Eligibility has two gates. First, the Watch must not be watching, notifying
// or blocked. Second, the obligation must be old: for a closed or archived
// Watch the close time is the reference, and for a Watch whose row is gone the
// obligation's own last activity is the reference. A non-live Watch with no
// close time is kept, because its state is not proven closed.
//
// Apply rechecks each candidate inside the erase transaction. The Watch
// status, its close time and the obligation revision must still match the
// snapshot. A changed or newly created row is skipped and reported with
// Skipped set, so a second process cannot lose an obligation it created after
// the plan. Every erase still writes the ledger before the delete, which
// keeps a repeated pass from removing anything.
func (d *DB) GCObligations(ctx context.Context, now time.Time, retention time.Duration, apply bool) ([]ObligationGCItem, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		return nil, errors.New("store: gc-obligations: zero time")
	}
	if retention <= 0 {
		return nil, errors.New("store: gc-obligations: retention must be positive")
	}

	live, err := d.LiveWatches()
	if err != nil {
		return nil, fmt.Errorf("store: gc-obligations: live watches: %w", err)
	}
	liveIDs := make(map[string]bool, len(live))
	for _, w := range live {
		liveIDs[w.ID] = true
	}

	states := make(map[string]gcWatchState)
	all, err := d.AllWatches()
	if err != nil {
		return nil, fmt.Errorf("store: gc-obligations: watches: %w", err)
	}
	for _, w := range all {
		if liveIDs[w.ID] {
			continue
		}
		states[w.ID] = gcWatchState{known: true, status: w.Status, closedAt: utcOrZero(w.ClosedAt)}
	}
	archived, err := d.ArchivedWatches()
	if err != nil {
		return nil, fmt.Errorf("store: gc-obligations: archive: %w", err)
	}
	for _, w := range archived {
		states[w.ID] = gcWatchState{known: true, status: w.Status, closedAt: utcOrZero(w.ClosedAt), archived: true}
	}

	rows, err := d.readDB.QueryContext(ctx, `SELECT id, watch_id, payload FROM obligations ORDER BY seq`)
	if err != nil {
		return nil, fmt.Errorf("store: gc-obligations: obligations: %w", err)
	}
	defer rows.Close()

	plan := make([]gcCandidate, 0)
	for rows.Next() {
		var id, watchID string
		var payload []byte
		if err := rows.Scan(&id, &watchID, &payload); err != nil {
			return nil, fmt.Errorf("store: gc-obligations: scan: %w", err)
		}
		if liveIDs[watchID] {
			continue
		}
		st := states[watchID]
		if st.known && st.closedAt.IsZero() {
			continue
		}
		var o domain.Obligation
		if err := json.Unmarshal(payload, &o); err != nil {
			return nil, fmt.Errorf("store: gc-obligations: decode %s: %w", id, err)
		}
		// A scheduled retry keeps the obligation alive even after the retention
		// window has passed and the Watch row is gone.
		if !o.NextRetryAt.IsZero() && o.NextRetryAt.After(now) {
			continue
		}
		reason := "closed"
		if st.archived {
			reason = "archived"
		}
		ref := st.closedAt
		if !st.known {
			reason = "missing"
			ref = obligationLastActivity(o)
			// Without any timestamp the row cannot be placed in time. Keep it
			// rather than erase an obligation that might still be replayed.
			if ref.IsZero() {
				continue
			}
		}
		if now.Sub(ref) < retention {
			continue
		}
		plan = append(plan, gcCandidate{
			item:               ObligationGCItem{ID: id, WatchID: watchID, Reason: reason},
			watchKnown:         st.known,
			watchArchived:      st.archived,
			watchStatus:        st.status,
			watchClosedAt:      st.closedAt,
			obligationRevision: o.Revision,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: gc-obligations: rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("store: gc-obligations: close rows: %w", err)
	}
	if !apply {
		items := make([]ObligationGCItem, len(plan))
		for i := range plan {
			items[i] = plan[i].item
		}
		return items, nil
	}

	gcBetweenPlanAndApply()
	items := make([]ObligationGCItem, 0, len(plan))
	for _, cand := range plan {
		erased, err := gcEraseIfUnchanged(ctx, d, cand, now)
		if err != nil {
			return nil, fmt.Errorf("store: gc-obligations: erase %s: %w", cand.item.ID, err)
		}
		item := cand.item
		item.Skipped = !erased
		items = append(items, item)
	}
	return items, nil
}

// gcEraseIfUnchanged rechecks one candidate and erases it in the same writer
// transaction. A Watch or obligation that changed after the plan leaves the
// row in place and returns false. The ledger write and delete mirror
// EraseObligation so an erase always keeps its audit record.
func gcEraseIfUnchanged(ctx context.Context, d *DB, cand gcCandidate, at time.Time) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	erased := false
	err := d.Write(ctx, func(tx *sql.Tx) error {
		var payload []byte
		err := tx.QueryRowContext(ctx, `SELECT payload FROM obligations WHERE id = ?`, cand.item.ID).Scan(&payload)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read obligation: %w", err)
		}
		var o domain.Obligation
		if err := json.Unmarshal(payload, &o); err != nil {
			return fmt.Errorf("decode obligation: %w", err)
		}
		if o.Revision != cand.obligationRevision {
			return nil
		}

		var watchPayload []byte
		found := false
		archived := false
		err = tx.QueryRowContext(ctx, `SELECT payload FROM watches WHERE id = ?`, cand.item.WatchID).Scan(&watchPayload)
		if err == nil {
			found = true
		} else if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, `SELECT payload FROM watch_archive WHERE id = ?`, cand.item.WatchID).Scan(&watchPayload)
			if err == nil {
				found = true
				archived = true
			}
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read watch: %w", err)
		}
		if found != cand.watchKnown || archived != cand.watchArchived {
			return nil
		}
		if found {
			var w domain.Watch
			if err := json.Unmarshal(watchPayload, &w); err != nil {
				return fmt.Errorf("decode watch: %w", err)
			}
			if w.Status != cand.watchStatus || !utcOrZero(w.ClosedAt).Equal(cand.watchClosedAt) {
				return nil
			}
		}

		if _, err := tx.Exec(createObligationEraseLedgerSQL); err != nil {
			return fmt.Errorf("create ledger: %w", err)
		}
		var ledgerID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM obligation_erase_ledger WHERE id = ?`, cand.item.ID).Scan(&ledgerID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read ledger: %w", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO obligation_erase_ledger (id, actor, reason, erased_at, payload)
			VALUES (?, ?, ?, ?, ?)`, cand.item.ID, gcObligationActor, "gc:"+cand.item.Reason, timeToNano(at), payload); err != nil {
			return fmt.Errorf("record ledger: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM obligations WHERE id = ?`, cand.item.ID); err != nil {
			return fmt.Errorf("delete obligation: %w", err)
		}
		erased = true
		return nil
	})
	return erased, err
}

// obligationLastActivity returns the newest timestamp written on an obligation.
// It is the fallback reference for an obligation whose Watch row is gone.
// Retry and delivery-attempt timestamps count as activity, so a recent retry
// extends the retention window.
func obligationLastActivity(o domain.Obligation) time.Time {
	last := o.StagedAt
	for _, at := range []time.Time{o.DeliveredAt, o.ClaimedAt, o.ReadAt, o.EscalatedAt, o.NextRetryAt} {
		if at.After(last) {
			last = at
		}
	}
	for _, a := range o.Attempts {
		if a.At.After(last) {
			last = a.At
		}
	}
	return utcOrZero(last)
}

func utcOrZero(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.UTC()
}
