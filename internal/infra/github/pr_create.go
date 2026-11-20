// Package github holds the GitHub adapter for Beacon.
//
// This file extends the read-only REST client with the write operations the
// `beacon pr create` command needs. It uses go-github directly, so no gh CLI
// process is spawned per call. Token resolution prefers the environment and
// never shells out (ADR 0004, T3.329).
package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/refusedguy/beacon/internal/domain"
)

// PRProviderName is the name registered by the default cmd wiring.
const PRProviderName = "github"

// PRClient is the write-capable GitHub client. It owns a go-github client with
// its own timeout and user agent; no ETag cache is shared with RestClient,
// because writes must never be answered from a conditional GET cache.
type PRClient struct {
	gh *gh.Client
}

// TokenFromEnv resolves a GitHub token from the same environment chain as the
// read client, without its legacy gh fallback. A missing token is an error,
// never an anonymous request.
func TokenFromEnv() (string, error) {
	for _, name := range tokenEnvChain {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token, nil
		}
	}
	return "", errors.New("GitHub token: BEACON_GITHUB_TOKEN and GITHUB_TOKEN are empty")
}

// NewPRClient builds a client from the environment token.
func NewPRClient() (*PRClient, error) {
	token, err := TokenFromEnv()
	if err != nil {
		return nil, err
	}
	return NewPRClientWithToken(token), nil
}

// NewPRClientWithToken builds a client from an explicit token. Tests use a
// local HTTP client through NewPRClientWithGH and never need a real token.
func NewPRClientWithToken(token string) *PRClient {
	client, err := gh.NewClient(
		gh.WithAuthToken(token),
		gh.WithTimeout(restTimeout),
		gh.WithUserAgent(restUserAgent),
	)
	if err != nil {
		return nil
	}
	return NewPRClientWithGH(client)
}

// NewPRClientWithGH adapts a preconfigured go-github client. It is the test
// seam for an httptest server or a custom transport.
func NewPRClientWithGH(client *gh.Client) *PRClient {
	if client == nil {
		return nil
	}
	return &PRClient{gh: client}
}

// Name implements the provider name used by the cmd adapter.
func (c *PRClient) Name() string { return PRProviderName }

// CreatePRInput is the GitHub-specific input of a create call.
type CreatePRInput struct {
	Owner string
	Repo  string
	Head  string
	Base  string
	Title string
	Body  string
	Draft bool
}

// PRCreateResult is the GitHub-specific result of a create call.
type PRCreateResult struct {
	Number  int
	URL     string
	HeadSHA string
	BaseRef string
}

// Create opens a pull request through POST /repos/{owner}/{repo}/pulls.
func (c *PRClient) Create(ctx context.Context, in CreatePRInput) (PRCreateResult, error) {
	if c == nil || c.gh == nil {
		return PRCreateResult{}, errors.New("github: PR client is nil")
	}
	if err := validateRepo(in.Owner, in.Repo); err != nil {
		return PRCreateResult{}, err
	}
	if strings.TrimSpace(in.Head) == "" || strings.TrimSpace(in.Base) == "" || strings.TrimSpace(in.Title) == "" {
		return PRCreateResult{}, errors.New("github: head, base and title are required")
	}
	title, body, draft := in.Title, in.Body, in.Draft
	pr, _, err := c.gh.PullRequests.Create(ctx, in.Owner, in.Repo, gh.CreatePullRequest{
		Title: &title,
		Head:  in.Head,
		Base:  in.Base,
		Body:  &body,
		Draft: &draft,
	})
	if err != nil {
		return PRCreateResult{}, fmt.Errorf("github create PR %s/%s: %w", in.Owner, in.Repo, err)
	}
	if pr == nil {
		return PRCreateResult{}, errors.New("github create PR: empty response")
	}
	return PRCreateResult{
		Number:  pr.GetNumber(),
		URL:     pr.GetHTMLURL(),
		HeadSHA: pr.GetHead().GetSHA(),
		BaseRef: pr.GetBase().GetRef(),
	}, nil
}

// PullState is the GitHub-specific state snapshot of one PR.
type PullState struct {
	State     string
	HeadSHA   string
	BaseRef   string
	Mergeable string
}

