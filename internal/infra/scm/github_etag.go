package scm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v93/github"
	"github.com/sony/gobreaker/v2"
	"golang.org/x/sync/singleflight"

	"github.com/refusedguy/beacon/pkg/core"
)

const (
	// etagCacheTTL bounds how long a cached response may be reused. An entry
	// older than this is fetched unconditionally, so a long-lived daemon
	// cannot serve a stale validator forever.
	etagCacheTTL = 30 * time.Second
	// maxETagCacheEntries bounds each cache so it cannot grow with every PR or
	// SHA the daemon has ever observed.
	maxETagCacheEntries = 64
)

// etagValue is one cached response with its validator.
type etagValue[V any] struct {
	etag   string
	value  V
	stored time.Time
}

// etagCache is a bounded, TTL-limited store of conditional HTTP results. The
// call site owns a singleflight group so a cold cache is fetched once per key
// even when several Observes race on it.
type etagCache[V any] struct {
	mu      sync.Mutex
	entries map[string]*etagValue[V]
}

func newETagCache[V any]() *etagCache[V] {
	return &etagCache[V]{entries: make(map[string]*etagValue[V])}
}

// get returns a live cached entry, or nil when it is missing or stale.
func (c *etagCache[V]) get(key string) *etagValue[V] {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil {
		return nil
	}
	if time.Since(entry.stored) > etagCacheTTL {
		delete(c.entries, key)
		return nil
	}
	return entry
}

// set stores a fresh result and evicts the oldest entries past the bound. The
// key breaks ties so concurrent writers converge on the same victim.
func (c *etagCache[V]) set(key string, entry *etagValue[V]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = entry
	for len(c.entries) > maxETagCacheEntries {
		var oldest string
		var at time.Time
		first := true
		for k, e := range c.entries {
			if first || e.stored.Before(at) || (e.stored.Equal(at) && k < oldest) {
				oldest, at, first = k, e.stored, false
			}
		}
		delete(c.entries, oldest)
	}
}

// update applies fn to a cached value and refreshes its timestamp while
// keeping its validator. A mutation writes through so the next conditional
// GET starts from the state the mutation produced.
func (c *etagCache[V]) update(key string, fn func(V) V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil {
		return
	}
	c.entries[key] = &etagValue[V]{etag: entry.etag, value: fn(entry.value), stored: time.Now()}
}

// cachedFetch coalesces concurrent reads of key. A fresh validator produces a
// conditional fetch, and a 304 returns the cached value. Errors are never
// cached. fetch returns (value, notModified, newETag, err).
func cachedFetch[V any](
	group *singleflight.Group,
	cache *etagCache[V],
	key string,
	fetch func(etag string) (V, bool, string, error),
) (V, error) {
	var zero V
	v, err, _ := group.Do(key, func() (any, error) {
		if cached := cache.get(key); cached != nil && cached.etag != "" {
			value, notModified, etag, err := fetch(cached.etag)
			if err != nil {
				return nil, err
			}
			if notModified {
				return cached.value, nil
			}
			cache.set(key, &etagValue[V]{etag: etag, value: value, stored: time.Now()})
			return value, nil
		}
		value, _, etag, err := fetch("")
		if err != nil {
			return nil, err
		}
		cache.set(key, &etagValue[V]{etag: etag, value: value, stored: time.Now()})
		return value, nil
	})
	if err != nil {
		return zero, err
	}
	return v.(V), nil
}

// conditionalResult is one conditional GET through the breaker. A 304 is not
// an error, so it cannot open the circuit; notModified reports it instead.
type conditionalResult[T any] struct {
	value       T
	resp        *github.Response
	notModified bool
}

// conditionalRequest performs one GET with an optional validator. It decodes
// the body into value on success and leaves value untouched on 304.
func conditionalRequest[T any](ctx context.Context, client *github.Client, br *gobreaker.CircuitBreaker[any], endpoint, etag string) (conditionalResult[T], error) {
	result, err := guarded(br, func() (conditionalResult[T], error) {
		req, err := client.NewRequest(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return conditionalResult[T]{}, err
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		var value T
		resp, err := client.Do(req, &value)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotModified {
				return conditionalResult[T]{resp: resp, notModified: true}, nil
			}
			return conditionalResult[T]{}, err
		}
		return conditionalResult[T]{value: value, resp: resp}, nil
	})
	return result, err
}

