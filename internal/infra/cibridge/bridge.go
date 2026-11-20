// Package cibridge observes CI for active Watches and persists the verdict so
// that beacon_get_result can answer with ready or retry. It never merges and
// never runs checks itself; the CI provider reports facts and the domain state
// machine decides the verdict.
package cibridge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// DefaultPollInterval is the cadence used when BEACON_CI_POLL_INTERVAL is
// absent, malformed or non-positive. The old five-second tick called gh twice
// for every live Watch: twelve Watches produced about 288 gh calls a minute
// and ran into TLS timeouts and exit status 1. Thirty seconds is slow enough
// to keep the same fleet under fifty calls a minute while verdicts stay fresh.
const DefaultPollInterval = 30 * time.Second

// ciPollIntervalEnv is the operator override for the bridge cadence.
const ciPollIntervalEnv = "BEACON_CI_POLL_INTERVAL"

// PollInterval reads BEACON_CI_POLL_INTERVAL. An invalid or non-positive value
// falls back to DefaultPollInterval so a bad setting cannot restore the hot
// polling loop. The caller passes the result to New; the bridge itself does
// not read the environment.
func PollInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(ciPollIntervalEnv))
	if raw == "" {
		return DefaultPollInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return DefaultPollInterval
	}
	return d
}

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
	// orchestratorID is the project-level fallback for a blocked Watch that
	// carries neither an orchestrator subscriber nor an OrchestratorID field.
	// It is a port so this package never imports the app config layer.
	orchestratorID OrchestratorIDSource

	// blockedNotified remembers the obligations whose block wake was already
	// delivered. A blocked Watch is observed on every tick, so without this
	// guard the orchestrator would be steered again and again for the same
	// block. A new block revision opens a fresh key and wakes again.
	mu              sync.Mutex
	blockedNotified map[string]struct{}
}

// ReadStamper stamps a durable read on an obligation. The MCP tool owns the
// production implementation; Bridge only needs this narrow port to let the
// orchestrator confirm that a verdict was actually read, not just observed.
type ReadStamper interface {
	StampRead(ctx context.Context, id string, at time.Time) (domain.Obligation, bool, error)
}

// OrchestratorIDSource returns the project-configured orchestrator session.
// The app layer owns the config file, so Bridge takes the value through this
// port and stays independent of it.
type OrchestratorIDSource func() string

