// Package orchestrator implements the background daemon that drives one
// automatic pass over Beacon Watches: trigger due schedules, check CI, enroll
// the orchestrator on active Watches, merge what is ready, print the workload
// summary and close Watches whose PR is finished. The loop owns no adapters.
// The composition root supplies the actions as narrow ports so tests can
// replace each one with a fake.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	// ARDH-15: observe is the documented cross-cutting exception. Metrics are
	// emitted at the decision sites; a port through every call would add noise
	// without moving an adapter.
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// DaemonInterval is the delay between passes when neither the flag nor the
// environment asks for a different value.
const DaemonInterval = 3 * time.Minute

// deliveredKeyRetention is the replay window for obligations of Watches that
// are no longer active. It mirrors store.ObligationGCRetention (24h). The app
// layer keeps its own duration so it does not import a storage package for one
// constant.
const deliveredKeyRetention = 24 * time.Hour

// DaemonDueSchedule is one schedule row the daemon can trigger. The adapter
// that implements DaemonSchedules maps a stored row onto these fields, so the
// pass sees no spec string and no SQL handle.
type DaemonDueSchedule struct {
	ID       string
	NextRun  time.Time
	Interval time.Duration
	// NextCron is supplied by the adapter for cron rows. It returns the next
	// firing strictly after now in the schedule's timezone. When set, it wins
	// over interval arithmetic: a fixed wall-clock time is kept and missed
	// intervals collapse into one future run.
	NextCron func(now time.Time) (time.Time, error)
}

// DaemonUnit is one ordered line of the per-unit report. Status is the outcome
// kind the night-shift worker recorded. Reason explains a failed unit and
// Action tells the caller what to do with a unit that was not started. Priority
// marks a kernel recovery unit.
type DaemonUnit struct {
	ID       string
	Status   string
	Priority bool
	Reason   string
	Action   string
}

// DaemonSchedules is the schedule port of one daemon pass. ListDue returns the
// rows whose next_run is at or before now. MarkTriggered moves a row's
// next_run forward so one period is triggered once, including after a restart.
type DaemonSchedules interface {
	ListDue(ctx context.Context, now time.Time) ([]DaemonDueSchedule, error)
	MarkTriggered(ctx context.Context, id string, nextRun time.Time) error
}

// DaemonPorts are the actions of one daemon pass.
//
// CheckAll lists open PRs, searches for an existing Watch for each one before
// registering a new one, and publishes the verdict. This is the daemon's
// "search before create" step: an open PR must reuse its Watch instead of
// producing a duplicate. Subscribe enrolls the orchestrator on every active
// Watch before the orchestrator pass. Schedules lists due schedules and marks
// each trigger once before orchestration. Orchestrate merges ready Watches and
// hands retry/fail Watches to the Waker. Summary returns the workload snapshot
// the pass prints after orchestration. Verify reports whether the conditions
// required to close a card are met. CloseMerged closes Watches whose PR is
// already merged or closed, but only after Verify passes. CheckAll, Orchestrate
// and CloseMerged are required. Subscribe, Schedules, Summary and Verify are
// optional: a nil port skips that step, and a nil Verify keeps the original
// close behaviour for older callers. UnitReport is optional: a nil port omits
// the per-unit lines from the pass log. A nil Waker is allowed and simply
// suppresses wakes. Tracer is optional: a nil tracer uses the process tracer
// from observe.Current, so a caller that wires nothing keeps the no-op default
// (T3.394).
type DaemonPorts struct {
	CheckAll  func(ctx context.Context) error
	Subscribe func(ctx context.Context) error
	Schedules DaemonSchedules
	// Intent records one durable intention for a schedule that is about to
	// fire, before the row's next_run is advanced. A nil port records nothing.
	Intent      func(ctx context.Context, schedule DaemonDueSchedule, at time.Time) error
	Orchestrate func(ctx context.Context, waker Waker) error
	Summary     func(ctx context.Context) (Workload, error)
	UnitReport  func(ctx context.Context) ([]DaemonUnit, error)
	Verify      func(ctx context.Context) (bool, error)
	CloseMerged func(ctx context.Context) error
	Waker       Waker
	Tracer      observe.Tracer
	Log         func(format string, args ...any)
}

