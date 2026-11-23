package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"
)

// daemonCheckAll is the fake CI pass. It counts calls and can fail or cancel
// the context from inside a pass.
type daemonCheckAll struct {
	calls int
	err   error
	hook  func()
}

func (d *daemonCheckAll) run(context.Context) error {
	d.calls++
	if d.hook != nil {
		d.hook()
	}
	return d.err
}

// daemonOrchestrator wraps the real Run so the daemon test observes the same
// merge and wake behaviour the production pass has. A custom function replaces
// it when the test only cares about call counts.
type daemonOrchestrator struct {
	store  Store
	merger Merger
	calls  int
	custom func(context.Context, Waker) error
}

func (d *daemonOrchestrator) run(ctx context.Context, w Waker) error {
	d.calls++
	if d.custom != nil {
		return d.custom(ctx, w)
	}
	_, err := Run(ctx, d.store, d.merger, w, Options{})
	return err
}

// daemonCloser acts on the merged/closed port. It closes Watches whose verdict
// is terminal for the PR, the same condition cibridge uses in production.
type daemonCloser struct {
	store Store
	calls int
	ids   []string
	err   error
	order *[]string
}

func (d *daemonCloser) run(ctx context.Context) error {
	d.calls++
	if d.order != nil {
		*d.order = append(*d.order, "close-merged")
	}
	if d.err != nil {
		return d.err
	}
	if d.store == nil {
		return nil
	}
	watches, err := d.store.ActiveWatches(ctx)
	if err != nil {
		return err
	}
	for _, w := range watches {
		v := strings.ToLower(strings.TrimSpace(w.Verdict))
		if v != "merged" && v != "closed" {
			continue
		}
		if err := d.store.CloseWatch(ctx, w.ID); err != nil {
			return err
		}
		d.ids = append(d.ids, w.ID)
	}
	return nil
}

func daemonPorts(check *daemonCheckAll, orch *daemonOrchestrator, closer *daemonCloser, waker Waker, logf func(string, ...any)) DaemonPorts {
	return DaemonPorts{
		CheckAll:    check.run,
		Orchestrate: orch.run,
		Verify:      (&daemonVerifier{ok: true}).run,
		CloseMerged: closer.run,
		Waker:       waker,
		Log:         logf,
	}
}

// daemonSubscribe is a mock of the Subscribe port. It counts calls and can
// fail, so a pass can prove that orchestration still runs after a subscribe
// error.
type daemonSubscribe struct {
	calls int
	err   error
	order *[]string
}

func (d *daemonSubscribe) run(context.Context) error {
	d.calls++
	if d.order != nil {
		*d.order = append(*d.order, "subscribe")
	}
	return d.err
}

// daemonSummaryPort is a mock of the Summary port. It returns a fixed workload
// report, so the daemon's log line can be checked against known counters.
type daemonSummaryPort struct {
	calls  int
	report Workload
	err    error
	order  *[]string
}

func (d *daemonSummaryPort) run(context.Context) (Workload, error) {
	d.calls++
	if d.order != nil {
		*d.order = append(*d.order, "summary")
	}
	return d.report, d.err
}

// daemonVerifier is a mock of the Verify port. ok reports whether the required
// checks passed, err makes the verification itself fail.
type daemonVerifier struct {
	ok    bool
	err   error
	calls int
	order *[]string
}

func (d *daemonVerifier) run(context.Context) (bool, error) {
	d.calls++
	if d.order != nil {
		*d.order = append(*d.order, "verify")
	}
	return d.ok, d.err
}

// daemonOrderPorts builds ports whose mocks record the order of every step.
func daemonOrderPorts(order *[]string) (DaemonPorts, *daemonSubscribe, *daemonSummaryPort) {
	check := &daemonCheckAll{hook: func() { *order = append(*order, "checkall") }}
	sub := &daemonSubscribe{order: order}
	summary := &daemonSummaryPort{order: order}
	verifier := &daemonVerifier{ok: true, order: order}
	orch := &daemonOrchestrator{custom: func(context.Context, Waker) error {
		*order = append(*order, "orchestrate")
		return nil
	}}
	closer := &daemonCloser{order: order}
	ports := daemonPorts(check, orch, closer, nil, nil)
	ports.Subscribe = sub.run
	ports.Summary = summary.run
	ports.Verify = verifier.run
	return ports, sub, summary
}

// daemonSchedules is a fake schedule port. MarkTriggered advances the stored
// row, so a second pass observes the acknowledgement made by the first.
type daemonSchedules struct {
	rows    []DaemonDueSchedule
	listErr error
	markErr error
	calls   int
	marks   []daemonScheduleMark
	order   *[]string
}

type daemonScheduleMark struct {
	id      string
	nextRun time.Time
}

