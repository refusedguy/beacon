package scm

import (
	"github.com/google/go-github/v93/github"

	"github.com/refusedguy/beacon/internal/infra/shared"
)

func NewGitHubClient(token string) (*github.Client, error) {
	return shared.NewGitHubClient(token)
}
