// Package scm adapts GitHub and GitLab to core.ScmGateway.
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
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/refusedguy/beacon/pkg/core"
)

// GitHub implements core.ScmGateway against the GitHub REST API.
type GitHub struct {
	Client *github.Client
	Owner  string
	Repo   string
	base   string
	br     *gobreaker.CircuitBreaker[any]
	etagMu sync.Mutex
	etags  map[int]*etagEntry
	prGet  singleflight.Group
}

// NewGitHub builds a GitHub gateway for owner and repo.
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

// maxGitHubPages is the explicit bound on paginated GitHub calls. A bad Link
// header must not keep the daemon fetching forever, but the bound must also
// cover large check and review sets. With PerPage=100 it covers 100,000 entries.
const maxGitHubPages = 1000

// observeParallelLimit bounds the goroutines that fetch independent PR signals.
// A single Observe stays within the circuit breaker's burst allowance instead
// of opening an unbounded number of requests at once.
const observeParallelLimit = 4

// githubPage carries one page through the breaker, which stores any.
type githubPage[T any] struct {
	items []T
	resp  *github.Response
}

// paginate follows NextPage until GitHub stops sending one or the cap is hit.
// Every request goes through br, so pagination does not bypass the breaker.
func paginate[T any](br *gobreaker.CircuitBreaker[any], fetch func(*github.ListOptions) ([]T, *github.Response, error)) ([]T, error) {
	var all []T
	opts := github.ListOptions{PerPage: 100}
	for n := 0; n < maxGitHubPages; n++ {
		page, err := guarded(br, func() (githubPage[T], error) {
			items, resp, err := fetch(&opts)
			return githubPage[T]{items: items, resp: resp}, err
		})
		if err != nil {
			return nil, err
		}
		all = append(all, page.items...)
		if page.resp == nil || page.resp.NextPage == 0 {
			return all, nil
		}
		opts.Page = page.resp.NextPage
	}
	return all, nil
}

// Observe reads the current state of pull request pr.
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
	// A false mergeable flag alone does not mean a conflict. GitHub reports it
	// for PRs blocked by required checks. Only these states describe a dirty
	// tree; checks stay a policy decision for ChecksRule.
	conflict := p.GetMergeableState() == "dirty" || p.GetMergeableState() == "conflict"
	o := core.Observation{
		PRNumber: pr,
		HeadSHA:  sha,
		PRState:  state,
		Conflict: conflict,
	}

	// A closed or merged PR is final. Do not let a failed check, approval or
	// compare call hide the final state and freeze its run in waiting_ci.
	if core.DecideFinalState(state).Closed {
		return o, nil
	}
	base := p.GetBase().GetRef()
	if base == "" {
		base = g.base
	}
	signals := g.fetchSignals(ctx, pr, sha, base)

	// Report failures in the original call order so callers see the same first
	// error and the same partially filled observation. Changed files depend on
	// the check result and stay in a second round.
	if signals.checksErr != nil {
		return o, signals.checksErr
	}
	o.Checks = signals.checks
	for _, c := range signals.checks {
		if c.State == core.CheckFailure {
			o.FailureExcerpt = c.Output
			break
		}
	}
	if state == "open" && len(signals.checks) == 0 {
		files, err := g.changedFiles(ctx, pr)
		if err != nil {
			return o, err
		}
		o.WorkflowSkipped = ignoredPathsOnly(files)
	}
	if signals.approvalsErr != nil {
		return o, signals.approvalsErr
	}
	o.Approvals = signals.approvals
	for _, l := range p.Labels {
		o.Labels = append(o.Labels, l.GetName())
	}
	if signals.compareErr != nil {
		return o, signals.compareErr
	}
	o.Behind = signals.cmp.GetBehindBy() > 0
	o.BaseSHA = signals.cmp.GetBaseCommit().GetSHA()
	return o, nil
}

// githubSignals carries the results of the first Observe round. Errors stay
// separate so Observe can report them in the original call order.
type githubSignals struct {
	checks       []core.CheckResult
	approvals    int
	cmp          *github.CommitsComparison
	checksErr    error
	approvalsErr error
	compareErr   error
}

