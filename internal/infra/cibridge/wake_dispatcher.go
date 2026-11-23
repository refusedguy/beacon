package cibridge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// DefaultParallelLimit is the default cap on units that may run at once. It
// reads the configured concurrency budget from the domain, so the cap has a
// single source and callers can override it with an explicit limit.
const DefaultParallelLimit = domain.DefaultSlotsTotal

// UnitStatus is the observable state of one unit in a bounded batch.
type UnitStatus string

const (
	UnitDone    UnitStatus = "done"
	UnitFailed  UnitStatus = "failed"
	UnitSkipped UnitStatus = "skipped"
)

// UnitResult carries the status of one bounded unit and its error when the
// unit failed. A skipped unit was never started and carries no error.
type UnitResult struct {
	Status UnitStatus
	Err    error
}

// BoundedRun runs work over n indices with at most limit workers. Each worker
// claims the next index atomically, so no index runs twice. When stopOnError
// is set, the first error stops new starts and every index not yet claimed is
// reported as skipped. The result holds one entry per index in input order.
// A nil context becomes Background; limit <= 0 selects DefaultParallelLimit.
// The worker must be safe for concurrent calls. BoundedRun waits for every
// started worker before it returns.
func BoundedRun(ctx context.Context, n, limit int, stopOnError bool, work func(context.Context, int) error) []UnitResult {
	if n <= 0 {
		return []UnitResult{}
	}
	results := make([]UnitResult, n)
	for i := range results {
		results[i].Status = UnitSkipped
	}
	if work == nil {
		for i := range results {
			results[i] = UnitResult{Status: UnitFailed, Err: errors.New("cibridge: bounded run: nil worker")}
		}
		return results
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = DefaultParallelLimit
	}
	if limit > n {
		limit = n
	}
	var next atomic.Int64
	next.Store(-1)
	var stopped atomic.Bool
	var wg sync.WaitGroup
	for i := 0; i < limit; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil || (stopOnError && stopped.Load()) {
					return
				}
				idx := int(next.Add(1))
				if idx >= n {
					return
				}
				// A sibling may have failed between the check at the top of
				// the loop and this claim. Leave the claimed index skipped
				// instead of starting new work after the failure.
				if stopOnError && stopped.Load() {
					return
				}
				if err := work(ctx, idx); err != nil {
					results[idx] = UnitResult{Status: UnitFailed, Err: err}
					if stopOnError {
						stopped.Store(true)
					}
				} else {
					results[idx] = UnitResult{Status: UnitDone}
				}
			}
		}()
	}
	wg.Wait()
	return results
}

// stage arms the ladder for the obligation behind a wake. The call is
// idempotent in the ladder, so an at-least-once refresh cannot reset a position
// that is already running.
func (b *wakeDispatcher) stage(ctx context.Context, o domain.Obligation, verdict domain.Verdict, at time.Time) {
	if b == nil || b.ladder == nil || o.ID == "" {
		return
	}
	if verdict == "" {
		verdict = o.VerdictPointer
	}
	if err := b.ladder.Stage(ctx, o, verdict, at); err != nil {
		observe.ActorError("ladder-stage", o.ID, err)
	}
}

// recordDelivery hands one delivery outcome to the ladder. Posted means no
// retries; a dead session is indeterminate and is never retried; any other
// delivery error is a transport failure that repeats the same pointer.
func (b *wakeDispatcher) recordDelivery(ctx context.Context, obligationID string, attempt int, outcome domain.AttemptOutcome, at time.Time) {
	if b == nil || b.ladder == nil || obligationID == "" {
		return
	}
	if err := b.ladder.Record(ctx, obligationID, attempt, outcome, at); err != nil {
		observe.ActorError("ladder-record", obligationID, err)
	}
}

// markDelivered persists a delivery on the obligation itself. The store
// adapter exposes DeliveryMarker; a legacy or test store is updated through
// SaveObligation. Either way the fact survives a process restart.
func (b *wakeDispatcher) markDelivered(id string, at time.Time, attempt int, lastErr string) {
	if b.delivery != nil {
		if err := b.delivery.MarkObligationDelivered(id, at, attempt, lastErr); err == nil {
			return
		}
	}
	o, err := b.store.Obligation(id)
	if err != nil {
		return
	}
	if o.DeliveredAt.IsZero() {
		o.DeliveredAt = at
	}
	seen := false
	for _, a := range o.Attempts {
		if a.N == attempt {
			seen = true
			break
		}
	}
	if !seen && attempt > 0 {
		o.Attempts = append(o.Attempts, domain.Attempt{N: attempt, At: at, Outcome: domain.OutcomePosted})
	}
	_ = b.store.SaveObligation(o)
}

// wakeCounterID is the durable wake counter of one subscriber of a Watch. It
// lives in the obligations table, so a process restart cannot reset the budget.
// The subscriber is part of the key: a shared counter spent two units per cycle
// when EvaluatePR named both an executor and an orchestrator, parking the Watch
// after three cycles even though neither party had used its own budget.
func wakeCounterID(w domain.Watch, subscriberID string) string {
	return domain.ObligationKey("wakes:"+w.ID+":"+subscriberID, 0, "count")
}

// wakeCount returns the number of wake attempts already delivered to one
// subscriber. A background subscriber can no longer consume another party's
// budget.
func (b *wakeDispatcher) wakeCount(w domain.Watch, subscriberID string) int {
	o, err := b.store.Obligation(wakeCounterID(w, subscriberID))
	if err != nil {
		return 0
	}
	return len(o.Attempts)
}

