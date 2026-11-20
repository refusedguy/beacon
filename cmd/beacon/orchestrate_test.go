package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
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

// recordingDomainMerger records the explicit repo argument and returns a
// scripted outcome, so the adapter can be checked without a network or gh.
type recordingDomainMerger struct {
	repo   string
	pr     int
	merged bool
	held   bool
	reason string
	err    error
}

func (m *recordingDomainMerger) Merge(_ context.Context, repo string, pr int) (bool, bool, string, error) {
	m.repo, m.pr = repo, pr
	return m.merged, m.held, m.reason, m.err
}

func TestDomainMergeAdapterPassesRepoAndReportsHold(t *testing.T) {
	fake := &recordingDomainMerger{held: true, reason: "conflict"}
	err := (domainMergeAdapter{merger: fake}).Merge(context.Background(), "owner/name", 42)
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("error = %v, want a hold with the provider reason", err)
	}
	if fake.repo != "owner/name" || fake.pr != 42 {
		t.Fatalf("merge called with %s#%d, want owner/name#42", fake.repo, fake.pr)
	}
}