// fetchSignals runs checks, approvals and the base comparison together. None of
// them depends on another result, so one slow call cannot hold up the others.
func (g *GitHub) fetchSignals(ctx context.Context, pr int, sha, base string) githubSignals {
	var out githubSignals
	var group errgroup.Group
	group.SetLimit(observeParallelLimit)
	group.Go(func() error {
		out.checks, out.checksErr = g.checks(ctx, sha)
		return out.checksErr
	})
	group.Go(func() error {
		out.approvals, out.approvalsErr = g.approvals(ctx, pr)
		return out.approvalsErr
	})
	group.Go(func() error {
		out.cmp, out.compareErr = guarded(g.br, func() (*github.CommitsComparison, error) {
			c, _, err := g.Client.Repositories.CompareCommits(ctx, g.Owner, g.Repo, base, sha, nil)
			return c, err
		})
		return out.compareErr
	})
	_ = group.Wait()
	return out
}

// checks returns the check runs for commit sha. GitHub lists every attempt of
// a named check on a SHA, so a cancelled earlier attempt must not shadow a
// later success. Keep the newest attempt per name by completion time, breaking
// ties by start time, and map a cancelled conclusion to skipped.
func (g *GitHub) checks(ctx context.Context, sha string) ([]core.CheckResult, error) {
	checkRuns, err := paginate(g.br, func(opts *github.ListOptions) ([]*github.CheckRun, *github.Response, error) {
		res, resp, err := g.Client.Checks.ListCheckRunsForRef(ctx, g.Owner, g.Repo, sha, &github.ListCheckRunsOptions{
			ListOptions: *opts,
		})
		if res == nil {
			return nil, resp, err
		}
		return res.CheckRuns, resp, err
	})
	if err != nil {
		return nil, err
	}
	latest := make(map[string]*github.CheckRun, len(checkRuns))
	for _, c := range checkRuns {
		name := c.GetName()
		prev := latest[name]
		if prev == nil || checkRunNewer(c, prev) {
			latest[name] = c
		}
	}
	out := make([]core.CheckResult, 0, len(latest))
	for _, c := range latest {
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

// checkRunNewer reports whether c is a later attempt than prev. Completion time
// orders attempts; a still-running attempt is newer than a completed one only
// when it started later, so a new attempt after a red one wins.
func checkRunNewer(c, prev *github.CheckRun) bool {
	ct, pt := c.GetCompletedAt().Time, prev.GetCompletedAt().Time
	if !ct.Equal(pt) {
		return ct.After(pt)
	}
	return c.GetStartedAt().Time.After(prev.GetStartedAt().Time)
}

// checkState maps a GitHub check run to a core.CheckState.
func checkState(c *github.CheckRun) core.CheckState {
	if c.GetStatus() != "completed" {
		return core.CheckPending
	}
	switch strings.ToLower(c.GetConclusion()) {
	case "success", "neutral", "skipped", "cancelled":
		if conclusion := strings.ToLower(c.GetConclusion()); conclusion == "skipped" || conclusion == "cancelled" {
			return core.CheckSkipped
		}
		return core.CheckSuccess
	case "":
		return core.CheckPending
	default:
		return core.CheckFailure
	}
}

// changedFiles reads every changed file of a PR, one page at a time.
func (g *GitHub) changedFiles(ctx context.Context, pr int) ([]*github.CommitFile, error) {
	return paginate(g.br, func(opts *github.ListOptions) ([]*github.CommitFile, *github.Response, error) {
		return g.Client.PullRequests.ListFiles(ctx, g.Owner, g.Repo, pr, opts)
	})
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

// approvals counts the latest approval review per user on pr.
func (g *GitHub) approvals(ctx context.Context, pr int) (int, error) {
	reviews, err := paginate(g.br, func(opts *github.ListOptions) ([]*github.PullRequestReview, *github.Response, error) {
		return g.Client.PullRequests.ListReviews(ctx, g.Owner, g.Repo, pr, opts)
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

// updateBranchResponse is the GraphQL response for a branch update.
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

// updateBranchMutation asks GitHub to update a pull request branch.
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
	// Read through the ETag cache so an update after an Observe sends one
	// conditional GET instead of refetching the whole pull request.
	p, err := g.getPR(ctx, pr)
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

// Merge squashes pr while its head is still expectedSHA.
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

var _ core.ScmGateway = (*GitHub)(nil)
