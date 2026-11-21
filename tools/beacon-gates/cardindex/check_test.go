package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitLogFromProcessRelativeRoot reproduces the CI cardindex failure. The
// workflow runs "go run ./cardindex -root ../.." with working-directory
// tools/beacon-gates, so the child process chdirs into the repository root
// before git runs. A relative -C is then resolved a second time and climbs out
// of the work tree. The path must be made absolute before it reaches git.
func TestGitLogFromProcessRelativeRoot(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	git("init", "-q")
	git("config", "user.email", "ci@example.test")
	git("config", "user.name", "CI")
	if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "fix: close T1.1 (#1)")

	// Mirror the workflow: cwd = tools/beacon-gates, root = ../..
	cwd := filepath.Join(repo, "tools", "beacon-gates")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	out, err := gitLogFromProcess("../..")
	if err != nil {
		t.Fatalf("gitLogFromProcess: %v", err)
	}
	if !strings.Contains(string(out), "fix: close T1.1 (#1)") {
		t.Fatalf("git log missing committed subject: %q", out)
	}
}