// PRState reads one pull request through GET /repos/{owner}/{repo}/pulls/{n}.
func (c *PRClient) PRState(ctx context.Context, owner, repo string, number int) (PullState, error) {
	if c == nil || c.gh == nil {
		return PullState{}, errors.New("github: PR client is nil")
	}
	if err := validateRepo(owner, repo); err != nil {
		return PullState{}, err
	}
	pr, _, err := c.gh.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return PullState{}, fmt.Errorf("github PR state %s/%s#%d: %w", owner, repo, number, err)
	}
	if pr == nil {
		return PullState{}, errors.New("github PR state: empty response")
	}
	mergeable := "unknown"
	if pr.Mergeable != nil {
		mergeable = fmt.Sprintf("%t", pr.GetMergeable())
	}
	return PullState{
		State:     pr.GetState(),
		HeadSHA:   pr.GetHead().GetSHA(),
		BaseRef:   pr.GetBase().GetRef(),
		Mergeable: mergeable,
	}, nil
}

// PRComment is one comment on a pull request.
type PRComment struct {
	ID     int64
	Author string
	Body   string
}

// ListComments reads the PR discussion through the REST comments endpoint.
func (c *PRClient) ListComments(ctx context.Context, owner, repo string, number int) ([]PRComment, error) {
	if c == nil || c.gh == nil {
		return nil, errors.New("github: PR client is nil")
	}
	if err := validateRepo(owner, repo); err != nil {
		return nil, err
	}
	comments, _, err := c.gh.PullRequests.ListComments(ctx, owner, repo, number, nil)
	if err != nil {
		return nil, fmt.Errorf("github PR comments %s/%s#%d: %w", owner, repo, number, err)
	}
	out := make([]PRComment, 0, len(comments))
	for _, comment := range comments {
		if comment == nil {
			continue
		}
		out = append(out, PRComment{
			ID:     comment.GetID(),
			Author: comment.GetUser().GetLogin(),
			Body:   comment.GetBody(),
		})
	}
	return out, nil
}

// MergePR lands a pull request with a squash merge. It is the write side of
// the PRProvider port; the orchestrator's legacy gh merger is unchanged.
func (c *PRClient) MergePR(ctx context.Context, owner, repo string, number int) error {
	if c == nil || c.gh == nil {
		return errors.New("github: PR client is nil")
	}
	if err := validateRepo(owner, repo); err != nil {
		return err
	}
	_, _, err := c.gh.PullRequests.Merge(ctx, owner, repo, number, "", &gh.PullRequestOptions{
		MergeMethod: "squash",
	})
	if err != nil {
		return fmt.Errorf("github merge PR %s/%s#%d: %w", owner, repo, number, err)
	}
	return nil
}

func validateRepo(owner, repo string) error {
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(repo) == "" {
		return errors.New("github: owner and repo are required")
	}
	return nil
}

// WakeRegistry is the shared wake adapter registry seen through its Deliver
// method. wake.Registry satisfies it; this package never imports that adapter.
type WakeRegistry interface {
	Deliver(ctx context.Context, adapter, sessionID string, p domain.WakePayload) error
}

// DeliverWakePointer sends an identifier-only pointer through the registry.
// It builds the domain payload here, because cmd and app must not import
// domain (ADR 0005); the adapter name stays opaque to the core. ObligationID
// is derived from the watch alias so a later ack can find it.
func DeliverWakePointer(ctx context.Context, registry WakeRegistry, adapter, sessionID, watchID, verdict string, revision uint64) error {
	if registry == nil {
		return errors.New("github: wake registry is nil")
	}
	if sessionID == "" {
		return errors.New("github: wake sessionID is empty")
	}
	return registry.Deliver(ctx, adapter, sessionID, domain.WakePayload{
		WatchID:      watchID,
		ObligationID: domain.ObligationKey(watchID, revision, sessionID),
		Revision:     revision,
		Verdict:      verdict,
	})
}

// PRClientTimeout is exposed so callers can reason about the write timeout
// without importing the unexported REST constants.
func PRClientTimeout() time.Duration { return restTimeout }