func etagOf(resp *github.Response) string {
	if resp == nil {
		return ""
	}
	return resp.Header.Get("ETag")
}

// getPR shares one fetch among concurrent callers for the same PR. Without
// this, two Observes racing on a cold cache both miss and send unconditional
// GETs, and the second writer overwrites the first ETag entry.
func (g *GitHub) getPR(ctx context.Context, number int) (*github.PullRequest, error) {
	return cachedFetch(&g.prGet, g.prCache, strconv.Itoa(number), func(etag string) (*github.PullRequest, bool, string, error) {
		endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d", g.Owner, g.Repo, number)
		r, err := conditionalRequest[github.PullRequest](ctx, g.Client, g.br, endpoint, etag)
		if err != nil {
			return nil, false, "", err
		}
		if r.notModified {
			return nil, true, "", nil
		}
		return &r.value, false, etagOf(r.resp), nil
	})
}

// writePRCache applies mutate to a fresh copy of the cached pull request. A
// mutation writes through so the next conditional GET starts from the state
// the mutation produced instead of the state it replaced.
func (g *GitHub) writePRCache(number int, mutate func(*github.PullRequest)) {
	g.prCache.update(strconv.Itoa(number), func(p *github.PullRequest) *github.PullRequest {
		if p == nil {
			return nil
		}
		updated := *p
		mutate(&updated)
		return &updated
	})
}

// checkCacheValue bundles the derived check results with the capped flag so a
// 304 can return both without refetching any page.
type checkCacheValue struct {
	checks []core.CheckResult
	capped bool
}

// reviewCacheValue bundles the approval count with the capped flag.
type reviewCacheValue struct {
	approvals int
	capped    bool
}

func checkRunsURL(owner, repo, sha string, opts *github.ListOptions) string {
	q := url.Values{}
	q.Set("filter", "latest")
	q.Set("per_page", strconv.Itoa(opts.PerPage))
	if opts.Page > 0 {
		q.Set("page", strconv.Itoa(opts.Page))
	}
	return fmt.Sprintf("repos/%s/%s/commits/%s/check-runs?%s", owner, repo, sha, q.Encode())
}

func reviewsURL(owner, repo string, pr int, opts *github.ListOptions) string {
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(opts.PerPage))
	if opts.Page > 0 {
		q.Set("page", strconv.Itoa(opts.Page))
	}
	return fmt.Sprintf("repos/%s/%s/pulls/%d/reviews?%s", owner, repo, pr, q.Encode())
}

// fetchChecks walks every check-run page, sending the cached validator only on
// the first one. A 304 means every page is unchanged, so the cached results
// stand. Otherwise the walk derives the newest attempt per check name again.
func (g *GitHub) fetchChecks(ctx context.Context, sha, etag string) (checkCacheValue, bool, string, error) {
	opts := github.ListOptions{PerPage: 100}
	seen := make(map[int64]bool)
	var runs []*github.CheckRun
	capped := true
	firstETag := ""
	for n := 0; n < maxGitHubPages; n++ {
		r, err := conditionalRequest[github.ListCheckRunsResults](ctx, g.Client, g.br, checkRunsURL(g.Owner, g.Repo, sha, &opts), etag)
		if err != nil {
			return checkCacheValue{}, false, "", err
		}
		if r.notModified {
			return checkCacheValue{}, true, "", nil
		}
		if n == 0 {
			firstETag = etagOf(r.resp)
		}
		for _, c := range r.value.CheckRuns {
			if id := c.GetID(); id != 0 {
				if seen[id] {
					continue
				}
				seen[id] = true
			}
			runs = append(runs, c)
		}
		if r.resp == nil || r.resp.NextPage == 0 {
			capped = false
			break
		}
		opts.Page = r.resp.NextPage
		etag = ""
	}
	latest := make(map[string]*github.CheckRun, len(runs))
	for _, c := range runs {
		name := c.GetName()
		if prev := latest[name]; prev == nil || checkRunNewer(c, prev) {
			latest[name] = c
		}
	}
	out := checkCacheValue{capped: capped, checks: make([]core.CheckResult, 0, len(latest))}
	for _, c := range latest {
		out.checks = append(out.checks, core.CheckResult{
			Name:        c.GetName(),
			State:       checkState(c),
			Output:      strings.TrimSpace(c.GetOutput().GetTitle() + " " + c.GetOutput().GetText()),
			StartedAt:   c.GetStartedAt().Time,
			CompletedAt: c.GetCompletedAt().Time,
		})
	}
	return out, false, firstETag, nil
}

