package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/domain"
)

type orchFakeStore struct {
	watches     []orchestrator.Watch
	merged      map[string]bool
	closed      []string
	fixAttempts []string
}

func newOrchFakeStore(watches ...orchestrator.Watch) *orchFakeStore {
	return &orchFakeStore{watches: watches, merged: make(map[string]bool)}
}

func (s *orchFakeStore) key(id string, revision uint64) string {
	return fmt.Sprintf("%s@%d", id, revision)
}

func (s *orchFakeStore) ActiveWatches(context.Context) ([]orchestrator.Watch, error) {
	return s.watches, nil
}

func (s *orchFakeStore) WasMerged(_ context.Context, watchID string, revision uint64) (bool, error) {
	return s.merged[s.key(watchID, revision)], nil
}

func (s *orchFakeStore) RecordMerge(_ context.Context, watchID string, revision uint64) error {
	s.merged[s.key(watchID, revision)] = true
	return nil
}

func (s *orchFakeStore) CloseWatch(_ context.Context, watchID string) error {
	s.closed = append(s.closed, watchID)
	return nil
}

func (s *orchFakeStore) RecordFixAttempt(_ context.Context, watchID string) error {
	s.fixAttempts = append(s.fixAttempts, watchID)
	return nil
}

type orchFakeMerger struct{ calls []int }

func (m *orchFakeMerger) Merge(_ context.Context, _ string, pr int) error {
	m.calls = append(m.calls, pr)
	return nil
}

func withOrchDeps(t *testing.T, st orchestrator.Store, merger orchestrator.Merger, waker orchestrator.Waker) {
	t.Helper()
	previous := makeOrchestratorDeps
	t.Cleanup(func() { SetOrchestratorDeps(previous) })
	SetOrchestratorDeps(func(*CLI) (orchestratorDeps, func() error, error) {
		return orchestratorDeps{Store: st, Merger: merger, Waker: waker}, func() error { return nil }, nil
	})
}

// TestProjectWatchCopiesBlocked proves the composition-root projection carries
// the block details, including FIXAttempts, into the orchestrator's decision
// shape. FailedChecks is copied so later mutation of the domain Watch cannot
// change a decision already made.
func TestProjectWatchCopiesBlocked(t *testing.T) {
	checks := []string{"ci-build", "gates"}
	w := domain.Watch{
		ID: "w1", Repo: "refusedguy/beacon", PR: 42, HeadSHA: "abc123",
		Verdict: domain.VerdictBlocked, Revision: 7,
		Blocked: &domain.BlockedInfo{
			Reason:       "чужой тест",
			FailedChecks: checks,
			Since:        time.Unix(1, 0),
			BlockedBy:    "executor",
			FixAttempts:  2,
		},
	}

	got := projectWatch(w)

	if got.Blocked == nil {
		t.Fatal("projection dropped Watch.Blocked")
	}
	if got.Blocked.Reason != "чужой тест" || got.Blocked.BlockedBy != "executor" {
		t.Fatalf("block context = %+v", got.Blocked)
	}
	if got.Blocked.FixAttempts != 2 {
		t.Fatalf("FixAttempts = %d, want 2", got.Blocked.FixAttempts)
	}
	if !reflect.DeepEqual(got.Blocked.FailedChecks, []string{"ci-build", "gates"}) {
		t.Fatalf("FailedChecks = %v", got.Blocked.FailedChecks)
	}
	if got.ID != w.ID || got.PR != w.PR || got.HeadSHA != w.HeadSHA ||
		got.Revision != w.Revision || got.Verdict != string(w.Verdict) {
		t.Fatalf("identity fields = %+v", got)
	}

	w.Blocked.FailedChecks[0] = "mutated"
	w.Blocked.FixAttempts = 99
	if got.Blocked.FailedChecks[0] != "ci-build" || got.Blocked.FixAttempts != 2 {
		t.Fatalf("projection shares block state with the domain Watch: %+v", got.Blocked)
	}

	if nilBlocked := projectWatch(domain.Watch{ID: "w2"}).Blocked; nilBlocked != nil {
		t.Fatalf("unblocked Watch projected with block info: %+v", nilBlocked)
	}
}