// recordWake increments one subscriber's durable counter after a successful
// delivery.
func (b *wakeDispatcher) recordWake(w domain.Watch, subscriberID string, now time.Time, attempt int) {
	id := wakeCounterID(w, subscriberID)
	if b.ensureObligation(domain.Obligation{
		ID: id, WatchID: w.ID, Revision: 0,
		SubscriberID: subscriberID, VerdictPointer: domain.VerdictWatching, StagedAt: now,
	}, now, stagingError("cibridge-wake-counter", w.ID)) {
		return
	}
	b.markDelivered(id, now, attempt, "")
}

// resetWakeCount clears one subscriber's budget when a notified Watch leaves
// parked. Without the reset the first wake after the resume would hit the cap
// again and park the Watch in the same cycle, so the notification would buy no
// chance to act.
func (b *wakeDispatcher) resetWakeCount(w domain.Watch, subscriberID string) {
	id := wakeCounterID(w, subscriberID)
	o, err := b.store.Obligation(id)
	if err != nil {
		return
	}
	o.Attempts = nil
	o.DeliveredAt = time.Time{}
	_ = b.store.SaveObligation(o)
}

// Circuit breaker tuning, expressed with Resilience4j's vocabulary. The
// breaker is per adapter and lives in the process: the sliding window and the
// open timer are outcome statistics, not durable state. A bulkhead, not this
// window, would limit parallelism.
const (
	breakerWindowSize   = 10               // sliding window of recent outcomes
	breakerMinimumCalls = 3                // minimumNumberOfCalls before an opening decision
	breakerFailureNum   = 1                // failure rate numerator
	breakerFailureDenom = 2                // failure rate denominator: 1/2
	breakerOpenWait     = 30 * time.Second // waitDurationInOpenState
)

// adapterBreakerState is the state of one adapter's circuit breaker.
type adapterBreakerState int

const (
	breakerClosed adapterBreakerState = iota
	breakerOpen
	breakerHalfOpen
)

// adapterBreaker is the state machine of a single wake adapter. outcomes is a
// sliding window of recent deliveries: true means a transport failure, false
// means success. openedAt is the wall-clock time the breaker went OPEN.
type adapterBreaker struct {
	state         adapterBreakerState
	outcomes      []bool
	openedAt      time.Time
	probeInFlight bool
}

// breakerFor returns the state of adapter, creating it on first use. The caller
// must hold b.breakerMu.
func (b *wakeDispatcher) breakerFor(adapter string) *adapterBreaker {
	if b.breakers == nil {
		b.breakers = make(map[string]*adapterBreaker)
	}
	s := b.breakers[adapter]
	if s == nil {
		s = &adapterBreaker{}
		// Restore the durable failure count. A restart used to leave every
		// breaker closed, so an adapter that was down looked healthy again.
		for i, failed := 0, b.breakerFailures(adapter); i < failed && i < breakerWindowSize; i++ {
			s.outcomes = append(s.outcomes, true)
		}
		if len(s.outcomes) >= breakerMinimumCalls {
			s.state = breakerOpen
			s.openedAt = b.clock.Now()
		}
		b.breakers[adapter] = s
	}
	return s
}

// breakerAdapterKey groups legacy (non adapter-aware) deliveries under one
// breaker so every wake path has a key.
func breakerAdapterKey(sub domain.Subscriber) string {
	if sub.Adapter != "" {
		return sub.Adapter
	}
	return "legacy"
}

// breakerPermit reports whether a wake for adapter may go out at now. CLOSED
// permits every call: the sliding window measures outcomes and does not cap
// concurrency. OPEN refuses calls until breakerOpenWait has passed, then moves
// to HALF_OPEN. HALF_OPEN permits exactly one probe at a time.
func (b *wakeDispatcher) breakerPermit(adapter string, now time.Time) bool {
	b.breakerMu.Lock()
	defer b.breakerMu.Unlock()
	s := b.breakerFor(adapter)
	switch s.state {
	case breakerOpen:
		if now.Sub(s.openedAt) < breakerOpenWait {
			return false
		}
		s.state = breakerHalfOpen
		s.outcomes = nil
		s.probeInFlight = true
	case breakerHalfOpen:
		if s.probeInFlight {
			return false
		}
		s.probeInFlight = true
	}
	return true
}

// breakerRecord records the outcome of a wake that breakerPermit allowed.
// A HALF_OPEN probe either closes the breaker on success or reopens it on
// failure. In CLOSED the outcome enters the sliding window, which opens the
// breaker once enough calls arrived and the failure rate crossed the threshold.
func (b *wakeDispatcher) breakerRecord(adapter string, now time.Time, failure bool) {
	b.breakerMu.Lock()
	defer b.breakerMu.Unlock()
	s := b.breakerFor(adapter)
	switch s.state {
	case breakerHalfOpen:
		s.probeInFlight = false
		if failure {
			b.openBreaker(s, now)
		} else {
			s.state = breakerClosed
			s.outcomes = nil
			b.clearBreakerFailures(adapter, now)
		}
	case breakerClosed:
		s.outcomes = append(s.outcomes, failure)
		if failure {
			b.recordBreakerFailure(adapter, now)
		}
		if len(s.outcomes) > breakerWindowSize {
			s.outcomes = s.outcomes[len(s.outcomes)-breakerWindowSize:]
		}
		if len(s.outcomes) < breakerMinimumCalls {
			return
		}
		failures := 0
		for _, f := range s.outcomes {
			if f {
				failures++
			}
		}
		if failures*breakerFailureDenom >= len(s.outcomes)*breakerFailureNum {
			b.openBreaker(s, now)
		}
	}
}

