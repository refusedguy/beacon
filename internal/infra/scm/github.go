// Package scm adapts GitHub and GitLab to core.ScmGateway.
package scm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
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

	prCache     *etagCache[*github.PullRequest]
	prGet       singleflight.Group
	checkCache  *etagCache[checkCacheValue]
	checkGet    singleflight.Group
	reviewCache *etagCache[reviewCacheValue]
	reviewGet   singleflight.Group
	compares    *compareCache
	compareGet  singleflight.Group
}

// NewGitHub builds a GitHub gateway for owner and repo.
func NewGitHub(client *github.Client, owner, repo, base string) *GitHub {
	return &GitHub{
		Client:      client,
		Owner:       owner,
		Repo:        repo,
		base:        base,
		br:          newSCMBreaker("github"),
		prCache:     newETagCache[*github.PullRequest](),
		checkCache:  newETagCache[checkCacheValue](),
		reviewCache: newETagCache[reviewCacheValue](),
		compares:    newCompareCache(),
	}
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

// paginateCapped follows NextPage until GitHub stops sending one or the cap is
// hit. It reports both the items and whether the cap cut the read short, so an
// adapter can mark an incomplete observation instead of acting on a partial
// list. Every request goes through br, so pagination does not bypass the
// breaker. When id is non-nil, entries with the same non-zero provider ID are
// collapsed: pages can shift under concurrent writes and repeat an entry.
func paginateCapped[T any](br *gobreaker.CircuitBreaker[any], fetch func(*github.ListOptions) ([]T, *github.Response, error), id func(T) int64) ([]T, bool, error) {
	var all []T
	seen := make(map[int64]bool)
	opts := github.ListOptions{PerPage: 100}
	for n := 0; n < maxGitHubPages; n++ {
		page, err := guarded(br, func() (githubPage[T], error) {
			items, resp, err := fetch(&opts)
			return githubPage[T]{items: items, resp: resp}, err
		})
		if err != nil {
			return nil, false, err
		}
		for _, item := range page.items {
			if id != nil {
				key := id(item)
				if key != 0 {
					if seen[key] {
						continue
					}
					seen[key] = true
				}
			}
			all = append(all, item)
		}
		if page.resp == nil || page.resp.NextPage == 0 {
			return all, false, nil
		}
		opts.Page = page.resp.NextPage
	}
	return all, true, nil
}

// paginate keeps the uncapped entry point used by callers that only need the
// items and cannot mark an observation incomplete.
func paginate[T any](br *gobreaker.CircuitBreaker[any], fetch func(*github.ListOptions) ([]T, *github.Response, error)) ([]T, error) {
	items, _, err := paginateCapped(br, fetch, nil)
	return items, err
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
	// tree; checks stay a policy decision for ChecksRule. The comparison below
	// cross-checks this flag for an open PR.
	conflict := mergeableConflict(p)
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
	signals := g.fetchSignals(ctx, pr, sha, compareBase(p, g.base))

	// Report hard failures in the original call order so callers see the same
	// first error. Capped reads are kept and marked incomplete instead.
	var failed observationErrors
	if signals.checksErr != nil {
		return o, signals.checksErr
	}
	o.Checks = signals.checks
	if signals.checksCapped {
		failed.add("checks", errCappedRead)
	}
	for _, c := range signals.checks {
		if c.State == core.CheckFailure {
			o.FailureExcerpt = c.Output
			break
		}
	}
	if state == "open" && len(signals.checks) == 0 {
		if err := g.observeWorkflowSkipped(ctx, &o, pr, &failed); err != nil {
			return o, err
		}
	}
	if signals.approvalsErr != nil {
		return o, signals.approvalsErr
	}
	if signals.approvalsCapped {
		failed.add("reviews", errCappedRead)
	}
	o.Approvals = signals.approvals
	o.Labels = observationLabels(p.Labels)
	applyCompare(&o, p, signals, &failed)
	failed.apply(&o)
	return o, nil
}

// observationLabels copies the provider labels into the observation.
func observationLabels(labels []*github.Label) []string {
	if len(labels) == 0 {
		return nil
	}
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		out = append(out, label.GetName())
	}
	return out
}

// compareBase returns the base commit to compare against. The commit in the PR
// object stays addressable after the base branch is deleted, unlike its ref.
func compareBase(p *github.PullRequest, fallback string) string {
	if sha := p.GetBase().GetSHA(); sha != "" {
		return sha
	}
	if ref := p.GetBase().GetRef(); ref != "" {
		return ref
	}
	return fallback
}

// applyCompare records the comparison and cross-checks the provider conflict
// flag. A dirty state with no commits ahead of the base is stale, not a
// conflict, and must not dispatch an agent in place of a branch update.
func applyCompare(o *core.Observation, p *github.PullRequest, signals githubSignals, failed *observationErrors) {
	if signals.compareErr != nil {
		failed.add("compare", compareFailure(signals.compareErr))
		return
	}
	if signals.cmp == nil {
		return
	}
	o.Conflict = crossCheckConflict(p, signals.cmp)
	o.Behind = signals.cmp.GetBehindBy() > 0
	o.BaseSHA = signals.cmp.GetBaseCommit().GetSHA()
}

