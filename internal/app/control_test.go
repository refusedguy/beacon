package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// busyStore injects SQLite write-lock conflicts into the fake store's saves.
type busyStore struct {
	*fakeStore
	mu       sync.Mutex
	failures map[core.RunID]int
	saves    map[core.RunID]int
}

func newBusyStore() *busyStore {
	return &busyStore{
		fakeStore: newFakeStore(),
		failures:  map[core.RunID]int{},
		saves:     map[core.RunID]int{},
	}
}

func (s *busyStore) Get(ctx context.Context, id core.RunID) (*core.Run, error) {
	run, err := s.fakeStore.Get(ctx, id)
	if err != nil || run == nil {
		return run, err
	}
	copy := *run
	return &copy, nil
}

func (s *busyStore) Save(ctx context.Context, r *core.Run) error {
	s.mu.Lock()
	s.saves[r.ID]++
	failures := s.failures[r.ID]
	if failures > 0 {
		s.failures[r.ID]--
	}
	s.mu.Unlock()
	if failures > 0 {
		return errors.New("database is locked (5) (SQLITE_BUSY)")
	}
	return s.fakeStore.Save(ctx, r)
}

func TestCancelRetriesTransientBusy(t *testing.T) {
	ctx := context.Background()
	store := newBusyStore()
	if err := store.fakeStore.Save(ctx, core.NewRun("r-busy", "T-1", time.Now())); err != nil {
		t.Fatalf(`store.fakeStore.Save(ctx, core.NewRun("r-busy", "T-1", time.Now())): %v`, err)
	}
	store.failures["r-busy"] = 1

	c := &Control{Store: store}
	if err := c.Cancel(ctx, "r-busy"); err != nil {
		t.Fatalf(`c.Cancel(ctx, "r-busy"): %v`, err)
	}
	if got := store.runs["r-busy"]; got.State != core.RunClosed {
		t.Fatalf("state = %s, want %s", got.State, core.RunClosed)
	}
	if store.saves["r-busy"] != 2 {
		t.Fatalf("saves = %d, want 2", store.saves["r-busy"])
	}
}

func TestCancelReturnsPersistentBusyAfterBoundedRetries(t *testing.T) {
	ctx := context.Background()
	store := newBusyStore()
	if err := store.fakeStore.Save(ctx, core.NewRun("r-busy", "T-1", time.Now())); err != nil {
		t.Fatalf(`store.fakeStore.Save(ctx, core.NewRun("r-busy", "T-1", time.Now())): %v`, err)
	}
	store.failures["r-busy"] = 100

	c := &Control{Store: store}
	err := c.Cancel(ctx, "r-busy")
	if err == nil || !strings.Contains(err.Error(), "SQLITE_BUSY") {
		t.Fatalf("error = %v, want SQLITE_BUSY", err)
	}
	if store.saves["r-busy"] != controlWriteAttempts {
		t.Fatalf("saves = %d, want %d", store.saves["r-busy"], controlWriteAttempts)
	}
	if got := store.runs["r-busy"]; got.State != core.RunPending {
		t.Fatalf("state = %s, want %s", got.State, core.RunPending)
	}
}

// codedSQLiteError stands in for a driver error that carries a result code
// instead of relying on its message.
type codedSQLiteError struct {
	code int
	msg  string
}

func (e codedSQLiteError) Error() string { return e.msg }
func (e codedSQLiteError) Code() int     { return e.code }

