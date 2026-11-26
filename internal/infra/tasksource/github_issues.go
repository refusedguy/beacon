package tasksource

import (
	"context"
	"strconv"
	"strings"

	"github.com/google/go-github/v93/github"

	"github.com/refusedguy/beacon/pkg/core"
)

type GitHubIssues struct {
	Client *github.Client
	Owner  string
	Repo   string
}

func NewGitHubIssues(client *github.Client, owner, repo string) *GitHubIssues {
	return &GitHubIssues{Client: client, Owner: owner, Repo: repo}
}

// maxIssuePages caps paginated issue listing so a bad Link header cannot keep
// the source fetching pages forever. With PerPage=100 it covers 10,000 issues.
const maxIssuePages = 100

// paginateIssues follows NextPage and accumulates every page of issues.
func paginateIssues[T any](fetch func(*github.ListOptions) ([]T, *github.Response, error)) ([]T, error) {
	var all []T
	opts := github.ListOptions{PerPage: 100}
	for n := 0; n < maxIssuePages; n++ {
		items, resp, err := fetch(&opts)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if resp == nil || resp.NextPage == 0 {
			return all, nil
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

func (g *GitHubIssues) ListAvailable(ctx context.Context) ([]core.Task, error) {
	issues, err := paginateIssues(func(opts *github.ListOptions) ([]*github.Issue, *github.Response, error) {
		return g.Client.Issues.ListByRepo(ctx, g.Owner, g.Repo, &github.IssueListByRepoOptions{
			State:       "open",
			ListOptions: *opts,
		})
	})
	if err != nil {
		return nil, err
	}
	var out []core.Task
	for _, issue := range issues {
		rec := githubIssueRecord(issue)
		if !issueAvailable(rec) {
			continue
		}
		out = append(out, parseIssueRecord(rec, "GH"))
	}
	core.SortTasks(out)
	return out, nil
}

// githubIssueRecord adapts a GitHub issue to the shared parser input.
func githubIssueRecord(issue *github.Issue) issueRecord {
	labels := make([]string, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		labels = append(labels, label.GetName())
	}
	return issueRecord{
		Number:      issue.GetNumber(),
		Title:       issue.GetTitle(),
		Body:        issue.GetBody(),
		Labels:      labels,
		SourceRef:   issue.GetHTMLURL(),
		PullRequest: issue.GetPullRequestLinks() != nil,
	}
}

func (g *GitHubIssues) Claim(ctx context.Context, id core.TaskID) error {
	n, err := issueNumber(id)
	if err != nil {
		return err
	}
	_, _, err = g.Client.Issues.AddLabelsToIssue(ctx, g.Owner, g.Repo, n, []string{"beacon/claimed"})
	return err
}

func (g *GitHubIssues) Complete(ctx context.Context, id core.TaskID) error {
	n, err := issueNumber(id)
	if err != nil {
		return err
	}
	_, _, err = g.Client.Issues.Update(ctx, g.Owner, g.Repo, n, github.UpdateIssueRequest{State: github.Ptr("closed")})
	return err
}

// MarkInWorkspace is a no-op: GitHub issues are updated through the API.
func (g *GitHubIssues) MarkInWorkspace(context.Context, core.TaskID, string, string) error {
	return nil
}

// Create opens a GitHub issue. The issue number becomes the task ID so the
// Claim and Complete calls can address it.
func (g *GitHubIssues) Create(ctx context.Context, task core.Task) (core.Task, error) {
	body, err := issueBody(task)
	if err != nil {
		return core.Task{}, err
	}
	issue, _, err := g.Client.Issues.Create(ctx, g.Owner, g.Repo, github.CreateIssueRequest{
		Title: task.Title,
		Body:  github.Ptr(body),
	})
	if err != nil {
		return core.Task{}, err
	}
	return parseIssueRecord(githubIssueRecord(issue), "GH"), nil
}

func issueNumber(id core.TaskID) (int, error) {
	s := strings.TrimPrefix(string(id), "GH-")
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, core.Errorf(core.CodeInvalidInput, "issue id %q", id)
	}
	return n, nil
}
