// Package cibridge observes CI for active Watches and persists the verdict so
// that beacon_get_result can answer with ready or retry. It never merges and
// never runs checks itself; the CI provider reports facts and the domain state
// machine decides the verdict.
package cibridge

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// Bridge refreshes active Watches on a ticker. One Bridge owns one CI provider
// and one Store, so a refresh is a plain read followed by a conditional write.
type Bridge struct {
	store    domain.Store
	ci       domain.CIProvider
	clock    domain.Clock
	interval     time.Duration
	waker        domain.WakeDeliverer
	adapterWaker domain.AdapterWakeDeliverer
	reader       ReadStamper
}

// ReadStamper stamps a durable read on an obligation. The MCP tool owns the
// production implementation; Bridge only needs this narrow port to let the
// orchestrator confirm that a verdict was actually read, not just observed.
type ReadStamper interface {
	StampRead(ctx context.Context, id string, at time.Time) (domain.Obligation, bool, error)
}

// WithReader binds the read stamper. It is optional: without one ReadVerdict
// still reports the verdict, but cannot prove the read in the store.
func (b *Bridge) WithReader(s ReadStamper) *Bridge {
	if b == nil {
		return nil
	}
	b.reader = s
	return b
}

// WithWaker binds a wake adapter. A non-terminal verdict then steers each
// subscriber's session through the host harness. It accepts both the legacy
// WakeDeliverer, which has no adapter, and the adapter-aware
// AdapterWakeDeliverer, so a registry can be wired in without changing callers.
// Without a waker the verdict is still persisted, so a caller that reads
// get_result is unaffected.
func (b *Bridge) WithWaker(w any) *Bridge {
	if b == nil {
		return nil
	}
	switch v := w.(type) {
	case domain.AdapterWakeDeliverer:
		b.adapterWaker = v
	case domain.WakeDeliverer:
		b.waker = v
	}
	return b
}

// WithAdapterWaker binds an adapter-aware wake deliverer explicitly. The
// subscriber's adapter is passed on every delivery so a registry can dispatch.
func (b *Bridge) WithAdapterWaker(w domain.AdapterWakeDeliverer) *Bridge {
	if b == nil {
		return nil
	}
	b.adapterWaker = w
	return b
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
// verdict. Only Watches with subscribers are considered: a Watch without a
// subscriber has nobody to notify. A terminal PR state closes the Watch even
// when its verdict already moved to ready or retry.
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
		if len(w.Subscribers) == 0 {
			continue
		}
		state, err := b.ci.PRState(ctx, w.Repo, w.PR)
		if err != nil {
			log.Printf("cibridge: %s#%d: %v", w.Repo, w.PR, err)
			continue
		}
		verdict := verdictFromState(state)
		if !isTerminalVerdict(verdict) {
			// A non-terminal refresh re-evaluates only the current verdict at
			// the same head. Terminal PR states outrank both guards: a merged
			// or closed PR closes the Watch from any live verdict.
			if w.Verdict != domain.VerdictWatching {
				continue
			}
			if state.HeadRefOid != "" && state.HeadRefOid != w.HeadSHA {
				continue
			}
		}
		if verdict == domain.VerdictWatching {
			continue
		}
		n, err := b.publish(ctx, w, state, verdict)
		if err != nil {
			log.Printf("cibridge: publish %s: %v", w.ID, err)
			continue
		}
		updated += n
	}
	return updated
}

// verdictFromState derives a verdict from one CI observation. A terminal PR
// state wins over checks: a merged or closed PR closes its Watch even when its
// checks are green or absent.
func verdictFromState(state domain.PRState) domain.Verdict {
	switch strings.ToUpper(strings.TrimSpace(state.State)) {
	case "MERGED":
		return domain.VerdictMerged
	case "CLOSED":
		return domain.VerdictClosed
	default:
		return verdictFromChecks(state.Checks)
	}
}

