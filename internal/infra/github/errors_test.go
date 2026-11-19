package github

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// testNormalizer fixes the clock and jitter so retryAfterMs values are exact.
func testNormalizer(now time.Time, jitter time.Duration) *Normalizer {
	return &Normalizer{
		now:    func() time.Time { return now },
		jitter: func() time.Duration { return jitter },
	}
}

func header(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

func TestNormalizeRateLimitSources(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	jitter := 250 * time.Millisecond
	n := testNormalizer(now, jitter)

	resetIn := func(d time.Duration) string {
		return strconv.FormatInt(now.Add(d).Unix(), 10)
	}

	cases := []struct {
		name           string
		status         int
		header         http.Header
		body           string
		wantRetryAfter int64
	}{
		{
			name:           "graphql 200 with errors and remaining 0",
			status:         http.StatusOK,
			header:         header("x-ratelimit-remaining", "0", "x-ratelimit-reset", resetIn(60*time.Second)),
			body:           `{"errors":[{"message":"API rate limit exceeded"}]}`,
			wantRetryAfter: (60 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
		{
			name:           "REST 429 with retry-after",
			status:         http.StatusTooManyRequests,
			header:         header("retry-after", "30"),
			wantRetryAfter: (30 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
		{
			name:           "REST 403 with retry-after",
			status:         http.StatusForbidden,
			header:         header("retry-after", "60"),
			wantRetryAfter: (60 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
		{
			name:           "secondary 200 with retry-after",
			status:         http.StatusOK,
			header:         header("retry-after", "45"),
			wantRetryAfter: (45 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
		{
			name:           "secondary 200 with x-ratelimit-reset",
			status:         http.StatusOK,
			header:         header("x-ratelimit-reset", resetIn(20*time.Second)),
			wantRetryAfter: (20 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
		{
			name:           "REST 429 with body retry hint",
			status:         http.StatusTooManyRequests,
			body:           `{"retry_after":10}`,
			wantRetryAfter: (10 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := n.Normalize(tc.status, tc.header, []byte(tc.body))
			if got.Retry {
				t.Fatal("Retry = true, want RATE_LIMITED error")
			}
			if got.Err == nil {
				t.Fatal("Err = nil, want RATE_LIMITED")
			}
			if got.Err.Code != domain.CodeRateLimited {
				t.Fatalf("Code = %s, want %s", got.Err.Code, domain.CodeRateLimited)
			}
			if got.Err.Status != http.StatusTooManyRequests {
				t.Fatalf("Status = %d, want 429", got.Err.Status)
			}
			if got.Err.RetryAfterMs == nil {
				t.Fatal("RetryAfterMs = nil, want a value with jitter")
			}
			if *got.Err.RetryAfterMs != tc.wantRetryAfter {
				t.Fatalf("RetryAfterMs = %d, want %d", *got.Err.RetryAfterMs, tc.wantRetryAfter)
			}
		})
	}
}

func TestNormalizeRetriesServerErrors(t *testing.T) {
	n := testNormalizer(time.Now(), 0)

	cases := []struct {
		name       string
		status     int
		header     http.Header
		body       string
		wantRetry  bool
		wantAppErr bool
	}{
		{name: "502 bad gateway", status: http.StatusBadGateway, wantRetry: true},
		{name: "504 gateway timeout", status: http.StatusGatewayTimeout, wantRetry: true},
		{name: "200 healthy", status: http.StatusOK, body: `{"data":{}}`},
		{
			name:   "200 graphql error without remaining 0",
			status: http.StatusOK,
			header: header("x-ratelimit-remaining", "42"),
			body:   `{"errors":[{"message":"bad query"}]}`,
		},
		{name: "403 without rate signal", status: http.StatusForbidden},
		{name: "500 is not retried", status: http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := n.Normalize(tc.status, tc.header, []byte(tc.body))
			if got.Retry != tc.wantRetry {
				t.Fatalf("Retry = %v, want %v", got.Retry, tc.wantRetry)
			}
			if got.Retry && got.Backoff <= 0 {
				t.Fatalf("Backoff = %v, want positive backoff on retry", got.Backoff)
			}
			if (got.Err != nil) != tc.wantAppErr {
				t.Fatalf("Err = %v, wantAppErr = %v", got.Err, tc.wantAppErr)
			}
		})
	}
}

func TestNormalizeHeaderOverridesBody(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	jitter := 250 * time.Millisecond
	n := testNormalizer(now, jitter)

	cases := []struct {
		name           string
		status         int
		header         http.Header
		body           string
		wantRetryAfter int64
	}{
		{
			name:           "retry-after header beats body retry_after",
			status:         http.StatusTooManyRequests,
			header:         header("retry-after", "90"),
			body:           `{"retry_after":5}`,
			wantRetryAfter: (90 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
		{
			name:           "x-ratelimit-reset header beats body reset",
			status:         http.StatusOK,
			header:         header("x-ratelimit-reset", strconv.FormatInt(now.Add(90*time.Second).Unix(), 10)),
			body:           `{"reset":` + strconv.FormatInt(now.Add(5*time.Second).Unix(), 10) + `}`,
			wantRetryAfter: (90 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := n.Normalize(tc.status, tc.header, []byte(tc.body))
			if got.Err == nil || got.Err.RetryAfterMs == nil {
				t.Fatalf("want RATE_LIMITED with retryAfterMs, got %+v", got)
			}
			if *got.Err.RetryAfterMs != tc.wantRetryAfter {
				t.Fatalf("RetryAfterMs = %d, want %d (header value, not body)",
					*got.Err.RetryAfterMs, tc.wantRetryAfter)
			}
		})
	}
}

func TestNormalizeNoRetryAfterWithoutDelay(t *testing.T) {
	n := testNormalizer(time.Now(), 0)
	got := n.Normalize(http.StatusTooManyRequests, http.Header{}, nil)
	if got.Err == nil {
		t.Fatal("429 with no delay is still RATE_LIMITED")
	}
	if got.Err.RetryAfterMs != nil {
		t.Fatalf("RetryAfterMs = %v, want nil without a delay", *got.Err.RetryAfterMs)
	}
}

func TestBackoffDoublesAndCaps(t *testing.T) {
	n := testNormalizer(time.Now(), 300*time.Millisecond)

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 100*time.Millisecond + 300*time.Millisecond},
		{attempt: 2, want: 200*time.Millisecond + 300*time.Millisecond},
		{attempt: 3, want: 400*time.Millisecond + 300*time.Millisecond},
		{attempt: 0, want: 100*time.Millisecond + 300*time.Millisecond},
	}
	for _, tc := range cases {
		if got := n.Backoff(tc.attempt); got != tc.want {
			t.Errorf("Backoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
	if got := n.Backoff(8); got < maxBackoff {
		t.Errorf("Backoff(8) = %v, want at least maxBackoff %v", got, maxBackoff)
	}
}
