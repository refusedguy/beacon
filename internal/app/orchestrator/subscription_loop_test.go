package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type subReader struct {
	events map[string][]SubEvent
	cursor map[string]time.Time
	err    error
	calls  []string
}

func (r *subReader) Read(ctx context.Context, sub SubSubscription, after time.Time) ([]SubEvent, time.Time, error) {
	r.calls = append(r.calls, sub.ID)
	if r.err != nil {
		return nil, after, r.err
	}
	next := r.cursor[sub.ID]
	if next.IsZero() {
		next = after
	}
	return r.events[sub.ID], next, nil
}

type subStore struct {
	subs      []SubSubscription
	delivered map[string]time.Time
	err       error
}

func (s *subStore) Live(now time.Time) ([]SubSubscription, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.subs, nil
}

func (s *subStore) MarkDelivered(id string, at time.Time) error {
	if s.delivered == nil {
		s.delivered = make(map[string]time.Time)
	}
	s.delivered[id] = at
	return nil
}

type subWaker struct {
	wakes []SubWake
	err   error
}

func (w *subWaker) Send(ctx context.Context, wake SubWake) error {
	if w.err != nil {
		return w.err
	}
	w.wakes = append(w.wakes, wake)
	return nil
}

func testLoop(subs []SubSubscription, events map[string][]SubEvent, now time.Time) (*SubscriptionLoop, *subStore, *subWaker) {
	store := &subStore{subs: subs}
	reader := &subReader{events: events, cursor: map[string]time.Time{}}
	waker := &subWaker{}
	loop := NewSubscriptionLoop(reader, store, waker, func() time.Time { return now })
	return loop, store, waker
}

func testSubSubscription(now time.Time) SubSubscription {
	return SubSubscription{
		ID:             "sub-1",
		Source:         SubGitHubPRReview,
		OwnerSessionID: "ses_owner",
		ExpiresAt:      now.Add(24 * time.Hour),
		CoalesceWindow: 30 * time.Second,
	}
}

func TestRunPassCoalescesBatchIntoOneWake(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	sub := testSubSubscription(now)
	events := map[string][]SubEvent{
		sub.ID: {
			{SubscriptionID: sub.ID, Source: sub.Source, Ref: "pr/1", At: now},
			{SubscriptionID: sub.ID, Source: sub.Source, Ref: "pr/2", At: now.Add(5 * time.Second)},
			{SubscriptionID: sub.ID, Source: sub.Source, Ref: "pr/3", At: now.Add(10 * time.Second)},
		},
	}
	loop, store, waker := testLoop([]SubSubscription{sub}, events, now)

	n, err := loop.RunPass(context.Background())
	if err != nil {
		t.Fatalf("RunPass: %v", err)
	}
	if n != 1 || len(waker.wakes) != 1 {
		t.Fatalf("want 1 wake, got n=%d wakes=%d", n, len(waker.wakes))
	}
	w := waker.wakes[0]
	if w.EventCount != 3 || !w.RereadSource || w.SourceRef != "pr/3" {
		t.Fatalf("wrong coalesced wake: %+v", w)
	}
	if !strings.Contains(w.Instruction(), "Перечитай") {
		t.Errorf("wake instruction must demand a re-read, got %q", w.Instruction())
	}
	if _, ok := store.delivered[sub.ID]; !ok {
		t.Error("delivered subscription must be recorded")
	}
}

func TestRunPassSecondWakeAfterWindow(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	sub := testSubSubscription(now)
	events := map[string][]SubEvent{
		sub.ID: {
			{SubscriptionID: sub.ID, Source: sub.Source, Ref: "pr/1", At: now},
			{SubscriptionID: sub.ID, Source: sub.Source, Ref: "pr/2", At: now.Add(5 * time.Second)},
			{SubscriptionID: sub.ID, Source: sub.Source, Ref: "pr/3", At: now.Add(40 * time.Second)},
		},
	}
	loop, _, waker := testLoop([]SubSubscription{sub}, events, now)

	if _, err := loop.RunPass(context.Background()); err != nil {
		t.Fatalf("RunPass: %v", err)
	}
	if len(waker.wakes) != 2 {
		t.Fatalf("want 2 wakes, got %d", len(waker.wakes))
	}
	if waker.wakes[0].EventCount != 2 || waker.wakes[1].EventCount != 1 {
		t.Errorf("want batches of 2 and 1, got %d and %d", waker.wakes[0].EventCount, waker.wakes[1].EventCount)
	}
}

func TestRunPassSkipsExpiredAndUnknownSubscriptions(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	expired := testSubSubscription(now)
	expired.ID = "sub-expired"
	expired.ExpiresAt = now.Add(-time.Minute)
	unknown := testSubSubscription(now)
	unknown.ID = "sub-unknown"
	unknown.Source = SubSource("carrier_pigeon")

	loop, _, waker := testLoop([]SubSubscription{expired, unknown}, nil, now)
	n, err := loop.RunPass(context.Background())
	if err != nil {
		t.Fatalf("RunPass: %v", err)
	}
	if n != 0 || len(waker.wakes) != 0 {
		t.Fatalf("expired and unknown subscriptions must be skipped, got n=%d wakes=%d", n, len(waker.wakes))
	}
}

func TestRunPassSendErrorIsNotMarkedDelivered(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	sub := testSubSubscription(now)
	events := map[string][]SubEvent{
		sub.ID: {{SubscriptionID: sub.ID, Source: sub.Source, Ref: "pr/1", At: now}},
	}
	loop, store, waker := testLoop([]SubSubscription{sub}, events, now)
	waker.err = errors.New("transport down")

	n, err := loop.RunPass(context.Background())
	if err != nil {
		t.Fatalf("RunPass: %v", err)
	}
	if n != 0 {
		t.Errorf("failed send must not count as delivered, got %d", n)
	}
	if _, ok := store.delivered[sub.ID]; ok {
		t.Error("failed send must not mark the subscription delivered")
	}
}

func TestNewSubscriptionLoopNilClockUsesWallClock(t *testing.T) {
	loop := NewSubscriptionLoop(&subReader{}, &subStore{}, &subWaker{}, nil)
	if loop == nil || loop.clock == nil {
		t.Fatal("constructor must install a clock")
	}
	if loop.clock().IsZero() {
		t.Error("fallback clock must report a time")
	}
}
