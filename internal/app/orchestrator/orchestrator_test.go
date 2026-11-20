package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type fakeStore struct {
	watches      []Watch
	merged       map[string]bool
	closed       []string
	activeErr    error
	wasMergedErr error
	recordErr    error
	closeErr     error
}

func newFakeStore(watches ...Watch) *fakeStore {
	return &fakeStore{watches: watches, merged: make(map[string]bool)}
}

func mergeKey(watchID string, revision uint64) string {
	return fmt.Sprintf("%s@%d", watchID, revision)
}

func (s *fakeStore) ActiveWatches(context.Context) ([]Watch, error) {
	return s.watches, s.activeErr
}

func (s *fakeStore) WasMerged(_ context.Context, watchID string, revision uint64) (bool, error) {
	return s.merged[mergeKey(watchID, revision)], s.wasMergedErr
}

func (s *fakeStore) RecordMerge(_ context.Context, watchID string, revision uint64) error {
	s.merged[mergeKey(watchID, revision)] = true
	return s.recordErr
}

func (s *fakeStore) CloseWatch(_ context.Context, watchID string) error {
	s.closed = append(s.closed, watchID)
	return s.closeErr
}

type mergeCall struct {
	repo string
	pr   int
}

type fakeMerger struct {
	calls []mergeCall
	err   error
}

func (m *fakeMerger) Merge(_ context.Context, repo string, pr int) error {
	m.calls = append(m.calls, mergeCall{repo, pr})
	return m.err
}

type wakeCall struct {
	watchID  string
	revision uint64
	verdict  string
}

type fakeWaker struct {
	calls []wakeCall
	err   error
}

func (w *fakeWaker) Wake(_ context.Context, watchID string, revision uint64, verdict string) error {
	w.calls = append(w.calls, wakeCall{watchID, revision, verdict})
	return w.err
}

func readyWatch(id string, pr int, revision uint64) Watch {
	return Watch{ID: id, Repo: "refusedguy/beacon", PR: pr, Verdict: VerdictReady, Revision: revision}
}

func TestRunMergesReadyWatchThenClosesIt(t *testing.T) {
	st := newFakeStore(readyWatch("w1", 42, 3))
	merger := &fakeMerger{}

	result, err := Run(context.Background(), st, merger, nil, Options{})

	if err != nil {
		t.Fatal(err)
	}
	if len(merger.calls) != 1 || merger.calls[0] != (mergeCall{"refusedguy/beacon", 42}) {
		t.Fatalf("merger calls = %+v", merger.calls)
	}
	if !st.merged[mergeKey("w1", 3)] {
		t.Fatal("merge was not recorded in the ledger")
	}
	if !reflect.DeepEqual(st.closed, []string{"w1"}) {
		t.Fatalf("closed = %q, want [w1]", st.closed)
	}
	if !reflect.DeepEqual(result.Merged, []int{42}) {
		t.Fatalf("merged = %v, want [42]", result.Merged)
	}
	if len(result.NeedsRebase) != 0 {
		t.Fatalf("needs rebase = %v, want empty", result.NeedsRebase)
	}
}

func TestRunSkipsAlreadyMergedRevision(t *testing.T) {
	st := newFakeStore(readyWatch("w1", 42, 3))
	st.merged[mergeKey("w1", 3)] = true
	merger := &fakeMerger{}

	result, err := Run(context.Background(), st, merger, nil, Options{})

	if err != nil {
		t.Fatal(err)
	}
	if len(merger.calls) != 0 {
		t.Fatalf("merger must not be called: %+v", merger.calls)
	}
	if len(st.closed) != 0 {
		t.Fatalf("watch must stay open on a skip: %q", st.closed)
	}
	if !reflect.DeepEqual(result.Skipped, []int{42}) {
		t.Fatalf("skipped = %v, want [42]", result.Skipped)
	}
}