// breakerIgnore records a dead-session failure. It is neither a success nor a
// failure for the backend, so it never opens the breaker. A HALF_OPEN probe
// that hits a dead session releases its slot for a later tick.
func (b *wakeDispatcher) breakerIgnore(adapter string) {
	b.breakerMu.Lock()
	defer b.breakerMu.Unlock()
	s := b.breakerFor(adapter)
	if s.state == breakerHalfOpen {
		s.probeInFlight = false
	}
}

// openBreaker moves s to OPEN and starts the wait timer. The caller must hold
// b.breakerMu.
func (b *wakeDispatcher) openBreaker(s *adapterBreaker, now time.Time) {
	s.state = breakerOpen
	s.openedAt = now
	s.outcomes = nil
	s.probeInFlight = false
}

// actionableWakeVerdict reports whether a verdict carries an instruction the
// subscriber must carry out. Losing one of these pointers leaves the Watch
// stuck with no owner, so they go out even while the adapter breaker is OPEN.
// Informational and terminal verdicts keep the breaker protection.
func actionableWakeVerdict(v domain.Verdict) bool {
	switch v {
	case domain.VerdictKeepWorking, domain.VerdictRebase, domain.VerdictRetry,
		domain.VerdictBlocked, domain.VerdictTimeout:
		return true
	default:
		return false
	}
}

// wakeOne validates the address, applies the wake cap, checks the per-adapter
// circuit breaker, delivers the pointer and records the attempt. A dead-session
// error marks the subscriber orphaned and escalates to the orchestrator once;
// other errors are left pending so a later tick can retry. The returned error
// is the delivery error, so a caller that batches wakes can stop new starts
// after a failure. Skips and parked wakes return nil. Existing callers ignore
// the value and keep their behavior.
func (b *wakeDispatcher) wakeOne(ctx context.Context, w domain.Watch, sub domain.Subscriber, text string, now time.Time, cycleID string, breaker bool) error {
	if b.waker == nil && b.adapterWaker == nil {
		return nil
	}
	target, err := wakeAddressee(w, w.ID, sub.ID)
	if err != nil || target != sub.ID {
		observe.WakeSkipped(sub.Adapter, w.ID, "obligation target mismatch")
		log.Printf("cibridge: skip wake for watch %s: subscriber %q (%v)", w.ID, sub.ID, err)
		return nil
	}
	if breaker && b.wakeCount(w, sub.ID) >= wakeCap(w) {
		b.parkForWakeCap(ctx, w, cycleID)
		return nil
	}
	adapter := breakerAdapterKey(sub)
	// An actionable verdict must reach its subscriber even when the adapter is
	// down, so it takes the breaker=false path. A replay or a forced chaos wake
	// already carries breaker=false and likewise must not spend a HALF_OPEN
	// probe slot that some other wake owns.
	useBreaker := breaker && !actionableWakeVerdict(w.Verdict)
	if useBreaker && !b.breakerPermit(adapter, now) {
		// An OPEN breaker used to drop the pointer on the floor. Park the wake
		// with its full payload and count the skip in the durable failure
		// counter, so the wake is replayed when the adapter can deliver again.
		b.deferWake(deferredWake{
			watchID: w.ID, revision: w.Revision, subscriberID: sub.ID,
			adapter: adapter, text: text, verdict: w.Verdict,
			cycleID: cycleID, stagedAt: now, watch: w, subscriber: sub,
		})
		b.recordBreakerFailure(adapter, now)
		return nil
	}
	b.drainDeferred(ctx, adapter, w, sub, now, cycleID, useBreaker)
	p := domain.WakePayload{
		WatchID:      w.ID,
		ObligationID: domain.ObligationKey(w.ID, w.Revision, sub.ID),
		Revision:     w.Revision,
		Verdict:      text,
	}
	// Arm the ladder at delivery, not at obligation creation. Subscribers that
	// only own a readable pointer are not woken and must not escalate.
	if o, err := b.store.Obligation(p.ObligationID); err == nil {
		b.stage(ctx, o, "", now)
	}
	start := time.Now()
	attempt := b.wakeCount(w, sub.ID) + 1
	var deliverErr error
	if sw, ok := b.adapterWaker.(interface {
		DeliverToSubscriber(context.Context, domain.Watch, domain.WakePayload, string) error
	}); ok {
		deliverErr = sw.DeliverToSubscriber(ctx, w, p, target)
	} else if b.adapterWaker != nil {
		deliverErr = b.adapterWaker.Deliver(ctx, sub.Adapter, target, p)
	} else {
		deliverErr = b.waker.Deliver(ctx, target, p)
	}
	if deliverErr != nil {
		dead := isDeadSessionErr(deliverErr)
		outcome := domain.OutcomeTransportFailed
		if dead {
			// A dead session can never send a receipt. It is indeterminate,
			// which the ladder never retries (ADR 0017). It is also not a
			// backend failure, so ignoreExceptions keeps the breaker closed.
			outcome = domain.OutcomeIndeterminate
			b.breakerIgnore(adapter)
		} else if useBreaker {
			b.breakerRecord(adapter, now, true)
		}
		b.recordDelivery(ctx, p.ObligationID, attempt, outcome, now)
		observe.WakeFailed(sub.Adapter, w.ID, deliverErr, w.Revision)
		observe.WakeOutcomeCorrelated(sub.Adapter, string(outcome), w.ID, observe.Fields{
			observe.FieldCycleID:      cycleID,
			observe.FieldObligationID: p.ObligationID,
			observe.FieldRole:         string(sub.Role),
			observe.FieldAttempt:      attempt,
		}, w.Revision)
		observe.ActorError("wake", w.ID, deliverErr)
		log.Printf("cibridge: wake %s: %v", target, deliverErr)
		if dead {
			// The addressee is gone. Flag the delivery event and mark the
			// subscriber orphaned; OrphanWake counts it once in markOrphaned.
			observe.WakeDeliveredOrphan(sub.Adapter, w.ID, w.Revision)
			b.markOrphaned(ctx, w, sub, now, cycleID)
		}
		return deliverErr
	}
	if useBreaker {
		b.breakerRecord(adapter, now, false)
	}
	observe.WakeDuration(sub.Adapter, time.Since(start))
	observe.WakeDelivered(sub.Adapter, w.ID, w.Revision)
	observe.WakeOutcomeCorrelated(sub.Adapter, observe.WakeOutcomePosted, w.ID, observe.Fields{
		observe.FieldCycleID:      cycleID,
		observe.FieldObligationID: p.ObligationID,
		observe.FieldRole:         string(sub.Role),
		observe.FieldAttempt:      attempt,
	}, w.Revision)
	b.recordDelivery(ctx, p.ObligationID, attempt, domain.OutcomePosted, now)
	b.recordWake(w, sub.ID, now, attempt)
	b.markDelivered(p.ObligationID, now, attempt, "")
	b.cycleEvent(cycleID, "wake", cycleFields{
		watchID: w.ID, obligationID: p.ObligationID, revision: w.Revision,
		role: sub.Role, adapter: adapter, verdict: w.Verdict, attempt: attempt,
	}, observe.Fields{"sessionID": target})
	return nil
}

