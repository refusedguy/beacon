package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		ref  string
		want bool
	}{
		{"T3.100", true},
		{" T3.100 ", true},
		{"T3.101", false},
		{"T3.102", true},
		{"T3.999", false},
	} {
		got, err := r.Satisfied(context.Background(), tc.ref)
		if err != nil {
			t.Fatalf("Satisfied(%q): %v", tc.ref, err)
		}
		if got != tc.want {
			t.Fatalf("Satisfied(%q) = %t, want %t", tc.ref, got, tc.want)
		}
	}
}

func TestResolverSatisfiesMergedPR(t *testing.T) {
	runner := &fakeRunner{out: map[string][]byte{
		runnerKey("gh", "pr", "view", "77", "--json", "state"): []byte(`{"state":"MERGED"}`),
	}}
	r := NewDependencyResolver(runner)

	for _, ref := range []string{"#77", "PR#77", "pr/77"} {
		ok, err := r.Satisfied(context.Background(), ref)
		if err != nil || !ok {
			t.Fatalf("Satisfied(%q) = %t, %v, want true", ref, ok, err)
		}
	}
	if len(runner.calls) != 3 {
		t.Fatalf("gh calls = %d, want 3: %+v", len(runner.calls), runner.calls)
	}
	want := runnerCall{"gh", []string{"pr", "view", "77", "--json", "state"}}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("gh call = %+v, want %+v", runner.calls[0], want)
	}

	closed := &fakeRunner{out: map[string][]byte{
		runnerKey("gh", "pr", "view", "78", "--json", "state"): []byte(`{"state":"CLOSED"}`),
	}}
	ok, err := (&FileDependencyResolver{Runner: closed}).Satisfied(context.Background(), "#78")
	if err != nil || ok {
		t.Fatalf("closed PR satisfied = %t, %v, want false", ok, err)
	}

	failing := &fakeRunner{err: map[string]error{
		runnerKey("gh", "pr", "view", "79", "--json", "state"): errors.New("gh: auth"),
	}}
	if _, err := (&FileDependencyResolver{Runner: failing}).Satisfied(context.Background(), "#79"); err == nil {
		t.Fatal("gh failure must be a resolver error, not an unsatisfied flag")
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

func TestRunUnsatisfiedDependencyWaitsWithReasonAndWakes(t *testing.T) {
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
			wantWakes := []wakeCall{{"w1", 3, VerdictWait}}
			if !reflect.DeepEqual(waker.calls, wantWakes) {
				t.Fatalf("wakes = %+v, want %+v", waker.calls, wantWakes)
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

func TestRunWaitWakeCarriesReasonToReasonWaker(t *testing.T) {
	w := readyWatch("w1", 42, 3)
	policy := AutoMergePolicy{WaitFor: []string{"T3.100"}}
	w.Policy = &policy
	st := newFakeStore(w)
	waker := &fakeReasonWaker{}
	r := &fakeResolver{ok: map[string]bool{"T3.100": false}}

	if _, err := Run(context.Background(), st, &fakeMerger{}, waker, Options{Resolver: r}); err != nil {
		t.Fatal(err)
	}
	want := []reasonWakeCall{{"w1", 3, VerdictWait, "wait_for:T3.100"}}
	if !reflect.DeepEqual(waker.calls, want) {
		t.Fatalf("reason wakes = %+v, want %+v", waker.calls, want)
	}
}

// ---- CheckedMerger: gh gate + post-merge confirmation ----

func TestRunCheckedMergePendingKeepsWatchOpen(t *testing.T) {
	st := newFakeStore(readyWatch("w1", 312, 3))
	merger := &fakeCheckedMerger{outcomes: []MergeOutcome{{Pending: true}}}

	result, err := Run(context.Background(), st, merger, nil, Options{})

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

	result, err := Run(context.Background(), st, merger, nil, Options{})

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

	result, err := Run(context.Background(), st, merger, nil, Options{})

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

	first, err := Run(context.Background(), st, merger, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.closed) != 0 || len(st.merged) != 0 {
		t.Fatalf("first pass must only arm: closed=%q merged=%v", st.closed, st.merged)
	}
	second, err := Run(context.Background(), st, merger, nil, Options{})
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
