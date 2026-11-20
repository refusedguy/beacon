package orchestrator

import (
	"context"
	"errors"
	"fmt"
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