// WakeUnit is one wake submitted to DeliverWakes.
type WakeUnit struct {
	Watch      domain.Watch
	Subscriber domain.Subscriber
	Text       string
	CycleID    string
	Breaker    bool
}

// WakeUnitResult is the outcome of one batched wake. Status is done, failed or
// skipped, and Err carries the delivery error when the status is failed.
type WakeUnitResult struct {
	Status UnitStatus
	Err    error
}

// DeliverWakes delivers a batch of wakes with at most limit in flight. The
// first delivery error stops new starts, and wakes not yet started come back
// as skipped. A limit <= 0 selects DefaultParallelLimit. The now stamp is
// supplied by the caller so the batch shares one clock reading.
func (b *wakeDispatcher) DeliverWakes(ctx context.Context, now time.Time, units []WakeUnit, limit int) []WakeUnitResult {
	statuses := BoundedRun(ctx, len(units), limit, true, func(ctx context.Context, i int) error {
		u := units[i]
		return b.wakeOne(ctx, u.Watch, u.Subscriber, u.Text, now, u.CycleID, u.Breaker)
	})
	out := make([]WakeUnitResult, len(statuses))
	for i, s := range statuses {
		out[i] = WakeUnitResult{Status: s.Status, Err: s.Err}
	}
	return out
}

// drainDeferred replays parked wakes when a wake is being delivered on the
// same adapter. permitHeld says that the current wake already owns a breaker
// permit; otherwise the replay takes and releases its own permit.
func (b *wakeDispatcher) drainDeferred(ctx context.Context, adapter string, w domain.Watch, sub domain.Subscriber, now time.Time, cycleID string, permitHeld bool) {
	var current string
	if w.ID != "" && sub.ID != "" {
		current = domain.ObligationKey(w.ID, w.Revision, sub.ID)
	}
	b.replayDeferred(ctx, adapter, now, cycleID, current, permitHeld)
}

// replayDeferred replays every parked wake of one adapter. A replay wake has
// breaker=false, so this method accounts for the permit itself when the caller
// did not already hold one. Entries that still fail delivery are returned to
// the queue and the durable marker stays pending for the next Refresh.
func (b *wakeDispatcher) replayDeferred(ctx context.Context, adapter string, now time.Time, cycleID, skipKey string, permitHeld bool) {
	b.deferredMu.Lock()
	if b.draining || len(b.deferred[adapter]) == 0 {
		b.deferredMu.Unlock()
		return
	}
	b.draining = true
	b.deferredMu.Unlock()
	defer func() {
		b.deferredMu.Lock()
		b.draining = false
		b.deferredMu.Unlock()
	}()

	if !permitHeld && !b.breakerPermit(adapter, now) {
		return
	}
	failed := make([]deferredWake, 0)
	for _, dw := range b.takeDeferred(adapter) {
		if skipKey != "" && domain.ObligationKey(dw.watchID, dw.revision, dw.subscriberID) == skipKey {
			continue
		}
		if dw.watch.ID == "" || dw.subscriber.ID == "" {
			continue
		}
		// A marker or delivered pointer from a previous replay wins over a
		// duplicate queue entry, so the same obligationID is delivered once.
		if b.deferredDelivered(dw) {
			b.markDeferredDelivered(dw, now)
			continue
		}
		replayCycle := dw.cycleID
		if replayCycle == "" {
			replayCycle = cycleID
		}
		before := b.wakeCount(dw.watch, dw.subscriber.ID)
		b.wakeOne(ctx, dw.watch, dw.subscriber, dw.text, now, replayCycle, false)
		// A successful delivery advances the durable wake counter even when the
		// caller never staged an obligation for this revision. That keeps the
		// marker closed in both production and thin harness setups.
		if b.deferredDelivered(dw) || b.wakeCount(dw.watch, dw.subscriber.ID) > before {
			b.markDeferredDelivered(dw, now)
			continue
		}
		failed = append(failed, dw)
	}
	if len(failed) > 0 {
		b.deferredMu.Lock()
		b.deferred[adapter] = append(failed, b.deferred[adapter]...)
		b.deferredMu.Unlock()
	}
	if !permitHeld {
		b.breakerRecord(adapter, now, len(failed) > 0)
	}
}

