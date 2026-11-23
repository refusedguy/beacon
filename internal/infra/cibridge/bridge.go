// Package cibridge observes CI for active Watches, asks the domain verdict
// engine what each observation means, persists the result and drives the
// follow-up actions (wake, merge, block, head change). It reads CI through
// domain.BatchCIProvider with a domain.CIProvider fallback, merges through
// domain.Merger and keeps blocker incidents in a narrow control-plane port so
// this package never imports the store adapter.
package cibridge

import (
	"context"
	"encoding/json"
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

// DefaultPollInterval is the cadence used when BEACON_CI_POLL_INTERVAL is
// absent, malformed or non-positive. The old five-second tick called gh twice
// for every live Watch: twelve Watches produced about 288 gh calls a minute
// and ran into TLS timeouts and exit status 1. Thirty seconds is slow enough
// to keep the same fleet under fifty calls a minute while verdicts stay fresh.
const DefaultPollInterval = 30 * time.Second

// DefaultInWorkTimeout is the quiet window an in_work subscriber gets before
// the watchdog treats it as stalled. A subscriber that shows no new head, MCP
// claim/read or commit inside the window is replaced instead of woken again.
const DefaultInWorkTimeout = 20 * time.Minute

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

// fieldCycleID is the local alias of the canonical observe field name. The
// alias keeps cibridge call sites and tests readable while observe stays the
// single source of truth for log field names.
const fieldCycleID = observe.FieldCycleID

// escalationFile is the one-line-per-escalation journal. A Watch that hits its
// wake cap without green CI is parked and its reason is appended here once.
const escalationFile = "ESCALATIONS.md"

// mergeHeldRecheck is the machine-readable hold a provider returns when the
// base branch moved after the batch snapshot. applyMerge turns it into the
// recheck verdict instead of a rebase, so a stale snapshot never ships.
const mergeHeldRecheck = "recheck"

// stagingError is the one observability channel for obligation staging. Each
// site keeps its metric name here, so a failure reaches ActorError even where
// the old copy used log.Printf or returned without a report.
func stagingError(metric, watchID string) func(error) {
	return func(err error) {
		observe.ActorError(metric, watchID, err)
	}
}

// ensureObligation stages o when no obligation exists under its key. The
// callback keeps the site's error action. The bool result lets the caller keep
// its own return, continue or return false without a closure capturing control
// flow. StagedAt comes from now so callers pass the cycle time once.
func (b *Bridge) ensureObligation(o domain.Obligation, now time.Time, onErr func(error)) bool {
	if _, err := b.store.Obligation(o.ID); err == nil {
		return false
	}
	o.StagedAt = now
	if err := b.store.SaveObligation(o); err != nil {
		if onErr != nil {
			onErr(err)
		}
		return true
	}
	return false
}

// deliveredOrStage stops when the obligation is already delivered or staging
// failed. It reports a staging failure through onErr and runs onDelivered only
// for a real delivered marker. The returned obligation comes from the store
// when one exists, so ladder staging does not overwrite fields from an earlier
// attempt.
func (b *Bridge) deliveredOrStage(o domain.Obligation, now time.Time, onErr func(error), onDelivered func()) (domain.Obligation, bool) {
	o.StagedAt = now
	staged, err := b.store.Obligation(o.ID)
	if err == nil {
		if !staged.DeliveredAt.IsZero() {
			if onDelivered != nil {
				onDelivered()
			}
			return staged, true
		}
		return staged, false
	}
	if err := b.store.SaveObligation(o); err != nil {
		if onErr != nil {
			onErr(err)
		}
		return o, true
	}
	return o, false
}

const (
	// rebaseCounterSubscriber is the reserved subscriber id of the durable
	// obligation that records one rebase attempt for a Watch revision. One
	// obligation per revision keeps the rebase attempt across a process restart.
	rebaseCounterSubscriber = "rebase-counter"

	// defaultMaxRebases matches the project default in ADR 0259. A Watch with
	// no explicit budget gets this limit instead of an unbounded retry loop.
	defaultMaxRebases = 3
)

var cycleSeq atomic.Uint64

// newCycleID returns a process-unique id for one Refresh. The timestamp keeps
// cycles from different restarts distinct; the counter separates cycles that
// start in the same nanosecond.
func newCycleID(now time.Time) string {
	return fmt.Sprintf("%s-%d", now.UTC().Format("20060102T150405.000000000Z"), cycleSeq.Add(1))
}

// ReadStamper stamps a durable read on an obligation. The MCP tool owns the
// production implementation; Bridge only needs this narrow port to let the
// orchestrator confirm that a verdict was actually read, not just observed.
type ReadStamper interface {
	StampRead(ctx context.Context, id string, at time.Time) (domain.Obligation, bool, error)
}

// WakeLadder drives the ADR 0017 delivery lifecycle of an obligation. The
// production implementation is ladder.Ladder, bound by the composition root.
// The port uses domain types only, so Bridge stays independent of sibling
// infra packages (ADR 0005).
//
// Stage arms the ladder when a wake goes out, for the obligation that carries
// that pointer. Record consumes the delivery outcome: posted means no retries,
// transport-failed repeats the same pointer, indeterminate (no receipt, dead
// session) is never retried.
type WakeLadder interface {
	Stage(ctx context.Context, o domain.Obligation, verdict domain.Verdict, at time.Time) error
	Record(ctx context.Context, obligationID string, attempt int, outcome domain.AttemptOutcome, at time.Time) error
}

// OrchestratorIDSource returns the project-configured orchestrator session.
// The app layer owns the config file, so Bridge takes the value through this
// port and stays independent of it.
type OrchestratorIDSource func() string

// stateStore is the local aggregate of the narrow state ports the bridge needs.
// The concrete store is assembled once in the composition root and passed to
// every consumer, so the bridge stays independent of the infra store package.
type stateStore interface {
	domain.WatchStore
	domain.ObligationStore
}

// liveWatchSource is the narrower alternative to domain.WatchStore.AllWatches.
// The store adapter excludes closed Watches from LiveWatches; a caller that does
// not implement it still works through AllWatches.
type liveWatchSource interface {
	LiveWatches() ([]domain.Watch, error)
}

// activitySource exposes the obligation history used by the in_work watchdog
// to see MCP claim and read stamps. The store adapter implements it; a store
// without it still works through the current-revision obligation.
type activitySource interface {
	WatchObligations(watchID string) ([]domain.Obligation, error)
}

// DeliveryMarker persists one wake delivery on the obligation itself. Only the
// wake dispatcher writes it; incident lifecycle methods do not need this port.
// A store that does not implement it keeps the SaveObligation fallback.
type DeliveryMarker interface {
	MarkObligationDelivered(id string, at time.Time, attempt int, lastErr string) error
}

// GenerationControl is the generation-stamping port used when an unblock
// returns a Watch to watching (T3.457). It is declared here, in infra, so
// cibridge never imports the app layer (ADR 0005): the orchestrator claim
// model implements it and the composition root injects one shared instance.
// A string token and a task ID cross the boundary, no app type does.
type GenerationControl interface {
	ResolveBlocked(taskID string) (string, bool)
}

// escalationWriter is the injection point for the ESCALATIONS.md append. Tests
// replace it so a parked Watch does not touch the working tree.
type escalationWriter func(line string)

// stateMerger is an optional refinement of domain.Merger. A provider that
// already holds a fresh PRState from the batch can merge with it directly,
// skipping a second GraphQL read whose lazy mergeable field may report UNKNOWN.
// callMerge copies the batch base head into PRState.BaseHeadSHA before this
// call, so the provider sees the base SHA alongside the PR without a signature
// change. Mergers that only implement domain.Merger keep the fallback.
type stateMerger interface {
	MergePRWithState(ctx context.Context, repo string, pr int, state domain.PRState) (bool, bool, string, error)
}

// Rebaser updates a stale PR branch onto its base and reports the new head SHA.
// It is an optional control-plane port: the composition root binds the real
// implementation so this package stays independent of the gh runner (ADR 0005).
// A nil rebaser keeps the legacy behaviour of waking the executor for a rebase.
type Rebaser interface {
	Rebase(ctx context.Context, repo string, pr int) (newHeadSHA string, err error)
}

// CardLinks closes the task card linked to a Watch. It is a narrow
// control-plane port declared here so cibridge does not import the store
// adapter or a card writer (ADR 0005). The production adapter reads the
// card_links row for watchID, sets that card to done and appends the PR to its
// confirmation section. CloseMerged must be idempotent by watchID: a repeated
// merged observation writes the card once.
type CardLinks interface {
	CloseMerged(ctx context.Context, watchID string, pr int, at time.Time) error
}

// CardUnlockSource reports which cards became ready after a card was closed.
// It is an optional refinement of CardLinks. A close adapter that also
// implements it lets the bridge publish one card_unlocked event per released
// card. A CardLinks without it keeps the close-only path.
type CardUnlockSource interface {
	UnlockedByClose(ctx context.Context, watchID string, at time.Time) ([]string, error)
}

// cardUnlockEvent is the journal event for one card released by a close.
const cardUnlockEvent = "card_unlocked"

// fieldUnlockedCard names the card released by a card_unlocked event.
const fieldUnlockedCard = "cardID"

// unlockingCardLinks wraps a CardLinks so a successful close publishes the
// cards it released. WithCardLinks and New install the wrapper, so
// verdict_applier keeps calling the same CloseMerged port. A repeated merged
// observation closes the card again without adding journal entries.
type unlockingCardLinks struct {
	CardLinks
	source CardUnlockSource

	mu   sync.Mutex
	seen map[string]bool
}

// wrapCardLinks installs the unlock wrapper only when the close adapter can
// report released cards. Other adapters pass through unchanged.
func wrapCardLinks(c CardLinks) CardLinks {
	if c == nil {
		return nil
	}
	source, ok := c.(CardUnlockSource)
	if !ok || source == nil {
		return c
	}
	return &unlockingCardLinks{CardLinks: c, source: source, seen: make(map[string]bool)}
}

func (d *unlockingCardLinks) CloseMerged(ctx context.Context, watchID string, pr int, at time.Time) error {
	if err := d.CardLinks.CloseMerged(ctx, watchID, pr, at); err != nil {
		return err
	}
	d.publishUnlocks(ctx, watchID, pr, at)
	return nil
}

func (d *unlockingCardLinks) publishUnlocks(ctx context.Context, watchID string, pr int, at time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen[watchID] {
		return
	}
	cards, err := d.source.UnlockedByClose(ctx, watchID, at)
	if err != nil {
		observe.ActorError("cibridge-card-unlocks", watchID, err)
		return
	}
	d.seen[watchID] = true
	for _, cardID := range cards {
		if strings.TrimSpace(cardID) == "" {
			continue
		}
		observe.Event(observe.LevelInfo, cardUnlockEvent, observe.Fields{
			observe.FieldWatchID: watchID,
			observe.FieldPR:      pr,
			fieldUnlockedCard:    cardID,
		})
	}
}

// WorktreeGC removes worktrees that are clean, unlocked and no longer backed
// by a live Watch. It is a narrow control-plane port declared here so cibridge
// does not import the worktree adapter (ADR 0005). The composition root binds
// the real collector with the git runner and the live-watch head set. A nil
// port skips collection, so a Bridge used without it keeps its old behaviour.
// A non-nil error is logged and never blocks the Watch close.
type WorktreeGC func(ctx context.Context) (removed int, err error)

// Bridge refreshes active Watches on a ticker. One Bridge owns one CI provider,
// one state store and the optional control-plane ports, so a refresh is a batched
// read followed by ordered writes.
type Bridge struct {
	// The four parts own one responsibility each. They embed *Bridge back so
	// their methods read the shared configuration through promotion; Bridge
	// keeps the fields, the 12 With* setters and the public method signatures.
	*ciRefresh
	*verdictApplier
	*wakeDispatcher
	*readAPI

	store          stateStore
	ci             domain.CIProvider
	batch          domain.BatchCIProvider
	merger         domain.Merger
	control        domain.BlockerIncidents
	delivery       DeliveryMarker
	cardLinks      CardLinks
	gc             WorktreeGC
	clock          domain.Clock
	interval       time.Duration
	waker          domain.WakeDeliverer
	adapterWaker   domain.AdapterWakeDeliverer
	reader         ReadStamper
	ladder         WakeLadder
	escalate       escalationWriter
	orchestratorID OrchestratorIDSource
	inWorkTimeout  time.Duration
	autoRebase     bool
	rebaser        Rebaser
	rebaseMu       sync.Mutex
	rebased        map[string]bool
	// refreshMu and refreshing form a single-flight guard: at most one
	// Refresh runs at a time, so a ticker tick cannot duplicate the CI polls
	// of a Refresh already in flight (a manual call or an earlier tick).
	refreshMu  sync.Mutex
	refreshing bool
	tracer     observe.Tracer
	// generations stamps a fresh generation on every Watch an unblock returns
	// to watching (T3.457). It is optional: a bridge built before the port was
	// added keeps its block and unblock behaviour, while one wired with the same
	// ledger as beacon_claim ends the previous owner's claim on resolve.
	generations GenerationControl

	// breakers holds the in-memory circuit breaker of each wake adapter.
	// The durable per-Watch wake cap stays separate and keeps its own counter.
	breakerMu sync.Mutex
	breakers  map[string]*adapterBreaker

	// deferred holds wakes an OPEN breaker refused. They are replayed once the
	// adapter can deliver again, so an outage does not drop the pointer. The
	// durable obligation written for each entry is the trace left after a
	// restart, when the in-memory queue no longer exists.
	deferredMu sync.Mutex
	deferred   map[string][]deferredWake
	draining   bool
}

// deferredWake is one wake parked while its adapter breaker was OPEN. The full
// payload is kept in memory so replay sends the same pointer the first wake
// would have sent.
type deferredWake struct {
	watchID      string
	revision     uint64
	subscriberID string
	adapter      string
	text         string
	verdict      domain.Verdict
	cycleID      string
	stagedAt     time.Time
	// watch and subscriber are the exact inputs of the refused wake. Replay
	// uses them directly, so a store write between the skip and the replay
	// cannot turn the wake into a different delivery.
	watch      domain.Watch
	subscriber domain.Subscriber
}

// deferredWakeID is the durable marker of one parked wake. It lives in the
// obligations table, so an OPEN breaker leaves a trace with the watchID and the
// adapter even when the process restarts before the queue is replayed.
func deferredWakeID(dw deferredWake) string {
	return domain.ObligationKey("deferred-wake:"+dw.watchID+":"+dw.adapter, dw.revision, dw.subscriberID)
}

// deferredWakeRecord is the durable form of one parked wake. The marker
// obligation keeps it as JSON in an attempt channel, so a restarted Bridge can
// rebuild the exact text, Watch and Subscriber the OPEN breaker refused.
type deferredWakeRecord struct {
	WatchID      string            `json:"watchID"`
	Revision     uint64            `json:"revision"`
	SubscriberID string            `json:"subscriberID"`
	Adapter      string            `json:"adapter"`
	Text         string            `json:"text"`
	Verdict      domain.Verdict    `json:"verdict"`
	CycleID      string            `json:"cycleID"`
	StagedAt     time.Time         `json:"stagedAt"`
	Watch        domain.Watch      `json:"watch"`
	Subscriber   domain.Subscriber `json:"subscriber"`
}

// deferredMarkerPrefix is the marker namespace as ObligationKey stores it: the
// colon inside the watchID component is escaped with a backslash.
const deferredMarkerPrefix = "deferred-wake\\:"

// deferredRecordPrefix marks an attempt channel as a parked wake payload. A
// marker written before this field existed has no such channel and is skipped
// on restore instead of producing an empty replay.
const deferredRecordPrefix = "deferred-wake-v1:"

func encodeDeferredWake(dw deferredWake) (string, error) {
	raw, err := json.Marshal(deferredWakeRecord{
		WatchID: dw.watchID, Revision: dw.revision, SubscriberID: dw.subscriberID,
		Adapter: dw.adapter, Text: dw.text, Verdict: dw.verdict,
		CycleID: dw.cycleID, StagedAt: dw.stagedAt,
		Watch: dw.watch, Subscriber: dw.subscriber,
	})
	if err != nil {
		return "", err
	}
	return deferredRecordPrefix + string(raw), nil
}

func decodeDeferredWake(channel string) (deferredWake, error) {
	if !strings.HasPrefix(channel, deferredRecordPrefix) {
		return deferredWake{}, fmt.Errorf("cibridge: not a deferred wake record")
	}
	var r deferredWakeRecord
	if err := json.Unmarshal([]byte(strings.TrimPrefix(channel, deferredRecordPrefix)), &r); err != nil {
		return deferredWake{}, err
	}
	return deferredWake{
		watchID: r.WatchID, revision: r.Revision, subscriberID: r.SubscriberID,
		adapter: r.Adapter, text: r.Text, verdict: r.Verdict,
		cycleID: r.CycleID, stagedAt: r.StagedAt,
		watch: r.Watch, subscriber: r.Subscriber,
	}, nil
}

// breakerFailureID is the durable failure counter of one adapter. A restart
// used to close every breaker and erase the failures that opened it.
func breakerFailureID(adapter string) string {
	return domain.ObligationKey("breaker:"+adapter, 0, "failures")
}

// breakerFailures returns the number of durable failures recorded for adapter.
func (b *Bridge) breakerFailures(adapter string) int {
	o, err := b.store.Obligation(breakerFailureID(adapter))
	if err != nil {
		return 0
	}
	return len(o.Attempts)
}

// recordBreakerFailure appends one durable failure to adapter's counter.
func (b *Bridge) recordBreakerFailure(adapter string, now time.Time) {
	id := breakerFailureID(adapter)
	if b.ensureObligation(domain.Obligation{
		ID: id, WatchID: adapter, Revision: 0,
		SubscriberID: "failures", VerdictPointer: domain.VerdictWatching, StagedAt: now,
	}, now, stagingError("cibridge-breaker-counter", adapter)) {
		return
	}
	b.markDelivered(id, now, b.breakerFailures(adapter)+1, "")
}

// clearBreakerFailures resets adapter's durable counter after a probe succeeds.
func (b *Bridge) clearBreakerFailures(adapter string, now time.Time) {
	id := breakerFailureID(adapter)
	if _, err := b.store.Obligation(id); err != nil {
		return
	}
	_ = b.store.SaveObligation(domain.Obligation{
		ID: id, WatchID: adapter, Revision: 0,
		SubscriberID: "failures", VerdictPointer: domain.VerdictWatching, StagedAt: now,
	})
}

// deferWake parks a wake an OPEN breaker refused. The in-memory queue drives
// the next permit; the obligation carries the full payload so a restarted
// Bridge can rebuild the queue and replay the same pointer.
func (b *Bridge) deferWake(dw deferredWake) {
	b.deferredMu.Lock()
	if b.deferred == nil {
		b.deferred = make(map[string][]deferredWake)
	}
	b.deferred[dw.adapter] = append(b.deferred[dw.adapter], dw)
	b.deferredMu.Unlock()

	encoded, err := encodeDeferredWake(dw)
	if err != nil {
		observe.ActorError("cibridge-defer-wake-encode", dw.watchID, err)
	} else {
		o := domain.Obligation{
			ID: deferredWakeID(dw), WatchID: dw.watchID, Revision: dw.revision,
			SubscriberID: dw.subscriberID, VerdictPointer: dw.verdict, StagedAt: dw.stagedAt,
			Attempts: []domain.Attempt{{N: 0, Channel: encoded}},
		}
		if err := b.store.SaveObligation(o); err != nil {
			observe.ActorError("cibridge-defer-wake", dw.watchID, err)
		}
	}
	observe.WakeDeferred(dw.adapter, dw.watchID, dw.stagedAt, observe.Fields{
		observe.FieldCycleID: dw.cycleID,
	}, dw.revision)
	observe.WakeSkipped(dw.adapter, dw.watchID,
		fmt.Sprintf("adapter circuit breaker open: wake deferred (cycleID=%s)", dw.cycleID))
	b.cycleEvent(dw.cycleID, "wake_deferred", cycleFields{
		watchID: dw.watchID, revision: dw.revision, verdict: dw.verdict,
		adapter: dw.adapter, role: dw.subscriber.Role,
	}, observe.Fields{observe.FieldCycleID: dw.cycleID})
	log.Printf("cibridge: wake for watch %s deferred in cycle %s: adapter %q circuit breaker open",
		dw.watchID, dw.cycleID, dw.adapter)
}

// restoreDeferred rebuilds the in-memory queue from the durable markers. A
// marker whose wake was already delivered is gone from PendingObligations, so
// a restart cannot replay a pointer the subscriber already received.
func (b *Bridge) restoreDeferred() {
	if b == nil || b.store == nil {
		return
	}
	all, err := b.store.PendingObligations()
	if err != nil {
		observe.ActorError("cibridge-deferred-restore", "", err)
		return
	}
	for _, o := range all {
		if !strings.HasPrefix(o.ID, deferredMarkerPrefix) {
			continue
		}
		for _, a := range o.Attempts {
			dw, err := decodeDeferredWake(a.Channel)
			if err != nil {
				continue
			}
			if dw.watch.ID == "" || dw.subscriber.ID == "" {
				continue
			}
			b.deferredMu.Lock()
			if b.deferred == nil {
				b.deferred = make(map[string][]deferredWake)
			}
			dup := false
			for _, existing := range b.deferred[dw.adapter] {
				if deferredWakeID(existing) == deferredWakeID(dw) {
					dup = true
					break
				}
			}
			if !dup {
				b.deferred[dw.adapter] = append(b.deferred[dw.adapter], dw)
			}
			b.deferredMu.Unlock()
			break
		}
	}
}

// takeDeferred removes and returns every wake parked for adapter.
func (b *Bridge) takeDeferred(adapter string) []deferredWake {
	b.deferredMu.Lock()
	defer b.deferredMu.Unlock()
	pending := b.deferred[adapter]
	delete(b.deferred, adapter)
	return pending
}

// wakeOne filters orphaned subscribers before the wake dispatcher sees them.
// markOrphaned and the in_work stall watchdog set WorkerPhaseOrphaned on a
// subscriber whose session is gone. A later verdict still names that role, so
// publishActionable would otherwise send another wake to a dead address. The
// obligation is still staged by the caller, which keeps the verdict readable.
func (b *Bridge) wakeOne(ctx context.Context, w domain.Watch, sub domain.Subscriber, text string, now time.Time, cycleID string, breaker bool) {
	if sub.Phase == domain.WorkerPhaseOrphaned {
		observe.WakeSkipped(sub.Adapter, w.ID, "subscriber orphaned")
		return
	}
	b.wakeDispatcher.wakeOne(ctx, w, sub, text, now, cycleID, breaker)
}

// WithGenerationStore binds the generation ledger shared with beacon_claim and
// the ack handler. When a merge or a fixer resolves a blocker, the bridge
// rotates the generation of every Watch it unblocks so the previous owner's
// claim cannot continue at the old revision. A nil ledger leaves the legacy
// unblock path unchanged.
func (b *Bridge) WithGenerationStore(g GenerationControl) *Bridge {
	if b != nil {
		b.generations = g
	}
	return b
}

// rotateGenerations restamps every Watch an unblock returned to watching. The
// ledger is idempotent, so a Watch with no live claim is left alone and a
// repeated resolve cannot rotate it twice.
func (b *Bridge) rotateGenerations(watchIDs ...string) {
	if b == nil || b.generations == nil {
		return
	}
	for _, id := range watchIDs {
		b.generations.ResolveBlocked(id)
	}
}

// spanTracer resolves the tracer once per call. Callers that inject a tracer
// get their own recorder; the rest share the process-wide one.
func (b *Bridge) spanTracer() observe.Tracer {
	if b != nil && b.tracer != nil {
		return b.tracer
	}
	return observe.Current()
}

// WithOrchestratorIDSource binds the config fallback used by blocked wakes.
// It is consulted only when a Watch names no orchestrator itself. A nil source
// leaves such a Watch unsent rather than guessing a recipient.
func (b *Bridge) WithOrchestratorIDSource(fn OrchestratorIDSource) *Bridge {
	if b != nil {
		b.orchestratorID = fn
	}
	return b
}

// WithInWorkTimeout sets the quiet window of the in_work stall watchdog. A
// non-positive value keeps DefaultInWorkTimeout, so an omitted or malformed
// setting cannot silently turn the watchdog off.
func (b *Bridge) WithInWorkTimeout(d time.Duration) *Bridge {
	if b == nil {
		return nil
	}
	if d > 0 {
		b.inWorkTimeout = d
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

// WithLadder binds the wake ladder. Without one the bridge still stages and
// delivers obligations, but no ADR 0017 timers are armed and no delivery
// outcome is recorded for retries or escalation.
func (b *Bridge) WithLadder(l WakeLadder) *Bridge {
	if b == nil {
		return nil
	}
	b.ladder = l
	return b
}

// WithWaker binds a wake adapter. A non-terminal verdict then steers each
// targeted subscriber's session through the host harness. It accepts both the
// legacy WakeDeliverer and the adapter-aware AdapterWakeDeliverer, so a
// registry can be wired in without changing callers. Without a waker the
// verdict is still persisted.
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

// WithBatchCI binds the batch provider. When absent the Bridge type-asserts
// the CIProvider passed to New, so a provider that implements both interfaces
// needs no extra wiring.
func (b *Bridge) WithBatchCI(p domain.BatchCIProvider) *Bridge {
	if b == nil {
		return nil
	}
	b.batch = p
	return b
}

// WithMerger binds the auto-merge adapter. A Watch with MergePolicy.AutoMerge
// and no bound merger keeps the old behaviour: the ready verdict is published
// and the orchestrator is woken.
func (b *Bridge) WithMerger(m domain.Merger) *Bridge {
	if b == nil {
		return nil
	}
	b.merger = m
	return b
}

// WithAutoRebase enables the stale-base recovery on auto-merge. A Watch whose
// first merge is held because its branch is BEHIND is rebased once and the
// merge is retried once with the new head. A nil rebaser leaves the option off,
// so an unbound composition root keeps the old wake-the-executor behaviour.
func (b *Bridge) WithAutoRebase(r Rebaser) *Bridge {
	if b == nil {
		return nil
	}
	b.rebaser = r
	b.autoRebase = r != nil
	return b
}

// rebaseAttemptID derives the durable counter obligation of one Watch revision.
// The Watch id and revision are the identity of a rebase attempt; the reserved
// subscriber id keeps the counter apart from subscriber obligations.
func rebaseAttemptID(w domain.Watch) string {
	return domain.ObligationKey(w.ID, w.Revision, rebaseCounterSubscriber)
}

// rebaseBudget returns the effective budget of a Watch. Zero means the project
// set no override, so the ADR 0259 default applies.
func rebaseBudget(w domain.Watch) int {
	if w.Policy.MaxRebases > 0 {
		return w.Policy.MaxRebases
	}
	return defaultMaxRebases
}

// rebaseAttempts counts durable rebase attempts recorded for a Watch across its
// revisions. The store adapter exposes WatchObligations; a store without that
// port falls back to the in-memory guard and never blocks on a budget it cannot
// read.
func (b *Bridge) rebaseAttempts(watchID string) int {
	if b == nil || b.store == nil {
		return 0
	}
	src, ok := b.store.(activitySource)
	if !ok {
		return 0
	}
	obligations, err := src.WatchObligations(watchID)
	if err != nil {
		return 0
	}
	used := 0
	for _, o := range obligations {
		if o.SubscriberID == rebaseCounterSubscriber {
			used++
		}
	}
	return used
}

// rebaseBudgetExhausted reports whether a Watch has spent its rebase budget.
// The count is read from the durable obligations, so a restarted process sees
// the same value.
func (b *Bridge) rebaseBudgetExhausted(w domain.Watch) bool {
	return b.rebaseAttempts(w.ID) >= rebaseBudget(w)
}

// spendRebase charges one rebase attempt to the Watch before the caller
// performs the rebase. The write goes to the store first, so a crash after the
// write but before the rebase leaves the count intact. The first result says
// whether the attempt was recorded; the second says the budget is exhausted.
func (b *Bridge) spendRebase(w domain.Watch, now time.Time) (allowed, exhausted bool) {
	if b == nil || b.store == nil {
		return false, false
	}
	key := rebaseAttemptID(w)
	if _, err := b.store.Obligation(key); err == nil {
		// This revision already spent its rebase. Durable dedupe replaces the
		// in-memory guard after a restart.
		return false, false
	}
	if b.rebaseAttempts(w.ID) >= rebaseBudget(w) {
		return false, true
	}
	o := domain.Obligation{
		ID:             key,
		WatchID:        w.ID,
		Revision:       w.Revision,
		SubscriberID:   rebaseCounterSubscriber,
		VerdictPointer: domain.VerdictRebase,
		StagedAt:       now,
	}
	if err := b.store.SaveObligation(o); err != nil {
		observe.ActorError("cibridge-rebase-counter", w.ID, err)
		return false, false
	}
	return true, false
}

// claimRebase reserves one rebase for (watchID, revision). A second tick that
// observes the same revision never rebases twice, even when the first rebase
// or the retry merge failed and the Watch has not moved on yet. The reservation
// is durable and the Watch's MaxRebases budget bounds the attempts across its
// revisions. Older in-memory entries for the same Watch are dropped so the
// guard stays bounded per Watch.
func (b *Bridge) claimRebase(w domain.Watch) bool {
	key := fmt.Sprintf("%s:%d", w.ID, w.Revision)
	b.rebaseMu.Lock()
	if b.rebased == nil {
		b.rebased = make(map[string]bool)
	}
	seen := b.rebased[key]
	b.rebaseMu.Unlock()
	if seen {
		return false
	}

	allowed, _ := b.spendRebase(w, b.clock.Now())
	if !allowed {
		return false
	}

	b.rebaseMu.Lock()
	defer b.rebaseMu.Unlock()
	prefix := w.ID + ":"
	for k := range b.rebased {
		if strings.HasPrefix(k, prefix) {
			delete(b.rebased, k)
		}
	}
	b.rebased[key] = true
	return true
}

// blockRebaseExhausted parks a Watch that spent its rebase budget. The block
// joins a shared BlockerIncident on the base branch, so one broken main blocks
// every affected Watch instead of spending a rebase on each of them.
func (b *Bridge) blockRebaseExhausted(ctx context.Context, w domain.Watch, now time.Time, cycleID string) int {
	info := domain.BlockedInfo{
		Reason:       domain.BlockReasonRebaseBudgetExhausted,
		FailedChecks: []string{"rebase-budget-exhausted"},
		Since:        now,
		BlockedBy:    "cibridge",
	}
	next, _ := domain.Transition(w, domain.BlockEvent(info, now))
	if next.Status != domain.StatusBlocked || next.Blocked == nil {
		return 0
	}
	if err := b.store.SaveWatch(next); err != nil {
		observe.ActorError("cibridge-rebase-block", next.ID, err)
		return 0
	}
	if b.control != nil {
		incident, _, err := b.control.GetOrCreateOpenBlocker(next.Repo, baseRefOf(next), info.FailedChecks, next.ID, now)
		if err != nil {
			observe.ActorError("cibridge-rebase-blocker", next.ID, err)
		} else if next.BlockerID != incident.ID {
			next.BlockerID = incident.ID
			if err := b.store.SaveWatch(next); err != nil {
				observe.ActorError("cibridge-rebase-blocker-id", next.ID, err)
			}
		}
	}
	b.cycleEvent(cycleID, "rebase_budget_exhausted", cycleFields{
		watchID: next.ID, revision: next.Revision, verdict: domain.VerdictBlocked,
	}, observe.Fields{observe.FieldBlockerID: next.BlockerID})
	log.Printf("cibridge: %s#%d blocked, rebase budget exhausted", next.Repo, next.PR)
	return 1
}

// WithBlockerIncidents binds the incident lifecycle port. New also detects a
// store that implements it directly, so callers may omit this setter.
func (b *Bridge) WithBlockerIncidents(c domain.BlockerIncidents) *Bridge {
	if b == nil {
		return nil
	}
	b.control = c
	return b
}

// WithCardLinks binds the card-closing port. Without one a merged Watch still
// closes and resolves blockers, but no task card is marked done. An adapter
// that also implements CardUnlockSource publishes the cards it released.
func (b *Bridge) WithCardLinks(c CardLinks) *Bridge {
	if b == nil {
		return nil
	}
	b.cardLinks = wrapCardLinks(c)
	return b
}

// WithWorktreeGC binds the garbage-collection port. Without one a merged Watch
// still closes and resolves blockers, but no worktree is collected.
func (b *Bridge) WithWorktreeGC(fn WorktreeGC) *Bridge {
	if b == nil {
		return nil
	}
	b.gc = fn
	return b
}

// WithEscalationWriter replaces the ESCALATIONS.md appender. It is mainly a
// test seam; production leaves the file writer installed.
func (b *Bridge) WithEscalationWriter(fn escalationWriter) *Bridge {
	if b == nil {
		return nil
	}
	b.escalate = fn
	return b
}

// New builds a Bridge. An empty interval falls back to DefaultPollInterval;
// composition roots that read BEACON_CI_POLL_INTERVAL pass PollInterval().
func New(store stateStore, ci domain.CIProvider, clock domain.Clock, interval time.Duration) *Bridge {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	if clock == nil {
		clock = wallClock{}
	}
	b := &Bridge{
		store: store, ci: ci, clock: clock, interval: interval,
		inWorkTimeout: DefaultInWorkTimeout,
		rebased:       make(map[string]bool),
	}
	// Bind the responsibility parts to this coordinator. Each part embeds b,
	// so its methods see the same configuration the setters mutate.
	b.ciRefresh = &ciRefresh{Bridge: b}
	b.verdictApplier = &verdictApplier{Bridge: b}
	b.wakeDispatcher = &wakeDispatcher{Bridge: b}
	b.readAPI = &readAPI{Bridge: b}
	if p, ok := ci.(domain.BatchCIProvider); ok {
		b.batch = p
	}
	if m, ok := ci.(domain.Merger); ok {
		b.merger = m
	}
	if c, ok := store.(domain.BlockerIncidents); ok {
		b.control = c
	}
	if m, ok := store.(DeliveryMarker); ok {
		b.delivery = m
	}
	if c, ok := store.(CardLinks); ok {
		b.cardLinks = wrapCardLinks(c)
	}
	b.restoreDeferred()
	return b
}

// replayDeferredOnRefresh gives every adapter with parked wakes a chance to
// deliver them when the breaker permits. Replaying here bounds the outage: the
// queue no longer waits for a fresh verdict on the same Watch to drain.
func (b *Bridge) replayDeferredOnRefresh(ctx context.Context, now time.Time, cycleID string) {
	b.deferredMu.Lock()
	adapters := make([]string, 0, len(b.deferred))
	for adapter, queue := range b.deferred {
		if len(queue) > 0 {
			adapters = append(adapters, adapter)
		}
	}
	b.deferredMu.Unlock()
	for _, adapter := range adapters {
		b.replayDeferred(ctx, adapter, now, cycleID, "", false)
	}
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

// cycleFields carries the correlation identifiers of one Refresh. cycleEvent
// copies the known ones onto every event, so a single cycleID filter returns
// the whole chain from Watch observation to delivery receipt. The zero value
// means the field is not known at that point; no value is ever invented.
type cycleFields struct {
	watchID      string
	obligationID string
	revision     uint64
	role         domain.Role
	adapter      string
	verdict      domain.Verdict
	attempt      int
}

// cycleEvent logs one observe event with the cycle id and the correlation
// fields of the current Refresh. Fields set by the caller win over the
// inherited ones, so a narrower call site can refine a value. The map is fresh
// on every call; the caller's map is never mutated.
func (b *Bridge) cycleEvent(cycleID, event string, cf cycleFields, fields observe.Fields) {
	out := observe.Fields{}
	if cf.watchID != "" {
		out[observe.FieldWatchID] = cf.watchID
	}
	if cf.obligationID != "" {
		out[observe.FieldObligationID] = cf.obligationID
	}
	if cf.revision != 0 {
		out[observe.FieldRevision] = cf.revision
	}
	if cf.role != "" {
		out[observe.FieldRole] = string(cf.role)
	}
	if cf.adapter != "" {
		out[observe.FieldAdapter] = cf.adapter
	}
	if cf.verdict != "" {
		out[observe.FieldVerdict] = string(cf.verdict)
	}
	if cf.attempt > 0 {
		out[observe.FieldAttempt] = cf.attempt
	}
	for k, v := range fields {
		out[k] = v
	}
	out[fieldCycleID] = cycleID
	observe.Event(observe.LevelInfo, event, out)
}

// Refresh reads every live Watch once, derives the verdict with
// domain.EvaluatePR and applies the resulting action. Each call gets its own
// cycleID, which is attached to every observe event it emits.
func (b *Bridge) Refresh(ctx context.Context) int {
	if b == nil || b.store == nil || b.ci == nil {
		return 0
	}
	// Single-flight guard: drop a concurrent Refresh instead of running a
	// second batch of CI polls against the same watches.
	b.refreshMu.Lock()
	if b.refreshing {
		b.refreshMu.Unlock()
		return 0
	}
	b.refreshing = true
	b.refreshMu.Unlock()
	defer func() {
		b.refreshMu.Lock()
		b.refreshing = false
		b.refreshMu.Unlock()
	}()
	// One span covers the whole refresh, not one per Watch: fifteen Watches
	// produce a single beacon.ci.refresh span. The child context lets any
	// deeper wake hook nest under it (T3.394).
	ctx, end := b.spanTracer().Start(ctx, "beacon.ci.refresh")
	defer end()
	now := b.clock.Now()
	cycleID := newCycleID(now)
	start := time.Now()
	updated, skipped := 0, 0
	defer func() { observe.PollCycleEnd(updated, skipped, time.Since(start)) }()

	// Replay parked wakes before polling. The breaker may have recovered since
	// the last verdict, and no new verdict is needed to drain the queue.
	b.replayDeferredOnRefresh(ctx, now, cycleID)

	// A parked Watch is absent from the live read. Resume one only after every
	// subscriber saw its park notice, before polling so it re-enters this cycle.
	b.resumeParkedAfterNotify(now, cycleID)

	watches, err := b.liveWatches()
	if err != nil {
		observe.ActorError("cibridge", "", err)
		log.Printf("cibridge: live watches: %v", err)
		return 0
	}
	observe.PollCycleStart(len(watches))
	b.cycleEvent(cycleID, "cycle_start", cycleFields{}, observe.Fields{observe.FieldWatches: len(watches)})

	observations, err := b.fetchSnapshots(ctx, watches, cycleID)
	if err != nil {
		observe.ActorError("cibridge-batch", "", err)
		log.Printf("cibridge: batch: %v", err)
		return 0
	}

	for i := range watches {
		w := watches[i]
		if w.Status == domain.StatusClosed {
			skipped++
			continue
		}
		obs, ok := observations[w.ID]

		// A moved head invalidates the Watch even when it is blocked. Run this
		// before the blocked branch so a force-push closes the old Watch and
		// stages a successor instead of leaving the block in place.
		if ok {
			if head := strings.TrimSpace(obs.pr.HeadRefOid); head != "" && head != w.HeadSHA && !isTerminalState(obs.pr.State) {
				b.handleHeadChanged(w, head, now, cycleID)
				skipped++
				continue
			}
		}

		// A blocked Watch waits on outside action. Do not publish a retry for
		// it; only steer the orchestrator so it can assign a FIX agent.
		if w.Status == domain.StatusBlocked || w.Blocked != nil {
			observe.BlockDetected(w.ID, w.PR, "blocked watch waits on outside action")
			b.handleBlocked(ctx, w, cycleID)
			skipped++
			continue
		}
		if len(w.Subscribers) == 0 {
			skipped++
			continue
		}
		if !ok {
			skipped++
			continue
		}

		// A Watch that spent its rebase budget is parked before the verdict
		// engine can publish another rebase. The count is durable, so a
		// restarted process reaches the same decision. The block joins the
		// shared incident on the base branch.
		if b.rebaseBudgetExhausted(w) {
			updated += b.blockRebaseExhausted(ctx, w, now, cycleID)
			skipped++
			continue
		}

		// A quiet in_work subscriber is replaced, not woken again. The head
		// check above runs first, so a new head or commit closes this Watch
		// instead of counting as a stall.
		if b.escalateInWorkStall(ctx, w, now, cycleID) {
			skipped++
			continue
		}

		snap := snapshotFor(w, obs, now)
		verdict, targets := domain.EvaluatePR(snap, w.Policy)
		// One info line per live Watch per cycle. It carries the raw PR state
		// and the derived verdict, so a Watch that stays watching because the
		// provider returned an empty state is visible without a debugger.
		b.cycleEvent(cycleID, "observe", cycleFields{
			watchID: w.ID, revision: w.Revision, verdict: verdict,
		}, observe.Fields{
			observe.FieldPR: w.PR, "state": obs.pr.State,
			"headSHA": obs.pr.HeadRefOid, "mergeable": obs.pr.Mergeable,
		})

		// Charge the rebase before publishing its verdict. The write is durable,
		// so a restart cannot issue the same rebase twice. When the budget is
		// gone, park the Watch on the shared incident instead.
		if verdict == domain.VerdictRebase {
			allowed, exhausted := b.spendRebase(w, now)
			if exhausted {
				updated += b.blockRebaseExhausted(ctx, w, now, cycleID)
				skipped++
				continue
			}
			if !allowed {
				skipped++
				continue
			}
		}

		updated += b.applyVerdict(ctx, w, obs, verdict, targets, now, cycleID)
	}

	// A green base releases every blocker incident on that branch without
	// waiting for a Watch to merge. Run this after the loop: handleBlocked on a
	// blocked Watch would otherwise recreate an incident resolved earlier in
	// the same cycle. The guard is fail-closed, so an unknown base does nothing.
	for id, obs := range observations {
		if !obs.base.ChecksKnown || len(obs.base.FailedChecks) != 0 {
			continue
		}
		for i := range watches {
			if watches[i].ID == id {
				b.resolveBaseGreenBlockers(watches[i], obs, now, cycleID)
				break
			}
		}
	}

	b.cycleEvent(cycleID, "cycle_end", cycleFields{}, observe.Fields{
		observe.FieldUpdated: updated, observe.FieldSkipped: skipped,
	})
	return updated
}
