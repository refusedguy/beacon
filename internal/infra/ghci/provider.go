package ghci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

const (
	// defaultCacheTTL keeps one pair of gh calls per PR across several bridge
	// ticks. It is shorter than the bridge's 30s default cadence, so a normal
	// refresh still observes fresh CI while a hot loop is absorbed by the cache.
	defaultCacheTTL = 20 * time.Second
	// defaultMinInterval is the floor between two real gh calls for the same
	// PR. It also covers a cache miss right after a successful call.
	defaultMinInterval = 2 * time.Second
	// defaultBackoff is the wait after the first transient failure; each
	// further failure doubles it up to maxBackoff.
	defaultBackoff = 5 * time.Second
	maxBackoff     = 60 * time.Second
	// maxJitter bounds the random wait added to a backoff deadline. Two
	// providers that see the same failure then retry at different times.
	maxJitter = time.Second
	// defaultMaxConsecutiveErrors is the retry budget. After this many failed
	// observations in a row the provider stops calling gh for the PR and
	// returns BudgetExhaustedError. The limit belongs to the retry rule, not
	// to each caller's configuration.
	defaultMaxConsecutiveErrors = 3
)

// Runner is the only external dependency of Provider: the authenticated gh
// CLI. The seam lets tests count calls and return the TLS timeouts and exit
// status 1 failures that motivated the pacing.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// Classifier reports whether a gh error can disappear on retry. True keeps the
// existing backoff path. False marks the request as permanent, so Provider
// reports the error once instead of doubling the wait on a request that cannot
// change. The seam lives in Config so tests can replace the production rule.
type Classifier func(err error) bool

// Config tunes Provider. Zero fields get the defaults above, so
// &ghci.Provider{} and ghci.New(ghci.Config{}) behave the same in production.
// Jitter adds a bounded random wait to the backoff deadline and is injectable
// so tests can fix the delay.
type Config struct {
	CacheTTL    time.Duration
	MinInterval time.Duration
	Backoff     time.Duration
	MaxBackoff  time.Duration
	// MaxConsecutiveErrors caps failed observations before the provider
	// refuses to call gh again. Zero selects defaultMaxConsecutiveErrors.
	MaxConsecutiveErrors int
	Now                  func() time.Time
	Run                  Runner
	Jitter               func() time.Duration
	Classify             Classifier
}

// prEntry is the per-PR state kept by Provider for the lifetime of the process.
type prEntry struct {
	cached            domain.PRState
	cachedAt          time.Time
	nextAllowed       time.Time
	inFlight          bool
	consecutiveErrors int
	backoff           time.Duration
	exhausted         bool
}

// Provider adapts the authenticated `gh` CLI to domain.CIProvider. The
// project already has a richer GitHub adapter under internal/infra/github;
// this one is the small composition-root bridge used by the local MCP server
// to turn observed checks into verdicts without a second credential store.
//
// PRState makes two gh calls per uncached PR (view plus checks). The provider
// caches the result briefly, keeps a minimum interval between calls for the
// same PR and backs off exponentially after transient failures. Without this,
// twelve Watches on a five-second tick produced about 288 gh calls a minute
// and GitHub answers with TLS handshake timeouts and exit status 1.
type Provider struct {
	mu      sync.Mutex
	entries map[string]*prEntry

	cacheTTL             time.Duration
	minInterval          time.Duration
	backoff              time.Duration
	maxBackoff           time.Duration
	maxConsecutiveErrors int
	now                  func() time.Time
	run                  Runner
	jitter               func() time.Duration
	classify             Classifier
}

var _ domain.CIProvider = (*Provider)(nil)

// New builds a Provider with the given pacing. Zero-value configuration uses
// the production defaults.
func New(cfg Config) *Provider {
	return &Provider{
		cacheTTL:             cfg.CacheTTL,
		minInterval:          cfg.MinInterval,
		backoff:              cfg.Backoff,
		maxBackoff:           cfg.MaxBackoff,
		maxConsecutiveErrors: cfg.MaxConsecutiveErrors,
		now:                  cfg.Now,
		run:                  cfg.Run,
		jitter:               cfg.Jitter,
		classify:             cfg.Classify,
	}
}

