package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
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

type testStore struct{ watches []Watch }

func (s testStore) ActiveWatches(context.Context) ([]Watch, error) { return s.watches, nil }

type testMerger struct{ merged map[int]bool }

func (m testMerger) IsMerged(_ context.Context, _ string, pr int) (bool, error) {
	return m.merged[pr], nil
}

type testHeadRefs struct {
	byPR map[int]string
	err  error
}

func (h *testHeadRefs) HeadRefName(_ context.Context, _ string, pr int) (string, error) {
	if h.err != nil {
		return "", h.err
	}
	return h.byPR[pr], nil
}

type shaUpdate struct{ id, sha string }

type rebaseUpdate struct {
	id    string
	files []string
}

type testUpdater struct {
	heads   []shaUpdate
	rebases []rebaseUpdate
	err     error
}

func (u *testUpdater) SetHeadSHA(_ context.Context, id, sha string) error {
	u.heads = append(u.heads, shaUpdate{id: id, sha: sha})
	return u.err
}

func (u *testUpdater) SetRebase(_ context.Context, id string, files []string) error {
	u.rebases = append(u.rebases, rebaseUpdate{id: id, files: append([]string(nil), files...)})
	return u.err
}

type wakeCall struct {
	id       string
	revision uint64
	verdict  string
	reason   string
}

type testWaker struct {
	calls []wakeCall
	err   error
}

func (w *testWaker) Wake(_ context.Context, id string, revision uint64, verdict string) error {
	w.calls = append(w.calls, wakeCall{id: id, revision: revision, verdict: verdict})
	return w.err
}

func (w *testWaker) WakeWithReason(_ context.Context, id string, revision uint64, verdict, reason string) error {
	w.calls = append(w.calls, wakeCall{id: id, revision: revision, verdict: verdict, reason: reason})
	return w.err
}

type plainWaker struct{ calls []wakeCall }

func (w *plainWaker) Wake(_ context.Context, id string, revision uint64, verdict string) error {
	w.calls = append(w.calls, wakeCall{id: id, revision: revision, verdict: verdict})
	return nil
}

func testWatch(pr int, sha string) Watch {
	return Watch{
		ID:      fmt.Sprintf("watch-%d", pr),
		Repo:    "refusedguy/beacon",
		PR:      pr,
		HeadSHA: sha,
		Verdict: "watching",
	}
}

const worktreeListing = `worktree /wt/w311
HEAD oldsha
branch refs/heads/feat/w311

worktree /wt/main
HEAD mainsha
branch refs/heads/main
`

const renamedListing = `worktree /wt/fix
HEAD oldsha
branch refs/heads/fix/arbitrary-name

worktree /wt/main
HEAD mainsha
branch refs/heads/main
`

const graphQLListing = `worktree /wt/w7b
HEAD oldsha
branch refs/heads/fix/w7-b

worktree /wt/main
HEAD mainsha
branch refs/heads/main
`

// successGit answers the full clean-behind path for one branch.
func successGit(listing, branch string) *fakeGit {
	git := &fakeGit{}
	git.respond = func(c gitCall) (string, error) {
		switch c.key() {
		case "worktree list --porcelain":
			return listing, nil
		case "status --porcelain":
			return "", nil
		case "fetch origin main":
			return "", nil
		case "rev-list --left-right --count origin/main...HEAD":
			return "2\t0\n", nil
		case "rebase origin/main":
			return "", nil
		case "rev-parse HEAD":
			return "newsha\n", nil
		case "push --force-with-lease origin " + branch:
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git %s", c.key())
		}
	}
	return git
}