func isTerminalVerdict(v domain.Verdict) bool {
	return v == domain.VerdictMerged || v == domain.VerdictClosed
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

// wakeAddressee resolves the one session a wake may address on w. The address
// must be subscriber.ID on this very Watch with role executor or orchestrator.
// No caller session, _meta value, notify target or role-wide broadcast replaces
// it. The check stays local so cibridge does not import another infra package.
func wakeAddressee(w domain.Watch, watchID, addressee string) (string, error) {
	if watchID == "" || w.ID != watchID {
		return "", fmt.Errorf("cibridge: watchID %q does not name watch %q", watchID, w.ID)
	}
	if !strings.HasPrefix(addressee, "ses_") || len(addressee) == len("ses_") {
		return "", fmt.Errorf("cibridge: subscriber %q is not a session", addressee)
	}
	for _, sub := range w.Subscribers {
		if sub.ID != addressee {
			continue
		}
		if sub.Role != domain.RoleExecutor && sub.Role != domain.RoleOrchestrator {
			return "", fmt.Errorf("cibridge: subscriber %q has role %q", sub.ID, sub.Role)
		}
		return sub.ID, nil
	}
	return "", fmt.Errorf("cibridge: session %q is not a subscriber of watch %s", addressee, watchID)
}

func (b *Bridge) publish(ctx context.Context, w domain.Watch, state domain.PRState, verdict domain.Verdict) (int, error) {
	now := b.clock.Now()

	// Re-read under the single-writer assumption: another refresh may have
	// moved the Watch since ActiveWatches was called.
	current, err := b.store.Watch(w.ID)
	if err != nil {
		return 0, err
	}
	if current.Status == domain.StatusClosed || current.HeadSHA != w.HeadSHA {
		return 0, nil
	}
	if !isTerminalVerdict(verdict) && current.Verdict != domain.VerdictWatching {
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
		o, err := b.store.Obligation(id)
		if err != nil {
			o = domain.Obligation{
				ID:             id,
				WatchID:        next.ID,
				Revision:       next.Revision,
				SubscriberID:   sub.ID,
				VerdictPointer: verdict,
				StagedAt:       now,
			}
			if err := b.store.SaveObligation(o); err != nil {
				return 0, err
			}
		}

		// A terminal verdict closes the Watch without steering a session:
		// there is no further work for a subscriber to do.
		if isTerminalVerdict(verdict) || (b.waker == nil && b.adapterWaker == nil) {
			continue
		}

		p := domain.WakePayload{
			WatchID:      next.ID,
			ObligationID: id,
			Revision:     next.Revision,
			Verdict:      string(verdict),
		}
		// The only delivery address is subscriber.ID from this Watch. It must
		// agree with the obligation and with the resolved Watch target; a
		// caller session or _meta value never substitutes for it.
		addressee := sub.ID
		target, err := wakeAddressee(next, p.WatchID, addressee)
		if err != nil || target != addressee || o.SubscriberID != addressee ||
			domain.ObligationKey(p.WatchID, p.Revision, addressee) != p.ObligationID {
			log.Printf("cibridge: skip wake for watch %s: obligation %s addresses %q, subscriber %q, target %q (%v)",
				next.ID, id, o.SubscriberID, addressee, target, err)
			continue
		}
		var deliverErr error
		if sw, ok := b.adapterWaker.(interface {
			DeliverToSubscriber(context.Context, domain.Watch, domain.WakePayload, string) error
		}); ok {
			deliverErr = sw.DeliverToSubscriber(ctx, next, p, addressee)
		} else if b.adapterWaker != nil {
			deliverErr = b.adapterWaker.Deliver(ctx, sub.Adapter, target, p)
		} else {
			deliverErr = b.waker.Deliver(ctx, addressee, p)
		}
		if deliverErr != nil {
			log.Printf("cibridge: wake %s: %v", target, deliverErr)
		}
	}
	log.Printf("cibridge: %s#%d -> %s revision=%d", next.Repo, next.PR, verdict, next.Revision)
	return 1, nil
}

// WatchResult is the orchestrator's own Watch as read back from the store.
// ReadAt is stamped by the first read and stays the same on later reads, so
// "verdict was read" is a durable fact rather than a claim in a chat report.
type WatchResult struct {
	WatchID      string
	Verdict      domain.Verdict
	Revision     uint64
	ObligationID string
	ReadAt       time.Time
}

// RegisterSelfWatch creates the orchestrator's Watch on its own PR. The card id
// is the subscriber: it is the same target beacon_watch_pr receives as
// notify.target, so the CI bridge wakes the session that owns the card. The
// registration is idempotent by watch id and never resets an existing verdict.
func (b *Bridge) RegisterSelfWatch(cardID, repo string, pr int, headSHA string) (domain.Watch, error) {
	if b == nil || b.store == nil {
		return domain.Watch{}, fmt.Errorf("cibridge: register self watch: store is nil")
	}
	if cardID == "" || repo == "" || headSHA == "" || pr <= 0 {
		return domain.Watch{}, fmt.Errorf("cibridge: register self watch: cardID, repo, pr and headSHA are required")
	}
	id := fmt.Sprintf("watch:%s:%d:%s", repo, pr, headSHA)
	if existing, err := b.store.Watch(id); err == nil {
		return existing, nil
	}
	now := b.clock.Now()
	w := domain.Watch{
		ID:      id,
		Repo:    repo,
		PR:      pr,
		HeadSHA: headSHA,
		Status:  domain.StatusWatching,
		Verdict: domain.VerdictWatching,
		Subscribers: []domain.Subscriber{{
			ID:           cardID,
			Role:         domain.RoleOrchestrator,
			Adapter:      "opencode",
			Target:       cardID,
			AddedAt:      now,
			LastActivity: now,
		}},
		CreatedAt: now,
	}
	if err := b.store.SaveWatch(w); err != nil {
		return domain.Watch{}, fmt.Errorf("cibridge: save self watch: %w", err)
	}
	return w, nil
}

// ReadVerdict reads the verdict of a Watch together with the subscriber's
// obligation and stamps readAt on the first read. A stubbed watch id or a
// subscriber that does not own an obligation is an error: there is nothing in
// the store to confirm. A Watch still in watching is reported as watching.
func (b *Bridge) ReadVerdict(ctx context.Context, watchID, subscriberID string) (WatchResult, error) {
	if b == nil || b.store == nil {
		return WatchResult{}, fmt.Errorf("cibridge: read verdict: store is nil")
	}
	w, err := b.store.Watch(watchID)
	if err != nil {
		return WatchResult{}, fmt.Errorf("cibridge: watch %s: %w", watchID, err)
	}
	id := domain.ObligationKey(watchID, w.Revision, subscriberID)
	o, err := b.store.Obligation(id)
	if err != nil {
		return WatchResult{}, fmt.Errorf("cibridge: obligation %s: %w", id, err)
	}
	if b.reader != nil {
		o, _, err = b.reader.StampRead(ctx, id, b.clock.Now())
		if err != nil {
			return WatchResult{}, fmt.Errorf("cibridge: stamp readAt: %w", err)
		}
	}
	return WatchResult{
		WatchID:      w.ID,
		Verdict:      w.Verdict,
		Revision:     w.Revision,
		ObligationID: o.ID,
		ReadAt:       o.ReadAt,
	}, nil
}
