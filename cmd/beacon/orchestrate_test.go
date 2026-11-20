package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
)

type orchFakeStore struct {
	watches []orchestrator.Watch
	merged  map[string]bool
	closed  []string
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

func TestGhMergerRunsSquashDeleteBranch(t *testing.T) {
	c, _, _, calls := t06NewCLI(t, func(call t06Call) runResult { return runResult{} })
	m := ghMerger{c: c}

	if err := m.Merge(context.Background(), "refusedguy/beacon", 42); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v, want one", *calls)
	}
	call := (*calls)[0]
	if call.name != "gh" || !reflect.DeepEqual(call.args,
		[]string{"pr", "merge", "42", "--auto", "--squash", "--delete-branch"}) {
		t.Fatalf("gh call = %+v", call)
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
	if !strings.Contains(out.String(), "gh pr merge 42 --auto --squash --delete-branch") {
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
