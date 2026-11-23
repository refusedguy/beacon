package scm

import (
	"context"

	"github.com/google/go-github/v93/github"

	"github.com/refusedguy/beacon/pkg/core"
)

// OpenPR creates a pull request from head into base and returns its number.
func (g *GitHub) OpenPR(ctx context.Context, head, base, title string) (int, error) {
	v, err := g.guarded(func() (any, error) {
		pr, _, err := g.Client.PullRequests.Create(ctx, g.Owner, g.Repo, github.CreatePullRequest{
			Title: github.Ptr(title),
			Head:  head,
			Base:  base,
		})
		return pr, err
	})
	if err != nil {
		return 0, err
	}
	pr, ok := v.(*github.PullRequest)
	if !ok || pr.GetNumber() == 0 {
		return 0, core.Errorf(core.CodeUnavailable, "open PR returned no number")
	}
	return pr.GetNumber(), nil
}
