package main

import (
	"os/exec"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/config"
)

func TestParseRemote(t *testing.T) {
	cases := []struct {
		raw      string
		provider string
		owner    string
		repo     string
	}{
		{"git@github.com:acme/demo.git", "github", "acme", "demo"},
		{"https://github.com/acme/demo.git", "github", "acme", "demo"},
		{"ssh://git@gitlab.com/acme/demo.git", "gitlab", "acme", "demo"},
		{"ssh://git@gitlab.example.com:2222/acme/demo.git", "gitlab", "acme", "demo"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			r, err := parseRemote(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if r.provider != tc.provider || r.owner != tc.owner || r.repo != tc.repo {
				t.Fatalf("got %+v", r)
			}
		})
	}
}

func TestRunInitUsesNewRebaseLimit(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/acme/demo.git")
	t.Chdir(dir)

	if err := runInit(&initFlags{}); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(".beacon/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Merge.MaxAttempts != 12 {
		t.Fatalf("generated merge.max_attempts = %d, want 12", cfg.Merge.MaxAttempts)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
