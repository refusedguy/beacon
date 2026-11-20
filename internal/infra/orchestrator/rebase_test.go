package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

type gitCall struct {
	dir  string
	args []string
}

func (c gitCall) key() string { return strings.Join(c.args, " ") }

// fakeGit records every command so the tests can assert the effects a caller
// observes outside the package: which worktree was rebased and whether a force
// push happened.
type fakeGit struct {
	calls   []gitCall
	respond func(gitCall) (string, error)
}

func (f *fakeGit) Run(_ context.Context, dir string, args ...string) (string, error) {
	call := gitCall{dir: dir, args: append([]string(nil), args...)}
	f.calls = append(f.calls, call)
	if f.respond == nil {
		return "", fmt.Errorf("unexpected git %s", call.key())
	}
	return f.respond(call)
}

func (f *fakeGit) callsFor(key string) []gitCall {
	var out []gitCall
	for _, c := range f.calls {
		if c.key() == key {
			out = append(out, c)
		}
	}
	return out
}

type testStore struct{ watches []domain.Watch }

func (s testStore) ActiveWatches() ([]domain.Watch, error) { return s.watches, nil }

type testMerger struct{ merged map[int]bool }

func (m testMerger) IsMerged(_ context.Context, _ string, pr int) (bool, error) {
	return m.merged[pr], nil
}

func testWatch(pr int, sha string) domain.Watch {
	return domain.Watch{
		ID:      fmt.Sprintf("watch-%d", pr),
		Repo:    "refusedguy/beacon",
		PR:      pr,
		HeadSHA: sha,
		Status:  domain.StatusWatching,
	}
}

const worktreeListing = `worktree /wt/w311
HEAD oldsha
branch refs/heads/feat/w311

worktree /wt/main
HEAD mainsha
branch refs/heads/main
`

