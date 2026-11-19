package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
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