// deferredDelivered reports whether a parked wake reached its subscriber. The
// marker is closed after a successful replay, but the delivery itself lands on
// the ordinary obligation, so either fact is enough to suppress a duplicate.
func (b *wakeDispatcher) deferredDelivered(dw deferredWake) bool {
	if o, err := b.store.Obligation(deferredWakeID(dw)); err == nil && !o.DeliveredAt.IsZero() {
		return true
	}
	id := domain.ObligationKey(dw.watchID, dw.revision, dw.subscriberID)
	if o, err := b.store.Obligation(id); err == nil && !o.DeliveredAt.IsZero() {
		return true
	}
	return false
}

func (b *wakeDispatcher) markDeferredDelivered(dw deferredWake, now time.Time) {
	b.markDelivered(deferredWakeID(dw), now, 1, "")
}

// wakeCap returns the effective wake limit of a Watch. A zero or negative
// MaxWakesPerWatch selects the domain default of six.
func wakeCap(w domain.Watch) int {
	cap := w.Policy.WakeCap()
	if cap <= 0 {
		return domain.DefaultMaxWakesPerWatch
	}
	return cap
}

// parkForWakeCap parks a Watch that spent its whole wake budget without a
// green CI verdict. It writes a structured escalation naming the Watch and the
// park incident, then notifies every subscriber. The Watch leaves parked only
// after all those notices were delivered (ATX-32).
func (b *wakeDispatcher) parkForWakeCap(ctx context.Context, w domain.Watch, cycleID string) {
	now := b.clock.Now()
	current, err := b.store.Watch(w.ID)
	if err != nil || current.Status == domain.StatusClosed || current.Status == domain.StatusParked {
		return
	}
	parked, effects := domain.Transition(current, domain.ParkEvent(domain.ParkReasonBudget,
		"wake cap reached without green CI", now))
	if parked.Revision == current.Revision {
		return
	}
	if err := b.store.SaveWatch(parked); err != nil {
		observe.ActorError("cibridge-park", parked.ID, err)
		return
	}
	esc := domain.ParkEscalation{
		WatchID:  parked.ID,
		Incident: domain.ParkIncidentKey(parked.ID, domain.ParkReasonBudget),
		Reason:   string(domain.ParkReasonBudget),
		PR:       parked.PR,
		Revision: parked.Revision,
		Wakes:    make(map[string]int, len(parked.Subscribers)),
		At:       now,
	}
	for _, sub := range parked.Subscribers {
		esc.Wakes[sub.ID] = b.wakeCount(parked, sub.ID)
	}
	line := esc.Line()
	b.appendEscalation(line)
	b.notifyPark(ctx, parked, now, cycleID)
	for _, e := range effects {
		b.cycleEvent(cycleID, "park", cycleFields{watchID: parked.ID, revision: parked.Revision}, observe.Fields{
			observe.FieldReason: string(e.ParkReason), observe.FieldError: e.ParkError,
			"incident": esc.Incident,
		})
	}
	log.Printf("cibridge: %s parked: %s", parked.ID, line)
}

// parkNoticeID is the durable identity of one subscriber's park notice. The
// Watch revision scopes a notice set to one park episode, so a later park does
// not inherit an earlier subscriber's delivery.
func parkNoticeID(w domain.Watch, subscriberID string) string {
	return domain.ObligationKey("park:"+w.ID, w.Revision, subscriberID)
}

// notifyPark delivers one park notice to every subscriber of a parked Watch.
// An undelivered notice stays that way, so a party that never saw the park
// keeps the Watch parked instead of letting it vanish from the control loop.
func (b *wakeDispatcher) notifyPark(ctx context.Context, w domain.Watch, now time.Time, cycleID string) {
	if b == nil || (b.waker == nil && b.adapterWaker == nil) {
		return
	}
	for _, sub := range w.Subscribers {
		id := parkNoticeID(w, sub.ID)
		if _, stop := b.deliveredOrStage(domain.Obligation{
			ID: id, WatchID: w.ID, Revision: w.Revision,
			SubscriberID: sub.ID, VerdictPointer: domain.VerdictParked, StagedAt: now,
		}, now, stagingError("cibridge-park-notice", w.ID), nil); stop {
			continue
		}
		target, err := wakeAddressee(w, w.ID, sub.ID)
		if err != nil {
			observe.WakeSkipped(sub.Adapter, w.ID, "park notice target mismatch")
			continue
		}
		p := domain.WakePayload{
			WatchID: w.ID, ObligationID: id, Revision: w.Revision,
			Verdict: fmt.Sprintf("parked: watch %s exhausted its wake budget; incident %s",
				w.ID, domain.ParkIncidentKey(w.ID, domain.ParkReasonBudget)),
		}
		var deliverErr error
		if b.adapterWaker != nil {
			deliverErr = b.adapterWaker.Deliver(ctx, sub.Adapter, target, p)
		} else {
			deliverErr = b.waker.Deliver(ctx, target, p)
		}
		if deliverErr != nil {
			b.recordDelivery(ctx, id, 1, domain.OutcomeTransportFailed, now)
			observe.WakeFailed(sub.Adapter, w.ID, deliverErr, w.Revision)
			continue
		}
		b.markDelivered(id, now, 1, "")
		b.cycleEvent(cycleID, "park_notice", cycleFields{
			watchID: w.ID, obligationID: id, revision: w.Revision,
			role: sub.Role, adapter: sub.Adapter,
		}, observe.Fields{"subscriberID": sub.ID, "sessionID": target})
	}
}

