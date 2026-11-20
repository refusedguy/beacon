// Package orchestrator implements the background daemon that drives one
// automatic pass over Beacon Watches: check CI, enroll the orchestrator on
// active Watches, merge what is ready, print the workload summary and close
// Watches whose PR is finished. The loop owns no adapters. The composition
// root supplies the actions as narrow ports so tests can replace each one
// with a fake.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

// DaemonInterval is the delay between passes when neither the flag nor the
// environment asks for a different value.
const DaemonInterval = 3 * time.Minute

// DaemonPorts are the actions of one daemon pass.
//
// CheckAll lists open PRs, searches for an existing Watch for each one before
// registering a new one, and publishes the verdict. This is the daemon's
// "search before create" step: an open PR must reuse its Watch instead of
// producing a duplicate. Subscribe enrolls the orchestrator on every active
// Watch before the orchestrator pass. Orchestrate merges ready Watches and
// hands retry/fail Watches to the Waker. Summary returns the workload snapshot
// the pass prints after orchestration. Verify reports whether the conditions
// required to close a card are met. CloseMerged closes Watches whose PR is
// already merged or closed, but only after Verify passes. CheckAll, Orchestrate
// and CloseMerged are required. Subscribe, Summary and Verify are optional: a
// nil port skips that step, and a nil Verify keeps the original close behaviour
// for older callers. A nil Waker is allowed and simply suppresses wakes.
type DaemonPorts struct {
	CheckAll    func(ctx context.Context) error
	Subscribe   func(ctx context.Context) error
	Orchestrate func(ctx context.Context, waker Waker) error
	Summary     func(ctx context.Context) (Workload, error)
	Verify      func(ctx context.Context) (bool, error)
	CloseMerged func(ctx context.Context) error
	Waker       Waker
	Log         func(format string, args ...any)
}

