// Package cibridge observes CI for active Watches, asks the domain verdict
// engine what each observation means, persists the result and drives the
// follow-up actions (wake, merge, block, head change). It reads CI through
// domain.BatchCIProvider with a domain.CIProvider fallback, merges through
// domain.Merger and keeps blocker incidents in a narrow control-plane port so
// this package never imports the store adapter.
package cibridge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
	"sort"
	"strings"
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

// fieldCycleID is the structured field that ties every event of one Refresh
// to the same cycle. It is defined here because the observe package is shared
// and this is the only producer that generates cycle ids.
const fieldCycleID = "cycleID"

// escalationFile is the one-line-per-escalation journal. A Watch that hits its
// wake cap without green CI is parked and its reason is appended here once.
const escalationFile = "ESCALATIONS.md"

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

// OrchestratorIDSource returns the project-configured orchestrator session.
// The app layer owns the config file, so Bridge takes the value through this
// port and stays independent of it.
type OrchestratorIDSource func() string

// liveWatchSource is the narrower alternative to domain.Store.AllWatches. The
// store adapter excludes closed Watches from LiveWatches; a caller that does
// not implement it still works through AllWatches.
type liveWatchSource interface {
	LiveWatches() ([]domain.Watch, error)
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

// escalationWriter is the injection point for the ESCALATIONS.md append. Tests
// replace it so a parked Watch does not touch the working tree.
type escalationWriter func(line string)

// Bridge refreshes active Watches on a ticker. One Bridge owns one CI provider,
// one Store and the optional control-plane ports, so a refresh is a batched
// read followed by ordered writes.
type Bridge struct {
	store          domain.Store
	ci             domain.CIProvider
	batch          domain.BatchCIProvider
	merger         domain.Merger
	control        blockerControl
	clock          domain.Clock
	interval       time.Duration
	waker          domain.WakeDeliverer
	adapterWaker   domain.AdapterWakeDeliverer
	reader         ReadStamper
	escalate       escalationWriter
	orchestratorID OrchestratorIDSource
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
func New(store domain.Store, ci domain.CIProvider, clock domain.Clock, interval time.Duration) *Bridge {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	if clock == nil {
		clock = wallClock{}
	}
	b := &Bridge{store: store, ci: ci, clock: clock, interval: interval}
	if p, ok := ci.(domain.BatchCIProvider); ok {
		b.batch = p
	}
	if m, ok := ci.(domain.Merger); ok {
		b.merger = m
	}
	if c, ok := store.(blockerControl); ok {
		b.control = c
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

// cycleEvent logs one observe event with the cycle id of the current Refresh.
func (b *Bridge) cycleEvent(cycleID, event string, fields observe.Fields) {
	if fields == nil {
		fields = observe.Fields{}
	}
	fields[fieldCycleID] = cycleID
	observe.Event(observe.LevelInfo, event, fields)
}

// liveWatches reads the Watches still in the control loop. LiveWatches is
// preferred because it includes blocked Watches but excludes closed ones; the
// AllWatches fallback keeps legacy stores working.
func (b *Bridge) liveWatches() ([]domain.Watch, error) {
	if src, ok := b.store.(liveWatchSource); ok {
		return src.LiveWatches()
	}
	return b.store.AllWatches()
}

// ciObservation is one normalized CI fact: the PR plus its base branch. The
// batch provider supplies both; the single-PR fallback supplies the base ref
// only, which is enough for the common case where main is green.
type ciObservation struct {
	pr   domain.PRState
	base domain.BaseBranchState
}

// fetchSnapshots reads every live Watch once. A BatchCIProvider is used when
// available so one GraphQL document covers all PRs and their base branches.
// Without one, the legacy CIProvider is called per Watch.
func (b *Bridge) fetchSnapshots(ctx context.Context, watches []domain.Watch, cycleID string) (map[string]ciObservation, error) {
	out := make(map[string]ciObservation, len(watches))
	candidates := make([]domain.Watch, 0, len(watches))
	for _, w := range watches {
		if w.Status != domain.StatusClosed && w.Status != domain.StatusBlocked {
			candidates = append(candidates, w)
		}
	}
	if len(candidates) == 0 {
		return out, nil
	}

	if b.batch != nil {
		callStart := time.Now()
		snaps, err := b.batch.FetchBatch(ctx, candidates)
		observe.ProviderCall("batchci", "FetchBatch", "", err, time.Since(callStart))
		if err != nil {
			return nil, err
		}
		used, budget := b.batch.PointStats()
		for _, w := range candidates {
			if s, ok := snaps[w.ID]; ok {
				out[w.ID] = ciObservation{pr: s.PR, base: s.Base}
			}
		}
		b.cycleEvent(cycleID, "ci_points", observe.Fields{"used": used, "budget": budget})
		return out, nil
	}

	for _, w := range candidates {
		callStart := time.Now()
		state, err := b.ci.PRState(ctx, w.Repo, w.PR)
		observe.ProviderCall("ghci", "PRState", w.ID, err, time.Since(callStart))
		if err != nil {
			log.Printf("cibridge: %s#%d: %v", w.Repo, w.PR, err)
			continue
		}
		out[w.ID] = ciObservation{
			pr:   state,
			base: domain.BaseBranchState{BaseRef: baseRefOf(w)},
		}
	}
	return out, nil
}

// baseRefOf returns the Watch base branch with "main" as the legacy default.
func baseRefOf(w domain.Watch) string {
	if strings.TrimSpace(w.BaseRef) != "" {
		return w.BaseRef
	}
	return "main"
}

// Refresh reads every live Watch once, derives the verdict with
// domain.EvaluatePR and applies the resulting action. Each call gets its own
// cycleID, which is attached to every observe event it emits.
func (b *Bridge) Refresh(ctx context.Context) int {
	if b == nil || b.store == nil || b.ci == nil {
		return 0
	}
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
	b.cycleEvent(cycleID, "cycle_start", observe.Fields{"watches": len(watches)})

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

		snap := snapshotFor(w, obs, now)
		verdict, targets := domain.EvaluatePR(snap, w.Policy)
		updated += b.applyVerdict(ctx, w, obs, verdict, targets, now, cycleID)
	}

	b.cycleEvent(cycleID, "cycle_end", observe.Fields{"updated": updated, "skipped": skipped})
	return updated
}

// isTerminalState reports a GitHub PR state that needs no further CI cycle.
func isTerminalState(state string) bool {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "MERGED", "CLOSED":
		return true
	}
	return false
}

// checkFacts normalizes a check map into the Rollup and Failures facts the
// verdict engine reads. It never decides a verdict: EvaluatePR owns that.
func checkFacts(checks map[string]domain.Check) (string, []string) {
	if len(checks) == 0 {
		return "SUCCESS", nil
	}
	rollup := "SUCCESS"
	failures := make([]string, 0)
	for name, c := range checks {
		switch strings.ToUpper(strings.TrimSpace(c.Conclusion)) {
		case "SUCCESS", "SKIPPED", "NEUTRAL":
			continue
		case "FAILURE", "ERROR", "CANCELLED", "CANCELED", "TIMED_OUT", "STARTUP_FAILURE":
			rollup = "FAILURE"
			failures = append(failures, name)
		default:
			if rollup != "FAILURE" {
				rollup = "PENDING"
			}
		}
	}
	sort.Strings(failures)
	return rollup, failures
}

// realFailedNames lists the checks that actually failed, with SKIPPED and
// NEUTRAL excluded. It feeds the self-teaching wake text.
func realFailedNames(checks map[string]domain.Check) []string {
	_, failures := checkFacts(checks)
	return failures
}

// snapshotFor turns one normalized CI observation into the domain Snapshot
// consumed by EvaluatePR. Suite composition was validated by the batch
// provider, so SuitesValid is true; no other fact is invented here.
func snapshotFor(w domain.Watch, obs ciObservation, now time.Time) domain.Snapshot {
	rollup, failures := checkFacts(obs.pr.Checks)
	return domain.Snapshot{
		State:            obs.pr.State,
		IsDraft:          obs.pr.IsDraft,
		HeadSHA:          obs.pr.HeadRefOid,
		WatchHeadSHA:     w.HeadSHA,
		Rollup:           rollup,
		Mergeable:        obs.pr.Mergeable,
		MergeStateStatus: obs.pr.MergeStateStatus,
		ReviewDecision:   obs.pr.ReviewDecision,
		Checks:           obs.pr.Checks,
		Failures:         failures,
		Flaky:            w.Flaky,
		At:               now,
		DeadlineAt:       w.DeadlineAt,
		NoChecksSince:    w.NoChecksSince,
		SuitesValid:      true,
		BaseFailedChecks: obs.base.FailedChecks,
	}
}

// successorWatchID mirrors the domain fan-out alias so a head-change successor
// has the same id as a fresh watch_pr registration on the new SHA.
func successorWatchID(w domain.Watch, head string) string {
	return fmt.Sprintf("watch:%s:%d:%s", w.Repo, w.PR, head)
}

// handleHeadChanged closes w with domain.HeadChangedEvent and persists the
// successor described by EffectCreateWatch. Subscriber and OrchestratorID are
// carried over; wake routing stays on the successor because its obligations
// are staged there.
func (b *Bridge) handleHeadChanged(w domain.Watch, head string, now time.Time, cycleID string) {
	current, err := b.store.Watch(w.ID)
	if err != nil || current.Status == domain.StatusClosed || current.HeadSHA == head {
		return
	}
	closed, effects := domain.Transition(current, domain.HeadChangedEvent(head, now))
	if closed.Status != domain.StatusClosed || closed.CloseReason != string(domain.CloseReasonHeadChanged) {
		return
	}
	if err := b.store.SaveWatch(closed); err != nil {
		observe.ActorError("cibridge-head", closed.ID, err)
		return
	}

	var subs []domain.Subscriber
	for _, e := range effects {
		if e.Kind == domain.EffectCreateWatch {
			subs = e.Subscribers
			break
		}
	}
	newID := successorWatchID(current, head)
	if _, err := b.store.Watch(newID); err == nil {
		b.cycleEvent(cycleID, "head_successor_exists", observe.Fields{"watchID": newID})
		return
	}
	next := domain.Watch{
		ID:             newID,
		Repo:           current.Repo,
		PR:             current.PR,
		HeadSHA:        head,
		BaseRef:        current.BaseRef,
		Issue:          current.Issue,
		Status:         domain.StatusWatching,
		Verdict:        domain.VerdictWatching,
		Flaky:          current.Flaky,
		Policy:         current.Policy,
		MergePolicy:    current.MergePolicy,
		Subscribers:    subs,
		OrchestratorID: current.OrchestratorID,
		FixFor:         current.FixFor,
		CreatedAt:      now,
	}
	for _, sub := range subs {
		id := domain.ObligationKey(next.ID, next.Revision, sub.ID)
		if _, err := b.store.Obligation(id); err != nil {
			if err := b.store.SaveObligation(domain.Obligation{
				ID: id, WatchID: next.ID, Revision: next.Revision,
				SubscriberID: sub.ID, VerdictPointer: domain.VerdictWatching, StagedAt: now,
			}); err != nil {
				observe.ActorError("cibridge-head-obligation", next.ID, err)
				return
			}
		}
	}
	if err := b.store.SaveWatch(next); err != nil {
		observe.ActorError("cibridge-head-save", next.ID, err)
		return
	}
	b.cycleEvent(cycleID, "head_changed", observe.Fields{
		"watchID": current.ID, "newWatchID": newID, "headSHA": head,
	})
	log.Printf("cibridge: %s head %s -> %s (closeReason=%s)", current.ID, current.HeadSHA, head, domain.CloseReasonHeadChanged)
}

// applyVerdict routes one EvaluatePR result to the right follow-up action.
func (b *Bridge) applyVerdict(ctx context.Context, w domain.Watch, obs ciObservation, verdict domain.Verdict, targets []domain.WakeTarget, now time.Time, cycleID string) int {
	current, err := b.store.Watch(w.ID)
	if err != nil || current.Status == domain.StatusClosed || current.Status == domain.StatusParked || current.Status == domain.StatusBlocked {
		return 0
	}
	// Re-read under the single-writer assumption: a head change between the
	// scan and here makes this observation stale.
	if current.HeadSHA != w.HeadSHA {
		return 0
	}
	current.Checks = obs.pr.Checks
	current.Merge = domain.MergeInfo{
		Mergeable:        obs.pr.Mergeable,
		MergeStateStatus: obs.pr.MergeStateStatus,
		ReviewDecision:   obs.pr.ReviewDecision,
	}

	switch verdict {
	case domain.VerdictWatching:
		return 0
	case domain.VerdictBlocked:
		return b.applyBlocked(ctx, current, obs, now, cycleID)
	case domain.VerdictReady:
		if current.MergePolicy.AutoMerge && b.merger != nil {
			return b.applyMerge(ctx, current, obs, now, cycleID)
		}
		return b.publishActionable(ctx, current, obs, verdict, targets, now, cycleID, "")
	case domain.VerdictMerged:
		return b.applyMerged(ctx, current, obs, now, cycleID)
	case domain.VerdictClosed:
		return b.closeWatch(ctx, current, verdict, now, cycleID)
	default:
		return b.publishActionable(ctx, current, obs, verdict, targets, now, cycleID, "")
	}
}

// publishActionable saves a non-terminal verdict and wakes the subscriber
// roles named by EvaluatePR. The wake text is role- and verdict-specific so
// the receiver sees a concrete instruction instead of a bare verdict word.
func (b *Bridge) publishActionable(ctx context.Context, w domain.Watch, obs ciObservation, verdict domain.Verdict, targets []domain.WakeTarget, now time.Time, cycleID, mergeErr string) int {
	next, _ := domain.Transition(w, domain.VerdictEvent(verdict, now))
	if next.Revision == w.Revision {
		return 0
	}
	if err := b.store.SaveWatch(next); err != nil {
		observe.ActorError("cibridge-publish", next.ID, err)
		return 0
	}

	roles := rolesFromTargets(targets)
	if mergeErr != "" {
		roles[string(domain.RoleExecutor)] = true
		roles[string(domain.RoleFixer)] = true
	}
	text := wakeText(verdict, obs, targets, mergeErr)
	for _, sub := range next.Subscribers {
		if !roles[string(sub.Role)] {
			continue
		}
		id := domain.ObligationKey(next.ID, next.Revision, sub.ID)
		if _, err := b.store.Obligation(id); err != nil {
			o := domain.Obligation{
				ID: id, WatchID: next.ID, Revision: next.Revision,
				SubscriberID: sub.ID, VerdictPointer: verdict, StagedAt: now,
			}
			if err := b.store.SaveObligation(o); err != nil {
				observe.ActorError("cibridge-obligation", next.ID, err)
				continue
			}
		}
		b.wakeOne(ctx, next, sub, text, now, cycleID, true)
	}
	observe.VerdictPublished(string(verdict), next.ID, next.PR, next.Revision)
	b.cycleEvent(cycleID, "verdict", observe.Fields{
		"watchID": next.ID, "verdict": string(verdict), "revision": next.Revision,
	})
	log.Printf("cibridge: %s#%d -> %s revision=%d", next.Repo, next.PR, verdict, next.Revision)
	return 1
}

// rolesFromTargets collects the subscriber roles EvaluatePR asked to wake.
// A rest_pin target has no role and therefore wakes nobody.
func rolesFromTargets(targets []domain.WakeTarget) map[string]bool {
	out := make(map[string]bool, len(targets))
	for _, t := range targets {
		if t.Role == "" || t.Kind == domain.WakeRestPin || t.Kind == domain.WakeHeadChanged {
			continue
		}
		out[string(t.Role)] = true
	}
	return out
}

// applyMerge runs the auto-merge step for a ready Watch. A confirmed merge
// closes the Watch with closeReason=merged and finishes every subscriber
// without waking anyone. A hold or error becomes a rebase (conflict/behind)
// or keep_working verdict with the merge error saved on the subscribers.
func (b *Bridge) applyMerge(ctx context.Context, w domain.Watch, obs ciObservation, now time.Time, cycleID string) int {
	observe.MergeAttempt(w.ID, w.PR)
	start := time.Now()
	merged, held, reason, err := b.merger.Merge(ctx, w.Repo, w.PR)
	observe.MergeDuration(time.Since(start))
	if merged {
		observe.MergeSuccess(w.ID, w.PR)
		n := b.closeWatch(ctx, w, domain.VerdictMerged, now, cycleID)
		b.cycleEvent(cycleID, "auto_merge_success", observe.Fields{"watchID": w.ID, "pr": w.PR})
		return n
	}
	observe.MergeFailure(w.ID, w.PR, err)
	mergeErr := strings.TrimSpace(reason)
	if mergeErr == "" && held {
		mergeErr = "merge held by policy"
	}
	if mergeErr == "" {
		mergeErr = errText(err)
	}
	verdict := domain.VerdictKeepWorking
	if isMergeConflict(reason, err) {
		verdict = domain.VerdictRebase
	}
	stored := w
	for i := range stored.Subscribers {
		stored.Subscribers[i].LastError = mergeErr
	}
	targets := []domain.WakeTarget{{Role: domain.RoleExecutor}, {Role: domain.RoleFixer}}
	if verdict == domain.VerdictRebase {
		observe.RebaseRequested(w.ID, w.PR)
	}
	return b.publishActionable(ctx, stored, obs, verdict, targets, now, cycleID, mergeErr)
}

// applyMerged handles a PR GitHub reports as MERGED. It closes the Watch and
// resolves every blocker incident attached to it or to its base branch, which
// unblocks all related Watches in one store transaction.
func (b *Bridge) applyMerged(ctx context.Context, w domain.Watch, obs ciObservation, now time.Time, cycleID string) int {
	n := b.closeWatch(ctx, w, domain.VerdictMerged, now, cycleID)
	if b.control != nil {
		for _, id := range []string{w.BlockerID, w.FixBlockerID} {
			if id == "" {
				continue
			}
			if _, err := b.control.ResolveBlocker(id, now); err != nil {
				observe.ActorError("cibridge-resolve", w.ID, err)
			}
		}
		if _, err := b.control.ResolveBlockersByBaseGreen(w.Repo, baseRefOf(w), obs.base.FailedChecks, now); err != nil {
			observe.ActorError("cibridge-base-green", w.ID, err)
		}
	} else {
		b.unblockFixed(w)
	}
	// A fixer Watch carries its own fixFor link. Resolve it in addition to the
	// incident path so a merged fixer always releases the Watch it repaired.
	if b.watchFixFor(w) != "" {
		b.unblockFixed(w)
	}
	b.cycleEvent(cycleID, "merged_resolve", observe.Fields{
		"watchID": w.ID, "blockerID": w.BlockerID, "fixBlockerID": w.FixBlockerID,
	})
	return n
}

// closeWatch applies a terminal verdict and marks each subscriber finished.
// It never wakes anyone: a closed Watch has no further work for a subscriber.
func (b *Bridge) closeWatch(ctx context.Context, w domain.Watch, verdict domain.Verdict, now time.Time, cycleID string) int {
	next, _ := domain.Transition(w, domain.VerdictEvent(verdict, now))
	if next.Revision == w.Revision {
		return 0
	}
	if verdict == domain.VerdictMerged {
		next.CloseReason = string(domain.CloseReasonMerged)
	}
	for i := range next.Subscribers {
		next.Subscribers[i].Phase = domain.WorkerPhaseFinished
	}
	if err := b.store.SaveWatch(next); err != nil {
		observe.ActorError("cibridge-close", next.ID, err)
		return 0
	}
	// The terminal verdict is still readable at the new revision, so get_result
	// can confirm the close without a wake.
	for i := range next.Subscribers {
		sub := next.Subscribers[i]
		id := domain.ObligationKey(next.ID, next.Revision, sub.ID)
		if _, err := b.store.Obligation(id); err != nil {
			if err := b.store.SaveObligation(domain.Obligation{
				ID: id, WatchID: next.ID, Revision: next.Revision,
				SubscriberID: sub.ID, VerdictPointer: verdict, StagedAt: now,
			}); err != nil {
				observe.ActorError("cibridge-close-obligation", next.ID, err)
			}
		}
	}
	observe.VerdictPublished(string(verdict), next.ID, next.PR, next.Revision)
	b.cycleEvent(cycleID, "watch_closed", observe.Fields{
		"watchID": next.ID, "verdict": string(verdict), "revision": next.Revision,
	})
	return 1
}

// applyBlocked records a failure already present on the base branch. It moves
// the Watch to StatusBlocked/VerdictBlocked through BlockEvent, folds the
// failed checks into a durable BlockerIncident and steers exactly one
// orchestrator session once per incident.
func (b *Bridge) applyBlocked(ctx context.Context, w domain.Watch, obs ciObservation, now time.Time, cycleID string) int {
	base := baseRefOf(w)
	failed := realFailedNames(obs.pr.Checks)
	info := domain.BlockedInfo{
		Reason:       "failed checks already present on base branch",
		FailedChecks: failed,
		Since:        now,
		BlockedBy:    "cibridge",
	}
	next, _ := domain.Transition(w, domain.BlockEvent(info, now))
	if next.Status != domain.StatusBlocked || next.Blocked == nil {
		return 0
	}
	if err := b.store.SaveWatch(next); err != nil {
		observe.ActorError("cibridge-block", next.ID, err)
		return 0
	}
	dedup := domain.ObligationKey(next.ID, next.Revision, "blocked")
	if b.control != nil {
		incident, _, err := b.control.GetOrCreateOpenBlocker(next.Repo, base, failed, next.ID, now)
		if err != nil {
			observe.ActorError("cibridge-blocker", next.ID, err)
		} else {
			dedup = domain.ObligationKey("blocker:"+incident.ID, 0, "wake")
			if next.BlockerID != incident.ID {
				next.BlockerID = incident.ID
				if err := b.store.SaveWatch(next); err != nil {
					observe.ActorError("cibridge-blocker-id", next.ID, err)
				}
			}
		}
	}
	if b.notifyBlocked(ctx, next, dedup, now, cycleID) {
		observe.VerdictPublished(string(domain.VerdictBlocked), next.ID, next.PR, next.Revision)
	}
	b.cycleEvent(cycleID, "block", observe.Fields{
		"watchID": next.ID, "blockerID": next.BlockerID, "failedChecks": failed,
	})
	return 1
}

// blockedNotice builds the orchestrator's wake text for a blocked Watch. It
// carries the PR, reason, failed checks and watch id and ends with the explicit
// stop-and-wait instruction.
func blockedNotice(w domain.Watch) string {
	info := w.Blocked
	checks := strings.Join(info.FailedChecks, ", ")
	if checks == "" {
		checks = "нет"
	}
	return fmt.Sprintf("нужен FIX-агент: PR %s#%d, причина: %s, failedChecks: %s, watchID: %s; остановись и жди разблокировки",
		w.Repo, w.PR, info.Reason, checks, w.ID)
}

// blockedDefaultAdapter is used when the resolved orchestrator address is not
// a subscriber on the Watch, so no subscriber record carries an adapter.
const blockedDefaultAdapter = "opencode"

// blockedOrchestrator resolves the one session allowed to receive a blocked
// notice: the orchestrator subscriber first, then the Watch's persisted
// OrchestratorID, then the configured fallback. A candidate that also names an
// executor subscriber is skipped.
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
// is a subscriber of w. Other sources fall back to the default adapter.
func (b *Bridge) blockedAdapter(w domain.Watch, sessionID string) string {
	for _, sub := range w.Subscribers {
		if sub.ID == sessionID && sub.Adapter != "" {
			return sub.Adapter
		}
	}
	return blockedDefaultAdapter
}

// notifyBlocked stages the subscriber obligations at the block revision and
// steers one orchestrator session. Delivery is remembered in the obligation
// identified by dedup, never in an in-memory map, so a process restart cannot
// steer the same incident twice and a new incident gets a fresh key.
func (b *Bridge) notifyBlocked(ctx context.Context, w domain.Watch, dedup string, now time.Time, cycleID string) bool {
	for _, sub := range w.Subscribers {
		id := domain.ObligationKey(w.ID, w.Revision, sub.ID)
		if _, err := b.store.Obligation(id); err != nil {
			if err := b.store.SaveObligation(domain.Obligation{
				ID: id, WatchID: w.ID, Revision: w.Revision,
				SubscriberID: sub.ID, VerdictPointer: domain.VerdictBlocked, StagedAt: now,
			}); err != nil {
				log.Printf("cibridge: blocked obligation %s: %v", id, err)
				return false
			}
		}
	}
	sessionID := b.blockedOrchestrator(w)
	if sessionID == "" {
		observe.WakeSkipped("", w.ID, "blocked Watch has no orchestrator address")
		return false
	}
	if b.waker == nil && b.adapterWaker == nil {
		return false
	}
	if delivered, err := b.store.Obligation(dedup); err == nil && !delivered.DeliveredAt.IsZero() {
		observe.WakeSkipped("", w.ID, "block notice already delivered for this incident")
		return false
	} else if err != nil {
		if err := b.store.SaveObligation(domain.Obligation{
			ID: dedup, WatchID: w.ID, Revision: w.Revision,
			SubscriberID: "wake", VerdictPointer: domain.VerdictBlocked, StagedAt: now,
		}); err != nil {
			log.Printf("cibridge: block dedup obligation %s: %v", dedup, err)
			return false
		}
	}

	obligationID := domain.ObligationKey(w.ID, w.Revision, sessionID)
	if _, err := b.store.Obligation(obligationID); err != nil {
		if err := b.store.SaveObligation(domain.Obligation{
			ID: obligationID, WatchID: w.ID, Revision: w.Revision,
			SubscriberID: sessionID, VerdictPointer: domain.VerdictBlocked, StagedAt: now,
		}); err != nil {
			log.Printf("cibridge: blocked obligation %s: %v", obligationID, err)
			return false
		}
	}
	p := domain.WakePayload{
		WatchID: w.ID, ObligationID: obligationID,
		Revision: w.Revision, Verdict: blockedNotice(w),
	}
	adapter := b.blockedAdapter(w, sessionID)
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
		return false
	}
	observe.WakeDelivered(adapter, w.ID, w.Revision)
	b.markDelivered(dedup, now, 1, "")
	b.markDelivered(obligationID, now, 1, "")
	b.cycleEvent(cycleID, "block_escalation", observe.Fields{
		"watchID": w.ID, "sessionID": sessionID, "incident": dedup,
	})
	return true
}

// handleBlocked observes an already blocked Watch. It re-reads it under the
// single-writer assumption, creates the incident when the control plane is
// bound and stages a blocked obligation for every subscriber before steering
// the orchestrator once.
func (b *Bridge) handleBlocked(ctx context.Context, w domain.Watch, cycleID string) {
	if w.Blocked == nil {
		return
	}
	now := b.clock.Now()
	current, err := b.store.Watch(w.ID)
	if err != nil || current.Status == domain.StatusClosed || current.Blocked == nil {
		return
	}
	w = current

	dedup := domain.ObligationKey(w.ID, w.Revision, "blocked")
	if b.control != nil {
		incident, _, err := b.control.GetOrCreateOpenBlocker(w.Repo, baseRefOf(w), w.Blocked.FailedChecks, w.ID, now)
		if err != nil {
			observe.ActorError("cibridge-blocker", w.ID, err)
		} else {
			dedup = domain.ObligationKey("blocker:"+incident.ID, 0, "wake")
			if w.BlockerID != incident.ID {
				w.BlockerID = incident.ID
				if err := b.store.SaveWatch(w); err != nil {
					observe.ActorError("cibridge-blocker-id", w.ID, err)
				}
			}
		}
	}
	b.notifyBlocked(ctx, w, dedup, now, cycleID)
}

// markDelivered persists a delivery on the obligation itself. The store
// adapter exposes MarkObligationDelivered; a legacy or test store is updated
// through SaveObligation. Either way the fact survives a process restart.
func (b *Bridge) markDelivered(id string, at time.Time, attempt int, lastErr string) {
	if b.control != nil {
		if err := b.control.MarkObligationDelivered(id, at, attempt, lastErr); err == nil {
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

// wakeCounterID is the durable per-Watch wake counter. It lives in the
// obligations table, so a process restart cannot reset the circuit breaker.
func wakeCounterID(w domain.Watch) string {
	return domain.ObligationKey("wakes:"+w.ID, 0, "count")
}

// wakeCount returns the number of wake attempts already recorded for a Watch.
func (b *Bridge) wakeCount(w domain.Watch) int {
	o, err := b.store.Obligation(wakeCounterID(w))
	if err != nil {
		return 0
	}
	return len(o.Attempts)
}

// recordWake increments the durable counter after a successful delivery.
func (b *Bridge) recordWake(w domain.Watch, now time.Time, attempt int) {
	id := wakeCounterID(w)
	if _, err := b.store.Obligation(id); err != nil {
		if err := b.store.SaveObligation(domain.Obligation{
			ID: id, WatchID: w.ID, Revision: 0,
			SubscriberID: "count", VerdictPointer: domain.VerdictWatching, StagedAt: now,
		}); err != nil {
			observe.ActorError("cibridge-wake-counter", w.ID, err)
			return
		}
	}
	b.markDelivered(id, now, attempt, "")
}

// wakeOne validates the address, applies the wake cap, delivers the pointer
// and records the attempt. A dead-session error marks the subscriber orphaned
// and escalates to the orchestrator once; other errors are left pending so a
// later tick can retry.
func (b *Bridge) wakeOne(ctx context.Context, w domain.Watch, sub domain.Subscriber, text string, now time.Time, cycleID string, breaker bool) {
	if b.waker == nil && b.adapterWaker == nil {
		return
	}
	target, err := wakeAddressee(w, w.ID, sub.ID)
	if err != nil || target != sub.ID {
		observe.WakeSkipped(sub.Adapter, w.ID, "obligation target mismatch")
		log.Printf("cibridge: skip wake for watch %s: subscriber %q (%v)", w.ID, sub.ID, err)
		return
	}
	if breaker && b.wakeCount(w) >= wakeCap(w) {
		b.parkForWakeCap(ctx, w, cycleID)
		return
	}
	p := domain.WakePayload{
		WatchID:      w.ID,
		ObligationID: domain.ObligationKey(w.ID, w.Revision, sub.ID),
		Revision:     w.Revision,
		Verdict:      text,
	}
	start := time.Now()
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
		observe.WakeFailed(sub.Adapter, w.ID, deliverErr, w.Revision)
		observe.ActorError("wake", w.ID, deliverErr)
		log.Printf("cibridge: wake %s: %v", target, deliverErr)
		if isDeadSessionErr(deliverErr) {
			b.markOrphaned(ctx, w, sub, now, cycleID)
		}
		return
	}
	observe.WakeDuration(sub.Adapter, time.Since(start))
	observe.WakeDelivered(sub.Adapter, w.ID, w.Revision)
	attempt := b.wakeCount(w) + 1
	b.recordWake(w, now, attempt)
	b.markDelivered(p.ObligationID, now, attempt, "")
	b.cycleEvent(cycleID, "wake", observe.Fields{
		"watchID": w.ID, "sessionID": target, "attempt": attempt,
	})
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
// green CI verdict and appends one line to ESCALATIONS.md. From then on the
// Watch is parked and no further wake is delivered.
func (b *Bridge) parkForWakeCap(ctx context.Context, w domain.Watch, cycleID string) {
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
	line := fmt.Sprintf("%s: watch %s parked after %d wakes without green CI",
		now.UTC().Format(time.RFC3339), w.ID, wakeCap(w))
	b.appendEscalation(line)
	for _, e := range effects {
		b.cycleEvent(cycleID, "park", observe.Fields{
			"watchID": parked.ID, "reason": string(e.ParkReason), "error": e.ParkError,
		})
	}
	log.Printf("cibridge: %s parked: %s", parked.ID, line)
}

// appendEscalation writes one deduplicated line to ESCALATIONS.md. The file is
// created on first use; an already recorded reason is never written twice.
func (b *Bridge) appendEscalation(line string) {
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
func (b *Bridge) markOrphaned(ctx context.Context, w domain.Watch, sub domain.Subscriber, now time.Time, cycleID string) {
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
	sessionID := b.blockedOrchestrator(current)
	if sessionID == "" {
		return
	}
	id := domain.ObligationKey("orphan:"+current.ID, 0, sessionID)
	if o, err := b.store.Obligation(id); err == nil && !o.DeliveredAt.IsZero() {
		return
	} else if err != nil {
		if err := b.store.SaveObligation(domain.Obligation{
			ID: id, WatchID: current.ID, Revision: current.Revision,
			SubscriberID: sessionID, VerdictPointer: domain.VerdictBlocked, StagedAt: now,
		}); err != nil {
			return
		}
	}
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
		observe.WakeFailed("", current.ID, deliverErr, current.Revision)
		return
	}
	b.markDelivered(id, now, 1, "")
	b.cycleEvent(cycleID, "executor_orphaned", observe.Fields{
		"watchID": current.ID, "subscriberID": sub.ID, "sessionID": sessionID,
	})
}

// wakeText builds the self-teaching instruction carried in a wake pointer.
// Each verdict names the concrete next action so a receiver cannot answer with
// an empty rebase or a blind re-run.
func wakeText(verdict domain.Verdict, obs ciObservation, targets []domain.WakeTarget, mergeErr string) string {
	failed := realFailedNames(obs.pr.Checks)
	switch verdict {
	case domain.VerdictKeepWorking:
		if len(failed) > 0 {
			return "keep_working: чини конкретные failedChecks: " + strings.Join(failed, ", ") + "; не делай пустой ребейз"
		}
		if mergeErr != "" {
			return "keep_working: ошибка мержа: " + mergeErr + "; не делай пустой ребейз"
		}
		return "keep_working: чини конкретные failedChecks, не делай пустой ребейз"
	case domain.VerdictRebase:
		return "rebase: git fetch origin main && git rebase origin/main && git push --force-with-lease"
	case domain.VerdictRetry:
		checks := make([]string, 0)
		for _, t := range targets {
			checks = append(checks, t.RerunChecks...)
		}
		if len(checks) == 0 {
			checks = failed
		}
		if len(checks) > 0 {
			return "retry: gh run rerun --failed (" + strings.Join(checks, ", ") + ")"
		}
		return "retry: gh run rerun --failed"
	case domain.VerdictBlocked, domain.VerdictTimeout:
		return "остановись и жди разблокировки"
	default:
		return string(verdict)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// isMergeConflict reports a merge hold or error that a rebase can fix.
func isMergeConflict(reason string, err error) bool {
	s := strings.ToLower(reason + " " + errText(err))
	for _, key := range []string{"conflict", "behind", "dirty", "409", "422"} {
		if strings.Contains(s, key) {
			return true
		}
	}
	return false
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

// fixForSource is an optional capability a Store may implement to expose the
// fixer link of a Watch. It keeps the legacy unblock path working until every
// caller persists FixFor on the Watch itself.
type fixForSource interface {
	FixForOf(watchID string) string
}

// watchFixFor returns the id of the Watch a fixer Watch is assigned to clear.
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

// unblockFixed resumes the Watch a merged fixer Watch points at. It is the
// fallback used when no control-plane port is bound.
func (b *Bridge) unblockFixed(merged domain.Watch) {
	fixer := merged
	if b.watchFixFor(fixer) == "" {
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

// WatchResult is the orchestrator's own Watch as read back from the store.
type WatchResult struct {
	WatchID      string
	Verdict      domain.Verdict
	Revision     uint64
	ObligationID string
	ReadAt       time.Time
}

// RegisterSelfWatch creates the orchestrator's Watch on its own PR. The card id
// is the subscriber: it is the same target beacon_watch_pr receives as
// notify.target. Registration is idempotent by watch id and never resets a
// verdict.
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
// the store to confirm.
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

// ChainFacts is the external evidence of one full run.
type ChainFacts struct {
	WatchID      string
	Revision     uint64
	Verdict      domain.Verdict
	ObligationID string
	WakeTarget   string
	ReadAt       time.Time
}

// ConfirmRead verifies the read side of the chain against the store.
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