// defaults fills in the zero fields once, under the same mutex that guards
// entries, so a bare &Provider{} needs no constructor.
func (p *Provider) defaults() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries == nil {
		p.entries = make(map[string]*prEntry)
	}
	if p.cacheTTL <= 0 {
		p.cacheTTL = defaultCacheTTL
	}
	if p.minInterval <= 0 {
		p.minInterval = defaultMinInterval
	}
	if p.backoff <= 0 {
		p.backoff = defaultBackoff
	}
	if p.maxBackoff <= 0 {
		p.maxBackoff = maxBackoff
	}
	if p.maxBackoff < p.backoff {
		p.maxBackoff = p.backoff
	}
	if p.maxConsecutiveErrors <= 0 {
		p.maxConsecutiveErrors = defaultMaxConsecutiveErrors
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.run == nil {
		p.run = runGH
	}
	if p.jitter == nil {
		p.jitter = defaultJitter
	}
	if p.classify == nil {
		p.classify = isRetryableGH
	}
}

func runGH(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "gh", args...).Output()
}

// defaultJitter returns a random wait in [0, maxJitter]. The seam in Config
// lets tests fix the value and compare two providers with different delays.
func defaultJitter() time.Duration {
	return time.Duration(rand.Int64N(int64(maxJitter) + 1))
}

// transientErrorPatterns are the error texts gh and net/http use for failures
// that leave PR state unchanged. Retrying one of these cannot double-merge:
// the orchestrator ledger still keys the decision on (watchID, revision).
var transientErrorPatterns = []string{
	"tls handshake timeout",
	"eof",
	"connection reset",
	"connection refused",
	"bad gateway",
	"http 502",
	"rate limit",
	"rate_limited",
}

// IsTransientError reports whether err is a short-lived transport or GitHub
// failure worth one more attempt. TLS handshake timeouts, EOF, connection
// resets, HTTP 502 and rate limits match. "Merge already in progress" does
// not: that merge is running, so the caller confirms state instead of writing
// again. Keeping the classifier here lets both merge paths share one rule
// without making infra packages import each other.
func IsTransientError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, pattern := range transientErrorPatterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

// permanentGHPatterns are the gh CLI answers that repeat unchanged: bad
// credentials, missing scopes, absent repositories and malformed arguments or
// output. Retrying them only adds calls and a growing wait.
var permanentGHPatterns = []string{
	"authentication",
	"bad credentials",
	"not authenticated",
	"401",
	"403",
	"404",
	"422",
	"could not resolve",
	"not found",
	"invalid argument",
	"unknown flag",
	"validation failed",
	"decode",
}

// isRetryableGH is the production classifier. Transient transport errors and
// the documented `exit status 1` keep retrying. Everything else that matches a
// permanent pattern is reported once. Unknown errors keep the old retry path
// rather than being silently classified as permanent.
func isRetryableGH(err error) bool {
	if err == nil {
		return false
	}
	if IsTransientError(err) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, pattern := range permanentGHPatterns {
		if strings.Contains(text, pattern) {
			return false
		}
	}
	return true
}

// permanentError marks a classified error so PRState can skip backoff. It
// keeps the gh error reachable through Unwrap for callers that inspect it.
type permanentError struct {
	cause error
}

func (e *permanentError) Error() string { return e.cause.Error() }
func (e *permanentError) Unwrap() error { return e.cause }

func isPermanentError(err error) bool {
	var permanent *permanentError
	return errors.As(err, &permanent)
}

// classifyError applies the configured classifier at the gh boundary.
func (p *Provider) classifyError(err error) error {
	if err == nil || p.classify == nil || p.classify(err) {
		return err
	}
	return &permanentError{cause: err}
}

// throttleError is returned while a PR is in backoff. It deliberately reaches
// cibridge as an error: publishing a verdict from a stale observation while gh
// is failing could merge a PR whose checks changed.
type throttleError struct {
	repo  string
	pr    int
	fails int
	wait  time.Duration
}

func (e throttleError) Error() string {
	if e.wait < 0 {
		e.wait = 0
	}
	return fmt.Sprintf("ghci: %s#%d: %d consecutive errors, next call in %s",
		e.repo, e.pr, e.fails, e.wait)
}