// DaemonRun runs one pass immediately and then repeats every interval until
// ctx is cancelled. With once it stops after the first pass, which is what the
// tests and one-shot invocations use.
//
// One pass runs checkall, subscribe, orchestrate, summary, verify and
// close-merged in that order. Verify gates close-merged: a card whose required
// checks did not pass stays open. An error from any port is logged and
// swallowed: one failing step must never bring the daemon down or hide the
// remaining actions.
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
		if err := ports.CheckAll(ctx); err != nil {
			logf("daemon: checkall: %v", err)
		}
		if ports.Subscribe != nil {
			if err := ports.Subscribe(ctx); err != nil {
				logf("daemon: subscribe: %v", err)
			}
		}
		if err := ports.Orchestrate(ctx, ports.Waker); err != nil {
			logf("daemon: orchestrate: %v", err)
		}
		if ports.Summary != nil {
			report, err := ports.Summary(ctx)
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
		// Verify is the gate before the final transition. A card closes only
		// when its required checks pass. An error or a false result leaves
		// every card open and logs the reason. A nil Verify keeps the older
		// pass, which closes without a gate.
		verified := ports.Verify == nil
		if ports.Verify != nil {
			ok, err := ports.Verify(ctx)
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
			if err := ports.CloseMerged(ctx); err != nil {
				logf("daemon: close merged: %v", err)
			}
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

// DaemonWatch is the primitive projection of a Watch the daemon's waker reads.
// Adapter and SubscriberID come from the executor subscriber; the composition
// root performs the projection so neither cmd nor app names a domain type
// (ADR 0005 layer matrix).
type DaemonWatch struct {
	ID           string
	PR           int
	Adapter      string
	SubscriberID string
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
// adapter registry in internal/infra/wake selects a deliverer by the
// subscriber's own adapter name; no harness name is hard-coded here.
func NewDaemonWaker(lookup DaemonWatchLookup) Waker {
	adapters := wake.T211NewAdapterRegistry()
	adapters.Register(wake.AdapterOpenCode, &daemonWakeAdapter{api: &wake.T202API{}})
	return &daemonWaker{
		lookup:        lookup,
		adapters:      adapters,
		deliveredKeys: make(map[string]struct{}),
	}
}

// WakeRoute is the verdict-delivery hook supplied by the composition root.
// cmd owns the notify router and passes a route here, so this package never
// imports the notify package in the same app layer (ADR 0005). The hook gets
// the projected Watch, the revision and the identifier-only text.
type WakeRoute func(ctx context.Context, watch DaemonWatch, revision uint64, verdict, text string) error

// NewDaemonWakerWithRoute builds a Waker that hands verdict delivery to route.
// A nil route keeps the built-in adapter registry used by NewDaemonWaker and
// by the tests. Validation and dedup run in Wake for both forms.
func NewDaemonWakerWithRoute(lookup DaemonWatchLookup, route WakeRoute) Waker {
	w := NewDaemonWaker(lookup).(*daemonWaker)
	w.route = route
	return w
}

type daemonWaker struct {
	lookup   DaemonWatchLookup
	adapters *wake.T211AdapterRegistry
	route    WakeRoute

	// mu guards deliveredKeys and the check-deliver-record sequence, so two
	// concurrent passes cannot steer the same obligation twice. deliveredKeys
	// holds obligation keys already admitted to a subscriber in this process.
	// The ledger is in-memory only: a daemon restart starts with an empty map
	// and may deliver the same (watchID, revision, subscriber) again. The host
	// prompt API keeps its own admission id, so the duplicate is admitted as a
	// new task. Surviving a restart needs a durable receipt store, which is out
	// of scope for this fix.
	mu            sync.Mutex
	deliveredKeys map[string]struct{}
}

// daemonWakeIsDelivered reports whether an adapter receipt counts as a
// delivered wake. Only these outcomes suppress a repeat: an error or a skip
// leaves the obligation eligible for the next daemon pass.
func daemonWakeIsDelivered(outcome string) bool {
	switch outcome {
	case wake.T210Delivered, wake.T210Queued, wake.T210Enqueued:
		return true
	default:
		return false
	}
}

// daemonWakeAlreadyExists is the daemon's "search before create" check for
// wakes. Before a repeated pass steers an executor again, it searches the
// in-memory ledger for an admission already recorded for the same obligation.
// A miss means no wake was created yet for this (watchID, revision,
// subscriber), so the caller may go on and deliver one. Only successful
// receipts are recorded, so an error or a skip stays eligible for the next
// pass (see Wake).
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
	if w == nil || w.lookup == nil || w.adapters == nil {
		return errors.New("daemon: waker is not configured")
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
		adapter = wake.AdapterOpenCode
	}
	text := fmt.Sprintf("%s: verdict=%s watchID=%s pr=%d revision=%d",
		daemonWakeInstruction(verdict), verdict, watchID, watch.PR, revision)
	pointer := wake.T210WakePointer{
		Adapter:      adapter,
		Target:       watch.SubscriberID,
		WatchID:      watchID,
		ObligationID: obligationKey(watchID, revision, watch.SubscriberID),
		Revision:     revision,
		VerdictRef:   text,
		Attempt:      1,
		Delivery:     string(wake.T202DeliverySteer),
	}
	// The address and the obligation must name the same subscriber. A lookup
	// that substitutes the current caller's session is refused before delivery.
	if err := wake.ValidateT210Target(pointer); err != nil {
		log.Printf("daemon: skip wake for watch %s: %v", watchID, err)
		return fmt.Errorf("daemon: watch %s: %w", watchID, err)
	}
	// A Watch whose verdict did not change keeps its revision, so every daemon
	// pass builds the same pointer. The obligation key folds watchID, revision
	// and subscriber into one string, which is exactly the unit that must be
	// delivered once. Check and record under the same lock as the delivery.
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.deliveredKeys == nil {
		w.deliveredKeys = make(map[string]struct{})
	}
	// Search for an existing admission before creating a new wake. A repeated
	// daemon pass over an unchanged Watch must not steer the executor twice.
	if daemonWakeAlreadyExists(w, pointer.ObligationID) {
		return nil
	}
	if w.route != nil {
		if err := w.route(ctx, watch, revision, verdict, text); err != nil {
			return err
		}
	} else {
		receipt, ok := wake.T210Deliver(ctx, pointer, nil, nil, w.adapters, nil)
		if !ok {
			return fmt.Errorf("daemon: no wake adapter %q for watch %s", adapter, watchID)
		}
		if receipt.Outcome == wake.T210Errored {
			return fmt.Errorf("daemon: wake %s: %s", watchID, receipt.Outcome)
		}
		if !daemonWakeIsDelivered(receipt.Outcome) {
			return nil
		}
	}
	w.deliveredKeys[pointer.ObligationID] = struct{}{}
	return nil
}

// daemonWakeAdapter is the one built-in deliverer the daemon registers. It
// implements wake.T210WakeAdapter so T210Deliver can route by the adapter name
// carried on the pointer, and it uses only wake's primitive API for delivery.
type daemonWakeAdapter struct {
	api *wake.T202API
}

func (a *daemonWakeAdapter) Bind(p wake.T210WakePointer, _ *wake.T211SessionRegistry) (wake.T210Target, bool) {
	if err := wake.ValidateT210Target(p); err != nil {
		log.Printf("daemon: skip wake bind for %s: %v", p.ObligationID, err)
		return "", false
	}
	// Defence in depth: the opencode deliverer only accepts sessions. A stale
	// subscriber must never reach Deliver even if a caller bypasses Wake.
	if err := ValidateWakeTarget(p.Target); err != nil {
		log.Printf("daemon: skip wake bind for %s: %v", p.ObligationID, err)
		return "", false
	}
	return wake.T210Target(p.Target), true
}

func (a *daemonWakeAdapter) Deliver(p wake.T210WakePointer, target wake.T210Target) (wake.T210Receipt, error) {
	receipt := wake.T210Receipt{
		ObligationID: p.ObligationID,
		Attempt:      p.Attempt,
		Delivery:     string(wake.T202DeliverySteer),
	}
	// No session substitution at the adapter boundary either.
	if string(target) != p.Target {
		err := fmt.Errorf("daemon: target %q is not subscriber %q", target, p.Target)
		receipt.Outcome = wake.T210Errored
		return receipt, err
	}
	if a == nil || a.api == nil {
		receipt.Outcome = wake.T210Errored
		return receipt, errors.New("daemon: wake API is nil")
	}
	if _, err := a.api.Prompt(context.Background(), string(target), p.VerdictRef, wake.T202DeliverySteer); err != nil {
		receipt.Outcome = wake.T210Errored
		return receipt, err
	}
	receipt.Outcome = wake.T210Delivered
	return receipt, nil
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
