// Package cibridge observes CI for active Watches, asks the domain verdict
// engine what each observation means, persists the result and drives the
// follow-up actions (wake, merge, block, head change). It reads CI through
// domain.BatchCIProvider with a domain.CIProvider fallback, merges through
// domain.Merger and keeps blocker incidents in a narrow control-plane port so
// this package never imports the store adapter.
package cibridge

import (
	"context"
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

// blockerControl is the control-plane subset of the store adapter. It is a
// domain-shaped port declared here so cibridge does not import internal/infra/store.
// A store that does not implement it still gets the legacy fixFor unblock path.
type blockerControl interface {
	GetOrCreateOpenBlocker(repo, baseRef string, failedChecks []string, watchID string, now time.Time) (domain.BlockerIncident, bool, error)
	ResolveBlocker(blockerID string, now time.Time) ([]domain.Watch, error)
	ResolveBlockersByBaseGreen(repo, baseRef string, baseFailed map[string]bool, now time.Time) ([]domain.BlockerIncident, error)
	ActiveBlockers() ([]domain.BlockerIncident, error)
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
	control        blockerControl
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

// WithTracer binds the tracer used for the per-refresh span. It is optional:
// a nil tracer falls back to observe.Current, so a Bridge built without it
// keeps the process tracer and its no-op default (T3.394).
func (b *Bridge) WithTracer(t observe.Tracer) *Bridge {
	if b != nil {
		b.tracer = t
	}
	return b
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

// WithAdapterWaker binds an adapter-aware wake deliverer explicitly.
func (b *Bridge) WithAdapterWaker(w domain.AdapterWakeDeliverer) *Bridge {
	if b == nil {
		return nil
	}
	b.adapterWaker = w
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

// claimRebase reserves one rebase for (watchID, revision). A second tick that
// observes the same revision never rebases twice, even when the first rebase
// or the retry merge failed and the Watch has not moved on yet. Older entries
// for the same Watch are dropped so the guard stays bounded per Watch.
func (b *Bridge) claimRebase(w domain.Watch) bool {
	key := fmt.Sprintf("%s:%d", w.ID, w.Revision)
	b.rebaseMu.Lock()
	defer b.rebaseMu.Unlock()
	if b.rebased == nil {
		b.rebased = make(map[string]bool)
	}
	if b.rebased[key] {
		return false
	}
	prefix := w.ID + ":"
	for k := range b.rebased {
		if strings.HasPrefix(k, prefix) {
			delete(b.rebased, k)
		}
	}
	b.rebased[key] = true
	return true
}

// WithBlockerControl binds the control-plane port. Wave 3 wires the real
// store; until then a store that implements the methods directly is detected
// by type assertion in New.
func (b *Bridge) WithBlockerControl(c blockerControl) *Bridge {
	if b == nil {
		return nil
	}
	b.control = c
	return b
}

// WithCardLinks binds the card-closing port. Without one a merged Watch still
// closes and resolves blockers, but no task card is marked done.
func (b *Bridge) WithCardLinks(c CardLinks) *Bridge {
	if b == nil {
		return nil
	}
	b.cardLinks = c
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
	if c, ok := store.(blockerControl); ok {
		b.control = c
	}
	if c, ok := store.(CardLinks); ok {
		b.cardLinks = c
	}
	return b
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
	cycleID := newCycleID(b.clock.Now())
	start := time.Now()
	updated, skipped := 0, 0
	defer func() { observe.PollCycleEnd(updated, skipped, time.Since(start)) }()

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
	now := b.clock.Now()

	for i := range watches {
		w := watches[i]
		if w.Status == domain.StatusClosed {
			skipped++
			continue
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
		obs, ok := observations[w.ID]
		if !ok {
			skipped++
			continue
		}

		// A moved head invalidates the Watch. Apply the domain event so the
		// old Watch closes with closeReason=head_changed and a successor with
		// the same subscribers and OrchestratorID is created for the new SHA.
		if head := strings.TrimSpace(obs.pr.HeadRefOid); head != "" && head != w.HeadSHA && !isTerminalState(obs.pr.State) {
			b.handleHeadChanged(w, head, now, cycleID)
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
		updated += b.applyVerdict(ctx, w, obs, verdict, targets, now, cycleID)
	}

	b.cycleEvent(cycleID, "cycle_end", cycleFields{}, observe.Fields{
		observe.FieldUpdated: updated, observe.FieldSkipped: skipped,
	})
	return updated
}
