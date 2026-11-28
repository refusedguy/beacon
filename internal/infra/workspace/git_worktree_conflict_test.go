package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

// TestGitWorktreeRebaseBaseAbortsConflictingAutostash proves that a stash
// apply conflict is not published. Git exits zero from the rebase but leaves
// unmerged paths and markers, so RebaseBase must abort before CommitPath runs.
func TestGitWorktreeRebaseBaseAbortsConflictingAutostash(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	ctx := context.Background()

	cardDir := filepath.Join(repo, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(cardDir, 0o755): %v", err)
	}
	card := filepath.Join(cardDir, "T-1.md")
	if err := os.WriteFile(card, []byte("status: available\nowner: base\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(card, base): %v", err)
	}
	gitCmd(t, repo, "add", "docs/tasks/T-1.md")
	gitCmd(t, repo, "commit", "-m", "add card")
	gitCmd(t, repo, "push", "origin", "main")

	ws, err := wt.Create(ctx, core.Task{ID: "T-AUTOSTASH-CONFLICT"})
	if err != nil {
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-AUTOSTASH-CONFLICT\"}): %v", err)
	}

	// An uncommitted card edit is the only dirt, so RebaseBase uses autostash.
	wsCard := filepath.Join(ws.Path, "docs", "tasks", "T-1.md")
	if err := os.WriteFile(wsCard, []byte("status: available\nowner: local\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(wsCard, local): %v", err)
	}

	// The base changes the same line, so applying the autostash conflicts.
	if err := os.WriteFile(card, []byte("status: available\nowner: advanced\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(card, advanced): %v", err)
	}
	gitCmd(t, repo, "add", "docs/tasks/T-1.md")
	gitCmd(t, repo, "commit", "-m", "advance card")
	gitCmd(t, repo, "push", "origin", "main")

	err = wt.RebaseBase(ctx, ws, "main")
	if err == nil {
		t.Fatal("RebaseBase() error = nil, want CodeConflict for the autostash conflict")
	}
	if code := core.CodeOf(err); code != core.CodeConflict {
		t.Fatalf("RebaseBase() code = %q, want %q (error %v)", code, core.CodeConflict, err)
	}
	if !strings.Contains(err.Error(), "docs/tasks/T-1.md") {
		t.Fatalf("RebaseBase() error = %v, want the unmerged path named", err)
	}

	if out := gitCmd(t, ws.Path, "ls-files", "--unmerged"); strings.TrimSpace(out) != "" {
		t.Fatalf("unmerged paths left in the index: %q", out)
	}
	content, err := os.ReadFile(wsCard)
	if err != nil {
		t.Fatalf("os.ReadFile(wsCard): %v", err)
	}
	for _, marker := range []string{"<<<<<<<", "=======", ">>>>>>>"} {
		if strings.Contains(string(content), marker) {
			t.Fatalf("conflict marker %q left in worktree: %q", marker, content)
		}
	}
}