func TestIsBusyUsesResultCodeNotMessage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"coded busy with changed message", codedSQLiteError{code: sqliteBusyCode, msg: "reworded lock conflict"}, true},
		{"wrapped coded busy", fmt.Errorf("save: %w", codedSQLiteError{code: sqliteBusyCode, msg: "reworded lock conflict"}), true},
		{"coded non-busy wins over message", codedSQLiteError{code: 6, msg: "(SQLITE_BUSY) in text"}, false},
		{"untyped canonical marker", errors.New("database is locked (5) (SQLITE_BUSY)"), true},
		{"untyped unrelated", errors.New("connection refused"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBusy(tc.err); got != tc.want {
				t.Fatalf("isBusy(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// typedBusyStore fails its first save with a coded busy error whose message
// contains no SQLite marker. Detection must come from the result code.
type typedBusyStore struct {
	*fakeStore
	fail bool
}

func (s *typedBusyStore) Get(ctx context.Context, id core.RunID) (*core.Run, error) {
	run, err := s.fakeStore.Get(ctx, id)
	if err != nil || run == nil {
		return run, err
	}
	copy := *run
	return &copy, nil
}

func (s *typedBusyStore) Save(ctx context.Context, r *core.Run) error {
	if s.fail {
		s.fail = false
		return codedSQLiteError{code: sqliteBusyCode, msg: "reworded lock conflict"}
	}
	return s.fakeStore.Save(ctx, r)
}

func TestCancelRetriesTypedBusyWithChangedMessage(t *testing.T) {
	ctx := context.Background()
	store := &typedBusyStore{fakeStore: newFakeStore(), fail: true}
	if err := store.fakeStore.Save(ctx, core.NewRun("r-typed-busy", "T-1", time.Now())); err != nil {
		t.Fatalf(`store.fakeStore.Save(ctx, core.NewRun("r-typed-busy", "T-1", time.Now())): %v`, err)
	}
	c := &Control{Store: store}
	if err := c.Cancel(ctx, "r-typed-busy"); err != nil {
		t.Fatalf(`c.Cancel(ctx, "r-typed-busy"): %v`, err)
	}
	if got := store.runs["r-typed-busy"]; got.State != core.RunClosed {
		t.Fatalf("state = %s, want %s", got.State, core.RunClosed)
	}
}

func TestParallelCancelsAllSucceed(t *testing.T) {
	ctx := context.Background()
	store := newBusyStore()
	const n = 8
	for i := 0; i < n; i++ {
		id := core.RunID(fmt.Sprintf("r-%d", i))
		if err := store.fakeStore.Save(ctx, core.NewRun(id, "T-1", time.Now())); err != nil {
			t.Fatalf(`store.fakeStore.Save(ctx, core.NewRun(id, "T-1", time.Now())): %v`, err)
		}
	}
	c := &Control{Store: store}

	type cancelResult struct {
		id  core.RunID
		err error
	}
	errs := make(chan cancelResult, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := core.RunID(fmt.Sprintf("r-%d", i))
			errs <- cancelResult{id: id, err: c.Cancel(ctx, id)}
		}(i)
	}
	wg.Wait()
	close(errs)
	for res := range errs {
		if res.err != nil {
			t.Fatalf("cancel %s: %v", res.id, res.err)
		}
	}
	for i := 0; i < n; i++ {
		got := store.runs[core.RunID(fmt.Sprintf("r-%d", i))]
		if got == nil || got.State != core.RunClosed {
			t.Fatalf("run %d = %+v, want closed", i, got)
		}
	}
}

func TestRetrySignalsReconciler(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r1", "T-1", time.Now())
	run.State = core.RunBlocked
	run.PRNumber = 42
	run.RebaseAttempts = 2
	run.FixAttempts = 2
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf(`store.Save(ctx, run): %v`, err)
	}

	var signals []int
	c := &Control{Store: store, OnChange: func(pr int) { signals = append(signals, pr) }}
	if err := c.Retry(ctx, "r1"); err != nil {
		t.Fatalf(`c.Retry(ctx, "r1"): %v`, err)
	}

	got := store.runs["r1"]
	if got.State != core.RunWaitingCI {
		t.Fatalf("state = %s, want %s", got.State, core.RunWaitingCI)
	}
	if got.RebaseAttempts != 0 || got.FixAttempts != 0 {
		t.Fatalf("attempts = %d/%d, want 0/0", got.RebaseAttempts, got.FixAttempts)
	}
	if len(signals) != 1 || signals[0] != 42 {
		t.Fatalf("signals = %v, want [42]", signals)
	}
}

func TestRetryClosedRunWithPRReturnsToWaitingCI(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r-closed-pr", "T-1", time.Now().UTC())
	run.State = core.RunClosed
	run.PRNumber = 42
	run.SessionID = "sess-old"
	run.WorkspacePath = "/ws/T-1"
	run.Reason = "worker session exited without PR"
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf(`store.Save(ctx, run): %v`, err)
	}

	var signals []int
	c := &Control{Store: store, OnChange: func(pr int) { signals = append(signals, pr) }}
	if err := c.Retry(ctx, "r-closed-pr"); err != nil {
		t.Fatalf(`c.Retry(ctx, "r-closed-pr"): %v`, err)
	}
	got := store.runs["r-closed-pr"]
	if got.State != core.RunWaitingCI || got.PRNumber != 42 {
		t.Fatalf("state=%s pr=%d, want waiting_ci/42", got.State, got.PRNumber)
	}
	if got.Reason != "retried by operator" {
		t.Fatalf("reason = %q", got.Reason)
	}
	if len(signals) != 1 || signals[0] != 42 {
		t.Fatalf("signals = %v, want [42]", signals)
	}
}

func TestRetryClosedRunWithoutPRReturnsToPending(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r-closed-no-pr", "T-1", time.Now().UTC())
	run.State = core.RunClosed
	run.SessionID = "sess-old"
	run.WorkspacePath = "/ws/T-1"
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf(`store.Save(ctx, run): %v`, err)
	}

	var signals []int
	c := &Control{Store: store, OnChange: func(pr int) { signals = append(signals, pr) }}
	if err := c.Retry(ctx, "r-closed-no-pr"); err != nil {
		t.Fatalf(`c.Retry(ctx, "r-closed-no-pr"): %v`, err)
	}
	got := store.runs["r-closed-no-pr"]
	if got.State != core.RunPending || got.SessionID != "" || got.WorkspacePath != "" {
		t.Fatalf("run = %+v, want pending with cleared session and worktree", got)
	}
	if got.Reason != "retried by operator" {
		t.Fatalf("reason = %q", got.Reason)
	}
	if len(signals) != 0 {
		t.Fatalf("signals = %v, want none", signals)
	}
}