func TestRunDefaultPolicyAllowsAutoMerge(t *testing.T) {
	st := newFakeStore(readyWatch("w1", 42, 3))
	merger := &fakeMerger{}

	if _, err := Run(context.Background(), st, merger, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	if len(merger.calls) != 1 {
		t.Fatalf("a Watch with no policy must merge by default, calls = %+v", merger.calls)
	}
}

func boolPtr(v bool) *bool { return &v }

func TestRunPolicyHoldBacks(t *testing.T) {
	cases := []struct {
		name   string
		policy AutoMergePolicy
	}{
		{"auto-merge off", AutoMergePolicy{AutoMerge: boolPtr(false)}},
		{"require approval", AutoMergePolicy{RequireApproval: true}},
		{"wait for check", AutoMergePolicy{WaitFor: []string{"ci"}}},
		{"wait for main", AutoMergePolicy{WaitForMain: []string{"T3.100"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := readyWatch("w1", 42, 3)
			policy := tc.policy
			w.Policy = &policy
			st := newFakeStore(w)
			merger := &fakeMerger{}

			result, err := Run(context.Background(), st, merger, nil, Options{})

			if err != nil {
				t.Fatal(err)
			}
			if len(merger.calls) != 0 {
				t.Fatalf("hold-back must block the merge: %+v", merger.calls)
			}
			if len(result.Merged) != 0 {
				t.Fatalf("merged = %v, want empty", result.Merged)
			}
		})
	}
}

func TestRunRetryAndFailAreLeftForWake(t *testing.T) {
	st := newFakeStore(
		Watch{ID: "w1", PR: 11, Verdict: VerdictRetry, Revision: 1},
		Watch{ID: "w2", PR: 12, Verdict: VerdictFail, Revision: 2},
		Watch{ID: "w3", PR: 13, Verdict: "watching", Revision: 1},
	)
	merger := &fakeMerger{}
	waker := &fakeWaker{}

	result, err := Run(context.Background(), st, merger, waker, Options{})

	if err != nil {
		t.Fatal(err)
	}
	if len(merger.calls) != 0 {
		t.Fatalf("retry/fail must not be merged: %+v", merger.calls)
	}
	if !reflect.DeepEqual(result.NeedsRebase, []int{11, 12}) {
		t.Fatalf("needs rebase = %v, want [11 12]", result.NeedsRebase)
	}
	wantWakes := []wakeCall{{"w1", 1, VerdictRetry}, {"w2", 2, VerdictFail}}
	if !reflect.DeepEqual(waker.calls, wantWakes) {
		t.Fatalf("wakes = %+v, want %+v", waker.calls, wantWakes)
	}
	if len(st.closed) != 0 {
		t.Fatalf("retry/fail watches must stay open: %q", st.closed)
	}
}

func TestRunDryRunDoesNotMergeRecordOrClose(t *testing.T) {
	st := newFakeStore(readyWatch("w1", 42, 3))
	merger := &fakeMerger{}

	result, err := Run(context.Background(), st, merger, nil, Options{DryRun: true})

	if err != nil {
		t.Fatal(err)
	}
	if len(merger.calls) != 0 {
		t.Fatalf("dry run must not call gh: %+v", merger.calls)
	}
	if len(st.merged) != 0 || len(st.closed) != 0 {
		t.Fatalf("dry run must not write state: merged=%v closed=%q", st.merged, st.closed)
	}
	if !result.DryRun || len(result.Actions) != 1 || result.Actions[0].Command != "gh pr merge 42 --squash --delete-branch" {
		t.Fatalf("dry run actions = %+v", result.Actions)
	}
}

func TestRunMergeFailureDoesNotCloseWatch(t *testing.T) {
	st := newFakeStore(readyWatch("w1", 42, 3))
	merger := &fakeMerger{err: errors.New("merge conflict")}

	result, err := Run(context.Background(), st, merger, nil, Options{})

	if err == nil {
		t.Fatal("expected merge failure to be reported")
	}
	if len(st.merged) != 0 || len(st.closed) != 0 {
		t.Fatalf("failed merge must not record or close: merged=%v closed=%q", st.merged, st.closed)
	}
	if len(result.Merged) != 0 {
		t.Fatalf("merged = %v, want empty", result.Merged)
	}
}

func TestPolicyAllows(t *testing.T) {
	if !DefaultAutoMergePolicy().Allows() {
		t.Fatal("default policy must allow auto-merge")
	}
	if !(AutoMergePolicy{}).Allows() {
		t.Fatal("a zero policy with no hold-backs must allow auto-merge")
	}
	if (AutoMergePolicy{RequireApproval: true}).Allows() {
		t.Fatal("requireApproval must block auto-merge")
	}
}