func TestRebaseAllUsesGraphQLHeadRef(t *testing.T) {
	git := successGit(graphQLListing, "fix/w7-b")
	updater, waker := &testUpdater{}, &testWaker{}
	r := &Rebaser{
		Git:                   git,
		Store:                 testStore{watches: []Watch{testWatch(311, "oldsha")}},
		Merger:                testMerger{},
		Updater:               updater,
		Waker:                 waker,
		HeadRefs:              &testHeadRefs{byPR: map[int]string{311: "refs/heads/fix/w7-b"}},
		AutoRebaseCleanBehind: true,
	}

	results, err := r.RebaseAll(context.Background())
	if err != nil {
		t.Fatalf("RebaseAll: %v", err)
	}
	if len(results) != 1 || !results[0].Pushed || results[0].Branch != "fix/w7-b" {
		t.Fatalf("results = %+v, want one push on fix/w7-b", results)
	}
	pushes := git.callsFor("push --force-with-lease origin fix/w7-b")
	if len(pushes) != 1 || pushes[0].dir != "/wt/w7b" {
		t.Fatalf("pushes = %+v, want one in /wt/w7b", pushes)
	}
	if len(updater.heads) != 1 || updater.heads[0] != (shaUpdate{id: "watch-311", sha: "newsha"}) {
		t.Errorf("head updates = %+v, want watch-311 -> newsha", updater.heads)
	}
	if len(waker.calls) != 0 {
		t.Errorf("successful auto-rebase woke an agent: %+v", waker.calls)
	}
}

func TestRebaseAllUsesWorktreeHeadRefWithoutHardcodedBranch(t *testing.T) {
	git := successGit(renamedListing, "fix/arbitrary-name")
	r := &Rebaser{
		Git:                   git,
		Store:                 testStore{watches: []Watch{testWatch(311, "oldsha")}},
		Merger:                testMerger{},
		Updater:               &testUpdater{},
		AutoRebaseCleanBehind: true,
	}

	results, err := r.RebaseAll(context.Background())
	if err != nil {
		t.Fatalf("RebaseAll: %v", err)
	}
	if len(results) != 1 || !results[0].Pushed || results[0].Branch != "fix/arbitrary-name" {
		t.Fatalf("results = %+v, want one push on fix/arbitrary-name", results)
	}
	for _, c := range git.calls {
		if strings.Contains(c.key(), "feat/w") {
			t.Errorf("git call used a guessed branch: %s", c.key())
		}
	}
}