// observeWorkflowSkipped reads the changed files and records a capped read on
// the same error list. A hard failure is returned to Observe unchanged.
func (g *GitHub) observeWorkflowSkipped(ctx context.Context, o *core.Observation, pr int, failed *observationErrors) error {
	files, capped, err := g.changedFilesCapped(ctx, pr)
	if err != nil {
		return err
	}
	if capped {
		failed.add("files", errCappedRead)
	}
	o.WorkflowSkipped = ignoredPathsOnly(files)
	return nil
}

func mergeableConflict(p *github.PullRequest) bool {
	state := p.GetMergeableState()
	return state == "dirty" || state == "conflict"
}

// crossCheckConflict keeps a provider conflict only when the comparison shows
// the head actually diverged. An empty ahead-by count means there is nothing
// for the provider to conflict over.
func crossCheckConflict(p *github.PullRequest, cmp *github.CommitsComparison) bool {
	return mergeableConflict(p) && cmp.GetAheadBy() > 0
}

// compareFailure names a deleted base explicitly instead of leaking a raw
// provider error into the observation.
func compareFailure(err error) error {
	var respErr *github.ErrorResponse
	if errors.As(err, &respErr) && respErr.Response != nil && respErr.Response.StatusCode == http.StatusNotFound {
		return fmt.Errorf("base branch was deleted or its commit is unavailable: %w", err)
	}
	return err
}

// githubSignals carries the results of the first Observe round. Errors stay
// separate so Observe can report them in the original call order.
type githubSignals struct {
	checks          []core.CheckResult
	approvals       int
	cmp             *github.CommitsComparison
	checksCapped    bool
	approvalsCapped bool
	checksErr       error
	approvalsErr    error
	compareErr      error
}

// fetchSignals runs checks, approvals and the base comparison together. None of
// them depends on another result, so one slow call cannot hold up the others.
func (g *GitHub) fetchSignals(ctx context.Context, pr int, sha, base string) githubSignals {
	var out githubSignals
	var group errgroup.Group
	group.SetLimit(observeParallelLimit)
	group.Go(func() error {
		out.checks, out.checksCapped, out.checksErr = g.checksCapped(ctx, sha)
		return out.checksErr
	})
	group.Go(func() error {
		out.approvals, out.approvalsCapped, out.approvalsErr = g.approvalsCapped(ctx, pr)
		return out.approvalsErr
	})
	group.Go(func() error {
		out.cmp, out.compareErr = g.compare(ctx, base, sha)
		return out.compareErr
	})
	_ = group.Wait()
	return out
}

// checks returns the check runs for commit sha.
func (g *GitHub) checks(ctx context.Context, sha string) ([]core.CheckResult, error) {
	checks, _, err := g.checksCapped(ctx, sha)
	return checks, err
}

// checksCapped reads check runs keyed by SHA. Filter is set to latest
// explicitly so the adapter never depends on a provider default that can
// change. The conditional read returns the cached slice on a 304 and a newly
// completed check still replaces it on the next tick.
func (g *GitHub) checksCapped(ctx context.Context, sha string) ([]core.CheckResult, bool, error) {
	v, err := cachedFetch(&g.checkGet, g.checkCache, "checks:"+sha, func(etag string) (checkCacheValue, bool, string, error) {
		return g.fetchChecks(ctx, sha, etag)
	})
	if err != nil {
		return nil, false, err
	}
	return v.checks, v.capped, nil
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
	files, _, err := g.changedFilesCapped(ctx, pr)
	return files, err
}

func (g *GitHub) changedFilesCapped(ctx context.Context, pr int) ([]*github.CommitFile, bool, error) {
	return paginateCapped(g.br, func(opts *github.ListOptions) ([]*github.CommitFile, *github.Response, error) {
		return g.Client.PullRequests.ListFiles(ctx, g.Owner, g.Repo, pr, opts)
	}, nil)
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
	n, _, err := g.approvalsCapped(ctx, pr)
	return n, err
}

// reviewState is the newest review seen for one login. The monotonic review ID
// orders same-time reviews and stands in when a timestamp is missing.
type reviewState struct {
	state string
	id    int64
	at    time.Time
}

// reviewNewer picks the later review. A dismissed review submitted at the same
// instant as an approval must win, so equal timestamps fall back to the review
// ID instead of the order the pages happened to arrive in.
func reviewNewer(c, prev reviewState) bool {
	if c.at.IsZero() || prev.at.IsZero() {
		return c.id > prev.id
	}
	if !c.at.Equal(prev.at) {
		return c.at.After(prev.at)
	}
	return c.id > prev.id
}

// approvalsCapped counts approvals keyed by PR. The conditional read returns
// the cached count on a 304 and a new review replaces it on the next tick.
func (g *GitHub) approvalsCapped(ctx context.Context, pr int) (int, bool, error) {
	v, err := cachedFetch(&g.reviewGet, g.reviewCache, fmt.Sprintf("reviews:%d", pr), func(etag string) (reviewCacheValue, bool, string, error) {
		return g.fetchReviews(ctx, pr, etag)
	})
	if err != nil {
		return 0, false, err
	}
	return v.approvals, v.capped, nil
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
	g.writePRCache(pr, func(p *github.PullRequest) {
		if head := p.GetHead(); head != nil {
			updated := *head
			updated.SHA = github.String(sha)
			p.Head = &updated
		}
	})
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
	g.writePRCache(pr, func(p *github.PullRequest) {
		p.State = github.String("closed")
		p.Merged = github.Bool(true)
	})
	return nil
}

var _ core.ScmGateway = (*GitHub)(nil)