// TestProjectActiveWatchesIncludesBlocked proves the orchestrator actually
// sees a blocked Watch: domain ActiveWatches omits it, so the projection must
// read all Watches and keep the blocked status explicitly.
func TestProjectActiveWatchesIncludesBlocked(t *testing.T) {
	all := []domain.Watch{
		{ID: "watching", Status: domain.StatusWatching},
		{ID: "notifying", Status: domain.StatusNotifying},
		{ID: "blocked", Status: domain.StatusBlocked, Blocked: &domain.BlockedInfo{Reason: "чужой тест", FixAttempts: 1}},
		{ID: "closed", Status: domain.StatusClosed},
		{ID: "parked", Status: domain.StatusParked},
	}

	got := projectActiveWatches(all)

	ids := make([]string, 0, len(got))
	for _, w := range got {
		ids = append(ids, w.ID)
	}
	if !reflect.DeepEqual(ids, []string{"watching", "notifying", "blocked"}) {
		t.Fatalf("projected IDs = %q, want watching/notifying/blocked", ids)
	}
	if got[2].Blocked == nil || got[2].Blocked.FixAttempts != 1 {
		t.Fatalf("blocked projection lost FixAttempts: %+v", got[2])
	}
}

func TestOrchestrateDispatch(t *testing.T) {
	previous := orchestratorRunner
	t.Cleanup(func() { orchestratorRunner = previous })
	var gotArgs []string
	RegisterOrchestrator(func(_ *CLI, args []string) int {
		gotArgs = append([]string(nil), args...)
		return 7
	})
	c, _, _, _ := t06NewCLI(t, t06NoChild(t))

	if code := c.Run([]string{"orchestrate", "--dry-run"}); code != 7 {
		t.Fatalf("exit = %d, want 7", code)
	}
	if !reflect.DeepEqual(gotArgs, []string{"--dry-run"}) {
		t.Fatalf("args = %q, want [--dry-run]", gotArgs)
	}
}

func ghViewArgs(pr int, fields string) []string {
	return []string{"pr", "view", strconv.Itoa(pr), "--json", fields, "--repo", "refusedguy/beacon"}
}

func ghMergeArgs(pr int) []string {
	return []string{"pr", "merge", strconv.Itoa(pr), "--squash", "--delete-branch"}
}

func TestGhMergerRunsSquashDeleteBranchAfterCleanGate(t *testing.T) {
	c, _, _, calls := t06NewCLI(t, func(call t06Call) runResult {
		if call.name != "gh" {
			t.Fatalf("unexpected process: %+v", call)
		}
		switch {
		case reflect.DeepEqual(call.args, ghViewArgs(42, "mergeable,mergeStateStatus,state")):
			return runResult{stdout: []byte(`{"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`)}
		case reflect.DeepEqual(call.args, ghMergeArgs(42)):
			return runResult{}
		case reflect.DeepEqual(call.args, ghViewArgs(42, "state")):
			return runResult{stdout: []byte(`{"state":"MERGED"}`)}
		default:
			t.Fatalf("unexpected gh call: %+v", call)
			return runResult{}
		}
	})
	m := ghMerger{c: c}

	outcome, err := m.MergeChecked(context.Background(), "refusedguy/beacon", 42)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Merged || outcome.Pending || outcome.Reason != "" {
		t.Fatalf("outcome = %+v, want confirmed MERGE", outcome)
	}
	want := [][]string{ghViewArgs(42, "mergeable,mergeStateStatus,state"), ghMergeArgs(42), ghViewArgs(42, "state")}
	if len(*calls) != len(want) {
		t.Fatalf("calls = %+v, want %d", *calls, len(want))
	}
	for i, args := range want {
		if (*calls)[i].name != "gh" || !reflect.DeepEqual((*calls)[i].args, args) {
			t.Fatalf("call %d = %+v, want gh %v", i, (*calls)[i], args)
		}
	}
}

func TestGhMergerDirtyPRIsRefusedBeforeMerge(t *testing.T) {
	c, _, _, calls := t06NewCLI(t, func(call t06Call) runResult {
		if call.name != "gh" || !reflect.DeepEqual(call.args, ghViewArgs(312, "mergeable,mergeStateStatus,state")) {
			t.Fatalf("unexpected call: %+v", call)
		}
		return runResult{stdout: []byte(`{"mergeable":"MERGEABLE","mergeStateStatus":"DIRTY","state":"OPEN"}`)}
	})
	m := ghMerger{c: c}

	outcome, err := m.MergeChecked(context.Background(), "refusedguy/beacon", 312)
	if err != nil {
		t.Fatalf("dirty PR is a diagnostic, not an error: %v", err)
	}
	if outcome.Merged || outcome.Pending {
		t.Fatalf("outcome = %+v, want a held PR", outcome)
	}
	if !strings.Contains(outcome.Reason, "mergeStateStatus=DIRTY") {
		t.Fatalf("reason = %q, want DIRTY", outcome.Reason)
	}
	if len(*calls) != 1 {
		t.Fatalf("gh pr merge must not run for a dirty PR: %+v", *calls)
	}
}