func (s *daemonSchedules) ListDue(_ context.Context, now time.Time) ([]DaemonDueSchedule, error) {
	s.calls++
	if s.order != nil {
		*s.order = append(*s.order, "schedules")
	}
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := []DaemonDueSchedule{}
	for _, row := range s.rows {
		if row.NextRun.After(now) {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *daemonSchedules) MarkTriggered(_ context.Context, id string, nextRun time.Time) error {
	s.marks = append(s.marks, daemonScheduleMark{id: id, nextRun: nextRun})
	if s.markErr != nil {
		return s.markErr
	}
	for i := range s.rows {
		if s.rows[i].ID == id {
			s.rows[i].NextRun = nextRun
		}
	}
	return nil
}

// An overdue schedule is marked once, and the next pass sees no due row.
func TestDaemonDueSchedulesFireOncePerPeriod(t *testing.T) {
	now := time.Now()
	sched := &daemonSchedules{rows: []DaemonDueSchedule{{
		ID: "s-due", NextRun: now.Add(-time.Hour), Interval: time.Hour,
	}}}
	check := &daemonCheckAll{}
	orch := &daemonOrchestrator{custom: func(context.Context, Waker) error { return nil }}
	ports := daemonPorts(check, orch, &daemonCloser{}, nil, nil)
	ports.Schedules = sched

	if err := DaemonRun(context.Background(), ports, time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if len(sched.marks) != 1 || sched.marks[0].id != "s-due" {
		t.Fatalf("marks = %+v, want exactly one mark for s-due", sched.marks)
	}
	if !sched.marks[0].nextRun.After(now) {
		t.Fatalf("next run %v is not after now %v", sched.marks[0].nextRun, now)
	}

	if err := DaemonRun(context.Background(), ports, time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if len(sched.marks) != 1 {
		t.Fatalf("marks after second pass = %+v, want no repeat", sched.marks)
	}
}

// Several missed periods collapse into one future run, not one trigger per
// missed interval. A row without an interval is skipped.
func TestDaemonNextRunCollapsesMissedPeriods(t *testing.T) {
	now := time.Now()
	next, ok := daemonNextRun(now.Add(-3*time.Hour), time.Hour, now)
	if !ok || !next.Equal(now.Add(time.Hour)) {
		t.Fatalf("next run = %v, ok = %v, want %v", next, ok, now.Add(time.Hour))
	}
	if _, ok := daemonNextRun(now, 0, now); ok {
		t.Fatal("a row without a positive interval must not be triggered")
	}
	if _, ok := daemonNextRun(time.Time{}, time.Hour, now); ok {
		t.Fatal("a row without a next run must not be triggered")
	}
}

func TestDaemonSkipsScheduleWithoutInterval(t *testing.T) {
	now := time.Now()
	sched := &daemonSchedules{rows: []DaemonDueSchedule{{
		ID: "s-bad", NextRun: now.Add(-time.Hour),
	}}}
	ports := daemonPorts(&daemonCheckAll{}, &daemonOrchestrator{
		custom: func(context.Context, Waker) error { return nil },
	}, &daemonCloser{}, nil, nil)
	ports.Schedules = sched

	if err := DaemonRun(context.Background(), ports, time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if len(sched.marks) != 0 {
		t.Fatalf("marks = %+v, want none for a row without an interval", sched.marks)
	}
}

// A failing schedule port is logged and does not stop the rest of the pass.
func TestDaemonScheduleErrorDoesNotStopPass(t *testing.T) {
	sched := &daemonSchedules{listErr: errors.New("schedules boom")}
	check := &daemonCheckAll{}
	orch := &daemonOrchestrator{custom: func(context.Context, Waker) error { return nil }}
	closer := &daemonCloser{}
	var logs []string
	ports := daemonPorts(check, orch, closer, nil, func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	ports.Schedules = sched

	if err := DaemonRun(context.Background(), ports, time.Hour, true); err != nil {
		t.Fatalf("daemon must not fall on a schedule error: %v", err)
	}
	if orch.calls != 1 || closer.calls != 1 {
		t.Fatalf("orchestrate/close calls = %d/%d, want 1/1", orch.calls, closer.calls)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "daemon: schedules: schedules boom") {
		t.Fatalf("schedule error not logged: %q", logs)
	}
}

// The schedule step sits after subscribe and before orchestration, and does
// not disturb the order of the existing steps.
func TestDaemonSchedulesStepRunsBeforeOrchestrate(t *testing.T) {
	var order []string
	check := &daemonCheckAll{hook: func() { order = append(order, "checkall") }}
	orch := &daemonOrchestrator{custom: func(context.Context, Waker) error {
		order = append(order, "orchestrate")
		return nil
	}}
	closer := &daemonCloser{order: &order}
	ports := daemonPorts(check, orch, closer, nil, nil)
	ports.Schedules = &daemonSchedules{order: &order}

	if err := DaemonRun(context.Background(), ports, time.Hour, true); err != nil {
		t.Fatal(err)
	}
	want := []string{"checkall", "schedules", "orchestrate", "close-merged"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("step order = %q, want %q", order, want)
	}
}

func TestDaemonOnceRunsOnePass(t *testing.T) {
	check := &daemonCheckAll{}
	orch := &daemonOrchestrator{custom: func(context.Context, Waker) error { return nil }}
	closer := &daemonCloser{}

	if err := DaemonRun(context.Background(), daemonPorts(check, orch, closer, nil, nil), time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if check.calls != 1 || orch.calls != 1 || closer.calls != 1 {
		t.Fatalf("calls = %d/%d/%d, want 1/1/1", check.calls, orch.calls, closer.calls)
	}
}

func TestDaemonOnceMergesReadyWatch(t *testing.T) {
	st := newFakeStore(readyWatch("w1", 42, 3))
	merger := &fakeMerger{}
	waker := &fakeWaker{}
	orch := &daemonOrchestrator{store: st, merger: merger}
	closer := &daemonCloser{store: st}

	if err := DaemonRun(context.Background(), daemonPorts(&daemonCheckAll{}, orch, closer, waker, nil), time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if len(merger.calls) != 1 || merger.calls[0] != (mergeCall{"refusedguy/beacon", 42}) {
		t.Fatalf("merger calls = %+v", merger.calls)
	}
	if !reflect.DeepEqual(st.closed, []string{"w1"}) {
		t.Fatalf("closed = %q, want [w1]", st.closed)
	}
	if len(waker.calls) != 0 {
		t.Fatalf("ready watch must not wake the executor: %+v", waker.calls)
	}
}

func TestDaemonOnceWakesRetryWatch(t *testing.T) {
	st := newFakeStore(Watch{
		ID: "w2", Repo: "refusedguy/beacon", PR: 7,
		Verdict: VerdictRetry, Revision: 4,
	})
	merger := &fakeMerger{}
	waker := &fakeWaker{}
	orch := &daemonOrchestrator{store: st, merger: merger}

	if err := DaemonRun(context.Background(), daemonPorts(&daemonCheckAll{}, orch, &daemonCloser{store: st}, waker, nil), time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if len(waker.calls) != 1 || waker.calls[0] != (wakeCall{"w2", 4, VerdictRetry}) {
		t.Fatalf("waker calls = %+v", waker.calls)
	}
	if len(merger.calls) != 0 {
		t.Fatalf("retry watch must not be merged: %+v", merger.calls)
	}
}

func TestDaemonOnceClosesMergedWatch(t *testing.T) {
	st := newFakeStore(Watch{
		ID: "w3", Repo: "refusedguy/beacon", PR: 9,
		Verdict: "merged", Revision: 2,
	})
	merger := &fakeMerger{}
	orch := &daemonOrchestrator{store: st, merger: merger}
	closer := &daemonCloser{store: st}

	if err := DaemonRun(context.Background(), daemonPorts(&daemonCheckAll{}, orch, closer, nil, nil), time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(closer.ids, []string{"w3"}) || !reflect.DeepEqual(st.closed, []string{"w3"}) {
		t.Fatalf("closed = %q, store closed = %q, want [w3]", closer.ids, st.closed)
	}
}

func TestDaemonPassErrorsAreLoggedAndIgnored(t *testing.T) {
	check := &daemonCheckAll{err: errors.New("check boom")}
	orch := &daemonOrchestrator{custom: func(context.Context, Waker) error {
		return errors.New("orchestrate boom")
	}}
	closer := &daemonCloser{err: errors.New("close boom")}
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	if err := DaemonRun(context.Background(), daemonPorts(check, orch, closer, nil, logf), time.Hour, true); err != nil {
		t.Fatalf("daemon must not fall on a pass error: %v", err)
	}
	if check.calls != 1 || orch.calls != 1 || closer.calls != 1 {
		t.Fatalf("calls = %d/%d/%d, want every port called once", check.calls, orch.calls, closer.calls)
	}
	if len(logs) != 3 {
		t.Fatalf("logs = %q, want three error lines", logs)
	}
	for _, fragment := range []string{"checkall", "orchestrate", "close merged"} {
		found := false
		for _, line := range logs {
			if strings.Contains(line, fragment) {
				found = true
			}
		}
		if !found {
			t.Fatalf("no log line mentions %q: %q", fragment, logs)
		}
	}
}

func TestDaemonLoopStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	check := &daemonCheckAll{}
	check.hook = func() {
		if check.calls >= 3 {
			cancel()
		}
	}
	orch := &daemonOrchestrator{custom: func(context.Context, Waker) error { return nil }}

	if err := DaemonRun(ctx, daemonPorts(check, orch, &daemonCloser{}, nil, nil), time.Millisecond, false); err != nil {
		t.Fatal(err)
	}
	if check.calls < 3 {
		t.Fatalf("passes = %d, want at least three before cancel", check.calls)
	}
}

func TestDaemonRunRequiresPorts(t *testing.T) {
	if err := DaemonRun(context.Background(), DaemonPorts{}, time.Millisecond, true); err == nil {
		t.Fatal("daemon must reject a port-less configuration")
	}
}

// TestDaemonPassStepOrder pins the extension of the daemon pass: subscribe
// runs between checkall and orchestrate, the summary is printed after
// orchestrate, and verify runs before the final close-merged step.
func TestDaemonPassStepOrder(t *testing.T) {
	var order []string
	ports, sub, summary := daemonOrderPorts(&order)

	if err := DaemonRun(context.Background(), ports, time.Hour, true); err != nil {
		t.Fatal(err)
	}
	want := []string{"checkall", "subscribe", "orchestrate", "summary", "verify", "close-merged"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("step order = %q, want %q", order, want)
	}
	if sub.calls != 1 || summary.calls != 1 {
		t.Fatalf("subscribe/summary calls = %d/%d, want 1/1", sub.calls, summary.calls)
	}
}

// A failing subscribe is logged but does not stop orchestrate, summary, verify
// or close-merged: the pass keeps all of its actions.
func TestDaemonSubscribeErrorDoesNotStopPass(t *testing.T) {
	var order []string
	ports, sub, _ := daemonOrderPorts(&order)
	sub.err = errors.New("subscribe boom")

	var logs []string
	ports.Log = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	if err := DaemonRun(context.Background(), ports, time.Hour, true); err != nil {
		t.Fatalf("daemon must not fall on a subscribe error: %v", err)
	}
	want := []string{"checkall", "subscribe", "orchestrate", "summary", "verify", "close-merged"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("step order = %q, want %q", order, want)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "daemon: subscribe: subscribe boom") {
		t.Fatalf("subscribe error not logged: %q", logs)
	}
}

// The summary log line carries the same four headline counters as the workload
// report, so a daemon pass can be compared with `beacon workload --format json`.
func TestDaemonSummaryIsLogged(t *testing.T) {
	ports, _, summary := daemonOrderPorts(new([]string))
	summary.report = Workload{Total: 7, Ready: 2, NeedsAttention: 3, Watching: 4, Registered: 1}

	var logs []string
	ports.Log = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	if err := DaemonRun(context.Background(), ports, time.Hour, true); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(logs, "\n")
	for _, fragment := range []string{
		"total=7", "ready=2", "needsAttention=3", "watching=4", "registered=1",
	} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("summary log %q is missing %q", logs, fragment)
		}
	}
}

// A failing summary is logged and the pass still closes merged Watches.
func TestDaemonSummaryErrorIsLogged(t *testing.T) {
	var order []string
	ports, _, summary := daemonOrderPorts(&order)
	summary.err = errors.New("summary boom")

	var logs []string
	ports.Log = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	if err := DaemonRun(context.Background(), ports, time.Hour, true); err != nil {
		t.Fatalf("daemon must not fall on a summary error: %v", err)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "daemon: summary: summary boom") {
		t.Fatalf("summary error not logged: %q", logs)
	}
	want := []string{"checkall", "subscribe", "orchestrate", "summary", "verify", "close-merged"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("step order = %q, want %q", order, want)
	}
}

// Older callers that do not set Subscribe or Summary keep the original pass.
// The daemonPorts helper leaves those two nil and supplies a passing Verify,
// so close-merged still runs.
func TestDaemonWithoutSubscribeAndSummaryStillRuns(t *testing.T) {
	check := &daemonCheckAll{}
	orch := &daemonOrchestrator{custom: func(context.Context, Waker) error { return nil }}
	closer := &daemonCloser{}

	if err := DaemonRun(context.Background(), daemonPorts(check, orch, closer, nil, nil), time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if check.calls != 1 || orch.calls != 1 || closer.calls != 1 {
		t.Fatalf("calls = %d/%d/%d, want 1/1/1", check.calls, orch.calls, closer.calls)
	}
}

// A passing verify is the gate that allows the final close.
func TestDaemonVerifyPassedCloses(t *testing.T) {
	check := &daemonCheckAll{}
	orch := &daemonOrchestrator{custom: func(context.Context, Waker) error { return nil }}
	closer := &daemonCloser{}
	verifier := &daemonVerifier{ok: true}

	ports := daemonPorts(check, orch, closer, nil, nil)
	ports.Verify = verifier.run

	if err := DaemonRun(context.Background(), ports, time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if verifier.calls != 1 {
		t.Fatalf("verify calls = %d, want 1", verifier.calls)
	}
	if closer.calls != 1 {
		t.Fatalf("close-merged calls = %d, want 1 after a passing verify", closer.calls)
	}
}

// A verify that ran but did not pass keeps a merged card open.
func TestDaemonVerifyNotPassedDoesNotClose(t *testing.T) {
	st := newFakeStore(Watch{
		ID: "w-verify", Repo: "refusedguy/beacon", PR: 9,
		Verdict: "merged", Revision: 2,
	})
	orch := &daemonOrchestrator{custom: func(context.Context, Waker) error { return nil }}
	closer := &daemonCloser{store: st}
	verifier := &daemonVerifier{ok: false}

	ports := daemonPorts(&daemonCheckAll{}, orch, closer, nil, nil)
	ports.Verify = verifier.run

	if err := DaemonRun(context.Background(), ports, time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if verifier.calls != 1 {
		t.Fatalf("verify calls = %d, want 1", verifier.calls)
	}
	if closer.calls != 0 {
		t.Fatalf("close-merged calls = %d, want 0 when verify did not pass", closer.calls)
	}
	if len(st.closed) != 0 {
		t.Fatalf("closed = %q, want no watches closed when verify did not pass", st.closed)
	}
}

// An error from verify keeps the card open and logs the reason.
func TestDaemonVerifyErrorDoesNotClose(t *testing.T) {
	check := &daemonCheckAll{}
	orch := &daemonOrchestrator{custom: func(context.Context, Waker) error { return nil }}
	closer := &daemonCloser{}
	verifier := &daemonVerifier{err: errors.New("verify boom")}

	var logs []string
	ports := daemonPorts(check, orch, closer, nil, func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	ports.Verify = verifier.run

	if err := DaemonRun(context.Background(), ports, time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if verifier.calls != 1 {
		t.Fatalf("verify calls = %d, want 1", verifier.calls)
	}
	if closer.calls != 0 {
		t.Fatalf("close-merged calls = %d, want 0 on a verify error", closer.calls)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "daemon: verify: verify boom") {
		t.Fatalf("verify error not logged: %q", logs)
	}
}

// daemonRecordingAdapter counts deliveries at the same T210 boundary the real
// daemon adapter uses, so a stale target cannot slip past the test unnoticed.
type daemonRecordingAdapter struct {
	calls   int
	targets []string
	texts   []string
}

func (a *daemonRecordingAdapter) Deliver(_ context.Context, p WakePointer) (WakeReceipt, bool) {
	a.calls++
	a.targets = append(a.targets, p.Target)
	a.texts = append(a.texts, p.VerdictRef)
	return WakeReceipt{
		ObligationID: p.ObligationID,
		Attempt:      p.Attempt,
		Delivery:     p.Delivery,
		Outcome:      WakeDelivered,
	}, true
}

func daemonTestWaker(watch DaemonWatch, adapter WakeBackend) *daemonWaker {
	return &daemonWaker{
		lookup:  func(string) (DaemonWatch, error) { return watch, nil },
		backend: adapter,
	}
}

// daemonLogs runs fn with the package logger redirected, so the "log once"
// contract is checked without reaching for a global assertion after the call.
func daemonLogs(fn func()) string {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)
	fn()
	return buf.String()
}

func TestDaemonWakerSkipsLegacySubscriber(t *testing.T) {
	adapter := &daemonRecordingAdapter{}
	w := daemonTestWaker(DaemonWatch{
		ID: "w1", PR: 42, Adapter: WakeDefaultAdapter,
		SubscriberID: "beacon-ci-checkall",
	}, adapter)

	var wakeErr error
	logs := daemonLogs(func() {
		wakeErr = w.Wake(context.Background(), "w1", 3, VerdictRetry)
	})
	if wakeErr == nil {
		t.Fatal("a stale service subscriber must not produce a wake")
	}
	if !strings.Contains(wakeErr.Error(), "ses_") {
		t.Fatalf("error %q does not explain that a sessionID is required", wakeErr)
	}
	if adapter.calls != 0 {
		t.Fatalf("Deliver called %d times for a stale subscriber, want 0", adapter.calls)
	}
	if lines := strings.Count(logs, "\n"); lines != 1 {
		t.Fatalf("logs = %q, want exactly one line", logs)
	}
}

func TestDaemonWakerDeliversToSessionSubscriber(t *testing.T) {
	adapter := &daemonRecordingAdapter{}
	w := daemonTestWaker(DaemonWatch{
		ID: "w2", PR: 7, Adapter: WakeDefaultAdapter,
		SubscriberID: "ses_abc123",
	}, adapter)

	if err := w.Wake(context.Background(), "w2", 4, VerdictRetry); err != nil {
		t.Fatalf("valid subscriber must be delivered: %v", err)
	}
	if adapter.calls != 1 || !reflect.DeepEqual(adapter.targets, []string{"ses_abc123"}) {
		t.Fatalf("Deliver calls = %d, targets = %q, want one ses_abc123", adapter.calls, adapter.targets)
	}
}

// The wake pointer must tell the subscriber what the verdict asks for: a retry
// is a rerun, never a hardcoded rebase instruction.
func TestDaemonWakerTextMatchesVerdict(t *testing.T) {
	cases := []struct {
		verdict string
		want    string
	}{
		{"rebase", "нужен rebase"},
		{VerdictRetry, "gh run rerun --failed"},
		{"keep_working", "продолжай работу"},
	}
	for i, tc := range cases {
		adapter := &daemonRecordingAdapter{}
		watchID := fmt.Sprintf("w-text-%d", i)
		w := daemonTestWaker(DaemonWatch{
			ID: watchID, PR: 7, Adapter: WakeDefaultAdapter,
			SubscriberID: "ses_text",
		}, adapter)
		if err := w.Wake(context.Background(), watchID, 1, tc.verdict); err != nil {
			t.Fatalf("Wake(%s): %v", tc.verdict, err)
		}
		if len(adapter.texts) != 1 || !strings.Contains(adapter.texts[0], tc.want) {
			t.Fatalf("wake text for %s = %q, want it to contain %q", tc.verdict, adapter.texts, tc.want)
		}
		if !strings.Contains(adapter.texts[0], tc.verdict) {
			t.Fatalf("wake text for %s = %q, want the verdict name", tc.verdict, adapter.texts[0])
		}
	}
}

// The wake pointer must carry both diff coordinates so the subscriber can run
// git diff baseRef..HEAD against its own branch without a second lookup.
func TestDaemonWakerTextCarriesDiffCoordinates(t *testing.T) {
	adapter := &daemonRecordingAdapter{}
	watchID := "w-diff"
	const head = "abc123def456"
	const base = "main"
	w := daemonTestWaker(DaemonWatch{
		ID: watchID, PR: 12, HeadSHA: head, BaseRef: base,
		Adapter: WakeDefaultAdapter, SubscriberID: "ses_diff",
	}, adapter)
	if err := w.Wake(context.Background(), watchID, 2, VerdictRetry); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	if len(adapter.texts) != 1 {
		t.Fatalf("wake texts = %q, want exactly one", adapter.texts)
	}
	text := adapter.texts[0]
	if !strings.Contains(text, "headSHA="+head) {
		t.Fatalf("wake text %q does not contain headSHA=%s", text, head)
	}
	if !strings.Contains(text, "baseRef="+base) {
		t.Fatalf("wake text %q does not contain baseRef=%s", text, base)
	}
}

// daemonWakeAlreadyExists is the daemon's search-before-create check (HD-006):
// it must report a miss on an empty or nil ledger, a hit for a recorded
// obligation, and a miss for an unrelated one.
func TestDaemonWakeAlreadyExistsSearchesLedger(t *testing.T) {
	if daemonWakeAlreadyExists(nil, "k") {
		t.Fatal("a nil waker has no ledger and must report no existing wake")
	}
	w := &daemonWaker{}
	if daemonWakeAlreadyExists(w, "k") {
		t.Fatal("an uninitialised ledger must report no existing wake")
	}
	w.deliveredKeys = map[string]struct{}{"k": {}}
	if !daemonWakeAlreadyExists(w, "k") {
		t.Fatal("a recorded obligation must be found")
	}
	if daemonWakeAlreadyExists(w, "other") {
		t.Fatal("an unrelated obligation must not be found")
	}
}

// A Watch whose verdict did not change keeps its revision. Six daemon passes
// over the same retry Watch must steer the executor once, not six times.
func TestDaemonWakerDeliversSameObligationOnce(t *testing.T) {
	adapter := &daemonRecordingAdapter{}
	w := daemonTestWaker(DaemonWatch{
		ID: "w-repeat", PR: 42, Adapter: WakeDefaultAdapter,
		SubscriberID: "ses_repeat",
	}, adapter)

	for i := 0; i < 6; i++ {
		if err := w.Wake(context.Background(), "w-repeat", 1, VerdictRetry); err != nil {
			t.Fatalf("Wake #%d: %v", i+1, err)
		}
	}
	if adapter.calls != 1 {
		t.Fatalf("Deliver calls = %d, want exactly 1 for the same (watchID, revision, subscriber)", adapter.calls)
	}
}

// A new revision is a new obligation and must wake the same subscriber again.
func TestDaemonWakerDeliversOnNewRevision(t *testing.T) {
	adapter := &daemonRecordingAdapter{}
	w := daemonTestWaker(DaemonWatch{
		ID: "w-rev", PR: 7, Adapter: WakeDefaultAdapter,
		SubscriberID: "ses_rev",
	}, adapter)

	for _, revision := range []uint64{1, 2} {
		if err := w.Wake(context.Background(), "w-rev", revision, VerdictRetry); err != nil {
			t.Fatalf("Wake(revision=%d): %v", revision, err)
		}
	}
	if adapter.calls != 2 {
		t.Fatalf("Deliver calls = %d, want 2 for two revisions", adapter.calls)
	}
}

// The obligation key includes the subscriber. Changing the executor at the
// same revision must reach the new session and be deduped there on its own.
func TestDaemonWakerDeliversForAnotherSubscriber(t *testing.T) {
	adapter := &daemonRecordingAdapter{}
	view := DaemonWatch{ID: "w-sub", PR: 5, Adapter: WakeDefaultAdapter, SubscriberID: "ses_first"}
	w := &daemonWaker{
		lookup:  func(string) (DaemonWatch, error) { return view, nil },
		backend: adapter,
	}

	if err := w.Wake(context.Background(), "w-sub", 1, VerdictRetry); err != nil {
		t.Fatalf("Wake for ses_first: %v", err)
	}
	view.SubscriberID = "ses_second"
	for i := 0; i < 2; i++ {
		if err := w.Wake(context.Background(), "w-sub", 1, VerdictRetry); err != nil {
			t.Fatalf("Wake #%d for ses_second: %v", i+1, err)
		}
	}
	want := []string{"ses_first", "ses_second"}
	if !reflect.DeepEqual(adapter.targets, want) {
		t.Fatalf("Deliver targets = %q, want %q", adapter.targets, want)
	}
}

// The watch id is part of the obligation key. Two Watches at the same revision
// and subscriber each get one delivery.
func TestDaemonWakerDeliversForAnotherWatch(t *testing.T) {
	adapter := &daemonRecordingAdapter{}
	view := DaemonWatch{ID: "w-one", PR: 1, Adapter: WakeDefaultAdapter, SubscriberID: "ses_same"}
	w := &daemonWaker{
		lookup: func(id string) (DaemonWatch, error) {
			view.ID = id
			return view, nil
		},
		backend: adapter,
	}

	for _, id := range []string{"w-one", "w-two", "w-one"} {
		if err := w.Wake(context.Background(), id, 1, VerdictRetry); err != nil {
			t.Fatalf("Wake(%s): %v", id, err)
		}
	}
	if adapter.calls != 2 {
		t.Fatalf("Deliver calls = %d, want 2 for two watch ids", adapter.calls)
	}
}

// A blocked escalation uses the orchestrator route, not this executor ledger.
// A refused wake is not recorded, so a valid executor appearing later at the
// same revision still gets exactly one delivery.
func TestDaemonWakerRefusalDoesNotConsumeLedgerKey(t *testing.T) {
	adapter := &daemonRecordingAdapter{}
	view := DaemonWatch{ID: "w-block", PR: 9, Adapter: WakeDefaultAdapter}
	w := &daemonWaker{
		lookup:  func(string) (DaemonWatch, error) { return view, nil },
		backend: adapter,
	}

	if err := w.Wake(context.Background(), "w-block", 1, VerdictRetry); err == nil {
		t.Fatal("a Watch without an executor subscriber must be refused")
	}
	if adapter.calls != 0 {
		t.Fatalf("Deliver called %d times for a refused wake, want 0", adapter.calls)
	}
	view.SubscriberID = "ses_exec"
	for i := 0; i < 3; i++ {
		if err := w.Wake(context.Background(), "w-block", 1, VerdictRetry); err != nil {
			t.Fatalf("Wake #%d for ses_exec: %v", i+1, err)
		}
	}
	if adapter.calls != 1 || !reflect.DeepEqual(adapter.targets, []string{"ses_exec"}) {
		t.Fatalf("Deliver calls = %d, targets = %q, want one ses_exec", adapter.calls, adapter.targets)
	}
}

// Only a successful receipt suppresses a repeat. An errored delivery stays
// eligible so the next daemon pass can retry it.
func TestDaemonWakerRetriesAfterError(t *testing.T) {
	adapter := &flakyDaemonAdapter{}
	w := &daemonWaker{
		lookup: func(string) (DaemonWatch, error) {
			return DaemonWatch{ID: "w-err", PR: 3, Adapter: WakeDefaultAdapter, SubscriberID: "ses_err"}, nil
		},
		backend: adapter,
	}

	if err := w.Wake(context.Background(), "w-err", 1, VerdictRetry); err == nil {
		t.Fatal("an errored delivery must surface as an error")
	}
	if err := w.Wake(context.Background(), "w-err", 1, VerdictRetry); err != nil {
		t.Fatalf("retry after an error: %v", err)
	}
	if err := w.Wake(context.Background(), "w-err", 1, VerdictRetry); err != nil {
		t.Fatalf("repeat after a successful retry: %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("Deliver calls = %d, want 2 (error then success)", adapter.calls)
	}
}

// flakyDaemonAdapter errors once, then reports a normal delivery.
type flakyDaemonAdapter struct{ calls int }

func (a *flakyDaemonAdapter) Deliver(_ context.Context, p WakePointer) (WakeReceipt, bool) {
	a.calls++
	if a.calls == 1 {
		return WakeReceipt{ObligationID: p.ObligationID, Outcome: WakeErrored}, true
	}
	return WakeReceipt{ObligationID: p.ObligationID, Outcome: WakeDelivered}, true
}

func TestDaemonWakerWithoutSubscriberIsError(t *testing.T) {
	adapter := &daemonRecordingAdapter{}
	w := daemonTestWaker(DaemonWatch{ID: "w3", PR: 8, Adapter: WakeDefaultAdapter}, adapter)

	var wakeErr error
	logs := daemonLogs(func() {
		wakeErr = w.Wake(context.Background(), "w3", 5, VerdictRetry)
	})
	if wakeErr == nil {
		t.Fatal("a Watch without a subscriber must return an error")
	}
	if !strings.Contains(wakeErr.Error(), "ses_") {
		t.Fatalf("error %q does not explain that a sessionID is required", wakeErr)
	}
	if adapter.calls != 0 {
		t.Fatalf("Deliver called %d times without a subscriber, want 0", adapter.calls)
	}
	if lines := strings.Count(logs, "\n"); lines != 1 {
		t.Fatalf("logs = %q, want exactly one line", logs)
	}
}

func TestDaemonWakeSubscriberSelection(t *testing.T) {
	legacy := DaemonSubscriber{
		ID: "beacon-ci-checkall", Role: RoleExecutor, Adapter: WakeDefaultAdapter,
		Target: "beacon-ci-checkall",
	}
	orchestratorSub := DaemonSubscriber{
		ID: "ses_orch", Role: RoleOrchestrator, Adapter: WakeDefaultAdapter,
		Target: "ses_orch",
	}
	executorSub := DaemonSubscriber{
		ID: "ses_exec", Role: RoleExecutor, Adapter: WakeDefaultAdapter,
		Target: "ses_exec",
	}
	staleOrchestrator := DaemonSubscriber{
		ID: "beacon-daemon-checkall", Role: RoleOrchestrator,
		Target: "beacon-daemon-checkall",
	}
	fixerSub := DaemonSubscriber{
		ID: "ses_fix", Role: RoleFixer, Adapter: WakeDefaultAdapter,
		Target: "ses_fix",
	}

	tests := []struct {
		name    string
		subs    []DaemonSubscriber
		wantID  string
		wantErr bool
	}{
		{
			name:    "only orchestrator is never a wake target",
			subs:    []DaemonSubscriber{orchestratorSub},
			wantErr: true,
		},
		{
			name:   "executor wins among orchestrator and executor",
			subs:   []DaemonSubscriber{orchestratorSub, executorSub},
			wantID: "ses_exec",
		},
		{
			name:   "only executor is selected",
			subs:   []DaemonSubscriber{executorSub},
			wantID: "ses_exec",
		},
		{
			name:    "stale service subscribers are refused",
			subs:    []DaemonSubscriber{legacy, staleOrchestrator},
			wantErr: true,
		},
		{
			name:    "legacy executor plus orchestrator has no fallback",
			subs:    []DaemonSubscriber{legacy, orchestratorSub},
			wantErr: true,
		},
		{
			name:    "executor with a stale target is refused",
			subs:    []DaemonSubscriber{{ID: "ses_ok", Role: RoleExecutor, Target: "beacon-ci-checkall"}},
			wantErr: true,
		},
		{
			name:   "only fixer is selected",
			subs:   []DaemonSubscriber{fixerSub},
			wantID: "ses_fix",
		},
		{
			name:   "fixer wins among orchestrator and fixer",
			subs:   []DaemonSubscriber{orchestratorSub, fixerSub},
			wantID: "ses_fix",
		},
		{
			name:    "fixer with a stale target is refused",
			subs:    []DaemonSubscriber{{ID: "ses_ok", Role: RoleFixer, Target: "beacon-ci-checkall"}},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DaemonWakeSubscriber(tc.subs)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %+v", got)
				}
				if got.ID != "" {
					t.Fatalf("refused selection must be empty, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.ID != tc.wantID {
				t.Fatalf("selected %q, want %q", got.ID, tc.wantID)
			}
		})
	}
}

// A fixer receives the same retry pointer an executor receives. The pointer
// reaches the fixer's own session at the adapter boundary.
func TestDaemonWakerDeliversToFixer(t *testing.T) {
	adapter := &daemonRecordingAdapter{}
	subs := []DaemonSubscriber{{
		ID: "ses_fix", Role: RoleFixer, Adapter: WakeDefaultAdapter, Target: "ses_fix",
	}}
	w := &daemonWaker{
		lookup: func(id string) (DaemonWatch, error) {
			view := DaemonWatch{ID: id, PR: 7}
			if sub, err := DaemonWakeSubscriber(subs); err == nil {
				view.Adapter, view.SubscriberID = sub.Adapter, sub.ID
			}
			return view, nil
		},
		backend: adapter,
	}

	if err := w.Wake(context.Background(), "w-fix", 2, VerdictRetry); err != nil {
		t.Fatalf("Wake fixer: %v", err)
	}
	if adapter.calls != 1 || !reflect.DeepEqual(adapter.targets, []string{"ses_fix"}) {
		t.Fatalf("Deliver calls = %d, targets = %q, want one delivery to ses_fix", adapter.calls, adapter.targets)
	}
}

// TestDaemonWakerNeverDeliversToOrchestrator builds the same lookup the
// composition root uses: no SubscriberID reaches the waker when the Watch has
// no valid executor. The retry pointer must not reach the orchestrator's
// session through Deliver.
func TestDaemonWakerNeverDeliversToOrchestrator(t *testing.T) {
	adapter := &daemonRecordingAdapter{}
	w := &daemonWaker{
		lookup: func(id string) (DaemonWatch, error) {
			view := DaemonWatch{ID: id, PR: 7}
			subs := []DaemonSubscriber{{
				ID: "ses_orch", Role: RoleOrchestrator,
				Adapter: WakeDefaultAdapter, Target: "ses_orch",
			}}
			sub, err := DaemonWakeSubscriber(subs)
			if err != nil {
				return view, nil
			}
			view.Adapter, view.SubscriberID = sub.Adapter, sub.ID
			return view, nil
		},
		backend: adapter,
	}

	var wakeErr error
	logs := daemonLogs(func() {
		wakeErr = w.Wake(context.Background(), "w7", 2, VerdictRetry)
	})
	if wakeErr == nil {
		t.Fatal("an orchestrator-only Watch must not produce a wake")
	}
	if adapter.calls != 0 {
		t.Fatalf("Deliver called %d times for an orchestrator-only Watch, want 0", adapter.calls)
	}
	if lines := strings.Count(logs, "\n"); lines != 1 {
		t.Fatalf("logs = %q, want exactly one line", logs)
	}
}

// TestValidateFindingsKeepsFindingCitedInDiff: a finding whose cited file and
// evidence both appear in the reviewed diff is real and must survive.
func TestValidateFindingsKeepsFindingCitedInDiff(t *testing.T) {
	diff := "--- a/internal/app/orchestrator/daemon.go\n+func ValidateFindings(findings []Finding, diff string) FindingValidation {"
	f := Finding{
		ID:       "f-valid",
		Severity: "critical",
		File:     "internal/app/orchestrator/daemon.go",
		Evidence: "func ValidateFindings(findings []Finding, diff string) FindingValidation {",
		Summary:  "validator returns a filter summary",
	}

	v := ValidateFindings([]Finding{f}, diff)
	if len(v.Kept) != 1 || v.Kept[0] != f {
		t.Fatalf("kept = %+v, want the single cited finding", v.Kept)
	}
	if len(v.Dropped) != 0 {
		t.Fatalf("dropped = %+v, want none for a cited finding", v.Dropped)
	}
}

// TestValidateFindingsDropsInventedFinding: a finding whose citations are
// absent from the diff is fabricated and must be dropped with a reason.
func TestValidateFindingsDropsInventedFinding(t *testing.T) {
	f := Finding{ID: "f-invented", File: "does/not/exist.go", Evidence: "func nonexistent()", Summary: "invented issue"}
	diff := "--- a/real.go\n+package real"

	v := ValidateFindings([]Finding{f}, diff)
	if len(v.Kept) != 0 {
		t.Fatalf("kept = %+v, want an invented finding to be removed", v.Kept)
	}
	if len(v.Dropped) != 1 {
		t.Fatalf("dropped = %+v, want one rejection", v.Dropped)
	}
	if v.Dropped[0].Finding != f {
		t.Fatalf("dropped finding = %+v, want %+v", v.Dropped[0].Finding, f)
	}
	if v.Dropped[0].Reason != FindingDropNotInDiff {
		t.Fatalf("reason = %q, want %q", v.Dropped[0].Reason, FindingDropNotInDiff)
	}
}

// TestValidateFindingsEmptyList: no findings in, no findings out, and the
// result carries non-nil empty slices so a caller can range over them.
func TestValidateFindingsEmptyList(t *testing.T) {
	v := ValidateFindings(nil, "--- a/real.go\n+package real")
	if len(v.Kept) != 0 || len(v.Dropped) != 0 {
		t.Fatalf("empty input produced kept=%+v dropped=%+v", v.Kept, v.Dropped)
	}
	if v.Kept == nil || v.Dropped == nil {
		t.Fatalf("result slices must be non-nil, got kept=%v dropped=%v", v.Kept, v.Dropped)
	}
}

// TestValidateFindingsDropReason: every dropped finding carries a concrete,
// non-empty reason, and an uncited finding gets a different reason from one
// whose citation is simply absent.
func TestValidateFindingsDropReason(t *testing.T) {
	uncited := Finding{ID: "f-uncited", Summary: "vague claim with no evidence"}
	absent := Finding{ID: "f-absent", File: "ghost.go", Summary: "invented issue"}
	diff := "--- a/real.go\n+package real"

	v := ValidateFindings([]Finding{uncited, absent}, diff)
	if len(v.Kept) != 0 || len(v.Dropped) != 2 {
		t.Fatalf("kept=%+v dropped=%+v, want both findings dropped", v.Kept, v.Dropped)
	}
	reasons := make(map[string]string, len(v.Dropped))
	for _, d := range v.Dropped {
		if strings.TrimSpace(d.Reason) == "" {
			t.Fatalf("finding %s dropped without a reason", d.Finding.ID)
		}
		reasons[d.Finding.ID] = d.Reason
	}
	if reasons[uncited.ID] != FindingDropNoCitation {
		t.Fatalf("reason for %s = %q, want %q", uncited.ID, reasons[uncited.ID], FindingDropNoCitation)
	}
	if reasons[absent.ID] != FindingDropNotInDiff {
		t.Fatalf("reason for %s = %q, want %q", absent.ID, reasons[absent.ID], FindingDropNotInDiff)
	}
}

// TestValidateFindingsMixedListPreservesOrder: a mixed list keeps valid
// findings in their original order and reports only the invented ones.
func TestValidateFindingsMixedListPreservesOrder(t *testing.T) {
	diff := "--- a/keep.go\n+package keep"
	valid1 := Finding{ID: "a", File: "keep.go", Summary: "one"}
	invented := Finding{ID: "b", File: "phantom.go", Summary: "two"}
	valid2 := Finding{ID: "c", Evidence: "+package keep", Summary: "three"}

	v := ValidateFindings([]Finding{valid1, invented, valid2}, diff)
	if len(v.Kept) != 2 || v.Kept[0].ID != "a" || v.Kept[1].ID != "c" {
		t.Fatalf("kept = %+v, want findings a and c in order", v.Kept)
	}
	if len(v.Dropped) != 1 || v.Dropped[0].Finding.ID != "b" {
		t.Fatalf("dropped = %+v, want only finding b", v.Dropped)
	}
}

// TestValidateFindingsKeepsEvidenceOnlyFinding: a finding needs no File when
// its verbatim evidence appears in the diff.
func TestValidateFindingsKeepsEvidenceOnlyFinding(t *testing.T) {
	f := Finding{ID: "e", Evidence: "return err", Summary: "error path is untested"}
	v := ValidateFindings([]Finding{f}, "\treturn err\n")
	if len(v.Kept) != 1 || v.Kept[0] != f || len(v.Dropped) != 0 {
		t.Fatalf("evidence-only finding: kept=%+v dropped=%+v", v.Kept, v.Dropped)
	}
}

// TestDaemonWakerWithRouteUsesHook: an injected route receives the verdict
// pointer and keeps the dedup guarantee of the built-in delivery path.
func TestDaemonWakerWithRouteUsesHook(t *testing.T) {
	watch := DaemonWatch{ID: "w-route", PR: 9, Adapter: WakeDefaultAdapter, SubscriberID: "ses_route"}
	type call struct {
		watchID  string
		revision uint64
		verdict  string
		text     string
	}
	var calls []call
	route := func(_ context.Context, w DaemonWatch, rev uint64, verdict, text string) error {
		calls = append(calls, call{w.ID, rev, verdict, text})
		return nil
	}
	w := NewDaemonWakerWithRoute(func(string) (DaemonWatch, error) { return watch, nil }, route)

	if err := w.Wake(context.Background(), watch.ID, 4, VerdictRetry); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("route calls = %d, want 1", len(calls))
	}
	got := calls[0]
	if got.watchID != watch.ID || got.revision != 4 || got.verdict != VerdictRetry {
		t.Fatalf("route call = %+v", got)
	}
	if !strings.Contains(got.text, watch.ID) || !strings.Contains(got.text, VerdictRetry) {
		t.Fatalf("route text = %q, want watch and verdict identifiers", got.text)
	}

	// An unchanged revision must not reach the route a second time.
	if err := w.Wake(context.Background(), watch.ID, 4, VerdictRetry); err != nil {
		t.Fatalf("repeat Wake: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("route calls = %d after repeat, want 1", len(calls))
	}
}

// TestDaemonWakerWithRouteErrorStaysRetryable: a route error is returned and
// does not record a delivery, so the next pass may try again.
func TestDaemonWakerWithRouteErrorStaysRetryable(t *testing.T) {
	watch := DaemonWatch{ID: "w-err", PR: 3, Adapter: WakeDefaultAdapter, SubscriberID: "ses_err"}
	boom := errors.New("route refused")
	calls := 0
	route := func(context.Context, DaemonWatch, uint64, string, string) error {
		calls++
		return boom
	}
	w := NewDaemonWakerWithRoute(func(string) (DaemonWatch, error) { return watch, nil }, route)

	if err := w.Wake(context.Background(), watch.ID, 1, VerdictRetry); !errors.Is(err, boom) {
		t.Fatalf("Wake error = %v, want %v", err, boom)
	}
	if err := w.Wake(context.Background(), watch.ID, 1, VerdictRetry); !errors.Is(err, boom) {
		t.Fatalf("repeat Wake error = %v, want %v", err, boom)
	}
	if calls != 2 {
		t.Fatalf("route calls = %d, want 2 (a failed route is not delivered)", calls)
	}
}

// A blocked route must not hold the waker lock. A second Watch reaches its own
// delivery while the first route is still waiting (CC-03).
func TestDaemonWakerSlowRouteDoesNotBlockAnotherWatch(t *testing.T) {
	lookup := func(id string) (DaemonWatch, error) {
		return DaemonWatch{ID: id, PR: 1, Adapter: WakeDefaultAdapter, SubscriberID: "ses_" + id}, nil
	}
	slowEntered := make(chan struct{})
	release := make(chan struct{}, 1)
	unblock := func() {
		select {
		case release <- struct{}{}:
		default:
		}
	}
	defer unblock()
	route := func(_ context.Context, watch DaemonWatch, _ uint64, _, _ string) error {
		if watch.ID == "w-slow" {
			close(slowEntered)
			<-release
		}
		return nil
	}
	w := NewDaemonWakerWithRoute(lookup, route)

	slowDone := make(chan error, 1)
	go func() { slowDone <- w.Wake(context.Background(), "w-slow", 1, VerdictRetry) }()
	<-slowEntered

	fastDone := make(chan error, 1)
	go func() { fastDone <- w.Wake(context.Background(), "w-fast", 1, VerdictRetry) }()
	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatalf("wake for the other Watch: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a blocked route held the waker lock and stalled another Watch")
	}

	unblock()
	if err := <-slowDone; err != nil {
		t.Fatalf("wake for the slow Watch: %v", err)
	}
}

// Parallel passes for one obligation must reach the route once. The in-flight
// reservation covers the delivery window, and the delivered ledger covers the
// passes that arrive after it (CC-03).
func TestDaemonWakerConcurrentSameObligationDeliveredOnce(t *testing.T) {
	lookup := func(string) (DaemonWatch, error) {
		return DaemonWatch{ID: "w-parallel", PR: 11, Adapter: WakeDefaultAdapter, SubscriberID: "ses_parallel"}, nil
	}
	delivered := make(chan struct{}, 8)
	release := make(chan struct{}, 1)
	unblock := func() {
		select {
		case release <- struct{}{}:
		default:
		}
	}
	defer unblock()
	route := func(context.Context, DaemonWatch, uint64, string, string) error {
		delivered <- struct{}{}
		<-release
		return nil
	}
	w := NewDaemonWakerWithRoute(lookup, route)

	const n = 8
	start := make(chan struct{})
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			<-start
			results <- w.Wake(context.Background(), "w-parallel", 1, VerdictRetry)
		}()
	}
	close(start)

	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("no wake reached the route")
	}
	unblock()
	for i := 0; i < n; i++ {
		if err := <-results; err != nil {
			t.Fatalf("parallel Wake #%d: %v", i+1, err)
		}
	}
	if extra := len(delivered); extra != 0 {
		t.Fatalf("obligation was delivered %d times, want 1", 1+extra)
	}
}

// A Watch that stops receiving daemon passes is closed for delivery purposes.
// Once the replay window passes, its keys must leave the in-memory ledger while
// a Watch that still gets passes keeps its repeat suppression.
func TestDaemonWakerPrunesDeliveredKeysOfClosedWatch(t *testing.T) {
	base := time.Date(2026, time.November, 23, 12, 0, 0, 0, time.UTC)
	now := base
	watches := map[string]DaemonWatch{
		"w-closed": {ID: "w-closed", Adapter: WakeDefaultAdapter, SubscriberID: "ses_closed"},
		"w-live":   {ID: "w-live", Adapter: WakeDefaultAdapter, SubscriberID: "ses_live"},
	}
	adapter := &daemonRecordingAdapter{}
	w := &daemonWaker{
		lookup:  func(id string) (DaemonWatch, error) { return watches[id], nil },
		backend: adapter,
		clock:   func() time.Time { return now },
	}
	ctx := context.Background()
	for _, id := range []string{"w-closed", "w-live"} {
		if err := w.Wake(ctx, id, 1, VerdictRetry); err != nil {
			t.Fatalf("Wake(%s): %v", id, err)
		}
	}
	closedKey := obligationKey("w-closed", 1, "ses_closed")
	liveKey := obligationKey("w-live", 1, "ses_live")
	if !daemonWakeAlreadyExists(w, closedKey) || !daemonWakeAlreadyExists(w, liveKey) {
		t.Fatal("both obligations must be recorded before pruning")
	}

	// Only the live Watch gets another pass. The repeat is suppressed, but it
	// still proves the Watch is active.
	now = base.Add(time.Hour)
	if err := w.Wake(ctx, "w-live", 1, VerdictRetry); err != nil {
		t.Fatalf("repeat Wake(w-live): %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("Deliver calls = %d, want 2 for two obligations", adapter.calls)
	}

	now = base.Add(deliveredKeyRetention + 30*time.Minute)
	if removed := w.PruneDeliveredKeys(); removed != 1 {
		t.Fatalf("pruned keys = %d, want 1", removed)
	}
	if daemonWakeAlreadyExists(w, closedKey) {
		t.Fatal("closed Watch key survived the replay window")
	}
	if !daemonWakeAlreadyExists(w, liveKey) {
		t.Fatal("live Watch key was pruned")
	}
	if len(w.deliveredKeys) != 1 {
		t.Fatalf("deliveredKeys size = %d, want 1", len(w.deliveredKeys))
	}
	if again := w.PruneDeliveredKeys(); again != 0 {
		t.Fatalf("second prune removed %d keys, want 0", again)
	}
	if err := w.Wake(ctx, "w-live", 1, VerdictRetry); err != nil {
		t.Fatalf("post-prune Wake(w-live): %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("post-prune repeat delivered again: calls = %d, want 2", adapter.calls)
	}
}

// The ledger must not grow with the number of Watches that open, get one
// delivery and close in sequence.
func TestDaemonWakerDeliveredKeysStayBoundedAcrossClosedWatches(t *testing.T) {
	base := time.Date(2026, time.November, 23, 12, 0, 0, 0, time.UTC)
	now := base
	watches := map[string]DaemonWatch{}
	adapter := &daemonRecordingAdapter{}
	w := &daemonWaker{
		lookup:  func(id string) (DaemonWatch, error) { return watches[id], nil },
		backend: adapter,
		clock:   func() time.Time { return now },
	}
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("w-seq-%d", i)
		watches[id] = DaemonWatch{ID: id, Adapter: WakeDefaultAdapter, SubscriberID: "ses_" + id}
		if err := w.Wake(ctx, id, 1, VerdictRetry); err != nil {
			t.Fatalf("Wake(%s): %v", id, err)
		}
		if len(w.deliveredKeys) != 1 {
			t.Fatalf("after wake %d: deliveredKeys = %d, want 1", i, len(w.deliveredKeys))
		}
		now = now.Add(deliveredKeyRetention + time.Hour)
		w.PruneDeliveredKeys()
		if len(w.deliveredKeys) != 0 {
			t.Fatalf("after prune %d: deliveredKeys = %d, want 0", i, len(w.deliveredKeys))
		}
	}
	if adapter.calls != 6 {
		t.Fatalf("Deliver calls = %d, want 6", adapter.calls)
	}
}

// The daemon pass prunes through the optional Waker capability right after
// its close step. No composition-root change is needed for the hook to run.
func TestDaemonRunPrunesDeliveredKeysAfterCloseStep(t *testing.T) {
	base := time.Date(2026, time.November, 23, 12, 0, 0, 0, time.UTC)
	now := base
	adapter := &daemonRecordingAdapter{}
	w := &daemonWaker{
		lookup: func(string) (DaemonWatch, error) {
			return DaemonWatch{ID: "w-pass", Adapter: WakeDefaultAdapter, SubscriberID: "ses_pass"}, nil
		},
		backend: adapter,
		clock:   func() time.Time { return now },
	}
	ctx := context.Background()
	if err := w.Wake(ctx, "w-pass", 1, VerdictRetry); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	key := obligationKey("w-pass", 1, "ses_pass")
	if !daemonWakeAlreadyExists(w, key) {
		t.Fatal("obligation must be recorded before the daemon pass")
	}

	now = base.Add(deliveredKeyRetention + time.Hour)
	check := &daemonCheckAll{}
	orch := &daemonOrchestrator{custom: func(context.Context, Waker) error { return nil }}
	ports := daemonPorts(check, orch, &daemonCloser{}, w, nil)
	if err := DaemonRun(ctx, ports, time.Hour, true); err != nil {
		t.Fatalf("DaemonRun: %v", err)
	}
	if daemonWakeAlreadyExists(w, key) {
		t.Fatal("daemon pass did not prune the closed Watch key")
	}
}