// BudgetExhaustedError reports that a PR has reached maxConsecutiveErrors
// failed observations in a row. The provider stops calling gh for that PR.
// Callers can identify it with errors.As. It reaches cibridge as an error, so
// no verdict is published from a stale observation.
type BudgetExhaustedError struct {
	Repo  string
	PR    int
	Fails int
}

func (e BudgetExhaustedError) Error() string {
	return fmt.Sprintf("ghci: %s#%d: retry budget exhausted after %d consecutive errors",
		e.Repo, e.PR, e.Fails)
}

// PRState returns the current PR state, served from the short-lived cache when
// possible. A call that would be too soon for the same PR is skipped: while in
// backoff it reports a throttle error, otherwise it returns the cached state.
// The provider_call log carries consecutive_errors, so a rising count shows
// degradation before verdicts start disappearing.
func (p *Provider) PRState(ctx context.Context, repo string, pr int) (state domain.PRState, err error) {
	p.defaults()
	started := p.now()
	key := fmt.Sprintf("%s#%d", repo, pr)

	consecutive := 0
	exhausted := false
	defer func() {
		level := observe.LevelDebug
		fields := observe.Fields{
			observe.FieldAdapter: "ghci", "method": "PRState", "repo": repo,
			observe.FieldPR: pr, observe.FieldDuration: p.now().Sub(started).String(),
			"consecutive_errors":     consecutive,
			"retry_budget_exhausted": exhausted,
		}
		if err != nil {
			level = observe.LevelWarn
			fields[observe.FieldError] = err.Error()
		}
		observe.Event(level, "provider_call", fields)
	}()

	now := p.now()
	p.mu.Lock()
	e := p.entries[key]
	if e == nil {
		e = &prEntry{}
		p.entries[key] = e
	}
	consecutive = e.consecutiveErrors
	exhausted = e.exhausted
	if exhausted {
		p.mu.Unlock()
		return domain.PRState{}, BudgetExhaustedError{Repo: repo, PR: pr, Fails: e.consecutiveErrors}
	}

	// One fresh observation serves every Watch on this PR and several ticks.
	// This is the batch optimisation: no gh process is started on a cache hit.
	if !e.inFlight && consecutive == 0 && !e.cachedAt.IsZero() && now.Sub(e.cachedAt) < p.cacheTTL {
		cached := e.cached
		p.mu.Unlock()
		return cached, nil
	}
	// A concurrent caller already owns this PR, or the minimum interval or
	// backoff has not elapsed. Do not start a second gh process. Waiting here
	// is not another failure, so consecutiveErrors stays put.
	if e.inFlight || now.Before(e.nextAllowed) {
		fails, next, cached := e.consecutiveErrors, e.nextAllowed, e.cached
		p.mu.Unlock()
		if fails > 0 {
			return domain.PRState{}, throttleError{repo: repo, pr: pr, fails: fails, wait: next.Sub(now)}
		}
		return cached, nil
	}
	e.inFlight = true
	p.mu.Unlock()

	fetched, callErr := p.fetch(ctx, repo, pr)

	p.mu.Lock()
	e = p.entries[key]
	e.inFlight = false
	now = p.now()
	if callErr != nil {
		// A permanent gh answer will not change on retry. Leave the pacing
		// state untouched so one bad credential or argument cannot push the
		// PR into a 60s backoff that hides every later observation.
		if isPermanentError(callErr) {
			p.mu.Unlock()
			return domain.PRState{}, callErr
		}
		// TLS handshake timeouts and `gh ...: exit status 1` are transient.
		// One failure doubles the wait up to the cap; a success clears it.
		// Jitter is added to the deadline only, so repeated doubling still
		// respects maxBackoff and a zero jitter keeps the old timing.
		e.consecutiveErrors++
		consecutive = e.consecutiveErrors
		if e.consecutiveErrors >= p.maxConsecutiveErrors {
			// The budget belongs to the retry rule. Past this point there is
			// no nextAllowed to wait for: gh is not called again for this PR.
			e.exhausted = true
			exhausted = true
			e.nextAllowed = time.Time{}
			p.mu.Unlock()
			return domain.PRState{}, BudgetExhaustedError{Repo: repo, PR: pr, Fails: e.consecutiveErrors}
		}
		if e.backoff <= 0 {
			e.backoff = p.backoff
		} else if e.backoff < p.maxBackoff {
			e.backoff *= 2
			if e.backoff > p.maxBackoff {
				e.backoff = p.maxBackoff
			}
		}
		e.nextAllowed = now.Add(e.backoff + p.jitter())
		consecutive = e.consecutiveErrors
		p.mu.Unlock()
		return domain.PRState{}, callErr
	}
	e.consecutiveErrors = 0
	e.backoff = 0
	e.cached = fetched
	e.cachedAt = now
	e.nextAllowed = now.Add(p.minInterval)
	consecutive = 0
	p.mu.Unlock()
	return fetched, nil
}

