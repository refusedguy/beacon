package ghci

import (
	"context"
	"errors"
	"testing"
	"time"
)

const (
	testViewJSON   = `{"state":"OPEN","headRefOid":"abc123","baseRefName":"main"}`
	testChecksJSON = `[{"name":"build","state":"PASS","link":"https://example.test/build"}]`
)

// testGHRunner counts gh invocations and routes view and checks to payloads.
// fail toggles a transient checks failure without rebuilding the provider.
func testGHRunner(calls *int, fail *bool) Runner {
	return func(_ context.Context, args ...string) ([]byte, error) {
		*calls = *calls + 1
		if len(args) >= 2 && args[1] == "view" {
			return []byte(testViewJSON), nil
		}
		if *fail {
			return nil, errors.New("exit status 1")
		}
		return []byte(testChecksJSON), nil
	}
}

func assertThrottled(t *testing.T, p *Provider, ctx context.Context, repo string, pr int, calls int, wantFails int) throttleError {
	t.Helper()
	_, err := p.PRState(ctx, repo, pr)
	var te throttleError
	if !errors.As(err, &te) {
		t.Fatalf("PRState in backoff: err=%v, want throttleError", err)
	}
	if te.fails != wantFails {
		t.Errorf("throttleError.fails = %d, want %d", te.fails, wantFails)
	}
	if te.wait <= 0 {
		t.Errorf("throttleError.wait = %s, want positive", te.wait)
	}
	return te
}

