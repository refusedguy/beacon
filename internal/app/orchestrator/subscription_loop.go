// This file owns the loop that drains external event subscriptions and sends
// coalesced wakes back into the owner's conversation. It mirrors the small part
// of the domain subscription model it needs because ADR 0005 forbids an app
// import of internal/domain. It does not touch verdict.go, fanout.go or the
// merge policy, and a subscription is never turned into a Watch.
package orchestrator

import (
	"context"
	"sort"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

// SubSource mirrors domain.SourceKind (ADR 0005). The values are the same so a
// reader can match a subscription across the two layers.
type SubSource string

const (
	SubGitHubPRReview SubSource = "github_pr_review"
	SubGitHubCI       SubSource = "github_ci"
	SubSlackThread    SubSource = "slack_thread"
	SubLinearIssue    SubSource = "linear_issue"
	SubTimer          SubSource = "timer"
)

func (s SubSource) valid() bool {
	switch s {
	case SubGitHubPRReview, SubGitHubCI, SubSlackThread, SubLinearIssue, SubTimer:
		return true
	default:
		return false
	}
}

// SubEvent mirrors domain.ExternalEvent. Ref points at the authoritative
// source; the event text is never carried into a wake as the source of truth.
type SubEvent struct {
	SubscriptionID string
	Source         SubSource
	Ref            string
	At             time.Time
}

// SubSubscription is the app view of a live external event subscription.
type SubSubscription struct {
	ID             string
	Source         SubSource
	OwnerSessionID string
	ExpiresAt      time.Time
	CoalesceWindow time.Duration
	LastDeliveryAt time.Time
}

// SubWake mirrors domain.WakeRequest. RereadSource is always set on wakes this
// loop produces.
type SubWake struct {
	SubscriptionID string
	OwnerSessionID string
	Source         SubSource
	SourceRef      string
	EventCount     int
	RereadSource   bool
}

// SubInstruction mirrors domain.WakeInstruction: the receiver re-reads the
// source before acting on a coalesced batch.
const SubInstruction = "Перечитай внешний источник перед действием: события слиты в одно пробуждение и их текст не является источником истины."

func (w SubWake) Instruction() string { return SubInstruction }

// SubscriptionReader yields unseen events for one subscription. after is the
// last delivery instant; the returned cursor is the instant to pass next time.
type SubscriptionReader interface {
	Read(ctx context.Context, sub SubSubscription, after time.Time) (events []SubEvent, next time.Time, err error)
}

// SubscriptionStore lists live subscriptions and records a delivery.
type SubscriptionStore interface {
	Live(now time.Time) ([]SubSubscription, error)
	MarkDelivered(id string, at time.Time) error
}

// WakeSender delivers one coalesced wake into the owner's conversation.
type WakeSender interface {
	Send(ctx context.Context, wake SubWake) error
}

// SubscriptionLoop drains subscriptions and delivers one wake per batch.
type SubscriptionLoop struct {
	reader SubscriptionReader
	store  SubscriptionStore
	waker  WakeSender
	clock  func() time.Time
}

// NewSubscriptionLoop builds a loop over the three ports. A nil clock falls
// back to the wall clock.
func NewSubscriptionLoop(reader SubscriptionReader, store SubscriptionStore, waker WakeSender, clock func() time.Time) *SubscriptionLoop {
	if clock == nil {
		clock = time.Now
	}
	return &SubscriptionLoop{reader: reader, store: store, waker: waker, clock: clock}
}

// RunPass runs one drain pass and returns the number of wakes delivered. A read
// or send error goes to the observe seam and skips only that subscription; one
// broken source cannot stop the other subscriptions in the pass. A wake is
// recorded as delivered only after the sender accepted it.
func (l *SubscriptionLoop) RunPass(ctx context.Context) (int, error) {
	if l == nil || l.reader == nil || l.store == nil || l.waker == nil {
		return 0, nil
	}
	now := l.clock()
	subs, err := l.store.Live(now)
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, sub := range subs {
		if !sub.Source.valid() || sub.ExpiresAt.IsZero() || !now.Before(sub.ExpiresAt) || sub.CoalesceWindow <= 0 {
			continue
		}
		events, next, err := l.reader.Read(ctx, sub, sub.LastDeliveryAt)
		if err != nil {
			observe.ActorError("subscription-loop", sub.ID, err)
			continue
		}
		wakes := coalesceSubEvents(events, sub)
		for _, wake := range wakes {
			if err := l.waker.Send(ctx, wake); err != nil {
				observe.ActorError("subscription-loop", sub.ID, err)
				continue
			}
			delivered++
			at := next
			if at.IsZero() {
				at = now
			}
			if err := l.store.MarkDelivered(sub.ID, at); err != nil {
				observe.ActorError("subscription-loop", sub.ID, err)
			}
		}
	}
	return delivered, nil
}

// coalesceSubEvents mirrors domain.CoalesceEvents (ADR 0005 keeps the domain
// out of app). Consecutive events within CoalesceWindow of the group's first
// event, or of LastDeliveryAt when set, fold into one wake. An event outside
// the window starts a new wake. The newest event's Ref wins, and every wake
// carries the re-read instruction. Like the domain implementation it does not
// discard events whose timestamp sits after the pass clock; batching depends
// on the interval between events, not on the wall clock at read time.
func coalesceSubEvents(events []SubEvent, sub SubSubscription) []SubWake {
	group := make([]SubEvent, 0, len(events))
	for _, e := range events {
		if e.SubscriptionID != sub.ID || e.Source != sub.Source || !e.At.Before(sub.ExpiresAt) {
			continue
		}
		group = append(group, e)
	}
	sort.SliceStable(group, func(i, j int) bool { return group[i].At.Before(group[j].At) })

	start := sub.LastDeliveryAt
	if start.IsZero() && len(group) > 0 {
		start = group[0].At
	}

	var wakes []SubWake
	flush := func(g []SubEvent) {
		if len(g) == 0 {
			return
		}
		last := g[len(g)-1]
		wakes = append(wakes, SubWake{
			SubscriptionID: sub.ID,
			OwnerSessionID: sub.OwnerSessionID,
			Source:         sub.Source,
			SourceRef:      last.Ref,
			EventCount:     len(g),
			RereadSource:   true,
		})
	}

	var current []SubEvent
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
		current = []SubEvent{e}
		start = e.At
	}
	flush(current)
	return wakes
}