// fetchReviews walks every review page with the same conditional rule as
// check runs and recounts the latest approval per login.
func (g *GitHub) fetchReviews(ctx context.Context, pr int, etag string) (reviewCacheValue, bool, string, error) {
	opts := github.ListOptions{PerPage: 100}
	seen := make(map[int64]bool)
	var reviews []*github.PullRequestReview
	capped := true
	firstETag := ""
	for n := 0; n < maxGitHubPages; n++ {
		r, err := conditionalRequest[[]*github.PullRequestReview](ctx, g.Client, g.br, reviewsURL(g.Owner, g.Repo, pr, &opts), etag)
		if err != nil {
			return reviewCacheValue{}, false, "", err
		}
		if r.notModified {
			return reviewCacheValue{}, true, "", nil
		}
		if n == 0 {
			firstETag = etagOf(r.resp)
		}
		for _, rv := range r.value {
			if id := rv.GetID(); id != 0 {
				if seen[id] {
					continue
				}
				seen[id] = true
			}
			reviews = append(reviews, rv)
		}
		if r.resp == nil || r.resp.NextPage == 0 {
			capped = false
			break
		}
		opts.Page = r.resp.NextPage
		etag = ""
	}
	latest := make(map[string]reviewState, len(reviews))
	for _, rv := range reviews {
		if rv.GetUser() == nil {
			continue
		}
		login := rv.GetUser().GetLogin()
		next := reviewState{state: rv.GetState(), id: rv.GetID(), at: rv.GetSubmittedAt().Time}
		if prev, ok := latest[login]; !ok || reviewNewer(next, prev) {
			latest[login] = next
		}
	}
	approvals := 0
	for _, review := range latest {
		if review.at.IsZero() {
			continue
		}
		if strings.EqualFold(review.state, "APPROVED") {
			approvals++
		}
	}
	return reviewCacheValue{approvals: approvals, capped: capped}, false, firstETag, nil
}

const (
	// compareMemoTTL bounds how long a comparison is reused. Base and head
	// SHAs identify an immutable pair, so only the bound and TTL keep the memo
	// from growing with every historical SHA pair.
	compareMemoTTL = time.Minute
	maxCompareMemo = 128
)

type compareKey struct {
	base string
	head string
}

type compareEntry struct {
	cmp    *github.CommitsComparison
	stored time.Time
}

type compareCache struct {
	mu      sync.Mutex
	entries map[compareKey]compareEntry
}

func newCompareCache() *compareCache {
	return &compareCache{entries: make(map[compareKey]compareEntry)}
}

func (c *compareCache) get(key compareKey) (*github.CommitsComparison, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Since(entry.stored) > compareMemoTTL {
		delete(c.entries, key)
		return nil, false
	}
	return entry.cmp, true
}

func (c *compareCache) set(key compareKey, cmp *github.CommitsComparison) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = compareEntry{cmp: cmp, stored: time.Now()}
	for len(c.entries) > maxCompareMemo {
		var oldest compareKey
		var at time.Time
		first := true
		for k, e := range c.entries {
			if first || e.stored.Before(at) || (e.stored.Equal(at) && compareKeyLess(k, oldest)) {
				oldest, at, first = k, e.stored, false
			}
		}
		delete(c.entries, oldest)
	}
}

func compareKeyLess(a, b compareKey) bool {
	if a.base != b.base {
		return a.base < b.base
	}
	return a.head < b.head
}

// compare reuses a comparison for an unchanged base and head SHA pair. A new
// SHA produces a new key and a fresh fetch on the next Observe.
func (g *GitHub) compare(ctx context.Context, base, head string) (*github.CommitsComparison, error) {
	key := compareKey{base: base, head: head}
	flight := base + "\x00" + head
	v, err, _ := g.compareGet.Do(flight, func() (any, error) {
		if cmp, ok := g.compares.get(key); ok {
			return cmp, nil
		}
		cmp, err := guarded(g.br, func() (*github.CommitsComparison, error) {
			c, _, err := g.Client.Repositories.CompareCommits(ctx, g.Owner, g.Repo, base, head, nil)
			return c, err
		})
		if err != nil {
			return nil, err
		}
		g.compares.set(key, cmp)
		return cmp, nil
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return v.(*github.CommitsComparison), nil
}