// allSubscribersNotified reports whether every subscriber of a parked Watch has
// a delivered park notice for that park revision. A Watch with no subscribers
// has nobody to inform and stays parked.
func (b *wakeDispatcher) allSubscribersNotified(w domain.Watch) bool {
	if len(w.Subscribers) == 0 {
		return false
	}
	for _, sub := range w.Subscribers {
		o, err := b.store.Obligation(parkNoticeID(w, sub.ID))
		if err != nil || o.DeliveredAt.IsZero() {
			return false
		}
	}
	return true
}

// resumeParkedAfterNotify is the explicit exit from parked. A parked Watch is
// absent from ActiveWatches and invisible to the orchestrator; once every
// subscriber saw the park notice, the Watch returns to watching with a fresh
// per-subscriber budget. A Watch nobody saw stays parked.
func (b *wakeDispatcher) resumeParkedAfterNotify(now time.Time, cycleID string) {
	if b == nil || b.store == nil {
		return
	}
	all, err := b.store.AllWatches()
	if err != nil {
		return
	}
	for i := range all {
		w := all[i]
		if w.Status != domain.StatusParked || !b.allSubscribersNotified(w) {
			continue
		}
		resumed, _ := domain.Transition(w, domain.UnparkEvent(now))
		if resumed.Revision == w.Revision {
			continue
		}
		if err := b.store.SaveWatch(resumed); err != nil {
			observe.ActorError("cibridge-unpark", w.ID, err)
			continue
		}
		for _, sub := range w.Subscribers {
			b.resetWakeCount(w, sub.ID)
		}
		incident := domain.ParkIncidentKey(w.ID, domain.ParkReasonBudget)
		b.cycleEvent(cycleID, "unpark", cycleFields{watchID: w.ID, revision: resumed.Revision}, observe.Fields{
			"incident": incident,
		})
		log.Printf("cibridge: %s resumed after all subscribers saw park incident %s", w.ID, incident)
	}
}

// appendEscalation writes one deduplicated line to ESCALATIONS.md. The file is
// created on first use; an already recorded reason is never written twice.
func (b *wakeDispatcher) appendEscalation(line string) {
	if b.escalate != nil {
		b.escalate(line)
		return
	}
	raw, _ := os.ReadFile(escalationFile)
	if strings.Contains(string(raw), line) {
		return
	}
	f, err := os.OpenFile(escalationFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		observe.ActorError("cibridge-escalation", "", err)
		return
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, "- "+line); err != nil {
		observe.ActorError("cibridge-escalation", "", err)
	}
}

// markOrphaned records a Lost session on the subscriber and escalates to the
// orchestrator once. The escalation obligation is keyed by the Watch, so two
// delivery failures for one subscriber produce one executor_orphaned notice.
func (b *wakeDispatcher) markOrphaned(ctx context.Context, w domain.Watch, sub domain.Subscriber, now time.Time, cycleID string) {
	current, err := b.store.Watch(w.ID)
	if err != nil {
		return
	}
	found := false
	for i := range current.Subscribers {
		if current.Subscribers[i].ID == sub.ID {
			current.Subscribers[i].Phase = domain.WorkerPhaseOrphaned
			current.Subscribers[i].LastError = "session gone"
			found = true
			break
		}
	}
	if !found {
		return
	}
	if err := b.store.SaveWatch(current); err != nil {
		observe.ActorError("cibridge-orphan", current.ID, err)
		return
	}
	observe.OrphanWake(current.ID)
	sessionID := b.blockedOrchestrator(current)
	if sessionID == "" {
		return
	}
	id := domain.ObligationKey("orphan:"+current.ID, 0, sessionID)
	o, stop := b.deliveredOrStage(domain.Obligation{
		ID: id, WatchID: current.ID, Revision: current.Revision,
		SubscriberID: sessionID, VerdictPointer: domain.VerdictBlocked, StagedAt: now,
	}, now, stagingError("cibridge-orphan-obligation", current.ID), nil)
	if stop {
		return
	}
	b.stage(ctx, o, domain.VerdictBlocked, now)
	p := domain.WakePayload{
		WatchID: current.ID, ObligationID: id, Revision: current.Revision,
		Verdict: fmt.Sprintf("executor_orphaned: %s, acceptor %s, watchID %s", sub.Role, sub.ID, current.ID),
	}
	var deliverErr error
	if b.adapterWaker != nil {
		deliverErr = b.adapterWaker.Deliver(ctx, b.blockedAdapter(current, sessionID), sessionID, p)
	} else if b.waker != nil {
		deliverErr = b.waker.Deliver(ctx, sessionID, p)
	}
	if deliverErr != nil {
		outcome := domain.OutcomeTransportFailed
		if isDeadSessionErr(deliverErr) {
			outcome = domain.OutcomeIndeterminate
		}
		b.recordDelivery(ctx, id, 1, outcome, now)
		observe.WakeFailedOutcome("", current.ID, string(outcome), deliverErr, current.Revision)
		return
	}
	b.recordDelivery(ctx, id, 1, domain.OutcomePosted, now)
	b.markDelivered(id, now, 1, "")
	b.cycleEvent(cycleID, "executor_orphaned", cycleFields{
		watchID: current.ID, obligationID: id, revision: current.Revision,
		role: domain.RoleOrchestrator, adapter: b.blockedAdapter(current, sessionID),
		verdict: domain.VerdictBlocked,
	}, observe.Fields{"subscriberID": sub.ID, "sessionID": sessionID})
}

