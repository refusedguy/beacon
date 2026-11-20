// Package orchestrator implements the background daemon that drives one
// automatic pass over Beacon Watches: check CI, merge what is ready, leave a
// rebase pointer for the subscriber and close Watches whose PR is finished.
// The loop owns no adapters. The composition root supplies the three actions
// as narrow ports so tests can replace each one with a fake.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

// DaemonInterval is the delay between passes when neither the flag nor the
// environment asks for a different value.
const DaemonInterval = 3 * time.Minute

// DaemonPorts are the three actions of one daemon pass.
//
// CheckAll lists open PRs, finds or registers their Watches and publishes the
// verdict. Orchestrate merges ready Watches and hands retry/fail Watches to the
// Waker. CloseMerged closes Watches whose PR is already merged or closed.
// Every port is required; a nil Waker is allowed and simply suppresses wakes.
type DaemonPorts struct {
	CheckAll    func(ctx context.Context) error
	Orchestrate func(ctx context.Context, waker Waker) error
	CloseMerged func(ctx context.Context) error
	Waker       Waker
	Log         func(format string, args ...any)
}

// DaemonRun runs one pass immediately and then repeats every interval until
// ctx is cancelled. With once it stops after the first pass, which is what the
// tests and one-shot invocations use.
//
// An error from any port is logged and swallowed: one failing pass must never
// bring the daemon down or hide the other two actions.
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
		if err := ports.Orchestrate(ctx, ports.Waker); err != nil {
			logf("daemon: orchestrate: %v", err)
		}
		if err := ports.CloseMerged(ctx); err != nil {
			logf("daemon: close merged: %v", err)
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

type daemonWaker struct {
	lookup   DaemonWatchLookup
	adapters *wake.T211AdapterRegistry

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

var _ Waker = (*daemonWaker)(nil)

// Wake leaves a "do the rebase yourself" pointer for the Watch's executor. It
// never repairs the branch, reruns checks or merges: those belong to the
// subscriber and to the other daemon ports.
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
	text := fmt.Sprintf("нужен rebase, сделай его сам: watchID=%s pr=%d revision=%d",
		watchID, watch.PR, revision)
	pointer := wake.T210WakePointer{
		Adapter:      adapter,
		Target:       watch.SubscriberID,
		WatchID:      watchID,
		ObligationID: daemonObligationKey(watchID, revision, watch.SubscriberID),
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
	if _, seen := w.deliveredKeys[pointer.ObligationID]; seen {
		return nil
	}
	receipt, ok := wake.T210Deliver(ctx, pointer, nil, nil, w.adapters, nil)
	if !ok {
		return fmt.Errorf("daemon: no wake adapter %q for watch %s", adapter, watchID)
	}
	if receipt.Outcome == wake.T210Errored {
		return fmt.Errorf("daemon: wake %s: %s", watchID, receipt.Outcome)
	}
	if daemonWakeIsDelivered(receipt.Outcome) {
		w.deliveredKeys[pointer.ObligationID] = struct{}{}
	}
	return nil
}

// daemonKeyEscaper mirrors the domain obligation-key encoding. It is local
// because this layer may not import internal/domain.
var daemonKeyEscaper = strings.NewReplacer("\\", "\\\\", ":", "\\:")

func daemonObligationKey(watchID string, revision uint64, subscriberID string) string {
	return daemonKeyEscaper.Replace(watchID) + ":" +
		strconv.FormatUint(revision, 10) + ":" +
		daemonKeyEscaper.Replace(subscriberID)
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
