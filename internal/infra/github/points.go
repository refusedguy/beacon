// Package github holds the GitHub adapter for Beacon.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// BudgetPerHour is the hard point budget shared by the REST and GraphQL clients.
const BudgetPerHour = 500

const rateLimitPath = "/rate_limit"

// observation is the typed form of the point data read at the HTTP boundary.
// A negative used means the source did not report an absolute point total.
type observation struct {
	used       int
	resetAt    time.Time
	retryAfter time.Duration
}

// Counter tracks points spent against the shared GitHub budget.
// It reads the server value at startup and reconciles it on an interval.
type Counter struct {
	mu             sync.Mutex
	used           int
	resetAt        time.Time
	secondaryUntil time.Time

	client  *http.Client
	baseURL string
	now     func() time.Time
}

// NewCounter reads GET /rate_limit and returns a counter seeded with the
// server's point total. A failure to read the server value fails startup so
// Beacon never starts with a zero counter after a restart.
func NewCounter(ctx context.Context, client *http.Client, baseURL string) (*Counter, error) {
	if client == nil {
		client = http.DefaultClient
	}
	c := &Counter{
		client:  client,
		baseURL: strings.TrimRight(baseURL, "/"),
		now:     time.Now,
	}
	if err := c.Sync(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// Sync replaces the counter with a fresh GET /rate_limit reading.
func (c *Counter) Sync(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+rateLimitPath, nil)
	if err != nil {
		return fmt.Errorf("rate_limit request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("rate_limit GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("rate_limit GET: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("rate_limit body: %w", err)
	}

	obs := observeHeaders(resp.Header)
	fromBody, parseErr := parseRateLimitBody(body)
	if obs.used < 0 {
		if parseErr != nil {
			return parseErr
		}
		obs.used = fromBody.used
	}
	if obs.resetAt.IsZero() {
		obs.resetAt = fromBody.resetAt
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.apply(obs, 0)
	return nil
}

// Reconcile calls Sync on an interval until ctx is done. A failed sync is
// retried on the next tick.
func (c *Counter) Reconcile(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.Sync(ctx)
		}
	}
}

// Add records the cost of a response and returns the new point total.
func (c *Counter) Add(cost int) int {
	if cost < 0 {
		cost = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apply(observation{used: -1}, cost)
	return c.used
}

// Observe records an HTTP response and the cost read from its body. The
// x-ratelimit-* headers take priority: when they report a point total, the
// body cost is ignored. Headers are authoritative for both clients.
func (c *Counter) Observe(resp *http.Response, bodyCost int) int {
	if resp == nil {
		return c.Add(bodyCost)
	}
	if bodyCost < 0 {
		bodyCost = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apply(observeHeaders(resp.Header), bodyCost)
	return c.used
}

// Used returns the points spent in the current hour.
func (c *Counter) Used() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

// Budget returns the shared hourly budget.
func (c *Counter) Budget() int {
	return BudgetPerHour
}

// Remaining returns the points left in the hourly budget, never below zero.
func (c *Counter) Remaining() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used >= BudgetPerHour {
		return 0
	}
	return BudgetPerHour - c.used
}

// BudgetExhausted reports whether the hourly budget is spent.
func (c *Counter) BudgetExhausted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used >= BudgetPerHour
}

// RetryAfter returns the wait remaining on a secondary limit, or zero when
// the client is not throttled. The secondary limit is not visible through
// GET /rate_limit, so it is read from the retry-after header.
func (c *Counter) RetryAfter() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.secondaryUntil.IsZero() {
		return 0
	}
	left := c.secondaryUntil.Sub(c.now())
	if left <= 0 {
		return 0
	}
	return left
}

// LimitReached is the flag read by the tier-1 poller to stretch its interval.
// It is set when the hourly budget is spent or a secondary limit is active.
func (c *Counter) LimitReached() bool {
	return c.BudgetExhausted() || c.RetryAfter() > 0
}

// ResetAt returns the hour boundary reported by GitHub, if any.
func (c *Counter) ResetAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resetAt
}