// inWorkLastActivity is the newest of the subscriber's own activity stamp and
// the MCP claim/read stamps on its obligations. A legacy subscriber with no
// own stamp can still be shown alive by a fresh claim or read.
func (b *wakeDispatcher) inWorkLastActivity(w domain.Watch, sub domain.Subscriber) time.Time {
	last := sub.LastActivity
	consider := func(o domain.Obligation) {
		if o.SubscriberID != sub.ID {
			return
		}
		for _, at := range []time.Time{o.ClaimedAt, o.ReadAt} {
			if at.After(last) {
				last = at
			}
		}
	}
	if src, ok := b.store.(activitySource); ok {
		if obligations, err := src.WatchObligations(w.ID); err == nil {
			for _, o := range obligations {
				consider(o)
			}
			return last
		}
	}
	if o, err := b.store.Obligation(domain.ObligationKey(w.ID, w.Revision, sub.ID)); err == nil {
		consider(o)
	}
	return last
}

// escalateInWorkStall scans one Watch for in_work subscribers that have been
// quiet longer than inWorkTimeout. It returns true when at least one was
// replaced, so Refresh leaves the Watch alone for the rest of this cycle.
func (b *wakeDispatcher) escalateInWorkStall(ctx context.Context, w domain.Watch, now time.Time, cycleID string) bool {
	if b == nil || b.inWorkTimeout <= 0 {
		return false
	}
	current, err := b.store.Watch(w.ID)
	if err != nil || isTerminalState(string(current.Status)) || current.Status == domain.StatusParked || current.Status == domain.StatusBlocked {
		return false
	}
	escalated := false
	for _, sub := range current.Subscribers {
		if sub.Phase != domain.WorkerPhaseInWork {
			continue
		}
		last := b.inWorkLastActivity(current, sub)
		if last.IsZero() || !now.After(last.Add(b.inWorkTimeout)) {
			continue
		}
		if b.markInWorkStalled(ctx, current, sub, now, cycleID) {
			escalated = true
		}
	}
	return escalated
}

// markInWorkStalled replaces one stale in_work subscriber and escalates to the
// project orchestrator exactly once per (watch, subscriber, revision). The
// dedup record is an obligation in the store, so a restart or a repeated tick
// cannot append a second ESCALATIONS.md line or steer the session again.
func (b *wakeDispatcher) markInWorkStalled(ctx context.Context, w domain.Watch, sub domain.Subscriber, now time.Time, cycleID string) bool {
	sessionID := b.blockedOrchestrator(w)
	if sessionID == "" {
		observe.WakeSkipped(sub.Adapter, w.ID, "stalled in_work Watch has no orchestrator address")
		return false
	}
	if b.waker == nil && b.adapterWaker == nil {
		return false
	}
	id := domain.ObligationKey("inwork-stall:"+w.ID, w.Revision, sub.ID)
	o, stop := b.deliveredOrStage(domain.Obligation{
		ID: id, WatchID: w.ID, Revision: w.Revision,
		SubscriberID: sub.ID, VerdictPointer: domain.VerdictBlocked, StagedAt: now,
	}, now, stagingError("cibridge-stall-obligation", w.ID), nil)
	if stop {
		return false
	}
	b.stage(ctx, o, domain.VerdictBlocked, now)
	text := fmt.Sprintf("in_work_stalled: subscriber %s (%s) on watch %s revision %d had no head, MCP or commit activity for %s; replacement required",
		sub.ID, sub.Role, w.ID, w.Revision, b.inWorkTimeout)
	p := domain.WakePayload{WatchID: w.ID, ObligationID: id, Revision: w.Revision, Verdict: text}
	var deliverErr error
	if b.adapterWaker != nil {
		deliverErr = b.adapterWaker.Deliver(ctx, b.blockedAdapter(w, sessionID), sessionID, p)
	} else {
		deliverErr = b.waker.Deliver(ctx, sessionID, p)
	}
	if deliverErr != nil {
		outcome := domain.OutcomeTransportFailed
		if isDeadSessionErr(deliverErr) {
			outcome = domain.OutcomeIndeterminate
		}
		b.recordDelivery(ctx, id, 1, outcome, now)
		observe.WakeFailedOutcome("", w.ID, string(outcome), deliverErr, w.Revision)
		observe.ActorError("wake-in-work-stall", w.ID, deliverErr)
		return false
	}

	updated, err := b.store.Watch(w.ID)
	if err == nil {
		found := false
		for i := range updated.Subscribers {
			if updated.Subscribers[i].ID != sub.ID {
				continue
			}
			updated.Subscribers[i].Phase = domain.WorkerPhaseOrphaned
			updated.Subscribers[i].LastError = "in_work stall: no activity for " + b.inWorkTimeout.String()
			found = true
			break
		}
		if found {
			if err := b.store.SaveWatch(updated); err != nil {
				observe.ActorError("cibridge-in-work-stall", updated.ID, err)
				return false
			}
		}
	}
	b.recordDelivery(ctx, id, 1, domain.OutcomePosted, now)
	b.markDelivered(id, now, 1, "")
	line := fmt.Sprintf("%s: watch %s subscriber %s stalled in_work after %s (revision %d)",
		now.UTC().Format(time.RFC3339), w.ID, sub.ID, b.inWorkTimeout, w.Revision)
	b.appendEscalation(line)
	observe.Event(observe.LevelInfo, observe.OrphanEscalated, observe.Fields{
		observe.FieldWatchID: w.ID, "subscriberID": sub.ID, "sessionID": sessionID, observe.FieldReason: "in_work_stall",
	})
	b.cycleEvent(cycleID, "in_work_stall", cycleFields{
		watchID: w.ID, obligationID: id, revision: w.Revision,
		role: domain.RoleOrchestrator, adapter: b.blockedAdapter(w, sessionID),
		verdict: domain.VerdictBlocked,
	}, observe.Fields{"subscriberID": sub.ID, "sessionID": sessionID})
	return true
}