// WithOrchestratorIDSource binds the config fallback used by blocked wakes.
// It is consulted only when a Watch names no orchestrator itself. A nil source
// leaves such a Watch unsent rather than guessing a recipient.
func (b *Bridge) WithOrchestratorIDSource(fn OrchestratorIDSource) *Bridge {
	if b != nil {
		b.orchestratorID = fn
	}
	return b
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

// New builds a Bridge. An empty interval falls back to DefaultPollInterval;
// composition roots that read BEACON_CI_POLL_INTERVAL pass PollInterval().
func New(store domain.Store, ci domain.CIProvider, clock domain.Clock, interval time.Duration) *Bridge {
	if interval <= 0 {
		interval = DefaultPollInterval
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

// Refresh reads every live Watch once and updates those whose CI reached a
// verdict. Only Watches with subscribers are considered: a Watch without a
// subscriber has nobody to notify. A terminal PR state closes the Watch even
// when its verdict already moved to ready or retry.
//
// The source is AllWatches rather than ActiveWatches because a blocked Watch
// carries StatusBlocked and is therefore absent from ActiveWatches. The bridge
// still has to observe it, first to keep CI from being polled and the executor
// from being woken, and second to steer the orchestrator. Closed Watches are
// skipped here; publish re-reads and skips them too as a second guard.
func (b *Bridge) Refresh(ctx context.Context) int {
	if b == nil || b.store == nil || b.ci == nil {
		return 0
	}
	start := time.Now()
	updated, skipped := 0, 0
	defer func() { observe.PollCycleEnd(updated, skipped, time.Since(start)) }()
	watches, err := b.store.AllWatches()
	if err != nil {
		observe.ActorError("cibridge", "", err)
		log.Printf("cibridge: live watches: %v", err)
		return 0
	}
	observe.PollCycleStart(len(watches))
	for i := range watches {
		w := watches[i]
		if w.Status == domain.StatusClosed {
			skipped++
			continue
		}
		// A blocked Watch may carry no subscribers at all and still have an
		// orchestrator address in its OrchestratorID field or in config. Let
		// handleBlocked decide; only a live, unblocked Watch with no subscribers
		// has nobody to wake.
		if len(w.Subscribers) == 0 && w.Blocked == nil {
			skipped++
			continue
		}
		if w.Blocked != nil {
			observe.BlockDetected(w.ID, w.PR, "blocked watch waits on outside action")
			// A blocked Watch waits on outside action. Do not poll CI and do
			// not publish a retry verdict for it; only steer the orchestrator
			// so it can assign a FIX agent. Polling resumes after unblock.
			b.handleBlocked(ctx, w)
			skipped++
			continue
		}
		callStart := time.Now()
		state, err := b.ci.PRState(ctx, w.Repo, w.PR)
		observe.ProviderCall("ghci", "PRState", w.ID, err, time.Since(callStart))
		if err != nil {
			log.Printf("cibridge: %s#%d: %v", w.Repo, w.PR, err)
			skipped++
			continue
		}
		verdict := verdictFromState(state)
		if !isTerminalVerdict(verdict) {
			// A non-terminal refresh re-evaluates only the current verdict at
			// the same head. Terminal PR states outrank both guards: a merged
			// or closed PR closes the Watch from any live verdict.
			if w.Verdict != domain.VerdictWatching {
				skipped++
				continue
			}
			if state.HeadRefOid != "" && state.HeadRefOid != w.HeadSHA {
				skipped++
				continue
			}
		}
		if verdict == domain.VerdictWatching {
			skipped++
			continue
		}
		if verdict == domain.VerdictMerged {
			// A merged fixer PR releases the Watch it was assigned to clear.
			b.unblockFixed(w)
		}
		n, err := b.publish(ctx, w, state, verdict)
		if err != nil {
			observe.ActorError("cibridge-publish", w.ID, err)
			log.Printf("cibridge: publish %s: %v", w.ID, err)
			skipped++
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
	}
	checksVerdict := verdictFromChecks(state.Checks)
	if checksVerdict != domain.VerdictWatching {
		return checksVerdict
	}
	// A doc-only PR has no checks at all (paths-ignore in CI). Such a PR is
	// ready as soon as GitHub says it is clean and mergeable. Without this,
	// no-check Watches stay watching forever and never reach the orchestrator.
	if len(state.Checks) == 0 && !state.IsDraft &&
		strings.EqualFold(state.Mergeable, "MERGEABLE") &&
		strings.EqualFold(state.MergeStateStatus, "CLEAN") {
		return domain.VerdictReady
	}
	return domain.VerdictWatching
}

func isTerminalVerdict(v domain.Verdict) bool {
	return v == domain.VerdictMerged || v == domain.VerdictClosed
}

// blockedNotice builds the orchestrator's wake text for a blocked Watch. It
// carries the PR, the block reason, the failed checks and the watch id, and
// nothing from check bodies. The text rides in the verdict field of the wake
// pointer, the same free-text slot wake.NotifyOrchestrator uses for a reason.
func blockedNotice(w domain.Watch) string {
	info := w.Blocked
	checks := strings.Join(info.FailedChecks, ", ")
	if checks == "" {
		checks = "нет"
	}
	return fmt.Sprintf("нужен FIX-агент: PR %s#%d, причина: %s, failedChecks: %s, watchID: %s",
		w.Repo, w.PR, info.Reason, checks, w.ID)
}

// blockedDefaultAdapter is used when the resolved orchestrator address is not
// a subscriber on the Watch, so no subscriber record carries an adapter. The
// host session in OrchestratorID or in config belongs to the same harness.
const blockedDefaultAdapter = "opencode"

// blockedOrchestrator resolves the one session allowed to receive a blocked
// notice: the orchestrator subscriber first, then the Watch's persisted
// OrchestratorID, then the configured fallback. A candidate that also names an
// executor subscriber is skipped, so a block never steers the executor that
// reported the failed checks.
func (b *Bridge) blockedOrchestrator(w domain.Watch) string {
	candidates := make([]string, 0, 2+len(w.Subscribers))
	for _, sub := range w.Subscribers {
		if sub.Role == domain.RoleOrchestrator {
			candidates = append(candidates, strings.TrimSpace(sub.ID))
		}
	}
	candidates = append(candidates, strings.TrimSpace(w.OrchestratorID))
	if b != nil && b.orchestratorID != nil {
		candidates = append(candidates, strings.TrimSpace(b.orchestratorID()))
	}
	for _, id := range candidates {
		if id == "" {
			continue
		}
		isExecutor := false
		for _, sub := range w.Subscribers {
			if sub.Role == domain.RoleExecutor && sub.ID == id {
				isExecutor = true
				break
			}
		}
		if !isExecutor {
			return id
		}
	}
	return ""
}

// blockedAdapter returns the adapter recorded on the resolved session when it
// is a subscriber of w. The other resolution sources fall back to the default
// adapter, since they carry only an opaque session id.
func (b *Bridge) blockedAdapter(w domain.Watch, sessionID string) string {
	for _, sub := range w.Subscribers {
		if sub.ID == sessionID && sub.Adapter != "" {
			return sub.Adapter
		}
	}
	return blockedDefaultAdapter
}

// handleBlocked observes a blocked Watch. It stages the subscriber obligations
// at the block revision so get_result can still project a verdict, then steers
// exactly one orchestrator session so a FIX agent can be assigned. The target
// comes from the orchestrator subscriber, Watch.OrchestratorID or the injected
// config fallback, in that order. The executor is deliberately not woken: a
// blocked Watch is not a retry. One notice is delivered per block revision; a
// later unblock clears the Watch and a new block gets a new revision.
func (b *Bridge) handleBlocked(ctx context.Context, w domain.Watch) {
	if w.Blocked == nil {
		return
	}
	now := b.clock.Now()

	// Re-read under the single-writer assumption, as publish does, so a Watch
	// unblocked between the live-watch scan and here is not woken.
	current, err := b.store.Watch(w.ID)
	if err != nil {
		log.Printf("cibridge: blocked watch %s: %v", w.ID, err)
		return
	}
	if current.Status == domain.StatusClosed || current.Blocked == nil {
		return
	}
	w = current

	// Stage one blocked obligation per real subscriber. Delivery does not
	// happen in this loop, so an executor subscriber is never a wake target.
	for _, sub := range w.Subscribers {
		id := domain.ObligationKey(w.ID, w.Revision, sub.ID)
		if _, err := b.store.Obligation(id); err != nil {
			if err := b.store.SaveObligation(domain.Obligation{
				ID:             id,
				WatchID:        w.ID,
				Revision:       w.Revision,
				SubscriberID:   sub.ID,
				VerdictPointer: domain.VerdictBlocked,
				StagedAt:       now,
			}); err != nil {
				log.Printf("cibridge: blocked obligation %s: %v", id, err)
				return
			}
		}
	}

	sessionID := b.blockedOrchestrator(w)
	if sessionID == "" {
		observe.WakeSkipped("", w.ID, "blocked Watch has no orchestrator address")
		return
	}
	if b.waker == nil && b.adapterWaker == nil {
		return
	}

	// The guard is per revision, not per subscriber: a blocked Watch must
	// steer its orchestrator at most once even when the address was resolved
	// from a different source on a later tick.
	noticeKey := fmt.Sprintf("blocked:%s:%d", w.ID, w.Revision)
	b.mu.Lock()
	if b.blockedNotified == nil {
		b.blockedNotified = make(map[string]struct{})
	}
	_, notified := b.blockedNotified[noticeKey]
	b.mu.Unlock()
	if notified {
		observe.WakeSkipped("", w.ID, "block notice already delivered for this revision")
		return
	}

	obligationID := domain.ObligationKey(w.ID, w.Revision, sessionID)
	if _, err := b.store.Obligation(obligationID); err != nil {
		if err := b.store.SaveObligation(domain.Obligation{
			ID:             obligationID,
			WatchID:        w.ID,
			Revision:       w.Revision,
			SubscriberID:   sessionID,
			VerdictPointer: domain.VerdictBlocked,
			StagedAt:       now,
		}); err != nil {
			log.Printf("cibridge: blocked obligation %s: %v", obligationID, err)
			return
		}
	}

	adapter := b.blockedAdapter(w, sessionID)
	p := domain.WakePayload{
		WatchID:      w.ID,
		ObligationID: obligationID,
		Revision:     w.Revision,
		Verdict:      blockedNotice(w),
	}
	var deliverErr error
	if b.adapterWaker != nil {
		deliverErr = b.adapterWaker.Deliver(ctx, adapter, sessionID, p)
	} else {
		deliverErr = b.waker.Deliver(ctx, sessionID, p)
	}
	if deliverErr != nil {
		observe.WakeFailed(adapter, w.ID, deliverErr, w.Revision)
		observe.ActorError("wake-blocked", w.ID, deliverErr)
		log.Printf("cibridge: blocked wake %s: %v", sessionID, deliverErr)
		return
	}
	observe.WakeDelivered(adapter, w.ID, w.Revision)
	b.mu.Lock()
	b.blockedNotified[noticeKey] = struct{}{}
	b.mu.Unlock()
}

// fixForSource is an optional capability a Store may implement to expose the
// fixer link of a Watch. The fixer-role work adds FixFor to domain.Watch; until
// that field exists in the domain type this seam lets cibridge read the link
// without naming a field that would not compile. watchFixFor reads the field
// by name first, so once it lands production needs no extra wiring.
type fixForSource interface {
	FixForOf(watchID string) string
}

// watchFixFor returns the id of the Watch a fixer Watch is assigned to clear.
// It prefers the domain field (read by name so this package compiles against a
// Watch that predates the fixer role) and falls back to the optional Store
// capability for callers that carry the link outside the domain type.
func (b *Bridge) watchFixFor(w domain.Watch) string {
	if f := reflect.ValueOf(w).FieldByName("FixFor"); f.IsValid() && f.Kind() == reflect.String {
		if s := strings.TrimSpace(f.String()); s != "" {
			return s
		}
	}
	if b.store != nil {
		if src, ok := b.store.(fixForSource); ok {
			return strings.TrimSpace(src.FixForOf(w.ID))
		}
	}
	return ""
}

// unblockFixed resumes the Watch a merged fixer Watch points at. The fixer
// link lives on the fixer Watch as fixFor and names a blocked Watch. The
// domain EventUnblock clears the block details, returns the target to watching
// and bumps its revision, so the next Refresh polls CI again. A target that is
// not blocked is left untouched, and the revision guard makes a repeated MERGE
// observation a no-op: one merge produces one unblock.
func (b *Bridge) unblockFixed(merged domain.Watch) {
	fixer := merged
	if b.watchFixFor(fixer) == "" {
		// The merged Watch is normally the fixer itself. A second Watch on the
		// same PR may also carry the link, so look for one before giving up.
		watches, err := b.store.AllWatches()
		if err != nil {
			observe.ActorError("cibridge-unblock", merged.ID, err)
			log.Printf("cibridge: unblock lookup %s#%d: %v", merged.Repo, merged.PR, err)
			return
		}
		for _, w := range watches {
			if w.Repo != merged.Repo || w.PR != merged.PR || w.Status == domain.StatusClosed {
				continue
			}
			if b.watchFixFor(w) != "" {
				fixer = w
				break
			}
		}
	}
	fixFor := b.watchFixFor(fixer)
	if fixFor == "" || fixFor == fixer.ID {
		return
	}
	target, err := b.store.Watch(fixFor)
	if err != nil {
		observe.ActorError("cibridge-unblock", fixer.ID, err)
		log.Printf("cibridge: unblock target %s: %v", fixFor, err)
		return
	}
	if target.Status != domain.StatusBlocked || target.Blocked == nil {
		return
	}
	next, _ := domain.Transition(target, domain.UnblockEvent(b.clock.Now()))
	if next.Revision == target.Revision || next.Status != domain.StatusWatching {
		return
	}
	if err := b.store.SaveWatch(next); err != nil {
		observe.ActorError("cibridge-unblock", target.ID, err)
		log.Printf("cibridge: unblock save %s: %v", target.ID, err)
		return
	}
	log.Printf("cibridge: %s unblocked by %s#%d revision=%d", target.ID, fixer.Repo, fixer.PR, next.Revision)
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
			observe.WakeSkipped(sub.Adapter, next.ID, "obligation target mismatch")
			log.Printf("cibridge: skip wake for watch %s: obligation %s addresses %q, subscriber %q, target %q (%v)",
				next.ID, id, o.SubscriberID, addressee, target, err)
			continue
		}
		wakeStart := time.Now()
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
			observe.WakeFailed(sub.Adapter, next.ID, deliverErr, next.Revision)
			observe.ActorError("wake", next.ID, deliverErr)
			log.Printf("cibridge: wake %s: %v", target, deliverErr)
		} else {
			observe.WakeDuration(sub.Adapter, time.Since(wakeStart))
			observe.WakeDelivered(sub.Adapter, next.ID, next.Revision)
		}
	}
	observe.VerdictPublished(string(verdict), next.ID, next.PR, next.Revision)
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

// ===== T3.225 сквозной прогон watch, CI, указатель, wake, прочтение (#320) =====

// ChainFacts is the external evidence of one full run. Each field comes from
// the store or from the CI provider; none of it is an in-memory test array.
type ChainFacts struct {
	WatchID      string
	Revision     uint64
	Verdict      domain.Verdict
	ObligationID string
	WakeTarget   string
	ReadAt       time.Time
}

// ConfirmRead verifies the read side of the chain. beacon_get_result stamps
// readAt on the obligation through the store writer and beacon_ack keeps that
// stamp; this method reads the fact back from the same store and projects it
// with the watch verdict and the wake target. A missing readAt means the chain
// stopped before the result was read and is reported as incomplete.
func (b *Bridge) ConfirmRead(watchID, obligationID string) (ChainFacts, error) {
	if b == nil || b.store == nil {
		return ChainFacts{}, errors.New("cibridge: no store")
	}
	o, err := b.store.Obligation(obligationID)
	if err != nil {
		return ChainFacts{}, err
	}
	if o.WatchID != watchID {
		return ChainFacts{}, fmt.Errorf("cibridge: obligation %s belongs to %s, not %s", obligationID, o.WatchID, watchID)
	}
	if o.ReadAt.IsZero() {
		return ChainFacts{}, fmt.Errorf("cibridge: obligation %s has no readAt: chain incomplete", obligationID)
	}
	w, err := b.store.Watch(watchID)
	if err != nil {
		return ChainFacts{}, err
	}
	target := ""
	for _, sub := range w.Subscribers {
		if sub.ID == o.SubscriberID {
			target = sub.Target
			break
		}
	}
	return ChainFacts{
		WatchID:      watchID,
		Revision:     o.Revision,
		Verdict:      o.VerdictPointer,
		ObligationID: obligationID,
		WakeTarget:   target,
		ReadAt:       o.ReadAt,
	}, nil
}
