package scm

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/go-github/v93/github"
)

type etagEntry struct {
	etag string
	pr   *github.PullRequest
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
		entry := &etagEntry{etag: resp.Header.Get("ETag"), pr: &pr}
		g.etagMu.Lock()
		g.etags[number] = entry
		g.etagMu.Unlock()
		return &pr, nil
	})
	if err != nil {
		return nil, err
	}
	return pr, nil
}

func initETagCache(g *GitHub) {
	if g.etags == nil {
		g.etags = make(map[int]*etagEntry)
	}
}
