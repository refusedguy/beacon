// Package github holds the GitHub adapter for Beacon.
//
// This file normalizes GitHub error responses into the shared domain.AppError
// shape. Three sources funnel into RATE_LIMITED: GraphQL 200-with-errors with
// x-ratelimit-remaining:0, REST 403/429, and the secondary limit carried by
// retry-after or x-ratelimit-reset. Server timeouts (502/504) are a retry
// decision with backoff, not an AppError. Header values are authoritative
// over hints in the response body.
package github

import (
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	baseBackoff = 100 * time.Millisecond
	maxBackoff  = 5 * time.Second
	maxJitter   = time.Second
)

// Result is the normalized classification of a GitHub response.
//
// Exactly one side is set: Err for a RATE_LIMITED response the caller must
// surface, or Retry/Backoff for a 502/504 the caller retries with backoff.
// A healthy response leaves both unset.
type Result struct {
	Err     *domain.AppError
	Retry   bool
	Backoff time.Duration
}

// Normalizer classifies GitHub responses. Jitter spreads retries so a fleet
// of clients does not hit GitHub again in lockstep. The clock and jitter are
// fields so tests can fix them and assert exact retryAfterMs values.
type Normalizer struct {
	now    func() time.Time
	jitter func() time.Duration
}

// NewNormalizer returns a Normalizer backed by the wall clock and random jitter.
func NewNormalizer() *Normalizer {
	return &Normalizer{now: time.Now, jitter: defaultJitter}
}

// Normalize inspects a GitHub response and returns how the caller should
// handle it. Header values win over body hints for the retry delay.
func (n *Normalizer) Normalize(status int, header http.Header, body []byte) Result {
	switch {
	case status == http.StatusBadGateway || status == http.StatusGatewayTimeout:
		return Result{Retry: true, Backoff: n.Backoff(1)}
	case status == http.StatusTooManyRequests:
		return n.rateLimited("GitHub REST 429", header, body)
	case status == http.StatusForbidden:
		if rateLimitSignaled(header, body) {
			return n.rateLimited("GitHub REST 403", header, body)
		}
		return Result{}
	case status == http.StatusOK:
		if hasGraphQLErrors(body) && remainingIsZero(header) {
			return n.rateLimited("GitHub GraphQL rate limit", header, body)
		}
		if n.delay(header, body) > 0 {
			return n.rateLimited("GitHub secondary rate limit", header, body)
		}
		return Result{}
	default:
		return Result{}
	}
}

// Backoff returns the delay before the next attempt of a 502/504 response.
// attempt is 1-based; the delay doubles until maxBackoff, then jitter is added.
func (n *Normalizer) Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := baseBackoff
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= maxBackoff {
			d = maxBackoff
			break
		}
	}
	return d + n.jitterOr()
}

func (n *Normalizer) rateLimited(message string, header http.Header, body []byte) Result {
	err := domain.NewAppError(domain.CodeRateLimited, message)
	if ms := n.retryAfterMs(n.delay(header, body)); ms != nil {
		err.RetryAfterMs = ms
	}
	return Result{Err: err}
}

// retryAfterMs converts a base delay into the MCP retryAfterMs field, adding
// jitter. A non-positive base yields no field.
func (n *Normalizer) retryAfterMs(base time.Duration) *int64 {
	if base <= 0 {
		return nil
	}
	ms := base.Milliseconds() + n.jitterOr().Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return &ms
}

// delay returns the wait before retrying. x-ratelimit-* and retry-after
// headers take priority over hints in the body, matching ADR 0013.
func (n *Normalizer) delay(header http.Header, body []byte) time.Duration {
	if d := headerDelay(header, n.nowOr()()); d > 0 {
		return d
	}
	return bodyDelay(body, n.nowOr()())
}

func rateLimitSignaled(header http.Header, body []byte) bool {
	return headerValue(header, "retry-after") != "" ||
		headerValue(header, "x-ratelimit-reset") != "" ||
		remainingIsZero(header) ||
		bodyDelay(body, time.Now()) > 0
}

func remainingIsZero(header http.Header) bool {
	return headerValue(header, "x-ratelimit-remaining") == "0"
}

func hasGraphQLErrors(body []byte) bool {
	var doc struct {
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return false
	}
	return len(doc.Errors) > 0
}

func headerDelay(h http.Header, now time.Time) time.Duration {
	if v := headerValue(h, "retry-after"); v != "" {
		if sec, err := strconv.Atoi(v); err == nil && sec > 0 {
			return time.Duration(sec) * time.Second
		}
	}
	if v := headerValue(h, "x-ratelimit-reset"); v != "" {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
			if d := time.Unix(sec, 0).Sub(now); d > 0 {
				return d
			}
		}
	}
	return 0
}

func bodyDelay(body []byte, now time.Time) time.Duration {
	var doc struct {
		RetryAfter   *float64 `json:"retry_after"`
		RetryAfterMs *int64   `json:"retryAfterMs"`
		Reset        int64    `json:"reset"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return 0
	}
	if doc.RetryAfter != nil && *doc.RetryAfter > 0 {
		return time.Duration(*doc.RetryAfter * float64(time.Second))
	}
	if doc.RetryAfterMs != nil && *doc.RetryAfterMs > 0 {
		return time.Duration(*doc.RetryAfterMs) * time.Millisecond
	}
	if doc.Reset > 0 {
		if d := time.Unix(doc.Reset, 0).Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func (n *Normalizer) nowOr() func() time.Time {
	if n.now == nil {
		return time.Now
	}
	return n.now
}

func (n *Normalizer) jitterOr() time.Duration {
	if n.jitter == nil {
		return defaultJitter()
	}
	return n.jitter()
}

func defaultJitter() time.Duration {
	return time.Duration(rand.Int64N(int64(maxJitter) + 1))
}
