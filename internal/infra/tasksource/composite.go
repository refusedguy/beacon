package tasksource

import (
	"fmt"
	"strings"

	"github.com/refusedguy/beacon/internal/infra/shared"
	"github.com/refusedguy/beacon/pkg/core"
)

type Config struct {
	Provider  string
	Dir       string
	Owner     string
	Repo      string
	GitlabURL string
}

func New(cfg Config, token string) (core.TaskSource, error) {
	switch strings.ToLower(cfg.Provider) {
	case "markdown", "":
		return NewMarkdown(cfg.Dir), nil
	case "github", "github_issues":
		client, err := shared.NewGitHubClient(token)
		if err != nil {
			return nil, err
		}
		return NewGitHubIssues(client, cfg.Owner, cfg.Repo), nil
	case "gitlab", "gitlab_issues":
		if cfg.Owner == "" || cfg.Repo == "" {
			return nil, fmt.Errorf("gitlab task source requires owner and repo")
		}
		return NewGitLabIssues(cfg.GitlabURL, token, cfg.Owner, cfg.Repo), nil
	default:
		return nil, fmt.Errorf("unknown task source provider %q", cfg.Provider)
	}
}
