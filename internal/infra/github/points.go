// Package github holds the GitHub adapter for Beacon.
package github

import (
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

// Artifact verdict, ADR 0027. A closed or merged pull request is terminal and
// needs no read of an agent's log, transcript or database. The typed artifact
// locators are validated where receipts are imported; this helper is the
// GitHub-side view of the same rule and stays pure over the observed state.

// ClosedPRVerdict returns the domain verdict for an observed PR state. States
// other than MERGED and CLOSED are not terminal.
func ClosedPRVerdict(state string) domain.Verdict {
	v, _ := domain.Evaluate(domain.Snapshot{State: state}, domain.Policy{})
	return v
}

// IsTerminalArtifactVerdict reports whether v closes the Watch.
func IsTerminalArtifactVerdict(v domain.Verdict) bool {
	return v == domain.VerdictMerged || v == domain.VerdictClosed
}

// Webhook delivery deduplication, ADR 0034 §4.
//
// The deduplication key here is the webhook delivery id event.id. This is a
// different key from the ADR 0025 stream cursor, which marks a position in a
// log, and from the ADR 0028 clientRequestId, which keys a client command. The
// state after an event is read from an authoritative source (GET or a trusted
// snapshot), never from the order in which events arrived or from state the
// event carried with it.

// webhookEventNamespace scopes an event.id so a store-backed cache cannot
// collide with the clientRequestId index (ADR 0028) or the stream cursor
// (ADR 0025) when they share one table.
const webhookEventNamespace = "webhook-event-id"

func webhookEventKey(id string) string {
	return domain.IdempotencyKey(webhookEventNamespace, id)
}

// WebhookEvent is one webhook delivery. It carries the delivery id and the
// event type only. The full resource state is never in the payload; it is read
// through a WebhookStateSource.
type WebhookEvent struct {
	ID   string
	Type string
}

// WebhookStateSource returns the authoritative state of the resource a webhook
// event refers to. An adapter implements it with a GET request to the resource
// or a trusted snapshot. Because the event carries no state, state can never
// be reconstructed from delivery order.
type WebhookStateSource func(ctx context.Context, event WebhookEvent) (domain.Snapshot, error)

// WebhookEventCache is the index of already handled webhook delivery ids.
// Implementations must be safe for concurrent use. A composition root can
// replace the in-memory default with a store-backed index written in the same
// transaction as the resulting state (ADR 0034 §4).
type WebhookEventCache interface {
	// Contains reports whether id is already in the index.
	Contains(id string) bool
	// Add records id in the index.
	Add(id string)
}

// memoryWebhookEventCache keeps one entry per event.id for the lifetime of the
// service.
type memoryWebhookEventCache struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// NewWebhookEventCache builds the in-process event.id cache.
func NewWebhookEventCache() WebhookEventCache {
	return &memoryWebhookEventCache{seen: make(map[string]struct{})}
}

func (m *memoryWebhookEventCache) Contains(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.seen[webhookEventKey(id)]
	return ok
}

func (m *memoryWebhookEventCache) Add(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen[webhookEventKey(id)] = struct{}{}
}

// WebhookProcessor handles webhook deliveries for one bridge. It drops a
// redelivery by event.id before doing any work, and reads the state produced by
// an accepted event from the authoritative source rather than from the event
// payload or the delivery order.
type WebhookProcessor struct {
	cache  WebhookEventCache
	source WebhookStateSource

	mu       sync.Mutex
	state    domain.Snapshot
	hasState bool
	effects  int
}

// NewWebhookProcessor builds a processor over the given cache and state
// source. A nil cache selects a fresh in-process cache.
func NewWebhookProcessor(cache WebhookEventCache, source WebhookStateSource) *WebhookProcessor {
	if cache == nil {
		cache = NewWebhookEventCache()
	}
	return &WebhookProcessor{cache: cache, source: source}
}

// Handle processes one delivery. It returns true when the event was new and
// produced an effect. A redelivery of the same event.id returns false and
// leaves the state and the effect count unchanged. An event without an id is
// dropped because it cannot be deduplicated.
//
// The state stored is whatever source returns at handling time. A later event
// with a different id replaces it with the source's newer value; a redelivery
// of an older id can never restore an older state, because state is never
// derived from the event or from delivery order. The id is recorded only after
// the source read succeeds, so a failed read leaves no effect and a redelivery
// may retry.
func (p *WebhookProcessor) Handle(ctx context.Context, ev WebhookEvent) (bool, error) {
	if ev.ID == "" {
		return false, nil
	}
	if p.source == nil {
		return false, errors.New("webhook: no state source")
	}

	// Hold p.mu across the check, the source read and the record so concurrent
	// redeliveries of one id cannot both pass the cache check.
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache.Contains(ev.ID) {
		return false, nil
	}
	snap, err := p.source(ctx, ev)
	if err != nil {
		return false, err
	}
	p.cache.Add(ev.ID)
	p.state = snap
	p.hasState = true
	p.effects++
	return true, nil
}

// State returns the last authoritative state read from the source and whether
// any event has produced an effect yet.
func (p *WebhookProcessor) State() (domain.Snapshot, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state, p.hasState
}

// Effects returns the number of accepted events that produced an effect.
func (p *WebhookProcessor) Effects() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.effects
}