func TestRebaseAllConflictAbortsAndWakesWithFiles(t *testing.T) {
	git := &fakeGit{}
	statusCalls := 0
	git.respond = func(c gitCall) (string, error) {
		switch c.key() {
		case "worktree list --porcelain":
			return worktreeListing, nil
		case "status --porcelain":
			statusCalls++
			if statusCalls == 1 {
				return "", nil
			}
			return "UU a.go\nAA c.go\n?? notes.txt\n", nil
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
	updater, waker := &testUpdater{}, &testWaker{}
	r := &Rebaser{
		Git:                   git,
		Store:                 testStore{watches: []Watch{testWatch(311, "oldsha")}},
		Merger:                testMerger{},
		Updater:               updater,
		Waker:                 waker,
		AutoRebaseCleanBehind: true,
	}

	results, err := r.RebaseAll(context.Background())
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want *ConflictError", err)
	}
	wantFiles := []string{"a.go", "b.go", "c.go"}
	if strings.Join(conflict.Files, ",") != strings.Join(wantFiles, ",") {
		t.Errorf("conflict files = %v, want %v", conflict.Files, wantFiles)
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
	if len(updater.rebases) != 1 || updater.rebases[0].id != "watch-311" ||
		strings.Join(updater.rebases[0].files, ",") != strings.Join(wantFiles, ",") {
		t.Errorf("rebase updates = %+v, want watch-311 with %v", updater.rebases, wantFiles)
	}
	if len(updater.heads) != 0 {
		t.Errorf("conflict wrote a new head SHA: %+v", updater.heads)
	}
	if len(waker.calls) != 1 {
		t.Fatalf("wake calls = %+v, want one", waker.calls)
	}
	call := waker.calls[0]
	if call.verdict != VerdictRebase || call.id != "watch-311" {
		t.Errorf("wake = %+v, want verdict rebase for watch-311", call)
	}
	for _, name := range wantFiles {
		if !strings.Contains(call.reason, name) {
			t.Errorf("wake reason %q misses %s", call.reason, name)
		}
	}
}

func TestRebaseAllConflictWithPlainWaker(t *testing.T) {
	git := &fakeGit{}
	git.respond = func(c gitCall) (string, error) {
		switch c.key() {
		case "worktree list --porcelain":
			return worktreeListing, nil
		case "status --porcelain":
			return "", nil
		case "fetch origin main":
			return "", nil
		case "rev-list --left-right --count origin/main...HEAD":
			return "2\t0\n", nil
		case "rebase origin/main":
			return "", errors.New("rebase failed")
		case "diff --name-only --diff-filter=U":
			return "conflict.go\n", nil
		case "rebase --abort":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git %s", c.key())
		}
	}
	waker := &plainWaker{}
	r := &Rebaser{
		Git:                   git,
		Store:                 testStore{watches: []Watch{testWatch(311, "oldsha")}},
		Updater:               &testUpdater{},
		Waker:                 waker,
		AutoRebaseCleanBehind: true,
	}
	if _, err := r.RebaseAll(context.Background()); err == nil {
		t.Fatal("RebaseAll: want conflict error")
	}
	if len(waker.calls) != 1 || waker.calls[0].verdict != VerdictRebase {
		t.Errorf("wake calls = %+v, want one rebase verdict", waker.calls)
	}
}

func TestRebaseAllSkipsWhenFlagOff(t *testing.T) {
	git := &fakeGit{}
	git.respond = func(c gitCall) (string, error) {
		if c.key() == "worktree list --porcelain" {
			return worktreeListing, nil
		}
		return "", fmt.Errorf("unexpected git %s", c.key())
	}
	updater, waker := &testUpdater{}, &testWaker{}
	r := &Rebaser{
		Git:     git,
		Store:   testStore{watches: []Watch{testWatch(311, "oldsha")}},
		Updater: updater,
		Waker:   waker,
	}

	results, err := r.RebaseAll(context.Background())
	if err != nil {
		t.Fatalf("RebaseAll: %v", err)
	}
	if len(results) != 1 || !results[0].Skipped || results[0].Reason != "auto-rebase disabled" {
		t.Fatalf("results = %+v, want skipped auto-rebase disabled", results)
	}
	if len(git.calls) != 1 {
		t.Errorf("git calls = %+v, want only the worktree listing", git.calls)
	}
	if len(updater.heads)+len(updater.rebases)+len(waker.calls) != 0 {
		t.Error("disabled rebaser changed Watch state or woke an agent")
	}
}

func TestRebaseAllSkipsDirtyWorktree(t *testing.T) {
	git := &fakeGit{}
	git.respond = func(c gitCall) (string, error) {
		switch c.key() {
		case "worktree list --porcelain":
			return worktreeListing, nil
		case "status --porcelain":
			return " M dirty.go\n", nil
		default:
			return "", fmt.Errorf("unexpected git %s", c.key())
		}
	}
	r := &Rebaser{
		Git:                   git,
		Store:                 testStore{watches: []Watch{testWatch(311, "oldsha")}},
		Updater:               &testUpdater{},
		AutoRebaseCleanBehind: true,
	}

	results, err := r.RebaseAll(context.Background())
	if err != nil {
		t.Fatalf("RebaseAll: %v", err)
	}
	if len(results) != 1 || !results[0].Skipped || results[0].Reason != "dirty worktree" {
		t.Fatalf("results = %+v, want skipped dirty worktree", results)
	}
	for _, call := range git.calls {
		if call.key() == "fetch origin main" || call.key() == "rebase origin/main" ||
			strings.HasPrefix(call.key(), "push ") {
			t.Errorf("dirty worktree was mutated by %s", call.key())
		}
	}
}

func TestRebaseAllIdempotent(t *testing.T) {
	rebased := false
	git := &fakeGit{}
	git.respond = func(c gitCall) (string, error) {
		switch c.key() {
		case "worktree list --porcelain":
			return worktreeListing, nil
		case "status --porcelain":
			return "", nil
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
	updater := &testUpdater{}
	r := &Rebaser{
		Git:                   git,
		Store:                 testStore{watches: []Watch{testWatch(311, "oldsha")}},
		Updater:               updater,
		AutoRebaseCleanBehind: true,
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
	if len(updater.heads) != 1 {
		t.Errorf("head updates = %+v, want 1", updater.heads)
	}
}

func TestRebaseAllSkipsMergedAndMissingWorktrees(t *testing.T) {
	git := &fakeGit{}
	git.respond = func(c gitCall) (string, error) {
		switch c.key() {
		case "worktree list --porcelain":
			return worktreeListing, nil
		case "status --porcelain", "fetch origin main", "rebase origin/main":
			return "", nil
		case "rev-list --left-right --count origin/main...HEAD":
			return "2\t0\n", nil
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
		Store: testStore{watches: []Watch{
			testWatch(311, "oldsha"),
			testWatch(312, "sha312"),
			testWatch(313, "sha313"),
		}},
		Merger:                testMerger{merged: map[int]bool{312: true}},
		Updater:               &testUpdater{},
		HeadRefs:              &testHeadRefs{byPR: map[int]string{313: "fix/no-worktree"}},
		AutoRebaseCleanBehind: true,
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

func TestRebaseAllHeadRefResolverError(t *testing.T) {
	git := &fakeGit{}
	git.respond = func(c gitCall) (string, error) {
		if c.key() == "worktree list --porcelain" {
			return worktreeListing, nil
		}
		return "", fmt.Errorf("unexpected git %s", c.key())
	}
	wantErr := errors.New("graphql snapshot unavailable")
	r := &Rebaser{
		Git:                   git,
		Store:                 testStore{watches: []Watch{testWatch(311, "oldsha")}},
		Updater:               &testUpdater{},
		HeadRefs:              &testHeadRefs{err: wantErr},
		AutoRebaseCleanBehind: true,
	}

	results, err := r.RebaseAll(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if !errors.Is(results[0].Err, wantErr) || results[0].Pushed {
		t.Errorf("result = %+v, want resolver error and no push", results[0])
	}
	for _, call := range git.calls {
		if call.key() != "worktree list --porcelain" {
			t.Errorf("git ran %s after a head ref error", call.key())
		}
	}
}

func TestRebaseAllNeedsUpdaterBeforePush(t *testing.T) {
	git := successGit(worktreeListing, "feat/w311")
	r := &Rebaser{
		Git:                   git,
		Store:                 testStore{watches: []Watch{testWatch(311, "oldsha")}},
		AutoRebaseCleanBehind: true,
	}

	results, err := r.RebaseAll(context.Background())
	if err == nil {
		t.Fatal("RebaseAll: want an error when no updater is installed")
	}
	if results[0].Pushed || results[0].Err == nil {
		t.Errorf("result = %+v, want no push and an error", results[0])
	}
	if pushes := git.callsFor("push --force-with-lease origin feat/w311"); len(pushes) != 0 {
		t.Errorf("branch was pushed without an updater: %+v", pushes)
	}
}

func TestParseWorktreesSkipsDetached(t *testing.T) {
	listing := `worktree /wt/attached
HEAD oldsha
branch refs/heads/feat/w311

worktree /wt/detached
HEAD detachedsha

worktree /wt/main
HEAD mainsha
branch refs/heads/main
`
	got := parseWorktrees(listing)
	if len(got) != 2 {
		t.Fatalf("worktrees = %+v, want two attached entries", got)
	}
	if got[0].dir != "/wt/attached" || got[0].branch != "feat/w311" || got[0].head != "oldsha" {
		t.Errorf("first worktree = %+v, want /wt/attached feat/w311 oldsha", got[0])
	}
	if got[1].dir != "/wt/main" || got[1].branch != "main" {
		t.Errorf("second worktree = %+v, want /wt/main main", got[1])
	}
}