// apply mutates state and must be called with mu held.
func (c *Counter) apply(obs observation, bodyCost int) {
	now := c.now()
	if !c.resetAt.IsZero() && !now.Before(c.resetAt) {
		c.used = 0
		c.secondaryUntil = time.Time{}
	}
	if obs.used >= 0 {
		c.used = obs.used
	} else {
		c.used += bodyCost
	}
	if !obs.resetAt.IsZero() {
		c.resetAt = obs.resetAt
	}
	if obs.retryAfter > 0 {
		c.secondaryUntil = now.Add(obs.retryAfter)
	}
}

// observeHeaders parses the ratelimit and retry-after headers. It accepts
// header maps built directly in tests as well as maps parsed by net/http.
func observeHeaders(h http.Header) observation {
	o := observation{used: -1}
	if v := headerValue(h, "x-ratelimit-used"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			o.used = n
		}
	}
	if o.used < 0 {
		limit, limitErr := strconv.Atoi(headerValue(h, "x-ratelimit-limit"))
		remaining, remainingErr := strconv.Atoi(headerValue(h, "x-ratelimit-remaining"))
		if limitErr == nil && remainingErr == nil {
			o.used = limit - remaining
			if o.used < 0 {
				o.used = 0
			}
		}
	}
	if v := headerValue(h, "x-ratelimit-reset"); v != "" {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
			o.resetAt = time.Unix(sec, 0)
		}
	}
	if v := headerValue(h, "retry-after"); v != "" {
		if sec, err := strconv.Atoi(v); err == nil {
			o.retryAfter = time.Duration(sec) * time.Second
		}
	}
	return o
}