func TestRetryClosedRunRejectsActiveSibling(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	closed := core.NewRun("r-closed", "T-1", time.Now().UTC())
	closed.State = core.RunClosed
	active := core.NewRun("r-active", "T-1", time.Now().UTC())
	active.State = core.RunRunning
	for _, run := range []*core.Run{closed, active} {
		if err := store.Save(ctx, run); err != nil {
			t.Fatalf(`store.Save(ctx, run): %v`, err)
		}
	}
	c := &Control{Store: store}
	err := c.Retry(ctx, "r-closed")
	if core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("error = %v, want conflict", err)
	}
	if got := store.runs["r-closed"]; got.State != core.RunClosed {
		t.Fatalf("state = %s, want closed", got.State)
	}
}

func TestNudgeCallsWakeWithOperatorGuidance(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r-nudge", "T-1", time.Now().UTC())
	run.State = core.RunWaitingCI
	run.PRNumber = 42
	run.HeadSHA = "sha42"
	run.SessionID = "sess-nudge"
	run.WorkspacePath = "/ws/T-1"
	run.Branch = "beacon/T-1"
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf(`store.Save(ctx, run): %v`, err)
	}
	h := &fakeHarness{}
	c := &Control{Store: store, Harness: h}
	if err := c.Nudge(ctx, "r-nudge", "please rebase first"); err != nil {
		t.Fatalf(`c.Nudge(ctx, "r-nudge", "please rebase first"): %v`, err)
	}
	if len(h.wakes) != 1 {
		t.Fatalf("wakes = %+v, want one", h.wakes)
	}
	w := h.wakes[0]
	if w.RunID != "r-nudge" || w.PRNumber != 42 || w.HeadSHA != "sha42" ||
		w.Reason != "operator guidance" || w.Excerpt != "please rebase first" ||
		w.Workspace != "/ws/T-1" || w.Branch != "beacon/T-1" {
		t.Fatalf("wake = %+v", w)
	}
}

func TestNudgeRejectsRunsWithoutLiveSession(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	terminal := core.NewRun("r-terminal", "T-1", time.Now().UTC())
	terminal.State = core.RunClosed
	terminal.SessionID = "sess-old"
	noSession := core.NewRun("r-no-session", "T-2", time.Now().UTC())
	noSession.State = core.RunRunning
	for _, run := range []*core.Run{terminal, noSession} {
		if err := store.Save(ctx, run); err != nil {
			t.Fatalf(`store.Save(ctx, run): %v`, err)
		}
	}
	h := &fakeHarness{}
	c := &Control{Store: store, Harness: h}
	if err := c.Nudge(ctx, "r-terminal", "hello"); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("terminal error = %v, want conflict", err)
	}
	if err := c.Nudge(ctx, "r-no-session", "hello"); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("no session error = %v, want conflict", err)
	}
	if len(h.wakes) != 0 {
		t.Fatalf("wakes = %+v, want none", h.wakes)
	}
}