// blockingFailedNames keeps only the failed checks that can block a merge. An
// informational check (Required: false) cannot block, so it must not appear in
// keep_working or retry wake text as a check to fix or re-run. This is the
// wake-side application of the single fail-fact rule from ATX-V01. A name
// absent from the map stays in the list: the verdict layer treats a failure
// without per-check detail as fail-closed, so dropping it would hide a real
// blocking failure.
func blockingFailedNames(names []string, checks map[string]domain.Check) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if c, observed := checks[name]; observed && !c.Required {
			continue
		}
		out = append(out, name)
	}
	return out
}

// wakeText builds the self-teaching instruction carried in a wake pointer.
// Each verdict names the concrete next action so a receiver cannot answer with
// an empty rebase or a blind re-run. The observed main head is appended so two
// checks with different upstream heads produce different wakes (ADR 0155).
func wakeText(verdict domain.Verdict, obs ciObservation, targets []domain.WakeTarget, mergeErr string) string {
	failed := blockingFailedNames(realFailedNames(obs.pr.Checks), obs.pr.Checks)
	var text string
	switch verdict {
	case domain.VerdictKeepWorking:
		switch {
		case len(failed) > 0:
			text = "keep_working: чини конкретные failedChecks: " + strings.Join(failed, ", ") + "; не делай пустой ребейз"
		case mergeErr != "":
			text = "keep_working: ошибка мержа: " + mergeErr + "; не делай пустой ребейз"
		default:
			text = "keep_working: чини конкретные failedChecks, не делай пустой ребейз"
		}
	case domain.VerdictRebase:
		text = "rebase: git fetch origin main && git rebase origin/main && git push --force-with-lease"
		if reason := wakeReason(targets); reason != "" {
			text += "; reason: " + reason
		}
	case domain.VerdictRecheck:
		text = "recheck: база main сдвинулась после снимка; повтори проверку CI на свежем main"
	case domain.VerdictRetry:
		checks := make([]string, 0)
		for _, t := range targets {
			checks = append(checks, t.RerunChecks...)
		}
		if len(checks) == 0 {
			checks = failed
		}
		if len(checks) > 0 {
			text = "retry: gh run rerun --failed (" + strings.Join(checks, ", ") + ")"
		} else {
			text = "retry: gh run rerun --failed"
		}
	case domain.VerdictBlocked, domain.VerdictTimeout:
		text = "остановись и жди разблокировки"
	default:
		text = string(verdict)
	}
	return text + mainHeadMarker(obs.base.HeadSHA)
}

// wakeReason returns the machine-readable reason carried on a wake target. An
// empty reason leaves the wake text unchanged for older verdicts that do not
// set one. The first non-empty reason wins when a verdict has several targets.
func wakeReason(targets []domain.WakeTarget) string {
	for _, t := range targets {
		if r := strings.TrimSpace(t.Reason); r != "" {
			return r
		}
	}
	return ""
}

// mainHeadMarker appends the main branch head observed for a Watch to the wake
// text. A subscriber reads it to tell an upstream move from its own error. An
// empty observation contributes nothing, so existing wake text is unchanged.
func mainHeadMarker(headSHA string) string {
	sha := strings.TrimSpace(headSHA)
	if sha == "" {
		return ""
	}
	return "; mainHeadSHA=" + sha
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// isDeadSessionErr reports a delivery failure caused by a session that no
// longer exists. Only those failures orphan a subscriber; a transient network
// error leaves it live so the next tick may retry.
func isDeadSessionErr(err error) bool {
	s := strings.ToLower(errText(err))
	for _, key := range []string{"session not found", "no such session", "session gone", "404", "410", "gone"} {
		if strings.Contains(s, key) {
			return true
		}
	}
	return false
}

// wakeAddressee resolves the one session a wake may address on w. The address
// must be subscriber.ID on this very Watch with role executor, fixer or
// orchestrator. No caller session, _meta value, notify target or role-wide
// broadcast replaces it.
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
		if sub.Role != domain.RoleExecutor && sub.Role != domain.RoleFixer && sub.Role != domain.RoleOrchestrator {
			return "", fmt.Errorf("cibridge: subscriber %q has role %q", sub.ID, sub.Role)
		}
		return sub.ID, nil
	}
	return "", fmt.Errorf("cibridge: session %q is not a subscriber of watch %s", addressee, watchID)
}
