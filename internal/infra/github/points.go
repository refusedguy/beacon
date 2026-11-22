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
// body cost is ignored. GraphQL headers use the 5000 point scale, so Observe
// converts them to the local 500 point budget before recording them. REST
// responses keep the scale they report.
func (c *Counter) Observe(resp *http.Response, bodyCost int) int {
	if resp == nil {
		return c.Add(bodyCost)
	}
	if bodyCost < 0 {
		bodyCost = 0
	}
	obs := observeHeaders(resp.Header)
	if isGraphQLResponse(resp) {
		obs = scaleGraphQLObservation(obs, resp.Header)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apply(obs, bodyCost)
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

// isGraphQLResponse reports whether the response came from a GraphQL POST.
// Both GraphQL clients in this package send POST requests. The resource
// header is an explicit marker for callers that build responses directly.
func isGraphQLResponse(resp *http.Response) bool {
	if strings.EqualFold(headerValue(resp.Header, "x-ratelimit-resource"), "graphql") {
		return true
	}
	return resp.Request != nil && resp.Request.Method == http.MethodPost
}

// scaleGraphQLObservation converts a reading on the GitHub 5000 point scale
// to the local budget. A limit at or below the budget needs no conversion.
func scaleGraphQLObservation(obs observation, h http.Header) observation {
	if obs.used < 0 {
		return obs
	}
	limit, err := strconv.Atoi(headerValue(h, "x-ratelimit-limit"))
	if err != nil || limit <= BudgetPerHour {
		return obs
	}
	obs.used = obs.used * BudgetPerHour / limit
	if obs.used < 0 {
		obs.used = 0
	}
	if obs.used > BudgetPerHour {
		obs.used = BudgetPerHour
	}
	return obs
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

func (c *Counter) AddCost(cost int) int { return c.Add(cost) }

func (c *Counter) Record(resp *http.Response, bodyCost int) int {
	return c.Observe(resp, bodyCost)
}

// Backpressure for task launches, ADR 0038 §2.
//
// The shared GitHub point budget gates when a task may start. While the budget
// is spent or a secondary limit is active, a launch is deferred with reason
// quota_exhausted instead of entering a blind retry. A deferred launch stays
// held until Resume sees that the limit has recovered. The wake ladder from
// ADR 0017 is untouched: this gate decides whether a task starts, not how a
// delivery resolves.

// QuotaExhausted is the reason recorded on a launch deferred by backpressure.
const QuotaExhausted = "quota_exhausted"

// LaunchDecision is the verdict for one launch attempt. Allowed reports
// whether the launch may start now. Reason is QuotaExhausted when the launch
// was deferred and empty otherwise. Wait is how long the counter expects the
// limit to last, so the caller can schedule a wake-up instead of spinning.
type LaunchDecision struct {
	Allowed bool
	Reason  string
	Wait    time.Duration
}

// LaunchGate applies GitHub quota backpressure to task launches. It counts
// deferred launches and releases them only when the shared counter shows the
// limit has recovered. It never calls a launch itself, so a deferred launch
// cannot turn into a blind retry loop.
type LaunchGate struct {
	counter *Counter

	mu      sync.Mutex
	pending int
}

// NewLaunchGate builds a gate over the shared point counter. A nil counter
// means there is no quota to enforce, so every launch is allowed.
func NewLaunchGate(counter *Counter) *LaunchGate {
	return &LaunchGate{counter: counter}
}

// Admit decides whether a launch may start now. When the quota is exhausted
// the launch is counted as deferred and the decision carries reason
// quota_exhausted; nothing is launched and no retry is scheduled inside the
// gate. The caller uses Wait to schedule a later Resume instead.
func (g *LaunchGate) Admit() LaunchDecision {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.counter != nil && g.counter.LimitReached() {
		g.pending++
		return LaunchDecision{Reason: QuotaExhausted, Wait: g.waitLocked()}
	}
	return LaunchDecision{Allowed: true}
}

// Resume releases all deferred launches once the quota has recovered and
// returns how many were released. While the limit is still active it returns
// zero and leaves every launch deferred, so nothing is retried blindly.
func (g *LaunchGate) Resume() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending == 0 {
		return 0
	}
	if g.counter != nil && g.counter.LimitReached() {
		return 0
	}
	released := g.pending
	g.pending = 0
	return released
}

// Pending returns the number of launches currently deferred.
func (g *LaunchGate) Pending() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pending
}

// waitLocked reports how long the counter expects the current limit to last.
// The caller must hold g.mu. A secondary limit takes priority over the hourly
// reset boundary, matching the counter's own view of the limit.
func (g *LaunchGate) waitLocked() time.Duration {
	if g.counter == nil {
		return 0
	}
	if w := g.counter.RetryAfter(); w > 0 {
		return w
	}
	if reset := g.counter.ResetAt(); !reset.IsZero() {
		if w := reset.Sub(g.counter.now()); w > 0 {
			return w
		}
	}
	return 0
}

// Preflight quota check before sending a task, ADR 0044 §6.
//
// T3.54 backpressure (LaunchGate) reacts to a GitHub API quota response and
// defers launches until Resume sees recovery. The preflight check is a
// different mechanism: it stands at the entry to sending a task, reads the
// live quota and either allows the send or refuses it with a reason before
// anything starts. It holds no pending count, schedules no wait and resumes
// nothing. A task refused here is not sent and is not retried by the guard.

// PreflightQuotaExhausted is the reason returned when a live quota reading
// shows the plan already spent. It shares the ADR 0038 §2 cause with
// backpressure but is a refusal at send time, not a deferred launch.
const PreflightQuotaExhausted = "quota_exhausted"

// PreflightQuotaUnknown is the reason returned when the live quota cannot be
// read. The send is refused so no attempt is spent on an unknown quota.
const PreflightQuotaUnknown = "quota_unknown"

// PreflightDecision is the verdict of the check before a task is sent.
// Allowed reports whether the send may proceed. Reason is set only when
// Allowed is false, so the caller can report why the task was not started.
type PreflightDecision struct {
	Allowed bool
	Reason  string
}

// PreflightGuard checks live quota before a task is sent. A nil counter means
// there is no quota to enforce, so every send is allowed.
type PreflightGuard struct {
	counter *Counter
}

// NewPreflightGuard builds a preflight check over the shared point counter.
func NewPreflightGuard(c *Counter) *PreflightGuard {
	return &PreflightGuard{counter: c}
}

// Check reads a fresh quota snapshot and reports whether the send may proceed.
// The GET /rate_limit reading is taken at check time, so the verdict reflects
// the live quota and does not depend on a response from a task already sent.
// An exhausted or unreadable quota returns Allowed=false with a reason.
func (g *PreflightGuard) Check(ctx context.Context) PreflightDecision {
	if g.counter == nil {
		return PreflightDecision{Allowed: true}
	}
	if err := g.counter.Sync(ctx); err != nil {
		return PreflightDecision{Reason: PreflightQuotaUnknown}
	}
	if g.counter.LimitReached() {
		return PreflightDecision{Reason: PreflightQuotaExhausted}
	}
	return PreflightDecision{Allowed: true}
}

// Send runs send only when Check allows the task to be sent. When the check
// refuses, send is not called and the decision carries the reason, so the task
// is never started against an exhausted quota and the refusal is known before
// any work begins.
func (g *PreflightGuard) Send(ctx context.Context, send func() error) (PreflightDecision, error) {
	d := g.Check(ctx)
	if !d.Allowed || send == nil {
		return d, nil
	}
	return d, send()
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

// CI verdict in the events ref, ADR 0040 §3, T3.68.
//
// The daemon reads one events ref for every pending get_result call instead of
// running one `gh pr checks --watch` per call. A verdict lives in the ref,
// keyed by the id of the awaited call. When the source behind the ref is not
// approved, the observer reports source degraded and never invents a check
// status: an unknown verdict stays unknown both here and in get_result. The
// ADR 0017 wake ladder and the ADR 0039 §2 observer budget are untouched.

// EventsRef is the single ref that carries every CI verdict (ADR 0040 §3).
const EventsRef = "refs/clarity/events"

// VerdictUnknown is reported when no approved, authoritative verdict is known
// for a key. It is never a fabricated success.
const VerdictUnknown domain.Verdict = "unknown"

// SourceStatus is the trust level of the events-ref reader.
type SourceStatus string

const (
	SourceOK       SourceStatus = "ok"
	SourceDegraded SourceStatus = "source degraded"
)

// EventsSource supplies the raw contents of the events ref. Read is called
// only when Approved reports true. An exec-backed source is approved only
// after a human has approved it; an unapproved source is degraded and is
// never read.
type EventsSource interface {
	Approved() bool
	Read(ctx context.Context, ref string) ([]byte, error)
}

// eventsVerdictEvent is one verdict record in the events ref.
type eventsVerdictEvent struct {
	Key     string         `json:"key"`
	Verdict domain.Verdict `json:"verdict"`
}

// EventsVerdictResult is the observer's answer for one pending call. Known
// separates a real verdict from an unknown one, so a caller cannot mistake
// VerdictUnknown for a check result it should act on.
type EventsVerdictResult struct {
	Verdict domain.Verdict
	Status  SourceStatus
	Known   bool
}

func degradedVerdict() EventsVerdictResult {
	return EventsVerdictResult{Verdict: VerdictUnknown, Status: SourceDegraded}
}

// knownVerdicts are the domain verdicts an events record may carry. Anything
// else is unknown and is never promoted to a terminal or ready verdict.
var knownVerdicts = map[domain.Verdict]bool{
	domain.VerdictWatching:    true,
	domain.VerdictReady:       true,
	domain.VerdictRebase:      true,
	domain.VerdictRetry:       true,
	domain.VerdictKeepWorking: true,
	domain.VerdictTimeout:     true,
	domain.VerdictMerged:      true,
	domain.VerdictClosed:      true,
}

// parseEventsRef parses the events ref at the boundary. It is pure over the
// bytes the source returned. Unrecognized verdicts become VerdictUnknown so
// an unknown state can never masquerade as a check result.
func parseEventsRef(data []byte) (map[string]domain.Verdict, error) {
	var doc struct {
		Events []eventsVerdictEvent `json:"events"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse events ref: %w", err)
	}
	out := make(map[string]domain.Verdict, len(doc.Events))
	for _, ev := range doc.Events {
		if ev.Key == "" {
			continue
		}
		v := ev.Verdict
		if !knownVerdicts[v] {
			v = VerdictUnknown
		}
		out[ev.Key] = v
	}
	return out, nil
}

// EventsVerdictObserver reads the events ref once per Refresh and serves every
// pending call from the one cached snapshot. One observer is shared by all
// waiting calls, so N get_result calls cost one ref read, not N watches.
type EventsVerdictObserver struct {
	mu       sync.Mutex
	ref      string
	source   EventsSource
	verdicts map[string]domain.Verdict
	status   SourceStatus
	reads    int
}

// NewEventsVerdictObserver builds an observer over the events-ref source. An
// unapproved or nil source starts degraded and reports no verdicts.
func NewEventsVerdictObserver(source EventsSource) *EventsVerdictObserver {
	return &EventsVerdictObserver{
		ref:      EventsRef,
		source:   source,
		verdicts: make(map[string]domain.Verdict),
		status:   SourceDegraded,
	}
}

// Refresh is the single ref read for all pending calls. With no approved
// source, a failed read or malformed ref it reports source degraded and clears
// cached verdicts, so a stale reading can never fabricate a check status.
func (o *EventsVerdictObserver) Refresh(ctx context.Context) SourceStatus {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.source == nil || !o.source.Approved() {
		o.status = SourceDegraded
		o.verdicts = make(map[string]domain.Verdict)
		return o.status
	}
	o.reads++
	data, err := o.source.Read(ctx, o.ref)
	if err != nil {
		o.status = SourceDegraded
		o.verdicts = make(map[string]domain.Verdict)
		return o.status
	}
	verdicts, err := parseEventsRef(data)
	if err != nil {
		o.status = SourceDegraded
		o.verdicts = make(map[string]domain.Verdict)
		return o.status
	}
	o.verdicts = verdicts
	o.status = SourceOK
	return o.status
}

// Verdict returns the authoritative verdict for key from the last refresh. It
// is a cheap cached lookup: no ref read and no watch. An unknown key, an
// unrecognized state or a degraded source returns VerdictUnknown with Known
// false, so an unknown state stays unknown in the get_result answer.
func (o *EventsVerdictObserver) Verdict(key string) EventsVerdictResult {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.status != SourceOK {
		return degradedVerdict()
	}
	v, ok := o.verdicts[key]
	if !ok || v == VerdictUnknown {
		return EventsVerdictResult{Verdict: VerdictUnknown, Status: o.status}
	}
	return EventsVerdictResult{Verdict: v, Status: o.status, Known: true}
}

// Status reports the trust level of the last refresh.
func (o *EventsVerdictObserver) Status() SourceStatus {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.status
}

// Reads returns how many times the ref source was actually read. One refresh
// per tick keeps this at one even with many pending calls.
func (o *EventsVerdictObserver) Reads() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.reads
}

// Ref returns the events ref this observer reads.
func (o *EventsVerdictObserver) Ref() string { return o.ref }
