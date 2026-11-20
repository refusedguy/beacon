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

	"github.com/refusedguy/beacon/internal/infra/wake"
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
}

func (d *daemonCloser) run(ctx context.Context) error {
	d.calls++
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
		CloseMerged: closer.run,
		Waker:       waker,
		Log:         logf,
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

// daemonRecordingAdapter counts deliveries at the same T210 boundary the real
// daemon adapter uses, so a stale target cannot slip past the test unnoticed.
type daemonRecordingAdapter struct {
	calls   int
	targets []string
}

func (a *daemonRecordingAdapter) Bind(p wake.T210WakePointer, _ *wake.T211SessionRegistry) (wake.T210Target, bool) {
	if err := wake.ValidateT210Target(p); err != nil {
		return "", false
	}
	return wake.T210Target(p.Target), true
}

func (a *daemonRecordingAdapter) Deliver(p wake.T210WakePointer, target wake.T210Target) (wake.T210Receipt, error) {
	a.calls++
	a.targets = append(a.targets, string(target))
	return wake.T210Receipt{
		ObligationID: p.ObligationID,
		Attempt:      p.Attempt,
		Delivery:     p.Delivery,
		Outcome:      wake.T210Delivered,
	}, nil
}

func daemonTestWaker(watch DaemonWatch, adapter *daemonRecordingAdapter) *daemonWaker {
	adapters := wake.T211NewAdapterRegistry()
	adapters.Register(wake.AdapterOpenCode, adapter)
	return &daemonWaker{
		lookup:   func(string) (DaemonWatch, error) { return watch, nil },
		adapters: adapters,
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
		ID: "w1", PR: 42, Adapter: wake.AdapterOpenCode,
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
		ID: "w2", PR: 7, Adapter: wake.AdapterOpenCode,
		SubscriberID: "ses_abc123",
	}, adapter)

	if err := w.Wake(context.Background(), "w2", 4, VerdictRetry); err != nil {
		t.Fatalf("valid subscriber must be delivered: %v", err)
	}
	if adapter.calls != 1 || !reflect.DeepEqual(adapter.targets, []string{"ses_abc123"}) {
		t.Fatalf("Deliver calls = %d, targets = %q, want one ses_abc123", adapter.calls, adapter.targets)
	}
}

func TestDaemonWakerWithoutSubscriberIsError(t *testing.T) {
	adapter := &daemonRecordingAdapter{}
	w := daemonTestWaker(DaemonWatch{ID: "w3", PR: 8, Adapter: wake.AdapterOpenCode}, adapter)

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
		ID: "beacon-ci-checkall", Role: RoleExecutor, Adapter: wake.AdapterOpenCode,
		Target: "beacon-ci-checkall",
	}
	orchestratorSub := DaemonSubscriber{
		ID: "ses_orch", Role: RoleOrchestrator, Adapter: wake.AdapterOpenCode,
		Target: "ses_orch",
	}
	executorSub := DaemonSubscriber{
		ID: "ses_exec", Role: RoleExecutor, Adapter: wake.AdapterOpenCode,
		Target: "ses_exec",
	}

	got, err := DaemonWakeSubscriber([]DaemonSubscriber{legacy, orchestratorSub})
	if err != nil || got.ID != "ses_orch" {
		t.Fatalf("legacy executor must be skipped: got %+v, err %v", got, err)
	}
	got, err = DaemonWakeSubscriber([]DaemonSubscriber{orchestratorSub, executorSub})
	if err != nil || got.ID != "ses_exec" {
		t.Fatalf("executor must win among valid subscribers: got %+v, err %v", got, err)
	}
	got, err = DaemonWakeSubscriber([]DaemonSubscriber{
		legacy,
		{ID: "beacon-daemon-checkall", Role: RoleOrchestrator, Target: "beacon-daemon-checkall"},
	})
	if err == nil || got.ID != "" {
		t.Fatalf("all-stale subscribers must be refused: got %+v, err %v", got, err)
	}
	got, err = DaemonWakeSubscriber([]DaemonSubscriber{
		{ID: "ses_ok", Target: "beacon-ci-checkall"},
	})
	if err == nil || got.ID != "" {
		t.Fatalf("a stale target must be refused too: got %+v, err %v", got, err)
	}
}