func TestWatchPRBindsActiveRun(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r-active", "T-1", time.Now().UTC())
	run.State = core.RunRunning
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf(`store.Save(ctx, run): %v`, err)
	}

	var signals []int
	c := &Control{Store: store, OnChange: func(pr int) { signals = append(signals, pr) }}
	if err := c.WatchPR(ctx, "r-active", 42); err != nil {
		t.Fatalf(`c.WatchPR(ctx, "r-active", 42): %v`, err)
	}
	got := store.runs["r-active"]
	if got.State != core.RunWaitingCI || got.PRNumber != 42 {
		t.Fatalf("state=%s pr=%d, want %s/42", got.State, got.PRNumber, core.RunWaitingCI)
	}
	if len(signals) != 1 || signals[0] != 42 {
		t.Fatalf("signals = %v, want [42]", signals)
	}
}

func TestWatchPRBindsPRToWaitingRun(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r-waiting", "T-1", time.Now().UTC())
	run.State = core.RunWaitingCI
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf(`store.Save(ctx, run): %v`, err)
	}

	var signals []int
	c := &Control{Store: store, OnChange: func(pr int) { signals = append(signals, pr) }}
	if err := c.WatchPR(ctx, "r-waiting", 42); err != nil {
		t.Fatalf(`c.WatchPR(ctx, "r-waiting", 42): %v`, err)
	}
	got := store.runs["r-waiting"]
	if got.State != core.RunWaitingCI || got.PRNumber != 42 {
		t.Fatalf("state=%s pr=%d, want %s/42", got.State, got.PRNumber, core.RunWaitingCI)
	}
	if len(signals) != 1 || signals[0] != 42 {
		t.Fatalf("signals = %v, want [42]", signals)
	}
}

func TestWatchPRReopensRunClosedByWorkspaceCleanup(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r-closed", "T-1", time.Now().UTC())
	run.State = core.RunClosed
	run.Reason = "workspace: conflict: refusing to remove dirty worktree /ws/T-1"
	run.SessionID = "sess-r-closed"
	run.WorkspacePath = "/ws/T-1"
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf(`store.Save(ctx, run): %v`, err)
	}

	var signals []int
	c := &Control{Store: store, OnChange: func(pr int) { signals = append(signals, pr) }}
	if err := c.WatchPR(ctx, "r-closed", 42); err != nil {
		t.Fatalf(`c.WatchPR(ctx, "r-closed", 42): %v`, err)
	}
	got := store.runs["r-closed"]
	if got.State != core.RunWaitingCI || got.PRNumber != 42 {
		t.Fatalf("state=%s pr=%d, want %s/42", got.State, got.PRNumber, core.RunWaitingCI)
	}
	if got.Reason != "" {
		t.Fatalf("reason = %q, want empty", got.Reason)
	}
	if len(signals) != 1 || signals[0] != 42 {
		t.Fatalf("signals = %v, want [42]", signals)
	}

	source := &fakeSource{}
	scm := &fakeSCM{obs: core.Observation{PRNumber: 42, PRState: "merged", HeadSHA: "sha2"}}
	r := &Reconciler{
		Store: store, SCM: scm, Source: source,
		Workspace: &fakeWorkspace{}, Harness: &fakeHarness{},
	}
	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatalf(`r.Reconcile(ctx, 42): %v`, err)
	}
	if merged := store.runs["r-closed"]; merged.State != core.RunMerged {
		t.Fatalf("state = %s, want %s", merged.State, core.RunMerged)
	}
	if len(source.complete) != 1 || source.complete[0] != "T-1" {
		t.Fatalf("completed tasks = %v, want [T-1]", source.complete)
	}
	if got := store.taskStatus["T-1"]; got != "done" {
		t.Fatalf("task status = %q, want done", got)
	}
}

func TestWatchPRRejectsRunClosedByOperator(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r-cancelled", "T-1", time.Now().UTC())
	run.State = core.RunClosed
	run.Reason = "cancelled by operator"
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf(`store.Save(ctx, run): %v`, err)
	}

	var signals []int
	c := &Control{Store: store, OnChange: func(pr int) { signals = append(signals, pr) }}
	err := c.WatchPR(ctx, "r-cancelled", 42)
	if core.CodeOf(err) != core.CodeInvalidTransition {
		t.Fatalf("error = %v, want invalid transition", err)
	}
	if got := store.runs["r-cancelled"]; got.State != core.RunClosed || got.PRNumber != 0 {
		t.Fatalf("state=%s pr=%d, want closed/0", got.State, got.PRNumber)
	}
	if len(signals) != 0 {
		t.Fatalf("signals = %v, want none", signals)
	}
}

