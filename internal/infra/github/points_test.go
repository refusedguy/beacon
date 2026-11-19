package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

func rateLimitServer(t *testing.T, used *atomic.Int64, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != rateLimitPath {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		reset := time.Now().Add(time.Hour).Unix()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"resources":{"core":{"limit":5000,"used":%d,"remaining":%d,"reset":%d}}}`,
			used.Load(), 5000-used.Load(), reset)
	}))
}

func newTestCounter(t *testing.T, used int64) (*Counter, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	usedValue := &atomic.Int64{}
	usedValue.Store(used)
	calls := &atomic.Int64{}
	srv := rateLimitServer(t, usedValue, calls)
	t.Cleanup(srv.Close)
	c, err := NewCounter(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("NewCounter: %v", err)
	}
	return c, usedValue, calls
}

func TestCounterStartsFromRateLimit(t *testing.T) {
	c, _, calls := newTestCounter(t, 137)

	if got := calls.Load(); got != 1 {
		t.Fatalf("GET /rate_limit calls = %d, want 1", got)
	}
	if got := c.Used(); got != 137 {
		t.Fatalf("Used = %d, want 137", got)
	}
	if got := c.Remaining(); got != 363 {
		t.Fatalf("Remaining = %d, want 363", got)
	}
	if c.LimitReached() {
		t.Fatal("LimitReached = true at 137 points")
	}
}

func TestNewCounterFailsWhenRateLimitUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if _, err := NewCounter(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("NewCounter succeeded on 503, counter would start from zero")
	}
}

func TestAddCostIncrementsAndSetsLimitFlag(t *testing.T) {
	c, _, _ := newTestCounter(t, 0)

	if got := c.Add(10); got != 10 {
		t.Fatalf("after Add(10) Used = %d, want 10", got)
	}
	if got := c.Add(489); got != 499 {
		t.Fatalf("after Add(489) Used = %d, want 499", got)
	}
	if c.LimitReached() {
		t.Fatal("LimitReached = true at 499 points")
	}
	if got := c.Add(1); got != 500 {
		t.Fatalf("after Add(1) Used = %d, want 500", got)
	}
	if !c.LimitReached() || !c.BudgetExhausted() {
		t.Fatal("limit flag not set at 500 points")
	}
	if got := c.Remaining(); got != 0 {
		t.Fatalf("Remaining = %d, want 0", got)
	}
	if got := c.Budget(); got != 500 {
		t.Fatalf("Budget = %d, want 500", got)
	}
}

func TestHeadersOverrideBodyCost(t *testing.T) {
	c, _, _ := newTestCounter(t, 100)

	withUsed := &http.Response{Header: http.Header{}}
	withUsed.Header.Set("x-ratelimit-used", "470")
	if got := c.Observe(withUsed, 999); got != 470 {
		t.Fatalf("x-ratelimit-used: Used = %d, want 470", got)
	}

	withRemaining := &http.Response{Header: http.Header{}}
	withRemaining.Header.Set("x-ratelimit-limit", "5000")
	withRemaining.Header.Set("x-ratelimit-remaining", "4600")
	if got := c.Observe(withRemaining, 50); got != 400 {
		t.Fatalf("x-ratelimit-remaining: Used = %d, want 400", got)
	}

	noHeaders := &http.Response{Header: http.Header{}}
	if got := c.Observe(noHeaders, 7); got != 407 {
		t.Fatalf("body cost: Used = %d, want 407", got)
	}
}

func TestStartupHeadersOverrideRateLimitBody(t *testing.T) {
	reset := time.Now().Add(time.Hour).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-used", "7")
		fmt.Fprintf(w, `{"resources":{"core":{"limit":5000,"used":42,"remaining":4958,"reset":%d}}}`, reset)
	}))
	defer srv.Close()

	c, err := NewCounter(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("NewCounter: %v", err)
	}
	if got := c.Used(); got != 7 {
		t.Fatalf("Used = %d, want 7 from x-ratelimit-used", got)
	}
}

func TestReconcileUpdatesFromServer(t *testing.T) {
	c, used, calls := newTestCounter(t, 100)
	used.Store(420)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	c.Reconcile(ctx, 25*time.Millisecond)

	if got := c.Used(); got != 420 {
		t.Fatalf("Used after reconcile = %d, want 420", got)
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("GET /rate_limit calls = %d, want at least 2", got)
	}
}

func TestSecondaryLimitUsesRetryAfter(t *testing.T) {
	c, _, _ := newTestCounter(t, 100)
	base := time.Now()
	c.now = func() time.Time { return base }

	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("retry-after", "30")
	c.Observe(resp, 1)

	if got := c.Used(); got != 101 {
		t.Fatalf("Used = %d, want 101", got)
	}
	if c.BudgetExhausted() {
		t.Fatal("BudgetExhausted = true at 101 points")
	}
	if got := c.RetryAfter(); got != 30*time.Second {
		t.Fatalf("RetryAfter = %v, want 30s", got)
	}
	if !c.LimitReached() {
		t.Fatal("LimitReached = false under secondary limit")
	}

	c.now = func() time.Time { return base.Add(31 * time.Second) }
	if got := c.RetryAfter(); got != 0 {
		t.Fatalf("RetryAfter after wait = %v, want 0", got)
	}
	if c.LimitReached() {
		t.Fatal("LimitReached = true after retry-after expired")
	}
}

func TestCounterResetsAtHourBoundary(t *testing.T) {
	c, _, _ := newTestCounter(t, 0)
	base := time.Now()
	c.now = func() time.Time { return base }

	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("x-ratelimit-used", "200")
	resp.Header.Set("x-ratelimit-reset", strconv.FormatInt(base.Add(time.Hour).Unix(), 10))
	c.Observe(resp, 0)
	if got := c.Used(); got != 200 {
		t.Fatalf("Used before reset = %d, want 200", got)
	}

	c.now = func() time.Time { return base.Add(time.Hour + time.Second) }
	if got := c.Add(5); got != 5 {
		t.Fatalf("Used after reset = %d, want 5", got)
	}
}

// TestWebhookRedeliveryHasNoSecondEffect covers the acceptance criterion of
// T3.34: delivering the same event.id again leaves state and effect count
// untouched and does not call the state source a second time.
func TestWebhookRedeliveryHasNoSecondEffect(t *testing.T) {
	var reads atomic.Int64
	source := func(ctx context.Context, ev WebhookEvent) (domain.Snapshot, error) {
		reads.Add(1)
		return domain.Snapshot{State: "merged", HeadSHA: "sha-1"}, nil
	}
	p := NewWebhookProcessor(nil, source)
	ev := WebhookEvent{ID: "evt-1", Type: "pull_request"}

	ok, err := p.Handle(context.Background(), ev)
	if err != nil || !ok {
		t.Fatalf("first delivery: ok=%v err=%v, want true, nil", ok, err)
	}
	if got := p.Effects(); got != 1 {
		t.Fatalf("Effects = %d, want 1", got)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("source reads = %d, want 1", got)
	}
	snap, has := p.State()
	if !has || snap.State != "merged" {
		t.Fatalf("State = (%q, %v), want (merged, true)", snap.State, has)
	}

	// Three redeliveries of the same id add no effect and no read.
	for i := 0; i < 3; i++ {
		ok, err = p.Handle(context.Background(), ev)
		if err != nil || ok {
			t.Fatalf("redelivery %d: ok=%v err=%v, want false, nil", i, ok, err)
		}
	}
	if got := p.Effects(); got != 1 {
		t.Fatalf("Effects after redeliveries = %d, want 1", got)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("source reads after redeliveries = %d, want 1", got)
	}
	snap, _ = p.State()
	if snap.State != "merged" {
		t.Fatalf("State after redeliveries = %q, want merged", snap.State)
	}
}

// TestWebhookStateFromSourceNotEventOrder proves ADR 0034 §4: state comes from
// the authoritative source at handling time, not from the event payload or the
// order events arrived. A redelivery of an old id cannot restore old state.
func TestWebhookStateFromSourceNotEventOrder(t *testing.T) {
	current := "open"
	source := func(ctx context.Context, ev WebhookEvent) (domain.Snapshot, error) {
		return domain.Snapshot{State: current}, nil
	}
	p := NewWebhookProcessor(nil, source)

	if ok, err := p.Handle(context.Background(), WebhookEvent{ID: "evt-a", Type: "opened"}); !ok || err != nil {
		t.Fatalf("event a: ok=%v err=%v", ok, err)
	}
	snap, _ := p.State()
	if snap.State != "open" {
		t.Fatalf("State after a = %q, want open", snap.State)
	}

	// The source moves on. A different id reads the newer state even though the
	// event itself carries no state.
	current = "merged"
	if ok, err := p.Handle(context.Background(), WebhookEvent{ID: "evt-b", Type: "closed"}); !ok || err != nil {
		t.Fatalf("event b: ok=%v err=%v", ok, err)
	}
	snap, _ = p.State()
	if snap.State != "merged" {
		t.Fatalf("State after b = %q, want merged", snap.State)
	}

	// Redelivering the older id is dropped, so it cannot roll state back.
	if ok, err := p.Handle(context.Background(), WebhookEvent{ID: "evt-a", Type: "opened"}); ok || err != nil {
		t.Fatalf("redelivery of a: ok=%v err=%v, want false, nil", ok, err)
	}
	snap, _ = p.State()
	if snap.State != "merged" {
		t.Fatalf("State after redelivery of a = %q, want merged", snap.State)
	}
	if got := p.Effects(); got != 2 {
		t.Fatalf("Effects = %d, want 2", got)
	}
}

// TestWebhookFailedFetchIsNotCached follows the house rule used for
// clientRequestId: a failed source read produced no effect, so its id stays
// unrecorded and a redelivery may retry (ADR 0034 §4).
func TestWebhookFailedFetchIsNotCached(t *testing.T) {
	var reads atomic.Int64
	source := func(ctx context.Context, ev WebhookEvent) (domain.Snapshot, error) {
		if reads.Add(1) == 1 {
			return domain.Snapshot{}, errors.New("GET failed")
		}
		return domain.Snapshot{State: "merged"}, nil
	}
	p := NewWebhookProcessor(nil, source)
	ev := WebhookEvent{ID: "evt-1", Type: "pull_request"}

	ok, err := p.Handle(context.Background(), ev)
	if err == nil || ok {
		t.Fatalf("first delivery: ok=%v err=%v, want false, error", ok, err)
	}
	if got := p.Effects(); got != 0 {
		t.Fatalf("Effects after failure = %d, want 0", got)
	}
	if _, has := p.State(); has {
		t.Fatal("State present after a failed read, want none")
	}

	ok, err = p.Handle(context.Background(), ev)
	if err != nil || !ok {
		t.Fatalf("retry: ok=%v err=%v, want true, nil", ok, err)
	}
	if got := p.Effects(); got != 1 {
		t.Fatalf("Effects after retry = %d, want 1", got)
	}
	snap, has := p.State()
	if !has || snap.State != "merged" {
		t.Fatalf("State after retry = (%q, %v), want (merged, true)", snap.State, has)
	}
}

// TestWebhookWithoutIDIsDropped checks that an event that cannot be keyed is
// ignored before the source is touched.
func TestWebhookWithoutIDIsDropped(t *testing.T) {
	var reads atomic.Int64
	source := func(ctx context.Context, ev WebhookEvent) (domain.Snapshot, error) {
		reads.Add(1)
		return domain.Snapshot{State: "merged"}, nil
	}
	p := NewWebhookProcessor(nil, source)

	ok, err := p.Handle(context.Background(), WebhookEvent{Type: "pull_request"})
	if err != nil || ok {
		t.Fatalf("empty-id delivery: ok=%v err=%v, want false, nil", ok, err)
	}
	if got := reads.Load(); got != 0 {
		t.Fatalf("source reads = %d, want 0", got)
	}
	if got := p.Effects(); got != 0 {
		t.Fatalf("Effects = %d, want 0", got)
	}
}

// TestWebhookConcurrentRedeliverySingleEffect checks that racing deliveries of
// one event.id produce exactly one effect.
func TestWebhookConcurrentRedeliverySingleEffect(t *testing.T) {
	var reads atomic.Int64
	source := func(ctx context.Context, ev WebhookEvent) (domain.Snapshot, error) {
		reads.Add(1)
		time.Sleep(time.Millisecond)
		return domain.Snapshot{State: "merged"}, nil
	}
	p := NewWebhookProcessor(nil, source)

	const n = 16
	var wg sync.WaitGroup
	var accepted atomic.Int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := p.Handle(context.Background(), WebhookEvent{ID: "evt-race", Type: "pull_request"})
			if err != nil {
				t.Errorf("Handle: %v", err)
			}
			if ok {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := accepted.Load(); got != 1 {
		t.Fatalf("accepted deliveries = %d, want 1", got)
	}
	if got := p.Effects(); got != 1 {
		t.Fatalf("Effects = %d, want 1", got)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("source reads = %d, want 1", got)
	}
}

// TestWebhookEventKeySeparateFromClientRequestID mirrors identity_test.go: the
// event.id index is namespaced away from the ADR 0028 clientRequestId and from
// the ADR 0025 cursor, even when the raw values match.
func TestWebhookEventKeySeparateFromClientRequestID(t *testing.T) {
	if got := webhookEventKey("req-1"); got == "req-1" {
		t.Fatalf("event key = %q, want a namespaced key", got)
	}
	if a, b := webhookEventKey("req-1"), webhookEventKey("req-2"); a == b {
		t.Fatalf("distinct event ids share a key: %q", a)
	}
	if a, b := webhookEventKey("req-1"), domain.IdempotencyKey("client-request", "req-1"); a == b {
		t.Fatalf("event.id key collides with clientRequestId key: %q", a)
	}
}
