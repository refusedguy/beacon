// Package domain holds the Beacon entities, status enums and port interfaces.
// This file models subscriptions to external events. A subscription lets an
// agent finish its turn and return to the same conversation with full context
// when the source produces an event. It uses only the standard library.
package domain

import (
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
)

// MaxSubscriptionLifetime is the longest allowed lifetime of an external event
// subscription. It is 180 days, the ceiling from the Cursor Cloud Agents
// subscription contract recorded in research-competitor-orchestrators.md.
const MaxSubscriptionLifetime = 180 * 24 * time.Hour

// SourceKind identifies the external source a subscription listens to. The set
// is closed so an unknown source cannot be persisted as a live subscription.
type SourceKind string

const (
	SourceGitHubPRReview SourceKind = "github_pr_review"
	SourceGitHubCI       SourceKind = "github_ci"
	SourceSlackThread    SourceKind = "slack_thread"
	SourceLinearIssue    SourceKind = "linear_issue"
	SourceTimer          SourceKind = "timer"
)

// Valid reports whether k is one of the defined sources. The zero value fails
// closed, so an uninitialized subscription never reads as live.
func (k SourceKind) Valid() bool {
	switch k {
	case SourceGitHubPRReview, SourceGitHubCI, SourceSlackThread, SourceLinearIssue, SourceTimer:
		return true
	default:
		return false
	}
}

// EventSubscription is a standing request to watch one external source and
// return to the owner's conversation when it produces an event. It is distinct
// from fanout.Subscription: that type is a subscriber's claim on a Watch
// revision, while this one listens outside Beacon. An EventSubscription is not
// a new Watch and never enters the merge policy.
//
// ExpiresAt is an absolute instant so a process restart cannot extend it.
// CoalesceWindow is the span in which several events fold into one wake.
// LastDeliveryAt is the anchor of the current window and advances on delivery.
type EventSubscription struct {
	ID             string
	SourceKind     SourceKind
	OwnerSessionID string
	ExpiresAt      time.Time
	CoalesceWindow time.Duration
	LastDeliveryAt time.Time
}

// NewEventSubscription builds and validates a subscription that lives for
// lifetime from now. It rejects an empty ID or owner, an unknown source, a
// lifetime outside (0, 180 days] and a non-positive coalesce window.
func NewEventSubscription(id string, source SourceKind, ownerSessionID string, now time.Time, lifetime, coalesceWindow time.Duration) (EventSubscription, error) {
	s := EventSubscription{
		ID:             strings.TrimSpace(id),
		SourceKind:     source,
		OwnerSessionID: strings.TrimSpace(ownerSessionID),
		CoalesceWindow: coalesceWindow,
		ExpiresAt:      now.Add(lifetime),
	}
	if err := s.Validate(); err != nil {
		return EventSubscription{}, err
	}
	if err := CheckLifetime(now, s.ExpiresAt); err != nil {
		return EventSubscription{}, err
	}
	return s, nil
}

// Validate checks the fields that do not depend on an evaluation instant.
func (s EventSubscription) Validate() error {
	if s.ID == "" {
		return NewAppError(CodeInvalidInput, "subscription: пустое поле id")
	}
	if !s.SourceKind.Valid() {
		return NewAppError(CodeInvalidInput, "subscription: неизвестный источник")
	}
	if s.OwnerSessionID == "" {
		return NewAppError(CodeInvalidInput, "subscription: пустое поле ownerSessionID")
	}
	if s.ExpiresAt.IsZero() {
		return NewAppError(CodeInvalidInput, "subscription: пустое поле expiresAt")
	}
	if s.CoalesceWindow <= 0 {
		return NewAppError(CodeInvalidInput, "subscription: неположительное окно слияния")
	}
	return nil
}

// CheckLifetime rejects a deadline that is not in the future of now or that is
// further away than MaxSubscriptionLifetime. An expired deadline uses the
// Watch-expired code so callers can treat it like any other stale handle.
func CheckLifetime(now, expiresAt time.Time) error {
	if expiresAt.IsZero() {
		return NewAppError(CodeInvalidInput, "subscription: пустое поле expiresAt")
	}
	if !expiresAt.After(now) {
		return NewAppError(CodeWatchExpired, "subscription: срок подписки истёк")
	}
	if expiresAt.Sub(now) > MaxSubscriptionLifetime {
		return NewAppError(CodeLimitExceeded, "subscription: срок подписки больше 180 дней")
	}
	return nil
}

// Expired reports whether the subscription had run out at at.
func (s EventSubscription) Expired(at time.Time) bool {
	return !s.ExpiresAt.IsZero() && !at.Before(s.ExpiresAt)
}

// ExternalEvent is one observation from an external source. Ref is an opaque
// pointer to the authoritative source, such as a PR URL, a CI commit or a
// thread id. Preview is only a hint carried for diagnostics; it never becomes
// the source of truth in a wake.
type ExternalEvent struct {
	SubscriptionID string
	SourceKind     SourceKind
	Ref            string
	Preview        string
	At             time.Time
}