func TestGhCheckConclusion(t *testing.T) {
	cases := map[string]string{
		"PASS": "SUCCESS", "SUCCESS": "SUCCESS",
		"SKIPPED": "SUCCESS", "NEUTRAL": "SUCCESS",
		"FAIL": "FAILURE", "ERROR": "FAILURE", "TIMED_OUT": "FAILURE",
		"CANCELLED": "CANCELLED",
		"PENDING":   "PENDING", "IN_PROGRESS": "PENDING", "": "PENDING",
	}
	for in, want := range cases {
		if got := ghCheckConclusion(in); got != want {
			t.Errorf("ghCheckConclusion(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestProviderBackoffJitterSpreadsRetries is the acceptance check for CV-03.
// Two providers fail at the same instant but different jitter delays make one
// eligible while the other is still waiting.
func TestProviderBackoffJitterSpreadsRetries(t *testing.T) {
	now := time.Unix(100, 0)
	fail := true
	callsA, callsB := 0, 0
	mk := func(jitter func() time.Duration, calls *int) *Provider {
		return New(Config{
			Backoff: 10 * time.Second, MaxBackoff: 60 * time.Second,
			Now: func() time.Time { return now },
			Run: testGHRunner(calls, &fail), Jitter: jitter,
		})
	}
	a := mk(func() time.Duration { return time.Second }, &callsA)
	b := mk(func() time.Duration { return 4 * time.Second }, &callsB)
	ctx := context.Background()

	if _, err := a.PRState(ctx, "o/r", 1); err == nil || callsA != 2 {
		t.Fatalf("first failure A: err=%v ghCalls=%d, want error/2", err, callsA)
	}
	if _, err := b.PRState(ctx, "o/r", 1); err == nil || callsB != 2 {
		t.Fatalf("first failure B: err=%v ghCalls=%d, want error/2", err, callsB)
	}

	now = now.Add(12 * time.Second)
	if _, err := a.PRState(ctx, "o/r", 1); err == nil || callsA != 4 {
		t.Fatalf("A at its deadline: err=%v ghCalls=%d, want error/4", err, callsA)
	}
	te := assertThrottled(t, b, ctx, "o/r", 1, callsB, 1)
	if te.wait != 2*time.Second {
		t.Errorf("B wait = %s, want 2s", te.wait)
	}
	if callsB != 2 {
		t.Errorf("B ghCalls = %d, want 2 while it waits", callsB)
	}
}

// TestProviderBackoffWithoutJitterKeepsCeiling is the negative control. Zero
// jitter reproduces the old doubling schedule and the maxBackoff cap. The
// retry budget is raised because this test needs four failures to reach the
// capped deadline, while CV-08 caps production at three.
func TestProviderBackoffWithoutJitterKeepsCeiling(t *testing.T) {
	now := time.Unix(200, 0)
	fail, calls := true, 0
	p := New(Config{
		Backoff: 5 * time.Second, MaxBackoff: 12 * time.Second,
		MaxConsecutiveErrors: 10,
		Now:                  func() time.Time { return now },
		Run:                  testGHRunner(&calls, &fail),
		Jitter:               func() time.Duration { return 0 },
	})
	ctx := context.Background()

	if _, err := p.PRState(ctx, "o/r", 2); err == nil || calls != 2 {
		t.Fatalf("first failure: err=%v ghCalls=%d, want error/2", err, calls)
	}
	now = now.Add(4 * time.Second)
	assertThrottled(t, p, ctx, "o/r", 2, calls, 1)
	if calls != 2 {
		t.Errorf("ghCalls inside first backoff = %d, want 2", calls)
	}
	now = now.Add(time.Second)
	if _, err := p.PRState(ctx, "o/r", 2); err == nil || calls != 4 {
		t.Fatalf("after 5s: err=%v ghCalls=%d, want error/4", err, calls)
	}
	now = now.Add(10 * time.Second)
	if _, err := p.PRState(ctx, "o/r", 2); err == nil || calls != 6 {
		t.Fatalf("after doubled 10s: err=%v ghCalls=%d, want error/6", err, calls)
	}
	now = now.Add(11 * time.Second)
	assertThrottled(t, p, ctx, "o/r", 2, calls, 3)
	if calls != 6 {
		t.Errorf("ghCalls before capped deadline = %d, want 6", calls)
	}
	now = now.Add(time.Second)
	if _, err := p.PRState(ctx, "o/r", 2); err == nil || calls != 8 {
		t.Fatalf("after capped 12s: err=%v ghCalls=%d, want error/8", err, calls)
	}
}

// TestProviderBackoffRecoveryClearsErrors moved here from the cibridge suite.
// A success after the backoff clears both the error count and the wait.
func TestProviderBackoffRecoveryClearsErrors(t *testing.T) {
	now := time.Unix(300, 0)
	fail, calls := true, 0
	p := New(Config{
		Backoff: 5 * time.Second, MaxBackoff: 20 * time.Second,
		Now:    func() time.Time { return now },
		Run:    testGHRunner(&calls, &fail),
		Jitter: func() time.Duration { return 0 },
	})
	ctx := context.Background()

	if _, err := p.PRState(ctx, "o/r", 3); err == nil || calls != 2 {
		t.Fatalf("first failure: err=%v ghCalls=%d, want error/2", err, calls)
	}
	now = now.Add(6 * time.Second)
	if _, err := p.PRState(ctx, "o/r", 3); err == nil || calls != 4 {
		t.Fatalf("second failure: err=%v ghCalls=%d, want error/4", err, calls)
	}

	fail = false
	now = now.Add(11 * time.Second)
	state, err := p.PRState(ctx, "o/r", 3)
	if err != nil || calls != 6 || len(state.Checks) != 1 {
		t.Fatalf("recovery: err=%v ghCalls=%d checks=%d, want nil/6/1", err, calls, len(state.Checks))
	}
	if _, err := p.PRState(ctx, "o/r", 3); err != nil || calls != 6 {
		t.Fatalf("recovery cache hit: err=%v ghCalls=%d, want nil/6", err, calls)
	}
}

// TestProviderRetryBudgetExhausted proves the default budget of three failed
// observations stops gh polling. The third failure returns a distinct error
// and a fourth call starts no gh process. Jitter is fixed at zero so the
// deadline math stays deterministic.
func TestProviderRetryBudgetExhausted(t *testing.T) {
	now := time.Unix(400, 0)
	calls, fail := 0, true
	p := New(Config{
		Now:    func() time.Time { return now },
		Run:    testGHRunner(&calls, &fail),
		Jitter: func() time.Duration { return 0 },
	})
	ctx := context.Background()

	for i, step := range []time.Duration{0, 5 * time.Second, 10 * time.Second} {
		now = now.Add(step)
		_, err := p.PRState(ctx, "o/r", 11)
		if err == nil {
			t.Fatalf("failure %d: got nil error, want a transient error", i+1)
		}
		var exhausted BudgetExhaustedError
		wantExhausted := i == 2
		if got := errors.As(err, &exhausted); got != wantExhausted {
			t.Fatalf("failure %d: errors.As(BudgetExhaustedError) = %v, want %v (err=%v)", i+1, got, wantExhausted, err)
		}
	}
	if calls != 6 {
		t.Fatalf("gh calls after three failures = %d, want 6", calls)
	}

	_, err := p.PRState(ctx, "o/r", 11)
	var exhausted BudgetExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("fourth PRState error = %v, want BudgetExhaustedError", err)
	}
	if exhausted.Repo != "o/r" || exhausted.PR != 11 || exhausted.Fails != 3 {
		t.Fatalf("exhaustion = %+v, want repo o/r, pr 11, fails 3", exhausted)
	}
	if calls != 6 {
		t.Fatalf("fourth PRState started gh: calls = %d, want 6", calls)
	}
}

// TestProviderRetryBudgetResetsOnSuccess is the negative control: a success
// after two failures clears the count, so the budget is not carried over.
func TestProviderRetryBudgetResetsOnSuccess(t *testing.T) {
	now := time.Unix(500, 0)
	calls, fail := 0, true
	p := New(Config{
		Now:    func() time.Time { return now },
		Run:    testGHRunner(&calls, &fail),
		Jitter: func() time.Duration { return 0 },
	})
	ctx := context.Background()

	failAt := func(step time.Duration, wantExhausted bool) {
		t.Helper()
		now = now.Add(step)
		_, err := p.PRState(ctx, "o/r", 12)
		if err == nil {
			t.Fatal("PRState returned nil error, want a failure")
		}
		var exhausted BudgetExhaustedError
		if got := errors.As(err, &exhausted); got != wantExhausted {
			t.Fatalf("errors.As(BudgetExhaustedError) = %v, want %v (err=%v)", got, wantExhausted, err)
		}
	}

	failAt(0, false)
	failAt(5*time.Second, false)

	fail = false
	now = now.Add(10 * time.Second)
	state, err := p.PRState(ctx, "o/r", 12)
	if err != nil {
		t.Fatalf("PRState after recovery: %v", err)
	}
	if state.HeadRefOid != "abc123" {
		t.Fatalf("PRState after recovery: head = %q, want abc123", state.HeadRefOid)
	}

	fail = true
	// Step past the 20s cache TTL, otherwise the cached success short-circuits
	// the first failure and returns no error.
	failAt(21*time.Second, false) // count 1 after reset
	failAt(5*time.Second, false)  // count 2 after reset, still under budget
	failAt(10*time.Second, true)  // count 3 reaches the budget
}

func TestIsRetryableGH(t *testing.T) {
	cases := map[string]bool{
		"gh: 401 Bad credentials":              false,
		"gh: 403 Resource not accessible":      false,
		"gh: 404 Not Found":                    false,
		"gh: invalid argument --repo":          false,
		"gh pr view decode: unexpected end":    false,
		"gh: tls handshake timeout":            true,
		"gh: bad gateway (HTTP 502)":           true,
		"gh: API rate limit exceeded":          true,
		"gh pr view: gh: exit status 1":        true,
		"gh pr view: gh: something unexpected": true,
	}
	for text, want := range cases {
		if got := isRetryableGH(errors.New(text)); got != want {
			t.Errorf("isRetryableGH(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestPermanentGHErrorKeepsBackoffState(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	calls := 0
	p := New(Config{
		Now:        func() time.Time { return now },
		Backoff:    time.Second,
		MaxBackoff: time.Minute,
		Run: func(context.Context, ...string) ([]byte, error) {
			calls++
			return nil, errors.New("gh: 401 Bad credentials (requires authentication)")
		},
	})

	_, err := p.PRState(context.Background(), "o/r", 7)
	if err == nil {
		t.Fatal("PRState returned nil error for a permanent gh failure")
	}
	if !isPermanentError(err) {
		t.Errorf("error %q was not classified as permanent", err)
	}
	e := p.entries["o/r#7"]
	if e.consecutiveErrors != 0 {
		t.Errorf("consecutiveErrors = %d, want 0", e.consecutiveErrors)
	}
	if e.backoff != 0 {
		t.Errorf("backoff = %s, want 0", e.backoff)
	}
	if !e.nextAllowed.IsZero() {
		t.Errorf("nextAllowed = %s, want zero", e.nextAllowed)
	}
	if calls != 1 {
		t.Errorf("gh calls = %d, want 1", calls)
	}
}

func TestTransientGHErrorStillBacksOff(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	p := New(Config{
		Now:        func() time.Time { return now },
		Backoff:    time.Second,
		MaxBackoff: time.Minute,
		Run: func(context.Context, ...string) ([]byte, error) {
			return nil, errors.New("gh: exit status 1")
		},
	})

	_, err := p.PRState(context.Background(), "o/r", 8)
	if err == nil {
		t.Fatal("PRState returned nil error for a transient gh failure")
	}
	if isPermanentError(err) {
		t.Errorf("transient error %q was classified as permanent", err)
	}
	e := p.entries["o/r#8"]
	if e.consecutiveErrors != 1 {
		t.Errorf("consecutiveErrors = %d, want 1", e.consecutiveErrors)
	}
	if e.backoff != time.Second {
		t.Errorf("backoff = %s, want 1s", e.backoff)
	}
	if want := now.Add(time.Second); !e.nextAllowed.Equal(want) {
		t.Errorf("nextAllowed = %s, want %s", e.nextAllowed, want)
	}
}

func TestConfiguredClassifierOverridesDefault(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	p := New(Config{
		Now:     func() time.Time { return now },
		Backoff: time.Second,
		Run: func(context.Context, ...string) ([]byte, error) {
			return nil, errors.New("gh: exit status 1")
		},
		Classify: func(error) bool { return false },
	})

	_, err := p.PRState(context.Background(), "o/r", 9)
	if err == nil || !isPermanentError(err) {
		t.Fatalf("custom classifier was not applied: err = %v", err)
	}
	e := p.entries["o/r#9"]
	if e.consecutiveErrors != 0 || e.backoff != 0 || !e.nextAllowed.IsZero() {
		t.Errorf("pacing state changed on a custom permanent error: %+v", e)
	}
}
