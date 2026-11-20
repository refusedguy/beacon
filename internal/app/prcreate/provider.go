// Package prcreate implements the "beacon pr create" workflow: open a pull
// request on a hosting service and subscribe the caller's session to a Beacon
// Watch in one command. The core is provider-neutral; concrete adapters are
// composed in cmd/beacon.
package prcreate

import (
	"context"
	"errors"
)

// DefaultProvider is used when no flag, environment variable or project config
// names a hosting service.
const DefaultProvider = "github"

// CreateRequest is the neutral input for opening a pull request. Repo is an
// owner/name slug; the provider splits it without the app knowing the host.
type CreateRequest struct {
	Repo    string
	HeadRef string
	BaseRef string
	Title   string
	Body    string
	HeadSHA string
	Draft   bool
}

// CreatedPR is the neutral result of opening a pull request. URL is the human
// page of the PR, not an API endpoint.
type CreatedPR struct {
	Number  int
	URL     string
	HeadSHA string
	BaseRef string
}

// PullState is the neutral state snapshot of one pull request.
type PullState struct {
	State     string
	HeadSHA   string
	BaseRef   string
	Mergeable string
}

// Comment is one pull request comment.
type Comment struct {
	ID     int64
	Author string
	Body   string
}

// PRProvider is the hosting-service port. Create opens a PR, PRState reads its
// state, ListComments reads its discussion and Merge lands it. Adding another
// host means implementing these four methods and registering the value in the
// Service; the workflow never names a concrete provider (ADR 0004, ADR 0005).
type PRProvider interface {
	Name() string
	Create(ctx context.Context, in CreateRequest) (CreatedPR, error)
	PRState(ctx context.Context, repo string, number int) (PullState, error)
	ListComments(ctx context.Context, repo string, number int) ([]Comment, error)
	Merge(ctx context.Context, repo string, number int) error
}

// ErrProviderNotRegistered reports a configured name with no adapter.
var ErrProviderNotRegistered = errors.New("prcreate: provider is not registered")
