package scm

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/go-github/v93/github"
)

type etagEntry struct {
	etag string
	pr   *github.PullRequest
}

func (g *GitHub) getPR(ctx context.Context, number int) (*github.PullRequest, error) {
	g.etagMu.Lock()
	cached := g.etags[number]
	g.etagMu.Unlock()

	v, err := g.guarded(func() (any, error) {
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
	return v.(*github.PullRequest), nil
}

func initETagCache(g *GitHub) {
	if g.etags == nil {
		g.etags = make(map[int]*etagEntry)
	}
}