// DeliveredKeyPruner is an optional capability of a Waker. DaemonRun calls it
// after the close step so a Waker that remembers delivered obligations can
// forget keys of Watches that have been inactive for the replay window. A
// Waker without this capability keeps its previous behaviour.
type DeliveredKeyPruner interface {
	PruneDeliveredKeys() int
}

// DaemonRun runs one pass immediately and then repeats every interval until
// ctx is cancelled. With once it stops after the first pass, which is what the
// tests and one-shot invocations use.
//
// One pass runs checkall, subscribe, schedules, orchestrate, summary, verify
// and close-merged in that order. A nil Schedules port skips that step. Verify
// gates close-merged: a card whose required checks did not pass stays open. An
// error from any port is logged and swallowed: one failing step must never
// bring the daemon down or hide the remaining actions.
func DaemonRun(ctx context.Context, ports DaemonPorts, interval time.Duration, once bool) error {
	if ports.CheckAll == nil || ports.Orchestrate == nil || ports.CloseMerged == nil {
		return errors.New("daemon: checkall, orchestrate and close-merged ports are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = DaemonInterval
	}
	logf := ports.Log
	if logf == nil {
		logf = log.Printf
	}

	pass := func() {
		observe.DaemonPass()
		tracer := ports.Tracer
		if tracer == nil {
			tracer = observe.Current()
		}
		// One span per pass, not per command: two ticks produce two daemon
		// spans. The child context carries the span so deeper hooks in the
		// wake path can nest under it (T3.394).
		passCtx, end := tracer.Start(ctx, "beacon.daemon.tick")
		defer end()
		if err := ports.CheckAll(passCtx); err != nil {
			logf("daemon: checkall: %v", err)
		}
		if ports.Subscribe != nil {
			if err := ports.Subscribe(passCtx); err != nil {
				logf("daemon: subscribe: %v", err)
			}
		}
		if ports.Schedules != nil {
			if err := triggerDueSchedules(passCtx, ports.Schedules, ports.Intent, time.Now()); err != nil {
				logf("daemon: schedules: %v", err)
			}
		}
		if err := ports.Orchestrate(passCtx, ports.Waker); err != nil {
			logf("daemon: orchestrate: %v", err)
		}
		if ports.Summary != nil {
			report, err := ports.Summary(passCtx)
			if err != nil {
				logf("daemon: summary: %v", err)
			} else {
				// The four headline counters use the same keys as
				// `beacon workload --format json`, so one log line is
				// comparable with the CLI report.
				logf("daemon: summary: total=%d ready=%d needsAttention=%d watching=%d registered=%d",
					report.Total, report.Ready, report.NeedsAttention, report.Watching, report.Registered)
			}
		}
		// The ordered per-unit report follows the workload summary. Each unit
		// carries its own status, reason and action, so one failed unit does
		// not hide the fate of the rest of the batch.
		if ports.UnitReport != nil {
			units, err := ports.UnitReport(passCtx)
			if err != nil {
				logf("daemon: unit report: %v", err)
			} else {
				for _, unit := range units {
					logf("daemon: unit: id=%s status=%s priority=%t reason=%q action=%q",
						unit.ID, unit.Status, unit.Priority, unit.Reason, unit.Action)
				}
			}
		}
		// Verify is the gate before the final transition. A card closes only
		// when its required checks pass. An error or a false result leaves
		// every card open and logs the reason. A nil Verify keeps the older
		// pass, which closes without a gate.
		verified := ports.Verify == nil
		if ports.Verify != nil {
			ok, err := ports.Verify(passCtx)
			switch {
			case err != nil:
				logf("daemon: verify: %v", err)
			case !ok:
				logf("daemon: verify: required checks did not pass, skipping close")
			default:
				verified = true
			}
		}
		if verified {
			if err := ports.CloseMerged(passCtx); err != nil {
				logf("daemon: close merged: %v", err)
			}
		}
		// The close step is the pass's cleanup point. A Waker that keeps
		// per-obligation keys can drop the ones whose Watch has not been seen
		// for the replay window. The capability is optional, so other Waker
		// implementations are untouched.
		if pruner, ok := ports.Waker.(DeliveredKeyPruner); ok && pruner != nil {
			pruner.PruneDeliveredKeys()
		}
	}

	pass()
	if once {
		return nil
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			pass()
		}
	}
}

