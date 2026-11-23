package shared

import "github.com/google/go-github/v93/github"

func NewGitHubClient(token string) (*github.Client, error) {
	if token == "" {
		return github.NewClient()
	}
	return github.NewClient(github.WithAuthToken(token))
}
