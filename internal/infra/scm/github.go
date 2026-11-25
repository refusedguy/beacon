package scm

import (
	"context"
	"fmt"
	"net/http"
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
	base   string
	br     *gobreaker.CircuitBreaker[any]
	etagMu sync.Mutex
	etags  map[int]*etagEntry
}

func NewGitHub(client *github.Client, owner, repo, base string) *GitHub {
	g := &GitHub{
		Client: client,
		Owner:  owner,
		Repo:   repo,
		base:   base,
		br:     newSCMBreaker("github"),
	}
	initETagCache(g)
	return g
}

// newSCMBreaker builds a circuit breaker with the settings shared by both SCM
// adapters.
func newSCMBreaker(name string) *gobreaker.CircuitBreaker[any] {
	return gobreaker.NewCircuitBreaker[any](gobreaker.Settings{
		Name:        name,
		Interval:    time.Minute,
		Timeout:     15 * time.Second,
		MaxRequests: 3,
	})
}

// guarded runs fn through br and returns its typed result. The breaker stores
// any, so the one checked conversion lives here instead of at every call site.
func guarded[T any](br *gobreaker.CircuitBreaker[any], fn func() (T, error)) (T, error) {
	var zero T
	v, err := br.Execute(func() (any, error) { return fn() })
	if err != nil {
		return zero, err
	}
	if v == nil {
		return zero, nil
	}
	out, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("scm breaker %s: unexpected result %T, want %T", br.Name(), v, zero)
	}
	return out, nil
}

// guarded keeps the any-shaped entry point used by OpenPR.
func (g *GitHub) guarded(fn func() (any, error)) (any, error) {
	return guarded(g.br, fn)
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
	if state == "open" && len(checks) == 0 {
		files, err := g.changedFiles(ctx, pr)
		if err != nil {
			return o, err
		}
		o.WorkflowSkipped = ignoredPathsOnly(files)
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
		base = g.base
	}
	cmp, err := guarded(g.br, func() (*github.CommitsComparison, error) {
		c, _, err := g.Client.Repositories.CompareCommits(ctx, g.Owner, g.Repo, base, sha, nil)
		return c, err
	})
	if err != nil {
		return o, err
	}
	o.Behind = cmp.GetBehindBy() > 0
	o.BaseSHA = cmp.GetBaseCommit().GetSHA()
	return o, nil
}

func (g *GitHub) checks(ctx context.Context, sha string) ([]core.CheckResult, error) {
	res, err := guarded(g.br, func() (*github.ListCheckRunsResults, error) {
		r, _, err := g.Client.Checks.ListCheckRunsForRef(ctx, g.Owner, g.Repo, sha, &github.ListCheckRunsOptions{
			ListOptions: github.ListOptions{PerPage: 100},
		})
		return r, err
	})
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, nil
	}
	var out []core.CheckResult
	for _, c := range res.CheckRuns {
		out = append(out, core.CheckResult{
			Name:        c.GetName(),
			State:       checkState(c),
			Output:      strings.TrimSpace(c.GetOutput().GetTitle() + " " + c.GetOutput().GetText()),
			StartedAt:   c.GetStartedAt().Time,
			CompletedAt: c.GetCompletedAt().Time,
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

// changedFiles reads every changed file of a PR. GitHub paginates at 100.
func (g *GitHub) changedFiles(ctx context.Context, pr int) ([]*github.CommitFile, error) {
	var all []*github.CommitFile
	opts := &github.ListOptions{PerPage: 100}
	for {
		var (
			page []*github.CommitFile
			resp *github.Response
		)
		if _, err := guarded(g.br, func() (any, error) {
			var err error
			page, resp, err = g.Client.PullRequests.ListFiles(ctx, g.Owner, g.Repo, pr, opts)
			return nil, err
		}); err != nil {
			return nil, err
		}
		all = append(all, page...)
		if resp == nil || resp.NextPage == 0 {
			return all, nil
		}
		opts.Page = resp.NextPage
	}
}

// ignoredPathsOnly reports whether every changed path is one the CI workflow
// ignores: anything under docs/ or a Markdown file at any depth. An empty set
// is not a docs-only change, and a rename must have ignored names on both sides.
func ignoredPathsOnly(files []*github.CommitFile) bool {
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		for _, name := range []string{f.GetFilename(), f.GetPreviousFilename()} {
			if name == "" {
				continue
			}
			if !strings.HasPrefix(name, "docs/") && !strings.HasSuffix(strings.ToLower(name), ".md") {
				return false
			}
		}
	}
	return true
}

func (g *GitHub) approvals(ctx context.Context, pr int) (int, error) {
	reviews, err := guarded(g.br, func() ([]*github.PullRequestReview, error) {
		r, _, err := g.Client.PullRequests.ListReviews(ctx, g.Owner, g.Repo, pr, &github.ListOptions{PerPage: 100})
		return r, err
	})
	if err != nil {
		return 0, err
	}
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

type updateBranchResponse struct {
	Data struct {
		UpdatePullRequestBranch struct {
			PullRequest struct {
				HeadRefOid string `json:"headRefOid"`
			} `json:"pullRequest"`
		} `json:"updatePullRequestBranch"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

const updateBranchMutation = `mutation($id: ID!, $sha: GitObjectID!) {
  updatePullRequestBranch(input: {pullRequestId: $id, expectedHeadOid: $sha}) {
    pullRequest {
      headRefOid
    }
  }
}`

// UpdateBranch calls the GraphQL mutation updatePullRequestBranch. The public
// REST API has no branch-update endpoint, so the old PUT returned 404 in
// production while the httptest mocked success. The mutation returns the new
// head SHA directly, so no polling is needed.
func (g *GitHub) UpdateBranch(ctx context.Context, pr int, expectedSHA string) (string, error) {
	p, err := guarded(g.br, func() (*github.PullRequest, error) {
		got, _, err := g.Client.PullRequests.Get(ctx, g.Owner, g.Repo, pr)
		return got, err
	})
	if err != nil {
		return "", err
	}
	nodeID := p.GetNodeID()
	if nodeID == "" {
		return "", core.Errorf(core.CodeUnavailable, "PR %d has no node ID", pr)
	}

	payload := map[string]any{
		"query":     updateBranchMutation,
		"variables": map[string]string{"id": nodeID, "sha": expectedSHA},
	}
	req, err := g.Client.NewRequest(ctx, http.MethodPost, "graphql", payload)
	if err != nil {
		return "", err
	}
	var result updateBranchResponse
	if _, err := guarded(g.br, func() (*github.Response, error) {
		return g.Client.Do(req, &result)
	}); err != nil {
		return "", err
	}
	if len(result.Errors) > 0 {
		return "", core.Errorf(core.CodeUnavailable, "update branch: %s", result.Errors[0].Message)
	}
	sha := result.Data.UpdatePullRequestBranch.PullRequest.HeadRefOid
	if sha == "" {
		return "", core.Errorf(core.CodeUnavailable, "update branch returned no head SHA")
	}
	return sha, nil
}

func (g *GitHub) Merge(ctx context.Context, pr int, expectedSHA, title string) error {
	merged, err := guarded(g.br, func() (*github.PullRequestMergeResult, error) {
		m, _, err := g.Client.PullRequests.Merge(ctx, g.Owner, g.Repo, pr, title, &github.PullRequestOptions{
			MergeMethod: "squash",
			SHA:         expectedSHA,
		})
		return m, err
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
