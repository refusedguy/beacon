package harness

import (
	"fmt"
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

type Config struct {
	Provider       string
	Command        string
	PromptTemplate string
}

func New(cfg Config) (core.AgentHarness, error) {
	switch strings.ToLower(cfg.Provider) {
	case "cli", "claude", "codex", "exec", "":
		if strings.TrimSpace(cfg.Command) == "" {
			return nil, fmt.Errorf("cli harness requires command")
		}
		return NewCLIExec(cfg.Command, cfg.PromptTemplate), nil
	default:
		return nil, fmt.Errorf("harness provider %q must be wired from its own module", cfg.Provider)
	}
}
