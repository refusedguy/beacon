package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

type fakeStore struct {
	watches      []Watch
	merged       map[string]bool
	closed       []string
	fixAttempts  []string
	holds        []holdCall
	activeErr    error
	wasMergedErr error
	recordErr    error
	closeErr     error
	fixErr       error
}

type holdCall struct {
	watchID string
	verdict string
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

func (s *fakeStore) RecordFixAttempt(_ context.Context, watchID string) error {
	s.fixAttempts = append(s.fixAttempts, watchID)
	return s.fixErr
}

func (s *fakeStore) HoldWatch(_ context.Context, watchID, verdict string) error {
	s.holds = append(s.holds, holdCall{watchID, verdict})
	return nil
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

// fakeCheckedMerger models the gh gate and confirmation behind one call. Each
// call consumes the next outcome; mergeCalls counts only the inner `gh pr merge`.
type fakeCheckedMerger struct {
	outcomes   []MergeOutcome
	err        error
	checks     int
	mergeCalls int
}

func (m *fakeCheckedMerger) Merge(_ context.Context, _ string, _ int) error {
	m.mergeCalls++
	return nil
}

func (m *fakeCheckedMerger) MergeChecked(ctx context.Context, repo string, pr int) (MergeOutcome, error) {
	m.checks++
	if m.err != nil {
		return MergeOutcome{}, m.err
	}
	if m.checks <= len(m.outcomes) {
		return m.outcomes[m.checks-1], nil
	}
	return MergeOutcome{}, nil
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

type reasonWakeCall struct {
	watchID  string
	revision uint64
	verdict  string
	reason   string
}

type fakeReasonWaker struct {
	calls []reasonWakeCall
}

func (w *fakeReasonWaker) Wake(_ context.Context, watchID string, revision uint64, verdict string) error {
	w.calls = append(w.calls, reasonWakeCall{watchID, revision, verdict, ""})
	return nil
}

func (w *fakeReasonWaker) WakeWithReason(_ context.Context, watchID string, revision uint64, verdict, reason string) error {
	w.calls = append(w.calls, reasonWakeCall{watchID, revision, verdict, reason})
	return nil
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
	wantRebase := []RebaseRef{
		{WatchID: "w1", Revision: 1, PR: 11},
		{WatchID: "w2", Revision: 2, PR: 12},
	}
	if !reflect.DeepEqual(result.NeedsRebase, wantRebase) {
		t.Fatalf("needs rebase = %+v, want %+v", result.NeedsRebase, wantRebase)
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

// ---- DependencyResolver: граф waitFor/waitForMain разрешает мерж ----

type fakeResolver struct {
	ok    map[string]bool
	err   map[string]error
	calls []string
}

func (r *fakeResolver) Satisfied(_ context.Context, ref string) (bool, error) {
	r.calls = append(r.calls, ref)
	if err := r.err[ref]; err != nil {
		return false, err
	}
	return r.ok[ref], nil
}

// noisyResolver возвращает true вместе с ошибкой: ошибка резолвера не должна
// давать ложное разрешение мержа.
type noisyResolver struct {
	value bool
	err   error
	calls []string
}

func (r *noisyResolver) Satisfied(_ context.Context, ref string) (bool, error) {
	r.calls = append(r.calls, ref)
	return r.value, r.err
}

type runnerCall struct {
	name string
	args []string
}

type fakeRunner struct {
	calls []runnerCall
	out   map[string][]byte
	err   map[string]error
}

func runnerKey(name string, args ...string) string {
	return name + " " + strings.Join(args, " ")
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, runnerCall{name, append([]string(nil), args...)})
	key := runnerKey(name, args...)
	return r.out[key], r.err[key]
}

func writeCard(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolverSatisfiesDoneCardOnly(t *testing.T) {
	dir := t.TempDir()
	writeCard(t, dir, "T3.100.md", "# T3.100\nСтатус: done\n")
	writeCard(t, dir, "T3.101.md", "# T3.101\nСтатус: todo\n")
	writeCard(t, dir, "T3.102.md", "---\nstatus: done\n---\n")
	r := &FileDependencyResolver{DocsDir: dir, Runner: &fakeRunner{}}

	for _, tc := range []struct {
		ref     string
		want    bool
		wantErr bool
	}{
		{"T3.100", true, false},
		{" T3.100 ", true, false},
		{"T3.101", false, false},
		{"T3.102", true, false},
		{"T3.999", false, true},
	} {
		got, err := r.Satisfied(context.Background(), tc.ref)
		if tc.wantErr {
			var missing *DependencyCardNotFoundError
			if !errors.As(err, &missing) {
				t.Fatalf("Satisfied(%q) error = %v, want *DependencyCardNotFoundError", tc.ref, err)
			}
			if want := strings.TrimSpace(tc.ref); missing.Ref != want {
				t.Fatalf("missing ref = %q, want %q", missing.Ref, want)
			}
		} else if err != nil {
			t.Fatalf("Satisfied(%q): %v", tc.ref, err)
		}
		if got != tc.want {
			t.Fatalf("Satisfied(%q) = %t, want %t", tc.ref, got, tc.want)
		}
	}
}

func TestResolverSatisfiesMergedPRWithoutGh(t *testing.T) {
	runner := &fakeRunner{}
	r := NewStoreDependencyResolver("docs/tasks", runner, MergedPRReaderFunc(
		func(_ context.Context, pr int) (bool, error) { return pr == 77, nil }))

	for _, ref := range []string{"#77", "PR#77", "pr/77"} {
		ok, err := r.Satisfied(context.Background(), ref)
		if err != nil || !ok {
			t.Fatalf("Satisfied(%q) = %t, %v, want true", ref, ok, err)
		}
	}
	ok, err := r.Satisfied(context.Background(), "#78")
	if err != nil || ok {
		t.Fatalf("open PR satisfied = %t, %v, want false", ok, err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("PR refs must not start a process: %+v", runner.calls)
	}

	failing := &FileDependencyResolver{MergedPR: MergedPRReaderFunc(
		func(context.Context, int) (bool, error) { return false, errors.New("store unavailable") })}
	if _, err := failing.Satisfied(context.Background(), "#79"); err == nil {
		t.Fatal("reader failure must be a resolver error, not an unsatisfied flag")
	}
	noReader := &FileDependencyResolver{Runner: runner}
	if _, err := noReader.Satisfied(context.Background(), "#77"); err == nil {
		t.Fatal("a PR ref without a merged-PR reader must fail closed")
	}
}

func TestResolverUsesGitShowForArbitraryRef(t *testing.T) {
	const present = "docs/tasks/T3.100.md"
	const absent = "docs/tasks/nope.md"
	const broken = "docs/tasks/broken.md"
	runner := &fakeRunner{
		out: map[string][]byte{runnerKey("git", "show", "main:"+present): []byte("card")},
		err: map[string]error{
			runnerKey("git", "show", "main:"+absent): ErrRefMissing,
			runnerKey("git", "show", "main:"+broken): errors.New("git: broken object"),
		},
	}
	r := NewDependencyResolver(runner)

	ok, err := r.Satisfied(context.Background(), present)
	if err != nil || !ok {
		t.Fatalf("present file = %t, %v, want true", ok, err)
	}
	ok, err = r.Satisfied(context.Background(), "./"+present)
	if err != nil || !ok {
		t.Fatalf("normalized file = %t, %v, want true", ok, err)
	}
	wantCall := runnerCall{"git", []string{"show", "main:" + present}}
	if !reflect.DeepEqual(runner.calls[0], wantCall) {
		t.Fatalf("git call = %+v, want %+v", runner.calls[0], wantCall)
	}
	ok, err = r.Satisfied(context.Background(), absent)
	if err != nil || ok {
		t.Fatalf("missing file = %t, %v, want false without error", ok, err)
	}
	if _, err := r.Satisfied(context.Background(), broken); err == nil {
		t.Fatal("git failure must be a resolver error")
	}
}

func TestCanAutoMergeResolvesEveryDependency(t *testing.T) {
	r := &fakeResolver{ok: map[string]bool{"T3.100": true, "#77": true}}
	w := readyWatch("w1", 42, 3)
	policy := AutoMergePolicy{WaitFor: []string{"T3.100"}, WaitForMain: []string{"#77"}}
	w.Policy = &policy

	verdict, reason, err := w.canAutoMerge(context.Background(), r)
	if err != nil || verdict != VerdictReady || reason != "" {
		t.Fatalf("canAutoMerge = %q, %q, %v, want ready", verdict, reason, err)
	}
	if !reflect.DeepEqual(r.calls, []string{"T3.100", "#77"}) {
		t.Fatalf("resolved refs = %v", r.calls)
	}
}

func TestRunMergesWhenAllDependenciesAreSatisfied(t *testing.T) {
	w := readyWatch("w1", 42, 3)
	policy := AutoMergePolicy{WaitFor: []string{"T3.100"}, WaitForMain: []string{"#77"}}
	w.Policy = &policy
	st := newFakeStore(w)
	merger := &fakeMerger{}
	r := &fakeResolver{ok: map[string]bool{"T3.100": true, "#77": true}}

	result, err := Run(context.Background(), st, merger, nil, Options{Resolver: r})

	if err != nil {
		t.Fatal(err)
	}
	if len(merger.calls) != 1 || merger.calls[0] != (mergeCall{"refusedguy/beacon", 42}) {
		t.Fatalf("merger calls = %+v", merger.calls)
	}
	if !reflect.DeepEqual(result.Merged, []int{42}) {
		t.Fatalf("merged = %v, want [42]", result.Merged)
	}
	if len(result.Waiting) != 0 {
		t.Fatalf("waiting = %v, want empty", result.Waiting)
	}
}

func TestRunUnsatisfiedDependencyHoldsWithoutWakingAnyone(t *testing.T) {
	cases := []struct {
		name   string
		policy AutoMergePolicy
		ok     map[string]bool
		reason string
	}{
		{
			name:   "waitFor",
			policy: AutoMergePolicy{WaitFor: []string{"T3.100", "T3.101"}},
			ok:     map[string]bool{"T3.100": true, "T3.101": false},
			reason: "wait_for:T3.101",
		},
		{
			name:   "waitForMain",
			policy: AutoMergePolicy{WaitForMain: []string{"#77"}},
			ok:     map[string]bool{"#77": false},
			reason: "wait_for_main:#77",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := readyWatch("w1", 42, 3)
			policy := tc.policy
			w.Policy = &policy
			st := newFakeStore(w)
			merger := &fakeMerger{}
			waker := &fakeWaker{}
			r := &fakeResolver{ok: tc.ok}

			result, err := RunWithResolver(context.Background(), st, merger, waker, r, Options{})

			if err != nil {
				t.Fatal(err)
			}
			if len(merger.calls) != 0 || len(result.Merged) != 0 {
				t.Fatalf("unsatisfied dependency must block the merge: calls=%+v merged=%v", merger.calls, result.Merged)
			}
			if !reflect.DeepEqual(result.Waiting, []int{42}) {
				t.Fatalf("waiting = %v, want [42]", result.Waiting)
			}
			foundWait := false
			for _, a := range result.Actions {
				if a.Action == ActionWait && a.Message == tc.reason {
					foundWait = true
				}
			}
			if !foundWait {
				t.Fatalf("wait action with reason %q missing: %+v", tc.reason, result.Actions)
			}
			if len(waker.calls) != 0 {
				t.Fatalf("an unsatisfied dependency wakes nobody: %+v", waker.calls)
			}
			wantHolds := []holdCall{{"w1", WaitDependencyVerdict(tc.reason)}}
			if !reflect.DeepEqual(st.holds, wantHolds) {
				t.Fatalf("holds = %+v, want %+v", st.holds, wantHolds)
			}
		})
	}
}

func TestRunResolverErrorNeverAllowsMerge(t *testing.T) {
	w := readyWatch("w1", 42, 3)
	policy := AutoMergePolicy{WaitFor: []string{"T3.100"}}
	w.Policy = &policy
	st := newFakeStore(w)
	merger := &fakeMerger{}
	waker := &fakeWaker{}
	r := &noisyResolver{value: true, err: errors.New("card store unavailable")}

	result, err := Run(context.Background(), st, merger, waker, Options{Resolver: r})

	if err == nil {
		t.Fatal("resolver error must be reported")
	}
	if len(merger.calls) != 0 || len(result.Merged) != 0 {
		t.Fatalf("resolver error must block the merge: %+v", merger.calls)
	}
	if len(waker.calls) != 0 {
		t.Fatalf("a resolver failure is not a subscriber wake: %+v", waker.calls)
	}
	if !reflect.DeepEqual(r.calls, []string{"T3.100"}) {
		t.Fatalf("resolved refs = %v", r.calls)
	}
}

func TestRunWithoutResolverWaitsForDependencies(t *testing.T) {
	w := readyWatch("w1", 42, 3)
	policy := AutoMergePolicy{WaitFor: []string{"T3.100"}}
	w.Policy = &policy
	st := newFakeStore(w)
	merger := &fakeMerger{}

	result, err := Run(context.Background(), st, merger, nil, Options{})

	if err != nil {
		t.Fatal(err)
	}
	if len(merger.calls) != 0 {
		t.Fatalf("no resolver must not allow a merge: %+v", merger.calls)
	}
	if !reflect.DeepEqual(result.Waiting, []int{42}) {
		t.Fatalf("waiting = %v, want [42]", result.Waiting)
	}
	found := false
	for _, a := range result.Actions {
		if a.Action == ActionWait && a.Message == "resolver_unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("wait action with resolver_unavailable missing: %+v", result.Actions)
	}
}

func TestRunWaitNeverWakesAReasonWaker(t *testing.T) {
	w := readyWatch("w1", 42, 3)
	policy := AutoMergePolicy{WaitFor: []string{"T3.100"}}
	w.Policy = &policy
	st := newFakeStore(w)
	waker := &fakeReasonWaker{}
	r := &fakeResolver{ok: map[string]bool{"T3.100": false}}

	if _, err := Run(context.Background(), st, &fakeMerger{}, waker, Options{Resolver: r, ReasonWaker: waker}); err != nil {
		t.Fatal(err)
	}
	if len(waker.calls) != 0 {
		t.Fatalf("a dependency hold must not wake even a ReasonWaker: %+v", waker.calls)
	}
}

// TestWakeOneUsesExplicitReasonWaker proves the reason reaches the subscriber
// only when the caller passes the alternate port explicitly. A plain Waker must
// not be upgraded by a hidden type assertion.
func TestWakeOneUsesExplicitReasonWaker(t *testing.T) {
	waker := &fakeWaker{}
	reasonWaker := &fakeReasonWaker{}
	w := Watch{ID: "w1", PR: 7, Revision: 3, Verdict: VerdictRetry}
	out := &Result{}
	errs := &observe.ErrorCollector{}

	wakeOne(context.Background(), waker, reasonWaker, w, VerdictRetry, "ci red", out, errs)

	if err := errs.Error(); err != nil {
		t.Fatalf("unexpected errors: %v", err)
	}
	if len(waker.calls) != 0 {
		t.Fatalf("plain waker must stay unused: %+v", waker.calls)
	}
	if len(reasonWaker.calls) != 1 || reasonWaker.calls[0] != (reasonWakeCall{"w1", 3, VerdictRetry, "ci red"}) {
		t.Fatalf("reason wake = %+v, want one call with the reason", reasonWaker.calls)
	}
}

// TestRunHeldWatchMergesOnNextTick proves the persisted wait_dependency verdict
// re-enters the same gate: once the ref is satisfied the Watch merges without
// a fresh ready verdict from CI.
func TestRunHeldWatchMergesOnNextTick(t *testing.T) {
	w := readyWatch("w1", 42, 3)
	w.Verdict = WaitDependencyVerdict("wait_for:#77")
	policy := AutoMergePolicy{WaitFor: []string{"#77"}}
	w.Policy = &policy
	st := newFakeStore(w)
	merger := &fakeMerger{}

	result, err := Run(context.Background(), st, merger, nil, Options{
		Resolver: &fakeResolver{ok: map[string]bool{"#77": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Merged, []int{42}) {
		t.Fatalf("merged = %v, want [42]", result.Merged)
	}
	if len(merger.calls) != 1 {
		t.Fatalf("merge calls = %+v, want one", merger.calls)
	}
	if !isWaitDependency(w.Verdict) {
		t.Fatal("hold verdict must be recognized as a dependency wait")
	}
}

// ---- CheckedMerger: gh gate + post-merge confirmation ----

func TestRunCheckedMergePendingKeepsWatchOpen(t *testing.T) {
	st := newFakeStore(readyWatch("w1", 312, 3))
	merger := &fakeCheckedMerger{outcomes: []MergeOutcome{{Pending: true}}}

	result, err := Run(context.Background(), st, merger, nil, Options{CheckedMerger: merger})

	if err != nil {
		t.Fatal(err)
	}
	if merger.mergeCalls != 0 {
		t.Fatalf("pending merge must not issue gh pr merge: %d", merger.mergeCalls)
	}
	if len(st.closed) != 0 || len(st.merged) != 0 {
		t.Fatalf("pending merge must not close or record: closed=%q merged=%v", st.closed, st.merged)
	}
	if len(result.Merged) != 0 {
		t.Fatalf("merged = %v, want empty until state=MERGED", result.Merged)
	}
	found := false
	for _, a := range result.Actions {
		if a.Action == ActionMerge && a.Message == "waiting for MERGE state" {
			found = true
		}
	}
	if !found {
		t.Fatalf("waiting action missing: %+v", result.Actions)
	}
}

func TestRunCheckedMergeConfirmedClosesWatch(t *testing.T) {
	st := newFakeStore(readyWatch("w1", 312, 3))
	merger := &fakeCheckedMerger{outcomes: []MergeOutcome{{Merged: true}}}

	result, err := Run(context.Background(), st, merger, nil, Options{CheckedMerger: merger})

	if err != nil {
		t.Fatal(err)
	}
	if merger.checks != 1 {
		t.Fatalf("merge checks = %d, want 1", merger.checks)
	}
	if !st.merged[mergeKey("w1", 3)] {
		t.Fatal("confirmed MERGE must be recorded in the ledger")
	}
	if !reflect.DeepEqual(st.closed, []string{"w1"}) {
		t.Fatalf("closed = %q, want [w1]", st.closed)
	}
	if !reflect.DeepEqual(result.Merged, []int{312}) {
		t.Fatalf("merged = %v, want [312]", result.Merged)
	}
}

func TestRunCheckedMergeHeldByGateDoesNotMerge(t *testing.T) {
	const reason = "mergeable=MERGEABLE mergeStateStatus=DIRTY"
	st := newFakeStore(readyWatch("w1", 312, 3))
	merger := &fakeCheckedMerger{outcomes: []MergeOutcome{{Reason: reason}}}

	result, err := Run(context.Background(), st, merger, nil, Options{CheckedMerger: merger})

	if err != nil {
		t.Fatalf("a held PR is a diagnostic, not an error: %v", err)
	}
	if merger.mergeCalls != 0 {
		t.Fatalf("gate must block gh pr merge: %d", merger.mergeCalls)
	}
	if len(st.closed) != 0 || len(st.merged) != 0 {
		t.Fatalf("held PR must not close or record: closed=%q merged=%v", st.closed, st.merged)
	}
	if !reflect.DeepEqual(result.Skipped, []int{312}) {
		t.Fatalf("skipped = %v, want [312]", result.Skipped)
	}
	found := false
	for _, a := range result.Actions {
		if a.Action == ActionSkip && a.Message == reason {
			found = true
		}
	}
	if !found {
		t.Fatalf("gate action with reason %q missing: %+v", reason, result.Actions)
	}
}

func TestRunCheckedMergeRepeatedPassDoesNotRemerge(t *testing.T) {
	st := newFakeStore(readyWatch("w1", 312, 3))
	merger := &fakeCheckedMerger{outcomes: []MergeOutcome{{Pending: true}, {Merged: true}}}

	first, err := Run(context.Background(), st, merger, nil, Options{CheckedMerger: merger})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.closed) != 0 || len(st.merged) != 0 {
		t.Fatalf("first pass must only arm: closed=%q merged=%v", st.closed, st.merged)
	}
	second, err := Run(context.Background(), st, merger, nil, Options{CheckedMerger: merger})
	if err != nil {
		t.Fatal(err)
	}
	if merger.mergeCalls != 0 {
		t.Fatalf("no gh pr merge on either pass: %d", merger.mergeCalls)
	}
	if !reflect.DeepEqual(st.closed, []string{"w1"}) || !st.merged[mergeKey("w1", 3)] {
		t.Fatalf("second pass must confirm and close: closed=%q merged=%v", st.closed, st.merged)
	}
	if !reflect.DeepEqual(first.Merged, []int{}) || !reflect.DeepEqual(second.Merged, []int{312}) {
		t.Fatalf("merged = %v then %v", first.Merged, second.Merged)
	}
}

func blockedWatch(id string, pr int, revision uint64, attempts int) Watch {
	return Watch{
		ID: id, Repo: "refusedguy/beacon", PR: pr, Revision: revision,
		// A delayed CI poll can leave a stale ready verdict on a blocked Watch.
		Verdict: VerdictReady,
		Blocked: &BlockedInfo{
			Reason:       "чужой тест",
			FailedChecks: []string{"ci-build"},
			BlockedBy:    "executor",
			FixAttempts:  attempts,
		},
	}
}

func installOrchMetrics(t *testing.T) *observe.Metrics {
	t.Helper()
	metrics := observe.NewMetrics()
	observe.SetDefault(observe.NewRecorder(nil, metrics))
	t.Cleanup(func() { observe.SetDefault(nil) })
	return metrics
}

func assertOrchCounter(t *testing.T, metrics *observe.Metrics, name string, want int) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(metrics.JSON(), &got); err != nil {
		t.Fatalf("metrics JSON: %v", err)
	}
	v, ok := got[name].(float64)
	if !ok || int(v) != want {
		t.Errorf("%s = %v, want %d", name, got[name], want)
	}
}

// TestWakeOneNamesOnlyRebaseAsRebase checks the log and metric an operator
// reads after a wake. A retry wake must say retry in wake_requested and must
// not raise rebase_requests_total. Only a rebase verdict raises that counter.
func TestWakeOneNamesOnlyRebaseAsRebase(t *testing.T) {
	for _, tc := range []struct {
		verdict    string
		wantRebase int
	}{
		{VerdictRetry, 0},
		{VerdictRebase, 1},
	} {
		t.Run(tc.verdict, func(t *testing.T) {
			var buf bytes.Buffer
			metrics := observe.NewMetrics()
			observe.SetDefault(observe.NewRecorder(observe.NewLogger("json", observe.LevelInfo, &buf), metrics))
			t.Cleanup(func() { observe.SetDefault(nil) })

			waker := &fakeWaker{}
			out := &Result{}
			errs := &observe.ErrorCollector{}
			w := Watch{ID: "w1", PR: 7, Revision: 3, Verdict: tc.verdict}
			wakeOne(context.Background(), waker, nil, w, tc.verdict, "reason", out, errs)

			assertOrchCounter(t, metrics, observe.CounterRebaseRequests, tc.wantRebase)
			if len(waker.calls) != 1 {
				t.Fatalf("waker calls = %d, want 1", len(waker.calls))
			}
			if !strings.Contains(buf.String(), `"event":"wake_requested"`) ||
				!strings.Contains(buf.String(), `"verdict":"`+tc.verdict+`"`) {
				t.Errorf("no wake_requested for %s: %s", tc.verdict, buf.String())
			}
		})
	}
}

func countActions(actions []Action, kind string) int {
	n := 0
	for _, a := range actions {
		if a.Action == kind {
			n++
		}
	}
	return n
}

// TestRunBlockedWatchSpawnsFixerOnce proves the blocked branch wins over a
// stale ready verdict, emits one fixer decision per pass and records one FIX
// attempt on the block record. The next pass then sees a larger budget. The
// Watch is never merged or closed on this path.
func TestRunBlockedWatchSpawnsFixerOnce(t *testing.T) {
	metrics := installOrchMetrics(t)
	st := newFakeStore(blockedWatch("w1", 42, 3, 0))
	merger := &fakeMerger{}

	result, err := Run(context.Background(), st, merger, nil, Options{})

	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.fixAttempts, []string{"w1"}) {
		t.Fatalf("fix attempts = %q, want [w1]", st.fixAttempts)
	}
	assertOrchCounter(t, metrics, observe.CounterFixersSpawned, 1)
	assertOrchCounter(t, metrics, observe.CounterBlocksEscalated, 0)
	if len(merger.calls) != 0 || len(st.closed) != 0 || len(result.Merged) != 0 {
		t.Fatalf("blocked Watch merged: calls=%v closed=%q merged=%v", merger.calls, st.closed, result.Merged)
	}
	if n := countActions(result.Actions, ActionBlockedFixAgent); n != 1 {
		t.Fatalf("blocked-fix-agent actions = %d, want 1: %+v", n, result.Actions)
	}
	for _, a := range result.Actions {
		if a.Action == ActionBlockedFixAgent && a.Message != "чужой тест" {
			t.Fatalf("fix agent message = %q, want block reason", a.Message)
		}
	}
}

// TestRunBlockedWatchEscalatesAtCap proves the third exhausted decision sends
// the escalation metric and spends no fix-attempt budget.
func TestRunBlockedWatchEscalatesAtCap(t *testing.T) {
	metrics := installOrchMetrics(t)
	st := newFakeStore(blockedWatch("w1", 42, 3, MaxFixAttempts))
	merger := &fakeMerger{}

	result, err := Run(context.Background(), st, merger, nil, Options{})

	if err != nil {
		t.Fatal(err)
	}
	assertOrchCounter(t, metrics, observe.CounterBlocksEscalated, 1)
	assertOrchCounter(t, metrics, observe.CounterFixersSpawned, 0)
	if len(st.fixAttempts) != 0 {
		t.Fatalf("escalation must not record a fix attempt: %q", st.fixAttempts)
	}
	if n := countActions(result.Actions, ActionBlockEscalated); n != 1 {
		t.Fatalf("block-escalated actions = %d, want 1: %+v", n, result.Actions)
	}
	for _, a := range result.Actions {
		if a.Action == ActionBlockEscalated && a.Message != ReasonEscalateHuman {
			t.Fatalf("escalation message = %q, want %q", a.Message, ReasonEscalateHuman)
		}
	}
}

// TestRunBlockedWatchReportsOncePerPass proves one decision and one recorded
// attempt per pass: two passes over the same block each report one fixer
// decision, emit one metric and charge one attempt.
func TestRunBlockedWatchReportsOncePerPass(t *testing.T) {
	metrics := installOrchMetrics(t)
	st := newFakeStore(blockedWatch("w1", 42, 3, 0))
	merger := &fakeMerger{}

	first, err := Run(context.Background(), st, merger, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if n := countActions(first.Actions, ActionBlockedFixAgent); n != 1 {
		t.Fatalf("first pass fix-agent actions = %d, want 1", n)
	}
	if !reflect.DeepEqual(st.fixAttempts, []string{"w1"}) {
		t.Fatalf("first pass fix attempts = %q, want [w1]", st.fixAttempts)
	}

	second, err := Run(context.Background(), st, merger, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if n := countActions(second.Actions, ActionBlockedFixAgent); n != 1 {
		t.Fatalf("second pass fix-agent actions = %d, want 1", n)
	}
	if !reflect.DeepEqual(st.fixAttempts, []string{"w1", "w1"}) {
		t.Fatalf("second pass fix attempts = %q, want [w1 w1]", st.fixAttempts)
	}
	assertOrchCounter(t, metrics, observe.CounterFixersSpawned, 2)
	assertOrchCounter(t, metrics, observe.CounterBlocksEscalated, 0)
}

// TestRunBlockedFixAttemptWriteFailureIsReported proves a failed persist is
// reported: the write is attempted once, the error reaches the caller and the
// fixer decision is still emitted.
func TestRunBlockedFixAttemptWriteFailureIsReported(t *testing.T) {
	metrics := installOrchMetrics(t)
	st := newFakeStore(blockedWatch("w1", 42, 3, 0))
	st.fixErr = errors.New("store down")

	result, err := Run(context.Background(), st, nil, nil, Options{})

	if err == nil || !strings.Contains(err.Error(), "record fix attempt w1") {
		t.Fatalf("error = %v, want the fix-attempt write error", err)
	}
	if !reflect.DeepEqual(st.fixAttempts, []string{"w1"}) {
		t.Fatalf("fix attempts = %q, want one attempted write", st.fixAttempts)
	}
	if n := countActions(result.Actions, ActionBlockedFixAgent); n != 1 {
		t.Fatalf("fix-agent actions = %d, want 1", n)
	}
	assertOrchCounter(t, metrics, observe.CounterFixersSpawned, 1)
}

// TestRunBlockedDryRunHasNoSideEffects keeps the merge dry-run contract: the
// action is reported, but no attempt is persisted and no fixer is announced.
func TestRunBlockedDryRunHasNoSideEffects(t *testing.T) {
	metrics := installOrchMetrics(t)
	st := newFakeStore(blockedWatch("w1", 42, 3, 0))

	result, err := Run(context.Background(), st, nil, nil, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.fixAttempts) != 0 {
		t.Fatalf("dry run persisted fix attempts: %q", st.fixAttempts)
	}
	assertOrchCounter(t, metrics, observe.CounterFixersSpawned, 0)
	if n := countActions(result.Actions, ActionBlockedFixAgent); n != 1 {
		t.Fatalf("dry run fix-agent actions = %d, want 1", n)
	}
}