// WakeRequest is the follow-up delivered into the owner's conversation. It
// carries identifiers and a source reference only. RereadSource is always set:
// the receiver must re-read the external source instead of trusting Preview.
type WakeRequest struct {
	SubscriptionID string
	OwnerSessionID string
	SourceKind     SourceKind
	SourceRef      string
	EventCount     int
	RereadSource   bool
}

// WakeInstruction is the text attached to every coalesced wake. The batched
// preview is not authoritative, so the receiver re-reads the source first.
const WakeInstruction = "Перечитай внешний источник перед действием: события слиты в одно пробуждение и их текст не является источником истины."

// Instruction returns the instruction that accompanies the wake. It is a
// method so a caller cannot build a wake without the re-read requirement.
func (w WakeRequest) Instruction() string { return WakeInstruction }

// Deliverable reports whether w carries the identifiers a wake needs.
func (w WakeRequest) Deliverable() bool {
	return w.SubscriptionID != "" && w.OwnerSessionID != "" && w.SourceRef != ""
}

// CoalesceEvents groups events by subscription into at most one wake per
// coalesce window. Events for a missing, unknown-source or expired subscription
// are dropped. For each subscription events are ordered by At; consecutive
// events within CoalesceWindow of the group's first event fold into one wake.
// An event outside the window starts a new group and therefore a second wake.
// When LastDeliveryAt is set it anchors the first window instead of the first
// event. The newest event's Ref wins because the receiver re-reads current
// source state.
func CoalesceEvents(events []ExternalEvent, subs map[string]EventSubscription, at time.Time) []WakeRequest {
	bySub := make(map[string][]ExternalEvent)
	for _, e := range events {
		sub, ok := subs[e.SubscriptionID]
		if !ok || !sub.SourceKind.Valid() || sub.Expired(e.At) || e.SourceKind != sub.SourceKind {
			continue
		}
		bySub[e.SubscriptionID] = append(bySub[e.SubscriptionID], e)
	}

	ids := slices.Sorted(maps.Keys(bySub))

	var wakes []WakeRequest
	for _, id := range ids {
		sub := subs[id]
		if sub.CoalesceWindow <= 0 || sub.Expired(at) {
			continue
		}
		group := bySub[id]
		sort.SliceStable(group, func(i, j int) bool { return group[i].At.Before(group[j].At) })

		start := sub.LastDeliveryAt
		if start.IsZero() {
			start = group[0].At
		}
		flush := func(g []ExternalEvent) {
			if len(g) == 0 {
				return
			}
			last := g[len(g)-1]
			wakes = append(wakes, WakeRequest{
				SubscriptionID: sub.ID,
				OwnerSessionID: sub.OwnerSessionID,
				SourceKind:     sub.SourceKind,
				SourceRef:      last.Ref,
				EventCount:     len(g),
				RereadSource:   true,
			})
		}

		var current []ExternalEvent
		for _, e := range group {
			if len(current) == 0 {
				current = append(current, e)
				continue
			}
			if e.At.Sub(start) < sub.CoalesceWindow {
				current = append(current, e)
				continue
			}
			flush(current)
			current = []ExternalEvent{e}
			start = e.At
		}
		flush(current)
	}
	return wakes
}

// CICommitResult is the single commit-wide result a github_ci subscription
// delivers. It waits for every check of the commit and then returns either
// success or the list of failed checks. ActionRequired is kept separate: it
// withholds readiness but does not block delivery, so a check that would stay
// pending can be closed with action_required and still yield a result.
type CICommitResult struct {
	CommitSHA      string
	FailedChecks   []string
	ActionRequired bool
	AllTerminal    bool
}

// Ready reports whether the commit is green and may be acted on. An
// actionRequired result is delivered but is never ready.
func (r CICommitResult) Ready() bool {
	return r.AllTerminal && len(r.FailedChecks) == 0 && !r.ActionRequired
}

// Deliverable reports whether the result can be sent to the owner. Only a
// commit with pending checks is held back; actionRequired does not hold it.
func (r CICommitResult) Deliverable() bool { return r.AllTerminal }

// AggregateCIResult folds the checks observed for one commit into a single
// commit-wide result. ACTION_REQUIRED is recorded as a flag and never as a
// failed check. PENDING, QUEUED and running states keep AllTerminal false so
// the result is held back until every check has an answer.
func AggregateCIResult(commitSHA string, checks map[string]Check) CICommitResult {
	r := CICommitResult{CommitSHA: commitSHA, FailedChecks: []string{}, AllTerminal: true}
	for name, c := range checks {
		value := c.Conclusion
		if value == "" {
			value = c.State
		}
		switch strings.ToUpper(strings.TrimSpace(value)) {
		case "ACTION_REQUIRED":
			r.ActionRequired = true
		case "SUCCESS", "PASS", "SUCCESSFUL", "NEUTRAL", "SKIPPED":
			// Passing checks do not change the result.
		case "FAILURE", "ERROR", "TIMED_OUT", "FAIL", "STARTUP_FAILURE":
			r.FailedChecks = append(r.FailedChecks, name)
		default:
			r.AllTerminal = false
		}
	}
	sort.Strings(r.FailedChecks)
	return r
}
