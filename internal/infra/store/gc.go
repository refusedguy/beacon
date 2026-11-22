package store

import (
	"context"
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

// ObligationGCItem is one obligation the collector selected. Reason names the
// state of the Watch: "closed", "archived" or "missing".
type ObligationGCItem struct {
	ID      string `json:"id"`
	WatchID string `json:"watchID"`
	Reason  string `json:"reason"`
}

// gcWatchState is what the collector knows about a Watch behind an obligation.
// known distinguishes a Watch still in the active table from one that is gone;
// a non-live Watch with no close time is known but not yet collectible.
type gcWatchState struct {
	known    bool
	closedAt time.Time
	archived bool
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
// Every erase goes through EraseObligation, which writes the audit ledger before
// the delete. A repeated pass therefore finds no erased rows and removes
// nothing.
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
		states[w.ID] = gcWatchState{known: true, closedAt: utcOrZero(w.ClosedAt)}
	}
	archived, err := d.ArchivedWatches()
	if err != nil {
		return nil, fmt.Errorf("store: gc-obligations: archive: %w", err)
	}
	for _, w := range archived {
		states[w.ID] = gcWatchState{known: true, closedAt: utcOrZero(w.ClosedAt), archived: true}
	}

	rows, err := d.readDB.QueryContext(ctx, `SELECT id, watch_id, payload FROM obligations ORDER BY seq`)
	if err != nil {
		return nil, fmt.Errorf("store: gc-obligations: obligations: %w", err)
	}
	defer rows.Close()

	plan := make([]ObligationGCItem, 0)
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
		plan = append(plan, ObligationGCItem{ID: id, WatchID: watchID, Reason: reason})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: gc-obligations: rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("store: gc-obligations: close rows: %w", err)
	}
	if !apply {
		return plan, nil
	}

	for _, item := range plan {
		if _, err := d.EraseObligation(ctx, item.ID, gcObligationActor, "gc:"+item.Reason, now); err != nil {
			return nil, fmt.Errorf("store: gc-obligations: erase %s: %w", item.ID, err)
		}
	}
	return plan, nil
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
