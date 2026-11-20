// Package cibridge observes CI for active Watches and persists the verdict so
// that beacon_get_result can answer with ready or retry. It never merges and
// never runs checks itself; the CI provider reports facts and the domain state
// machine decides the verdict.
package cibridge

import (
	"context"
	"log"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// Bridge refreshes active Watches on a ticker. One Bridge owns one CI provider
// and one Store, so a refresh is a plain read followed by a conditional write.
type Bridge struct {
	store    domain.Store
	ci       domain.CIProvider
	clock    domain.Clock
	interval time.Duration
}

// New builds a Bridge. An empty interval falls back to five seconds.
func New(store domain.Store, ci domain.CIProvider, clock domain.Clock, interval time.Duration) *Bridge {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if clock == nil {
		clock = wallClock{}
	}
	return &Bridge{store: store, ci: ci, clock: clock, interval: interval}
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

// Run blocks until ctx is cancelled, refreshing once at startup and then on
// every tick. A failing provider is logged and retried on the next tick.
func (b *Bridge) Run(ctx context.Context) {
	if b == nil {
		return
	}
	b.Refresh(ctx)
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.Refresh(ctx)
		}
	}
}

// Refresh reads every active Watch once and updates those whose CI reached a
// terminal verdict. Only Watches with subscribers are considered: a Watch
// without a subscriber has nobody to notify.
func (b *Bridge) Refresh(ctx context.Context) int {
	if b == nil || b.store == nil || b.ci == nil {
		return 0
	}
	watches, err := b.store.ActiveWatches()
	if err != nil {
		log.Printf("cibridge: active watches: %v", err)
		return 0
	}
	updated := 0
	for i := range watches {
		w := watches[i]
		if w.Verdict != domain.VerdictWatching || len(w.Subscribers) == 0 {
			continue
		}
		state, err := b.ci.PRState(ctx, w.Repo, w.PR)
		if err != nil {
			log.Printf("cibridge: %s#%d: %v", w.Repo, w.PR, err)
			continue
		}
		if state.HeadRefOid != "" && state.HeadRefOid != w.HeadSHA {
			continue
		}
		verdict := verdictFromChecks(state.Checks)
		if verdict == domain.VerdictWatching {
			continue
		}
		n, err := b.publish(w, state, verdict)
		if err != nil {
			log.Printf("cibridge: publish %s: %v", w.ID, err)
			continue
		}
		updated += n
	}
	return updated
}

// verdictFromChecks turns observed checks into a verdict. A pending check
// leaves the Watch watching; a failing or cancelled check asks the executor to
// retry; all green makes the Watch ready for the orchestrator's merge check.
func verdictFromChecks(checks map[string]domain.Check) domain.Verdict {
	if len(checks) == 0 {
		return domain.VerdictWatching
	}
	allGreen := true
	for _, c := range checks {
		switch c.Conclusion {
		case "SUCCESS":
			continue
		case "FAILURE", "CANCELLED":
			allGreen = false
			return domain.VerdictRetry
		default:
			allGreen = false
		}
	}
	if allGreen {
		return domain.VerdictReady
	}
	return domain.VerdictWatching
}

func (b *Bridge) publish(w domain.Watch, state domain.PRState, verdict domain.Verdict) (int, error) {
	now := b.clock.Now()

	// Re-read under the single-writer assumption: another refresh may have
	// moved the Watch since ActiveWatches was called.
	current, err := b.store.Watch(w.ID)
	if err != nil {
		return 0, err
	}
	if current.Verdict != domain.VerdictWatching || current.HeadSHA != w.HeadSHA {
		return 0, nil
	}

	current.Checks = state.Checks
	current.Merge = domain.MergeInfo{
		Mergeable:        state.Mergeable,
		MergeStateStatus: state.MergeStateStatus,
		ReviewDecision:   state.ReviewDecision,
	}
	next, _ := domain.Transition(current, domain.VerdictEvent(verdict, now))
	if next.Revision == current.Revision {
		return 0, nil
	}
	if err := b.store.SaveWatch(next); err != nil {
		return 0, err
	}

	// Every subscriber owns an obligation on the new revision, otherwise
	// get_result cannot project a verdict for them.
	for _, sub := range next.Subscribers {
		id := domain.ObligationKey(next.ID, next.Revision, sub.ID)
		if _, err := b.store.Obligation(id); err == nil {
			continue
		}
		if err := b.store.SaveObligation(domain.Obligation{
			ID:             id,
			WatchID:        next.ID,
			Revision:       next.Revision,
			SubscriberID:   sub.ID,
			VerdictPointer: verdict,
			StagedAt:       now,
		}); err != nil {
			return 0, err
		}
	}
	log.Printf("cibridge: %s#%d -> %s revision=%d", next.Repo, next.PR, verdict, next.Revision)
	return 1, nil
}