// fetch performs the two gh calls behind one cached PR observation.
func (p *Provider) fetch(ctx context.Context, repo string, pr int) (domain.PRState, error) {
	prArg := strconv.Itoa(pr)
	view, err := p.run(ctx, "pr", "view", prArg, "--repo", repo, "--json",
		"headRefOid,baseRefName,state,isDraft,mergeable,mergeStateStatus,reviewDecision")
	if err != nil {
		return domain.PRState{}, p.classifyError(fmt.Errorf("gh pr view: %w", err))
	}
	var raw struct {
		HeadRefOid       string `json:"headRefOid"`
		BaseRefName      string `json:"baseRefName"`
		State            string `json:"state"`
		IsDraft          bool   `json:"isDraft"`
		Mergeable        string `json:"mergeable"`
		MergeStateStatus string `json:"mergeStateStatus"`
		ReviewDecision   string `json:"reviewDecision"`
	}
	if err := json.Unmarshal(view, &raw); err != nil {
		return domain.PRState{}, p.classifyError(fmt.Errorf("gh pr view decode: %w", err))
	}

	checksOut, err := p.run(ctx, "pr", "checks", prArg, "--repo", repo, "--json",
		"name,state,link")
	if err != nil {
		// No checks yet is an honest pending state, not an error.
		if ee, ok := err.(*exec.ExitError); ok && strings.Contains(string(ee.Stderr), "no checks reported") {
			return domain.PRState{
				State: raw.State, IsDraft: raw.IsDraft, HeadRefOid: raw.HeadRefOid,
				BaseRefName: raw.BaseRefName, Mergeable: raw.Mergeable,
				MergeStateStatus: raw.MergeStateStatus, ReviewDecision: raw.ReviewDecision,
			}, nil
		}
		return domain.PRState{}, p.classifyError(fmt.Errorf("gh pr checks: %w", err))
	}
	var rows []struct {
		Name  string `json:"name"`
		State string `json:"state"`
		Link  string `json:"link"`
	}
	if err := json.Unmarshal(checksOut, &rows); err != nil {
		return domain.PRState{}, p.classifyError(fmt.Errorf("gh pr checks decode: %w", err))
	}

	checks := make(map[string]domain.Check, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.Name)
		if name == "" {
			continue
		}
		conclusion := ghCheckConclusion(row.State)
		checks[name] = domain.Check{
			State:      conclusion,
			Conclusion: conclusion,
			Required:   true,
			DetailsURL: row.Link,
		}
	}
	return domain.PRState{
		State: raw.State, IsDraft: raw.IsDraft, HeadRefOid: raw.HeadRefOid,
		BaseRefName: raw.BaseRefName, Mergeable: raw.Mergeable,
		MergeStateStatus: raw.MergeStateStatus, ReviewDecision: raw.ReviewDecision,
		Checks: checks,
	}, nil
}

// ghCheckConclusion maps gh's check state to Beacon's conclusion strings.
// SKIPPED and NEUTRAL are allowed, non-blocking results, so they map to SUCCESS
// rather than to a cancelled or pending check that would hold the merge.
func ghCheckConclusion(state string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "PASS", "SUCCESS", "SKIPPED", "NEUTRAL":
		return "SUCCESS"
	case "FAIL", "FAILURE", "ERROR", "TIMED_OUT", "ACTION_REQUIRED":
		return "FAILURE"
	case "CANCELLED":
		return "CANCELLED"
	default:
		return "PENDING"
	}
}