func headerValue(h http.Header, name string) string {
	if v := h.Get(name); v != "" {
		return v
	}
	for key, values := range h {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

type rateResource struct {
	Limit     int   `json:"limit"`
	Used      int   `json:"used"`
	Remaining int   `json:"remaining"`
	Reset     int64 `json:"reset"`
}

func parseRateLimitBody(data []byte) (observation, error) {
	var doc struct {
		Resources struct {
			Core    rateResource `json:"core"`
			GraphQL rateResource `json:"graphql"`
		} `json:"resources"`
		Rate rateResource `json:"rate"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return observation{}, fmt.Errorf("parse rate_limit: %w", err)
	}
	r := doc.Resources.Core
	if r == (rateResource{}) {
		r = doc.Rate
	}
	if r == (rateResource{}) {
		return observation{}, errors.New("rate_limit: no core data")
	}
	o := observation{used: r.Used}
	if r.Reset > 0 {
		o.resetAt = time.Unix(r.Reset, 0)
	}
	return o, nil
}

// Compatibility names used by the REST, GraphQL and tier-1 clients.
type PointCounter = Counter
type PointsCounter = Counter

func NewPointCounter(ctx context.Context, client *http.Client, baseURL string) (*Counter, error) {
	return NewCounter(ctx, client, baseURL)
}

func NewPointsCounter(ctx context.Context, client *http.Client, baseURL string) (*Counter, error) {
	return NewCounter(ctx, client, baseURL)
}

func (c *Counter) AddCost(cost int) int { return c.Add(cost) }

func (c *Counter) Record(resp *http.Response, bodyCost int) int {
	return c.Observe(resp, bodyCost)
}

// Artifact source, ADR 0027. A result is read from a git branch, a pull
// request or a file in an isolated worktree. There is deliberately no kind for
// an agent's private state: logs, transcripts and databases are not a public
// contract and are never a Beacon artifact source.

// ArtifactKind is the explicit kind of an artifact source.
type ArtifactKind string

const (
	ArtifactBranch   ArtifactKind = "branch"
	ArtifactPR       ArtifactKind = "pr"
	ArtifactWorktree ArtifactKind = "worktree"
)

// ArtifactSource is a validated, typed locator of a result. Branch and PR
// carry no filesystem path. A worktree file is a slash-separated path relative
// to the worktree root; absolute paths, home-relative paths, parent traversal
// and hidden path segments are rejected so no private agent state can be
// passed as an artifact.
type ArtifactSource struct {
	Kind         ArtifactKind `json:"kind"`
	Branch       string       `json:"branch,omitempty"`
	Repo         string       `json:"repo,omitempty"`
	PR           int          `json:"pr,omitempty"`
	State        string       `json:"state,omitempty"`
	WorktreeFile string       `json:"worktreeFile,omitempty"`
}

// UnmarshalJSON validates the source at the boundary. An unknown kind, a
// missing locator or a path outside a worktree is an error rather than a
// guess, so a private agent log can never become a public artifact source.
func (s *ArtifactSource) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind         string `json:"kind"`
		Branch       string `json:"branch"`
		Repo         string `json:"repo"`
		PR           int    `json:"pr"`
		State        string `json:"state"`
		WorktreeFile string `json:"worktreeFile"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&wire); err != nil {
		return fmt.Errorf("artifact source: decode: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("artifact source: trailing data")
	}

	kind := ArtifactKind(strings.ToLower(strings.TrimSpace(wire.Kind)))
	*s = ArtifactSource{Kind: kind, State: strings.ToUpper(strings.TrimSpace(wire.State))}
	switch kind {
	case ArtifactBranch:
		if wire.Repo != "" || wire.PR != 0 || wire.WorktreeFile != "" || wire.State != "" {
			return errors.New("artifact source: branch carries only a branch name")
		}
		branch := strings.TrimSpace(wire.Branch)
		if !validArtifactName(branch) {
			return fmt.Errorf("artifact source: bad branch %q", wire.Branch)
		}
		s.Branch = branch
	case ArtifactPR:
		if wire.Branch != "" || wire.WorktreeFile != "" {
			return errors.New("artifact source: pr carries only repo, pr and state")
		}
		repo := strings.TrimSpace(wire.Repo)
		if wire.PR <= 0 || !validArtifactRepo(repo) {
			return fmt.Errorf("artifact source: bad pr locator repo=%q pr=%d", wire.Repo, wire.PR)
		}
		s.Repo, s.PR = repo, wire.PR
	case ArtifactWorktree:
		if wire.Branch != "" || wire.Repo != "" || wire.PR != 0 || wire.State != "" {
			return errors.New("artifact source: worktree carries only a worktree file")
		}
		file := strings.TrimSpace(wire.WorktreeFile)
		if !validWorktreeFile(file) {
			return fmt.Errorf("artifact source: bad worktree file %q", wire.WorktreeFile)
		}
		s.WorktreeFile = file
	default:
		return fmt.Errorf("artifact source: unknown kind %q", wire.Kind)
	}
	return nil
}

// Verdict returns the domain verdict implied by the source. Only a PR source
// carries an observed state. A closed or merged PR is terminal, as ADR 0027
// requires, and the result comes from that state alone: no log, transcript or
// database is read.
func (s ArtifactSource) Verdict() domain.Verdict {
	if s.Kind != ArtifactPR {
		return domain.VerdictWatching
	}
	v, _ := domain.Evaluate(domain.Snapshot{State: s.State}, domain.Policy{})
	return v
}

// IsTerminalArtifactVerdict reports whether v closes the Watch.
func IsTerminalArtifactVerdict(v domain.Verdict) bool {
	return v == domain.VerdictMerged || v == domain.VerdictClosed
}

func validArtifactName(v string) bool {
	if v == "" || strings.ContainsAny(v, " \t\r\n\\~") || strings.HasPrefix(v, "-") {
		return false
	}
	for _, seg := range strings.Split(v, "/") {
		if !validSegment(seg) {
			return false
		}
	}
	return true
}

func validArtifactRepo(v string) bool {
	parts := strings.Split(v, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if !validSegment(part) {
			return false
		}
	}
	return true
}

func validWorktreeFile(v string) bool {
	if v == "" || strings.HasPrefix(v, "/") || strings.ContainsAny(v, "\\\x00") {
		return false
	}
	for _, seg := range strings.Split(v, "/") {
		if !validSegment(seg) {
			return false
		}
	}
	return true
}

// validSegment rejects hidden, parent and home-relative path pieces. A hidden
// segment is where agent products keep their private session state, so no
// artifact locator may use one.
func validSegment(seg string) bool {
	return seg != "" && seg != "." && seg != ".." && !strings.HasPrefix(seg, ".") &&
		!strings.ContainsAny(seg, " \t\r\n\\~")
}
