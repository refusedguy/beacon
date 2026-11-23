package scm

import (
	"fmt"
	"strings"

	"github.com/refusedguy/beacon/internal/infra/shared"
	"github.com/refusedguy/beacon/pkg/core"
)

type Config struct {
	Provider   string
	Owner      string
	Repo       string
	BaseURL    string
	BaseBranch string
}

func New(cfg Config, token string) (core.ScmGateway, error) {
	base := cfg.BaseBranch
	if base == "" {
		base = "main"
	}
	switch strings.ToLower(cfg.Provider) {
	case "github", "":
		client, err := shared.NewGitHubClient(token)
		if err != nil {
			return nil, err
		}
		return NewGitHub(client, cfg.Owner, cfg.Repo, base), nil
	case "gitlab":
		if cfg.Owner == "" || cfg.Repo == "" {
			return nil, fmt.Errorf("gitlab SCM requires owner and repo")
		}
		return NewGitLab(cfg.BaseURL, token, cfg.Owner, cfg.Repo), nil
	default:
		return nil, fmt.Errorf("unknown SCM provider %q", cfg.Provider)
	}
}