func TestRetryDoesNotSignalWhenRunIsNotBlocked(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r1", "T-1", time.Now())
	run.State = core.RunWaitingCI
	run.PRNumber = 42
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf(`store.Save(ctx, run): %v`, err)
	}

	var signals []int
	c := &Control{Store: store, OnChange: func(pr int) { signals = append(signals, pr) }}
	err := c.Retry(ctx, "r1")
	if core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("error = %v, want conflict", err)
	}
	if len(signals) != 0 {
		t.Fatalf("signals = %v, want none", signals)
	}
	if got := store.runs["r1"]; got.State != core.RunWaitingCI {
		t.Fatalf("state = %s, want %s", got.State, core.RunWaitingCI)
	}
}

// busyApprovalStore injects one SQLite write-lock conflict per run into
// SetApproval. It stands in for a store whose first write hits SQLITE_BUSY.
type busyApprovalStore struct {
	*fakeStore
	mu       sync.Mutex
	failures map[core.RunID]int
}

func newBusyApprovalStore(n int) *busyApprovalStore {
	ctx := context.Background()
	s := &busyApprovalStore{
		fakeStore: newFakeStore(),
		failures:  map[core.RunID]int{},
	}
	for i := 0; i < n; i++ {
		id := core.RunID(fmt.Sprintf("r-%d", i))
		if err := s.fakeStore.Save(ctx, core.NewRun(id, "T-1", time.Now())); err != nil {
			panic(err)
		}
	}
	return s
}

func (s *busyApprovalStore) resetFailures(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < n; i++ {
		s.failures[core.RunID(fmt.Sprintf("r-%d", i))] = 1
	}
}

func (s *busyApprovalStore) SetApproval(ctx context.Context, id core.RunID, sha string) error {
	s.mu.Lock()
	failures := s.failures[id]
	if failures > 0 {
		s.failures[id]--
	}
	s.mu.Unlock()
	if failures > 0 {
		return errors.New("database is locked (5) (SQLITE_BUSY)")
	}
	return s.fakeStore.SetApproval(ctx, id, sha)
}

// approveBatch runs n concurrent approvals, each hitting one busy retry.
// Every goroutine writes its own result slot, so there is no shared write to
// synchronize beyond the WaitGroup.
type approveBatch struct {
	latencies []time.Duration
	errs      []error
}

func runApproveBatch(ctx context.Context, c *Control, store *busyApprovalStore, n int) approveBatch {
	store.resetFailures(n)
	out := approveBatch{
		latencies: make([]time.Duration, n),
		errs:      make([]error, n),
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := core.RunID(fmt.Sprintf("r-%d", i))
			start := time.Now()
			out.errs[i] = c.Approve(ctx, id, "sha")
			out.latencies[i] = time.Since(start)
		}(i)
	}
	wg.Wait()
	return out
}

func TestConcurrentApprovesDoNotSerializeRetries(t *testing.T) {
	ctx := context.Background()
	const n = 100
	store := newBusyApprovalStore(n)
	c := &Control{Store: store}

	start := time.Now()
	out := runApproveBatch(ctx, c, store, n)
	elapsed := time.Since(start)

	for i, err := range out.errs {
		if err != nil {
			t.Fatalf("approve r-%d: %v", i, err)
		}
	}
	// A serialized retry loop sleeps 10ms per writer, so n writers cost at
	// least n*controlRetryBase. Overlapping retries finish far below half of
	// that bound even on a slow machine.
	if bound := n * controlRetryBase / 2; elapsed >= bound {
		t.Fatalf("batch took %v; retries appear serialized (want < %v)", elapsed, bound)
	}
	for i := 0; i < n; i++ {
		id := core.RunID(fmt.Sprintf("r-%d", i))
		ok, err := store.Approved(ctx, id, "sha")
		if err != nil || !ok {
			t.Fatalf("approval r-%d = %v, %v; want true, nil", i, ok, err)
		}
	}
}

func BenchmarkControlConcurrentApprove(b *testing.B) {
	ctx := context.Background()
	const n = 100
	store := newBusyApprovalStore(n)
	c := &Control{Store: store}
	var all []time.Duration
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := runApproveBatch(ctx, c, store, n)
		for _, err := range out.errs {
			if err != nil {
				b.Fatal(err)
			}
		}
		all = append(all, out.latencies...)
	}
	b.StopTimer()
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	p95 := all[len(all)*95/100]
	b.ReportMetric(float64(p95.Microseconds()), "p95-us")
}