func TestGhMergerMergeErrorStaysOneLine(t *testing.T) {
	const hint = "GraphQL: Auto merge is not allowed (enablePullRequestAutoMerge)\n" +
		"\nFor more information see: https://cli.github.com/manual/gh_pr_merge\n" +
		"Use 'gh pr merge --squash' instead."
	c, _, _, _ := t06NewCLI(t, func(call t06Call) runResult {
		switch {
		case reflect.DeepEqual(call.args, ghViewArgs(42, "mergeable,mergeStateStatus,state")):
			return runResult{stdout: []byte(`{"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`)}
		case reflect.DeepEqual(call.args, ghMergeArgs(42)):
			return runResult{code: 1, stderr: []byte(hint)}
		default:
			t.Fatalf("unexpected call: %+v", call)
			return runResult{}
		}
	})
	m := ghMerger{c: c}

	_, err := m.MergeChecked(context.Background(), "refusedguy/beacon", 42)
	if err == nil {
		t.Fatal("expected a merge failure")
	}
	if strings.Contains(err.Error(), "\n") || strings.Contains(err.Error(), "\r") {
		t.Fatalf("multi-line gh stderr leaked into the error: %q", err.Error())
	}
	if strings.Contains(err.Error(), "https://cli.github.com") {
		t.Fatalf("gh hint leaked into the error: %q", err.Error())
	}
	if !strings.HasPrefix(err.Error(), "gh pr merge 42: GraphQL:") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestGhMergerPendingStateKeepsWatchOpen(t *testing.T) {
	c, _, _, _ := t06NewCLI(t, func(call t06Call) runResult {
		switch {
		case reflect.DeepEqual(call.args, ghViewArgs(42, "mergeable,mergeStateStatus,state")):
			return runResult{stdout: []byte(`{"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`)}
		case reflect.DeepEqual(call.args, ghMergeArgs(42)):
			return runResult{}
		case reflect.DeepEqual(call.args, ghViewArgs(42, "state")):
			return runResult{stdout: []byte(`{"state":"OPEN"}`)}
		default:
			t.Fatalf("unexpected call: %+v", call)
			return runResult{}
		}
	})
	m := ghMerger{c: c}

	outcome, err := m.MergeChecked(context.Background(), "refusedguy/beacon", 42)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Pending || outcome.Merged {
		t.Fatalf("outcome = %+v, want pending", outcome)
	}
}

func TestOrchestrateDryRunPrintsActionsWithoutMerge(t *testing.T) {
	st := newOrchFakeStore(orchestrator.Watch{
		ID: "w1", Repo: "refusedguy/beacon", PR: 42, Verdict: "ready", Revision: 3,
	})
	merger := &orchFakeMerger{}
	withOrchDeps(t, st, merger, nil)
	c, out, errOut, _ := t06NewCLI(t, t06NoChild(t))

	if code := c.Run([]string{"orchestrate", "--dry-run"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if len(merger.calls) != 0 {
		t.Fatalf("dry run must not merge: %v", merger.calls)
	}
	if len(st.closed) != 0 {
		t.Fatalf("dry run must not close watches: %q", st.closed)
	}
	if !strings.Contains(out.String(), "gh pr merge 42 --squash --delete-branch") {
		t.Fatalf("stdout = %q, want the planned gh command", out.String())
	}
}

func TestOrchestrateMergeAndRebaseReport(t *testing.T) {
	st := newOrchFakeStore(
		orchestrator.Watch{ID: "w1", Repo: "refusedguy/beacon", PR: 42, Verdict: "ready", Revision: 3},
		orchestrator.Watch{ID: "w2", Repo: "refusedguy/beacon", PR: 7, Verdict: "retry", Revision: 1},
	)
	merger := &orchFakeMerger{}
	withOrchDeps(t, st, merger, nil)
	c, out, errOut, _ := t06NewCLI(t, t06NoChild(t))

	if code := c.Run([]string{"orchestrate", "--format", "json"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !reflect.DeepEqual(merger.calls, []int{42}) {
		t.Fatalf("merger calls = %v, want [42]", merger.calls)
	}
	if !reflect.DeepEqual(st.closed, []string{"w1"}) {
		t.Fatalf("closed = %q, want [w1]", st.closed)
	}
	var result struct {
		Merged      []int `json:"merged"`
		NeedsRebase []int `json:"needsRebase"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Merged, []int{42}) || !reflect.DeepEqual(result.NeedsRebase, []int{7}) {
		t.Fatalf("result = %+v, want merged [42] needsRebase [7]", result)
	}
}

func TestOrchestrateBadFormat(t *testing.T) {
	st := newOrchFakeStore()
	withOrchDeps(t, st, &orchFakeMerger{}, nil)
	c, out, errOut, _ := t06NewCLI(t, t06NoChild(t))

	if code := c.Run([]string{"orchestrate", "--format", "yaml"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "неизвестный формат") {
		t.Fatalf("stdout %q stderr %q", out.String(), errOut.String())
	}
}