// daemonNextRun moves nextRun forward by whole intervals until it is after
// now. Missed intervals collapse into one future run, so a daemon that was
// down for several periods produces one trigger after it comes back. A row
// with no usable interval is not triggered.
func daemonNextRun(nextRun time.Time, interval time.Duration, now time.Time) (time.Time, bool) {
	if nextRun.IsZero() || interval <= 0 {
		return time.Time{}, false
	}
	if nextRun.After(now) {
		return nextRun, true
	}
	elapsed := now.Sub(nextRun)
	return nextRun.Add(elapsed/interval*interval + interval), true
}

// daemonNextRunWith resolves the next run for one due row. A cron row carries a
// NextCron callback that knows the expression and its timezone; its result is
// handed to the same collapse rule so a restart after several missed periods
// produces one future firing. Rows without the callback use interval arithmetic.
func daemonNextRunWith(s DaemonDueSchedule, now time.Time) (time.Time, bool) {
	if s.NextCron == nil {
		return daemonNextRun(s.NextRun, s.Interval, now)
	}
	next, err := s.NextCron(now)
	if err != nil || next.IsZero() || !next.After(now) {
		return time.Time{}, false
	}
	return next, true
}

// triggerDueSchedules acknowledges every due row once. A failed ListDue or
// MarkTriggered is returned to the caller, which logs it and keeps the rest of
// the pass. One bad row does not stop the remaining rows from being marked. The
// intent port runs before MarkTriggered, so an acknowledged row always has a
// matching durable intention.
func triggerDueSchedules(ctx context.Context, port DaemonSchedules, intent func(context.Context, DaemonDueSchedule, time.Time) error, now time.Time) error {
	due, err := port.ListDue(ctx, now)
	if err != nil {
		return err
	}
	var firstErr error
	for _, s := range due {
		next, ok := daemonNextRunWith(s, now)
		if !ok {
			continue
		}
		if intent != nil {
			if err := intent(ctx, s, next); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if err := port.MarkTriggered(ctx, s.ID, next); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// DaemonWatch is the primitive projection of a Watch the daemon's waker reads.
// Adapter and SubscriberID come from the executor subscriber; the composition
// root performs the projection so neither cmd nor app names a domain type
// (ADR 0005 layer matrix).
type DaemonWatch struct {
	ID           string
	PR           int
	HeadSHA      string
	BaseRef      string
	Adapter      string
	SubscriberID string
	MainHeadSHA  string
}

// DaemonWatchLookup resolves one Watch into the primitive projection.
type DaemonWatchLookup func(watchID string) (DaemonWatch, error)

// DaemonSubscriber is one subscriber of a Watch, projected into primitive
// fields so cmd can supply it without naming a domain type (ADR 0005).
type DaemonSubscriber struct {
	ID      string
	Role    string
	Adapter string
	Target  string
}

// DaemonWakeSubscriber picks the executor or fixer a daemon wake may address.
// A daemon wake is a retry/rebase pointer for the party that owns the Watch;
// it is never addressed to the orchestrator. Orchestrator wakes travel a
// separate route (blocked -> NotifyOrchestrator) and do not use this function.
//
// Subscribers whose ID or target is not an OpenCode sessionID are stale service
// names from old Watches (beacon-ci-checkall, beacon-daemon-checkall) and are
// skipped: steering one returns HTTP 400 instead of a wake. Executors and
// fixers qualify because a fixer wakes like an executor. Orchestrators follow
// the blocked-wake route and never qualify. Without a valid recipient the
// caller must not deliver at all.
func DaemonWakeSubscriber(subs []DaemonSubscriber) (DaemonSubscriber, error) {
	for _, sub := range subs {
		if sub.Role != RoleExecutor && sub.Role != RoleFixer {
			continue
		}
		if ValidateWakeTarget(sub.ID) != nil {
			continue
		}
		if sub.Target != "" && ValidateWakeTarget(sub.Target) != nil {
			continue
		}
		return sub, nil
	}
	return DaemonSubscriber{}, errors.New("daemon: no executor or fixer subscriber with a valid OpenCode sessionID (ses_) to wake")
}

// NewDaemonWaker builds the Waker the daemon passes to the orchestrator. The
// composition root supplies the delivery backend and the shared dedup ledger
// as ports, so this package names no infra type (ARDH-15 DIP).
func NewDaemonWaker(lookup DaemonWatchLookup, backend WakeBackend, dedup WakeDedup) Waker {
	return &daemonWaker{
		lookup:        lookup,
		backend:       backend,
		dedup:         dedup,
		deliveredKeys: make(map[string]struct{}),
		inFlight:      make(map[string]struct{}),
	}
}

// WakeRoute is the verdict-delivery hook supplied by the composition root.
// cmd owns the notify router and passes a route here, so this package never
// imports the notify package in the same app layer (ADR 0005). The hook gets
// the projected Watch, the revision and the identifier-only text.
type WakeRoute func(ctx context.Context, watch DaemonWatch, revision uint64, verdict, text string) error

// NewDaemonWakerWithRoute builds a Waker that hands verdict delivery to route.
// A nil route keeps the local delivery path built by NewDaemonWaker.
func NewDaemonWakerWithRoute(lookup DaemonWatchLookup, route WakeRoute) Waker {
	w := NewDaemonWaker(lookup, nil, nil).(*daemonWaker)
	w.route = route
	return w
}

// NewDaemonWakerWithBackend builds the full Waker the composition root uses:
// a notify route, the wake backend and the shared dedup ledger.
func NewDaemonWakerWithBackend(lookup DaemonWatchLookup, route WakeRoute, backend WakeBackend, dedup WakeDedup) Waker {
	w := NewDaemonWaker(lookup, backend, dedup).(*daemonWaker)
	w.route = route
	return w
}

type daemonWaker struct {
	lookup  DaemonWatchLookup
	backend WakeBackend
	route   WakeRoute

	// mu guards deliveredKeys and inFlight. deliveredKeys holds obligation
	// keys already admitted to a subscriber in this process. inFlight holds
	// keys whose delivery is in progress, so parallel passes reserve the key
	// once and never steer the same obligation twice.
	mu            sync.Mutex
	deliveredKeys map[string]struct{}
	inFlight      map[string]struct{}

	// deliveredAt holds the admission time of each obligation key. watchKeys
	// groups keys by Watch and watchLastSeen is refreshed on every Wake for a
	// Watch. A Watch unseen for deliveredKeyRetention is treated as closed,
	// which lets PruneDeliveredKeys forget its keys without touching a live
	// Watch's repeat suppression.
	deliveredAt   map[string]time.Time
	watchKeys     map[string]map[string]struct{}
	watchLastSeen map[string]time.Time

	// clock makes the 24h window testable without sleeping.
	clock func() time.Time

	// dedup is the wake-identity ledger shared with the CI bridge. It stops
	// the bridge and the daemon from admitting the same obligation twice in
	// one process. A nil ledger disables the cross-supplier guard in tests
	// that build daemonWaker by hand.
	dedup WakeDedup
}

// validateDaemonWakePointer checks that the subscriber named on the pointer
// owns its obligation. It is the app-local form of the wake bridge check, so
// no infra type is needed here.
func validateDaemonWakePointer(p WakePointer) error {
	if p.Target == "" {
		return errors.New("daemon: empty subscriber target")
	}
	if !strings.HasPrefix(p.Target, "ses_") || len(p.Target) == len("ses_") {
		return nil
	}
	if strings.Contains(p.ObligationID, ":") {
		if want := obligationKey(p.WatchID, p.Revision, p.Target); p.ObligationID != want {
			return fmt.Errorf("daemon: subscriber %q does not own obligation %q", p.Target, p.ObligationID)
		}
	}
	return nil
}

// daemonWakeIsDelivered reports whether an adapter receipt counts as a
// delivered wake. Only these outcomes suppress a repeat: an error or a skip
// leaves the obligation eligible for the next daemon pass.
func daemonWakeIsDelivered(outcome string) bool {
	switch outcome {
	case WakeDelivered, WakeQueued, WakeEnqueued:
		return true
	default:
		return false
	}
}

// daemonWakeAlreadyExists is the daemon's "search before create" check for
// wakes. Before a repeated pass steers an executor again, it searches the
// in-memory ledger for an admission already recorded for the same obligation.
func daemonWakeAlreadyExists(w *daemonWaker, obligationID string) bool {
	if w == nil || w.deliveredKeys == nil {
		return false
	}
	_, seen := w.deliveredKeys[obligationID]
	return seen
}

var _ Waker = (*daemonWaker)(nil)

// daemonWakeInstruction maps a verdict to the action the subscriber must take.
// A retry asks for a rerun, a rebase verdict asks for a rebase and keep_working
// asks to continue. The old hardcoded rebase text was wrong for every verdict
// except rebase; an unknown verdict stays neutral and still carries its name.
func daemonWakeInstruction(verdict string) string {
	switch strings.ToLower(strings.TrimSpace(verdict)) {
	case "rebase":
		return "нужен rebase, сделай его сам"
	case "retry":
		return "перезапусти проверки: gh run rerun --failed"
	case "keep_working":
		return "продолжай работу"
	default:
		return "нужны действия по вердикту"
	}
}

// Wake leaves a verdict-shaped pointer for the Watch's executor. It never
// repairs the branch, reruns checks or merges: those belong to the subscriber
// and to the other daemon ports.
func (w *daemonWaker) Wake(ctx context.Context, watchID string, revision uint64, verdict string) error {
	if w == nil || w.lookup == nil || (w.backend == nil && w.route == nil) {
		return errors.New("daemon: waker is not configured")
	}
	now := time.Now()
	if w.clock != nil {
		now = w.clock()
	}
	watch, err := w.lookup(watchID)
	if err != nil {
		return fmt.Errorf("daemon: watch %s: %w", watchID, err)
	}
	if watch.SubscriberID == "" {
		log.Printf("daemon: skip wake for watch %s: no subscriber with a valid OpenCode sessionID (ses_)", watchID)
		return fmt.Errorf("daemon: watch %s has no subscriber with a valid OpenCode sessionID (ses_) to wake", watchID)
	}
	// The projection may come from an old Watch whose executor is a service
	// name. Refuse it here so no HTTP prompt is attempted and the reason is
	// logged exactly once, before any adapter is consulted.
	if err := ValidateWakeTarget(watch.SubscriberID); err != nil {
		log.Printf("daemon: skip wake for watch %s: %v", watchID, err)
		return fmt.Errorf("daemon: watch %s: %w", watchID, err)
	}
	adapter := watch.Adapter
	if adapter == "" {
		adapter = WakeDefaultAdapter
	}
	text := fmt.Sprintf("%s: verdict=%s watchID=%s pr=%d headSHA=%s baseRef=%s revision=%d",
		daemonWakeInstruction(verdict), verdict, watchID, watch.PR, watch.HeadSHA, watch.BaseRef, revision)
	if sha := strings.TrimSpace(watch.MainHeadSHA); sha != "" {
		text += " mainHeadSHA=" + sha
	}
	pointer := WakePointer{
		Adapter:      adapter,
		Target:       watch.SubscriberID,
		WatchID:      watchID,
		ObligationID: obligationKey(watchID, revision, watch.SubscriberID),
		Revision:     revision,
		VerdictRef:   text,
		Attempt:      1,
		Delivery:     "steer",
	}
	// The address and the obligation must name the same subscriber. A lookup
	// that substitutes the current caller's session is refused before delivery.
	if err := validateDaemonWakePointer(pointer); err != nil {
		log.Printf("daemon: skip wake for watch %s: %v", watchID, err)
		return fmt.Errorf("daemon: watch %s: %w", watchID, err)
	}
	// A Watch whose verdict did not change keeps its revision, so every daemon
	// pass builds the same pointer. The obligation key folds watchID, revision
	// and subscriber into one string, which is exactly the unit that must be
	// delivered once. Reserve the key under the lock, deliver without the lock,
	// then record the result under the lock again. A slow route or backend must
	// not stall a wake for another Watch (CC-03).
	w.mu.Lock()
	if w.deliveredKeys == nil {
		w.deliveredKeys = make(map[string]struct{})
	}
	if w.inFlight == nil {
		w.inFlight = make(map[string]struct{})
	}
	if w.deliveredAt == nil {
		w.deliveredAt = make(map[string]time.Time)
	}
	if w.watchKeys == nil {
		w.watchKeys = make(map[string]map[string]struct{})
	}
	if w.watchLastSeen == nil {
		w.watchLastSeen = make(map[string]time.Time)
	}
	// A repeat on the same obligation still proves the Watch is live. Refresh
	// before the dedup checks so a suppressed pass keeps the Watch in the live
	// set and its keys stay out of the pruner's reach.
	w.watchLastSeen[watchID] = now
	if daemonWakeAlreadyExists(w, pointer.ObligationID) {
		w.mu.Unlock()
		return nil
	}
	if _, busy := w.inFlight[pointer.ObligationID]; busy {
		w.mu.Unlock()
		return nil
	}
	if w.dedup != nil && w.dedup.Seen(pointer.ObligationID) {
		observe.WakeDuplicate(watchID, revision)
		w.mu.Unlock()
		return nil
	}
	w.inFlight[pointer.ObligationID] = struct{}{}
	w.mu.Unlock()

	var deliverErr error
	record := false
	if w.route != nil {
		deliverErr = w.route(ctx, watch, revision, verdict, text)
		record = deliverErr == nil
	} else {
		receipt, ok := w.backend.Deliver(ctx, pointer)
		switch {
		case !ok:
			deliverErr = fmt.Errorf("daemon: no wake adapter %q for watch %s", adapter, watchID)
		case receipt.Outcome == WakeErrored:
			deliverErr = fmt.Errorf("daemon: wake %s: %s", watchID, receipt.Outcome)
		case daemonWakeIsDelivered(receipt.Outcome):
			record = true
		}
	}

	w.mu.Lock()
	delete(w.inFlight, pointer.ObligationID)
	if record {
		w.deliveredKeys[pointer.ObligationID] = struct{}{}
		w.deliveredAt[pointer.ObligationID] = now
		keys := w.watchKeys[watchID]
		if keys == nil {
			keys = make(map[string]struct{})
			w.watchKeys[watchID] = keys
		}
		keys[pointer.ObligationID] = struct{}{}
		if w.dedup != nil {
			w.dedup.Claim(pointer.ObligationID)
		}
	}
	w.mu.Unlock()
	return deliverErr
}

// PruneDeliveredKeys forgets obligation keys of Watches that have not been
// seen for the replay window. A key is dropped only when both its Watch is
// inactive and the key itself is older than deliveredKeyRetention, so a live
// Watch keeps repeat suppression on its current revision. The operation is
// idempotent: a second call with the same clock removes nothing.
func (w *daemonWaker) PruneDeliveredKeys() int {
	if w == nil {
		return 0
	}
	now := time.Now()
	if w.clock != nil {
		now = w.clock()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.deliveredKeys == nil || w.watchLastSeen == nil {
		return 0
	}
	removed := 0
	for watchID, lastSeen := range w.watchLastSeen {
		if now.Sub(lastSeen) < deliveredKeyRetention {
			continue
		}
		keys := w.watchKeys[watchID]
		for key := range keys {
			if at, ok := w.deliveredAt[key]; ok && now.Sub(at) < deliveredKeyRetention {
				continue
			}
			if _, ok := w.deliveredKeys[key]; ok {
				delete(w.deliveredKeys, key)
				removed++
			}
			delete(w.deliveredAt, key)
			delete(keys, key)
		}
		if len(keys) == 0 {
			delete(w.watchKeys, watchID)
			delete(w.watchLastSeen, watchID)
		}
	}
	return removed
}

// ---------------------------------------------------------------------------
// Findings validation (HD-043, ADR 0301)
// ---------------------------------------------------------------------------

// Finding is one code-review finding reduced to primitive fields so this
// validator stays in app and imports no domain type (ADR 0005 layer matrix).
// File is the cited path and Evidence is a verbatim quote from the reviewed
// diff. Harbor's +check drops a finding when its citation exists neither in
// the diff nor on disk; Beacon applies the same test against the diff here.
type Finding struct {
	ID       string
	Severity string
	File     string
	Evidence string
	Summary  string
}

// FindingDrop pairs a removed finding with the reason it was removed.
type FindingDrop struct {
	Finding Finding
	Reason  string
}

// FindingValidation is the outcome of one validation pass. Kept holds the
// findings that survive, in their original order. Dropped holds every removed
// finding with its reason so the caller can render a filter summary.
type FindingValidation struct {
	Kept    []Finding
	Dropped []FindingDrop
}

// The reasons a finding is removed. A finding with neither File nor Evidence
// cannot be checked at all. One whose citations are absent from the diff is
// invented.
const (
	FindingDropNoCitation = "finding cites neither a file nor evidence"
	FindingDropNotInDiff  = "cited file or evidence is absent from the reviewed diff"
)

// ValidateFindings separates findings grounded in the reviewed diff from
// invented ones before they reach any output. A finding is kept when either
// its File path or its Evidence quote appears in diff. A finding with no
// citation, or one whose citations are missing from diff, is dropped with a
// reason. The function is pure and reads no filesystem, so a change that lives
// only in the diff still validates. A finding about unchanged surrounding code
// needs that code supplied in diff to survive.
func ValidateFindings(findings []Finding, diff string) FindingValidation {
	result := FindingValidation{
		Kept:    make([]Finding, 0, len(findings)),
		Dropped: make([]FindingDrop, 0),
	}
	for _, f := range findings {
		if reason := findingDropReason(f, diff); reason != "" {
			result.Dropped = append(result.Dropped, FindingDrop{Finding: f, Reason: reason})
			continue
		}
		result.Kept = append(result.Kept, f)
	}
	return result
}

// findingDropReason returns why f must be dropped, or empty when it is kept.
func findingDropReason(f Finding, diff string) string {
	file := strings.TrimSpace(f.File)
	evidence := strings.TrimSpace(f.Evidence)
	if file == "" && evidence == "" {
		return FindingDropNoCitation
	}
	if (file != "" && strings.Contains(diff, file)) ||
		(evidence != "" && strings.Contains(diff, evidence)) {
		return ""
	}
	return FindingDropNotInDiff
}
