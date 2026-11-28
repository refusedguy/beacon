package scm

import (
	"context"

	"github.com/google/go-github/v93/github"

	"github.com/refusedguy/beacon/pkg/core"
)

func (g *GitHub) BaseBranchStatus(ctx context.Context, base string) (core.BaseStatus, error) {
	branch, _, err := g.Client.Repositories.GetBranch(ctx, g.Owner, g.Repo, base, 1)
	if err != nil {
		return core.BaseStatus{}, redactErr(err)
	}
	sha := branch.GetCommit().GetSHA()
	checks, err := g.checks(ctx, sha)
	if err != nil {
		return core.BaseStatus{}, redactErr(err)
	}
	status := core.BaseStatus{BaseBranch: base, HeadSHA: sha, FailedChecks: checks}
	redactCheckOutputs(status.FailedChecks)
	for _, c := range status.FailedChecks {
		if c.State == core.CheckFailure {
			status.FailureExcerpt = c.Output
			break
		}
	}
	return status, nil
}

func (g *GitHub) FindOpenPRByBranch(ctx context.Context, branch string) (int, error) {
	prs, err := paginate(g.br, func(opts *github.ListOptions) ([]*github.PullRequest, *github.Response, error) {
		return g.Client.PullRequests.List(ctx, g.Owner, g.Repo, &github.PullRequestListOptions{
			State:       "open",
			Head:        g.Owner + ":" + branch,
			ListOptions: *opts,
		})
	})
	if err != nil {
		return 0, redactErr(err)
	}
	if len(prs) == 0 {
		return 0, core.Errorf(core.CodeNotFound, "no open PR for branch %s", branch)
	}
	return prs[0].GetNumber(), nil
}
