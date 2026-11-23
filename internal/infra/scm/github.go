package scm

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v93/github"
	"github.com/sony/gobreaker/v2"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

type GitHub struct {
	Client *github.Client
	Owner  string
	Repo   string
	Base   string
	br     *gobreaker.CircuitBreaker[any]
	etagMu sync.Mutex
	etags  map[int]*etagEntry
}

func NewGitHub(client *github.Client, owner, repo, base string) *GitHub {
	g := &GitHub{
		Client: client,
		Owner:  owner,
		Repo:   repo,
		Base:   base,
		br: gobreaker.NewCircuitBreaker[any](gobreaker.Settings{
			Name:        "github",
			Interval:    time.Minute,
			Timeout:     15 * time.Second,
			MaxRequests: 3,
		}),
	}
	initETagCache(g)
	return g
}

func (g *GitHub) guarded(fn func() (any, error)) (any, error) {
	return g.br.Execute(fn)
}

func (g *GitHub) Observe(ctx context.Context, pr int) (core.Observation, error) {
	p, err := g.getPR(ctx, pr)
	if err != nil {
		return core.Observation{}, err
	}
	sha := p.GetHead().GetSHA()
	state := p.GetState()
	if state == "closed" {
		if p.GetMerged() {
			state = "merged"
		}
	} else if p.GetMerged() {
		state = "merged"
	}
	conflict := p.GetMergeableState() == "dirty" || (p.Mergeable != nil && !p.GetMergeable())
	o := core.Observation{
		PRNumber: pr,
		HeadSHA:  sha,
		PRState:  state,
		Conflict: conflict,
	}

	checks, err := g.checks(ctx, sha)
	if err != nil {
		return o, err
	}
	o.Checks = checks
	for _, c := range checks {
		if c.State == core.CheckFailure {
			o.FailureExcerpt = c.Output
			break
		}
	}

	approvals, err := g.approvals(ctx, pr)
	if err != nil {
		return o, err
	}
	o.Approvals = approvals
	for _, l := range p.Labels {
		o.Labels = append(o.Labels, l.GetName())
	}

	base := p.GetBase().GetRef()
	if base == "" {
		base = g.Base
	}
	cmp, _, err := g.Client.Repositories.CompareCommits(ctx, g.Owner, g.Repo, base, sha, nil)
	if err != nil {
		return o, err
	}
	o.Behind = cmp.GetBehindBy() > 0
	return o, nil
}

func (g *GitHub) checks(ctx context.Context, sha string) ([]core.CheckResult, error) {
	v, err := g.guarded(func() (any, error) {
		r, _, err := g.Client.Checks.ListCheckRunsForRef(ctx, g.Owner, g.Repo, sha, &github.ListCheckRunsOptions{
			ListOptions: github.ListOptions{PerPage: 100},
		})
		return r, err
	})
	if err != nil {
		return nil, err
	}
	res := v.(*github.ListCheckRunsResults)
	var out []core.CheckResult
	for _, c := range res.CheckRuns {
		out = append(out, core.CheckResult{
			Name:   c.GetName(),
			State:  checkState(c),
			Output: strings.TrimSpace(c.GetOutput().GetTitle() + " " + c.GetOutput().GetText()),
		})
	}
	return out, nil
}

func checkState(c *github.CheckRun) core.CheckState {
	if c.GetStatus() != "completed" {
		return core.CheckPending
	}
	switch strings.ToLower(c.GetConclusion()) {
	case "success", "neutral", "skipped":
		if c.GetConclusion() == "skipped" {
			return core.CheckSkipped
		}
		return core.CheckSuccess
	case "":
		return core.CheckPending
	default:
		return core.CheckFailure
	}
}

func (g *GitHub) approvals(ctx context.Context, pr int) (int, error) {
	v, err := g.guarded(func() (any, error) {
		r, _, err := g.Client.PullRequests.ListReviews(ctx, g.Owner, g.Repo, pr, &github.ListOptions{PerPage: 100})
		return r, err
	})
	if err != nil {
		return 0, err
	}
	reviews := v.([]*github.PullRequestReview)
	latest := make(map[string]string)
	times := make(map[string]time.Time)
	for _, r := range reviews {
		if r.GetUser() == nil {
			continue
		}
		login := r.GetUser().GetLogin()
		at := r.GetSubmittedAt().Time
		if t, ok := times[login]; !ok || at.After(t) {
			times[login] = at
			latest[login] = r.GetState()
		}
	}
	n := 0
	for _, state := range latest {
		if strings.EqualFold(state, "APPROVED") {
			n++
		}
	}
	return n, nil
}

// UpdateBranch calls the GitHub "update branch" REST endpoint, then waits for
// the new head SHA because the endpoint itself returns no body.
func (g *GitHub) UpdateBranch(ctx context.Context, pr int, expectedSHA string) (string, error) {
	p, _, err := g.Client.PullRequests.Get(ctx, g.Owner, g.Repo, pr)
	if err != nil {
		return "", err
	}
	branch := p.GetHead().GetRef()
	path := fmt.Sprintf("repos/%s/%s/branches/%s/update", g.Owner, g.Repo, url.PathEscape(branch))
	req, err := g.Client.NewRequest(ctx, "PUT", path, map[string]string{"expected_head_sha": expectedSHA})
	if err != nil {
		return "", err
	}
	if _, err := g.guarded(func() (any, error) {
		_, err := g.Client.Do(req, nil)
		return nil, err
	}); err != nil {
		return "", err
	}
	for i := 0; i < 12; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
		cur, _, err := g.Client.PullRequests.Get(ctx, g.Owner, g.Repo, pr)
		if err != nil {
			return "", err
		}
		if sha := cur.GetHead().GetSHA(); sha != "" && sha != expectedSHA {
			return sha, nil
		}
	}
	return "", core.Errorf(core.CodeUnavailable, "branch %s did not advance", branch)
}

func (g *GitHub) Merge(ctx context.Context, pr int, expectedSHA, title string) error {
	merged, _, err := g.Client.PullRequests.Merge(ctx, g.Owner, g.Repo, pr, title, &github.PullRequestOptions{
		MergeMethod: "squash",
		SHA:         expectedSHA,
	})
	if err != nil {
		return err
	}
	if !merged.GetMerged() {
		return core.Errorf(core.CodeConflict, "merge of PR %d was rejected", pr)
	}
	return nil
}

var _ app.ScmGateway = (*GitHub)(nil)