func TestRebaseAllSuccess(t *testing.T) {
	git := &fakeGit{}
	git.respond = func(c gitCall) (string, error) {
		switch c.key() {
		case "worktree list --porcelain":
			return worktreeListing, nil
		case "fetch origin main":
			if c.dir != "/wt/w311" {
				t.Errorf("fetch dir = %q, want /wt/w311", c.dir)
			}
			return "", nil
		case "rev-list --left-right --count origin/main...HEAD":
			return "2\t0\n", nil
		case "rebase origin/main":
			if c.dir != "/wt/w311" {
				t.Errorf("rebase dir = %q, want /wt/w311", c.dir)
			}
			return "", nil
		case "rev-parse HEAD":
			return "newsha\n", nil
		case "push --force-with-lease origin feat/w311":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git %s", c.key())
		}
	}
	r := &Rebaser{
		Git:    git,
		Store:  testStore{watches: []domain.Watch{testWatch(311, "oldsha")}},
		Merger: testMerger{},
	}

	results, err := r.RebaseAll(context.Background())
	if err != nil {
		t.Fatalf("RebaseAll: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	got := results[0]
	if got.NewSHA != "newsha" || got.OldSHA != "oldsha" {
		t.Errorf("SHA old/new = %q/%q, want oldsha/newsha", got.OldSHA, got.NewSHA)
	}
	if !got.Pushed || got.Skipped {
		t.Errorf("pushed/skipped = %t/%t, want true/false", got.Pushed, got.Skipped)
	}
	if got.Worktree != "/wt/w311" {
		t.Errorf("worktree = %q, want /wt/w311", got.Worktree)
	}
	pushes := git.callsFor("push --force-with-lease origin feat/w311")
	if len(pushes) != 1 || pushes[0].dir != "/wt/w311" {
		t.Errorf("force pushes = %+v, want one in /wt/w311", pushes)
	}
}

func TestRebaseAllConflict(t *testing.T) {
	git := &fakeGit{}
	git.respond = func(c gitCall) (string, error) {
		switch c.key() {
		case "worktree list --porcelain":
			return worktreeListing, nil
		case "fetch origin main":
			return "", nil
		case "rev-list --left-right --count origin/main...HEAD":
			return "2\t0\n", nil
		case "rebase origin/main":
			return "CONFLICT (content): Merge conflict in a.go\n", errors.New("rebase failed")
		case "diff --name-only --diff-filter=U":
			return "a.go\nb.go\n", nil
		case "rebase --abort":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git %s", c.key())
		}
	}
	r := &Rebaser{
		Git:    git,
		Store:  testStore{watches: []domain.Watch{testWatch(311, "oldsha")}},
		Merger: testMerger{},
	}

	results, err := r.RebaseAll(context.Background())
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want *ConflictError", err)
	}
	if conflict.PR != 311 || strings.Join(conflict.Files, ",") != "a.go,b.go" {
		t.Errorf("conflict = %+v, want PR 311 and files a.go,b.go", conflict)
	}
	if len(results) != 1 || results[0].Pushed {
		t.Fatalf("results = %+v, want one result and no push", results)
	}
	if !errors.As(results[0].Err, new(*ConflictError)) {
		t.Errorf("result error = %v, want *ConflictError", results[0].Err)
	}
	if pushes := git.callsFor("push --force-with-lease origin feat/w311"); len(pushes) != 0 {
		t.Errorf("conflicted branch was pushed: %+v", pushes)
	}
	aborts := git.callsFor("rebase --abort")
	if len(aborts) != 1 || aborts[0].dir != "/wt/w311" {
		t.Errorf("aborts = %+v, want one in /wt/w311", aborts)
	}
}

func TestRebaseAllIdempotent(t *testing.T) {
	rebased := false
	git := &fakeGit{}
	git.respond = func(c gitCall) (string, error) {
		switch c.key() {
		case "worktree list --porcelain":
			return worktreeListing, nil
		case "fetch origin main":
			return "", nil
		case "rev-list --left-right --count origin/main...HEAD":
			if rebased {
				return "0\t2\n", nil
			}
			return "2\t0\n", nil
		case "rebase origin/main":
			rebased = true
			return "", nil
		case "rev-parse HEAD":
			return "newsha\n", nil
		case "push --force-with-lease origin feat/w311":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git %s", c.key())
		}
	}
	r := &Rebaser{
		Git:    git,
		Store:  testStore{watches: []domain.Watch{testWatch(311, "oldsha")}},
		Merger: testMerger{},
	}

	first, err := r.RebaseAll(context.Background())
	if err != nil {
		t.Fatalf("first RebaseAll: %v", err)
	}
	second, err := r.RebaseAll(context.Background())
	if err != nil {
		t.Fatalf("second RebaseAll: %v", err)
	}
	if !first[0].Pushed || first[0].NewSHA != "newsha" {
		t.Errorf("first result = %+v, want a push to newsha", first[0])
	}
	if !second[0].Skipped || second[0].Pushed || second[0].Reason != "up to date" {
		t.Errorf("second result = %+v, want skipped up to date", second[0])
	}
	if pushes := git.callsFor("push --force-with-lease origin feat/w311"); len(pushes) != 1 {
		t.Errorf("force pushes = %d, want 1", len(pushes))
	}
	if rebases := git.callsFor("rebase origin/main"); len(rebases) != 1 {
		t.Errorf("rebases = %d, want 1", len(rebases))
	}
}

func TestRebaseAllSkipsMergedAndMissingWorktrees(t *testing.T) {
	git := &fakeGit{}
	git.respond = func(c gitCall) (string, error) {
		switch c.key() {
		case "worktree list --porcelain":
			return worktreeListing, nil
		case "fetch origin main", "rev-list --left-right --count origin/main...HEAD", "rebase origin/main":
			if c.dir != "/wt/w311" {
				t.Errorf("command %s ran in %q, want only /wt/w311", c.key(), c.dir)
			}
			if c.key() == "rev-list --left-right --count origin/main...HEAD" {
				return "2\t0\n", nil
			}
			return "", nil
		case "rev-parse HEAD":
			return "newsha\n", nil
		case "push --force-with-lease origin feat/w311":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git %s", c.key())
		}
	}
	r := &Rebaser{
		Git: git,
		Store: testStore{watches: []domain.Watch{
			testWatch(311, "oldsha"),
			testWatch(312, "sha312"),
			testWatch(313, "sha313"),
		}},
		Merger: testMerger{merged: map[int]bool{312: true}},
	}

	results, err := r.RebaseAll(context.Background())
	if err != nil {
		t.Fatalf("RebaseAll: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	if !results[0].Pushed {
		t.Errorf("PR 311 result = %+v, want a push", results[0])
	}
	if !results[1].Skipped || results[1].Reason != "merged" || results[1].NewSHA != "sha312" {
		t.Errorf("PR 312 result = %+v, want skipped merged", results[1])
	}
	if !results[2].Skipped || results[2].Reason != "no worktree" {
		t.Errorf("PR 313 result = %+v, want skipped no worktree", results[2])
	}
}
