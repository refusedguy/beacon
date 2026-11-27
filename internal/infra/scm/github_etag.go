package scm

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/go-github/v93/github"
)

// prCacheTTL bounds how long a cached pull request may be reused. An entry
// older than this is fetched unconditionally instead of relying on its ETag,
// so a long-lived daemon cannot serve a stale provider state.
const prCacheTTL = 30 * time.Second

// maxPRCacheEntries bounds the ETag cache so it cannot grow with every PR the
// daemon has ever observed.
const maxPRCacheEntries = 64

type etagEntry struct {
	etag   string
	pr     *github.PullRequest
	stored time.Time
}

// getPR shares one fetch among concurrent callers for the same PR. Without
// this, two Observes racing on a cold cache both miss and send unconditional
// GETs, and the second writer overwrites the first ETag entry.
func (g *GitHub) getPR(ctx context.Context, number int) (*github.PullRequest, error) {
	v, err, _ := g.prGet.Do(strconv.Itoa(number), func() (any, error) {
		return g.fetchPR(ctx, number)
	})
	if err != nil {
		return nil, err
	}
	return v.(*github.PullRequest), nil
}

func (g *GitHub) fetchPR(ctx context.Context, number int) (*github.PullRequest, error) {
	g.etagMu.Lock()
	cached := g.etags[number]
	if cached != nil && time.Since(cached.stored) > prCacheTTL {
		cached = nil
	}
	g.etagMu.Unlock()

	pr, err := guarded(g.br, func() (*github.PullRequest, error) {
		url := fmt.Sprintf("repos/%s/%s/pulls/%d", g.Owner, g.Repo, number)
		req, err := g.Client.NewRequest(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		if cached != nil && cached.etag != "" {
			req.Header.Set("If-None-Match", cached.etag)
		}
		var pr github.PullRequest
		resp, err := g.Client.Do(req, &pr)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotModified && cached != nil && cached.pr != nil {
				return cached.pr, nil
			}
			return nil, err
		}
		entry := &etagEntry{etag: resp.Header.Get("ETag"), pr: &pr, stored: time.Now()}
		g.etagMu.Lock()
		g.etags[number] = entry
		trimPRCache(g.etags)
		g.etagMu.Unlock()
		return &pr, nil
	})
	if err != nil {
		return nil, err
	}
	return pr, nil
}

// trimPRCache evicts the oldest entries once the cache passes its bound. The
// number breaks ties so concurrent writes converge on the same victim.
func trimPRCache(cache map[int]*etagEntry) {
	for len(cache) > maxPRCacheEntries {
		var oldest int
		var at time.Time
		first := true
		for number, entry := range cache {
			if first || entry.stored.Before(at) || (entry.stored.Equal(at) && number < oldest) {
				oldest, at, first = number, entry.stored, false
			}
		}
		delete(cache, oldest)
	}
}

// writePRCache applies mutate to a fresh copy of the cached pull request. A
// mutation writes its result through so the next conditional GET starts from
// the state the mutation produced instead of the state it replaced.
func (g *GitHub) writePRCache(number int, mutate func(*github.PullRequest)) {
	g.etagMu.Lock()
	defer g.etagMu.Unlock()
	entry := g.etags[number]
	if entry == nil || entry.pr == nil {
		return
	}
	updated := *entry.pr
	mutate(&updated)
	g.etags[number] = &etagEntry{etag: entry.etag, pr: &updated, stored: time.Now()}
}

func initETagCache(g *GitHub) {
	if g.etags == nil {
		g.etags = make(map[int]*etagEntry)
	}
}
