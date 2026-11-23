package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/refusedguy/beacon/internal/infra/config"
)

type initFlags struct {
	maxInFlight     int
	maxActiveAgents int
	taskSource      string
	scm             string
	harness         string
	force           bool
}

func newInitCommand() *cobra.Command {
	f := &initFlags{}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create .beacon/config.yaml from the git remote",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(f)
		},
	}
	cmd.Flags().IntVar(&f.maxInFlight, "max-in-flight", 20, "maximum unfinished runs")
	cmd.Flags().IntVar(&f.maxActiveAgents, "max-active-agents", 4, "maximum active agent sessions")
	cmd.Flags().StringVar(&f.taskSource, "task-source", "", "markdown, github_issues or gitlab_issues")
	cmd.Flags().StringVar(&f.scm, "scm", "", "github or gitlab")
	cmd.Flags().StringVar(&f.harness, "harness", "opencode", "opencode or cli")
	cmd.Flags().BoolVar(&f.force, "force", false, "overwrite existing config")
	return cmd
}

type remote struct {
	provider string
	owner    string
	repo     string
	host     string
}

func parseRemote(raw string) (remote, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, ".git")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 && !strings.Contains(s[:i], "/") {
		rest := s[i+1:]
		if j := strings.Index(rest, "/"); j >= 0 && allDigits(rest[:j]) {
			rest = rest[j+1:]
		}
		s = s[:i] + "/" + rest
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 3 {
		return remote{}, fmt.Errorf("cannot parse git remote %q", raw)
	}
	host := parts[0]
	owner := parts[len(parts)-2]
	repo := parts[len(parts)-1]
	provider := "github"
	if strings.Contains(host, "gitlab") {
		provider = "gitlab"
	}
	return remote{provider: provider, owner: owner, repo: repo, host: host}, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func gitOutput(args ...string) string {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runInit(f *initFlags) error {
	r, err := parseRemote(gitOutput("remote", "get-url", "origin"))
	if err != nil {
		return err
	}
	base := gitOutput("rev-parse", "--abbrev-ref", "origin/HEAD")
	base = strings.TrimPrefix(base, "origin/")
	if base == "" || base == "origin/HEAD" {
		base = "main"
	}
	source := f.taskSource
	if source == "" {
		source = "github_issues"
		if r.provider == "gitlab" {
			source = "gitlab_issues"
		}
	}
	provider := f.scm
	if provider == "" {
		provider = r.provider
	}
	tokenEnv := "GITHUB_TOKEN"
	baseURL := ""
	if provider == "gitlab" {
		tokenEnv = "GITLAB_TOKEN"
		if r.host != "gitlab.com" {
			baseURL = "https://" + r.host + "/api/v4"
		} else {
			baseURL = "https://gitlab.com/api/v4"
		}
	}
	cfg := config.Config{
		StateDB:      ".beacon/state.db",
		WorkspaceDir: ".beacon/worktrees",
		BaseBranch:   base,
		PollInterval: "30s",
		Concurrency: config.Concurrency{
			MaxInFlight: f.maxInFlight, MaxActiveAgents: f.maxActiveAgents,
		},
		TaskSource: config.TaskSource{Provider: source, Dir: "docs/tasks", Owner: r.owner, Repo: r.repo, TokenEnv: tokenEnv},
		SCM:        config.SCM{Provider: provider, Owner: r.owner, Repo: r.repo, TokenEnv: tokenEnv, BaseURL: baseURL},
		Harness:    config.Harness{Provider: f.harness, Endpoint: "http://127.0.0.1:4096"},
		Merge:      config.Merge{RequiredChecks: []string{}, RequiredApprovals: 0, Labels: []string{}, CommandGate: "", AutoRebase: true, MaxAttempts: 3},
		OnBaseRed:  config.OnBaseRed{Mode: "spawn_fixer", MaxFixAttempts: 2},
	}
	path := ".beacon/config.yaml"
	if err := config.WriteDefault(path, cfg, f.force); err != nil {
		return err
	}
	if err := appendGitignore(".gitignore"); err != nil {
		return err
	}
	fmt.Printf("wrote %s for %s/%s (%s)\n", path, r.owner, r.repo, provider)
	return nil
}

func appendGitignore(path string) error {
	entries := []string{
		".beacon/state.db*",
		".beacon/worktrees/",
		".beacon/connection.json",
		".beacon/*.lock",
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(string(data), "\n")
	have := make(map[string]bool)
	for _, line := range lines {
		have[strings.TrimSpace(line)] = true
	}
	var add []string
	for _, e := range entries {
		if !have[e] {
			add = append(add, e)
		}
	}
	if len(add) == 0 {
		return nil
	}
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	lines = append(lines, add...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
