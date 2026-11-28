package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// guardNotifier records operator notices without a transport.
type guardNotifier struct {
	mu    sync.Mutex
	notes []core.Notification
	err   error
}

func (n *guardNotifier) Notify(_ context.Context, note core.Notification) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.err != nil {
		return n.err
	}
	n.notes = append(n.notes, note)
	return nil
}

func (n *guardNotifier) snapshot() []core.Notification {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]core.Notification(nil), n.notes...)
}

// guardDispatchPort records the budget handed to an automatic dispatch.
type guardDispatchPort struct {
	mu        sync.Mutex
	available int
	calls     []int
	result    DispatchResult
}

func (p *guardDispatchPort) Available(context.Context) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.available
}

func (p *guardDispatchPort) Dispatch(_ context.Context, n int) (DispatchResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, n)
	return p.result, nil
}

func (p *guardDispatchPort) budgets() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.calls...)
}

// guardPromoter reports that a tick reached promotion. Waiting for two reports
// proves the first tick finished its post-promotion work before the check.
type guardPromoter struct {
	ticks chan struct{}
}

func (p *guardPromoter) PromotePending(context.Context) ([]*core.Run, error) {
	select {
	case p.ticks <- struct{}{}:
	default:
	}
	return nil, nil
}

// guardStartPromoter starts one pending run on the first tick, as the real
// promoter does, so the following auto dispatch sees the task as active.
type guardStartPromoter struct {
	store *fakeStore
	ticks chan struct{}
	once  sync.Once
}

func (p *guardStartPromoter) PromotePending(ctx context.Context) ([]*core.Run, error) {
	p.once.Do(func() {
		run := core.NewRun("run-promoted", "T-PROMO", time.Now().UTC())
		run.State = core.RunRunning
		_ = p.store.Save(ctx, run)
	})
	select {
	case p.ticks <- struct{}{}:
	default:
	}
	return nil, nil
}

// runGuardTicks starts a reconciler with a short interval and returns a stop
// function. The caller must stop the loop before reading sink or store state,
// so assertions never race with a later tick.
func runGuardTicks(t *testing.T, r *Reconciler, ticks <-chan struct{}, n int) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, make(chan int)) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Errorf("Run error = %v, want context.Canceled", err)
			}
		})
	}
	t.Cleanup(stop)
	for i := 0; i < n; i++ {
		select {
		case <-ticks:
		case <-time.After(time.Second):
			t.Fatal("reconciler tick did not reach promotion")
		}
	}
	// Let the loop finish the work after the last report before it is stopped.
	select {
	case <-ticks:
	case <-time.After(50 * time.Millisecond):
	}
	return stop
}

func TestAutoDispatchDisabledByDefault(t *testing.T) {
	port := &guardDispatchPort{available: 3}
	promoter := &guardPromoter{ticks: make(chan struct{}, 4)}
	r := &Reconciler{
		Store: newFakeStore(), Interval: time.Millisecond,
		Promoter: promoter, Dispatch: port,
	}
	stop := runGuardTicks(t, r, promoter.ticks, 2)
	stop()
	if calls := port.budgets(); len(calls) != 0 {
		t.Fatalf("dispatch budgets = %v, want no calls while disabled", calls)
	}
}

func TestAutoDispatchBrakeFreezesIntakeOnly(t *testing.T) {
	port := &guardDispatchPort{available: 3}
	promoter := &guardPromoter{ticks: make(chan struct{}, 4)}
	r := &Reconciler{
		Store: newFakeStore(), Interval: time.Millisecond,
		Promoter: promoter, Dispatch: port,
		AutoDispatchEnabled: true, DispatchOpen: false,
	}
	stop := runGuardTicks(t, r, promoter.ticks, 2)
	stop()
	if calls := port.budgets(); len(calls) != 0 {
		t.Fatalf("dispatch budgets = %v, want no calls with dispatch_open false", calls)
	}
}

func TestAutoDispatchStartsUpToBudget(t *testing.T) {
	sink := &memoryDecisionSink{}
	port := &guardDispatchPort{
		available: 3,
		result: DispatchResult{
			Runs:    []*core.Run{core.NewRun("r-started", "T-A", time.Now().UTC())},
			Skipped: []SkippedTask{{TaskID: "T-B", Reason: "overlap"}},
		},
	}
	promoter := &guardPromoter{ticks: make(chan struct{}, 4)}
	r := &Reconciler{
		Store: newFakeStore(), Interval: time.Millisecond,
		Promoter: promoter, Dispatch: port,
		AutoDispatchEnabled: true, DispatchOpen: true,
		Decisions: DecisionLogger{Sink: sink},
	}
	stop := runGuardTicks(t, r, promoter.ticks, 2)
	stop()
	calls := port.budgets()
	if len(calls) == 0 || calls[len(calls)-1] != 3 {
		t.Fatalf("dispatch budgets = %v, want the three free slots", calls)
	}
	var auto *Decision
	for i := range sink.decisions {
		if sink.decisions[i].Kind == "dispatch.auto" {
			auto = &sink.decisions[i]
		}
	}
	if auto == nil {
		t.Fatalf("decisions = %+v, want a dispatch.auto row", sink.decisions)
	}
	if auto.Fields["started"] != 1 || auto.Fields["skipped"] != 1 || auto.Fields["budget"] != 3 {
		t.Fatalf("dispatch.auto fields = %+v, want started 1 skipped 1 budget 3", auto.Fields)
	}
}

func TestAutoDispatchBatchSizeCapsBudget(t *testing.T) {
	port := &guardDispatchPort{available: 3}
	promoter := &guardPromoter{ticks: make(chan struct{}, 4)}
	r := &Reconciler{
		Store: newFakeStore(), Interval: time.Millisecond,
		Promoter: promoter, Dispatch: port,
		AutoDispatchEnabled: true, DispatchOpen: true, AutoDispatchBatchSize: 2,
	}
	stop := runGuardTicks(t, r, promoter.ticks, 2)
	stop()
	calls := port.budgets()
	if len(calls) == 0 || calls[len(calls)-1] != 2 {
		t.Fatalf("dispatch budgets = %v, want the batch size 2", calls)
	}
}

func TestAutoDispatchDoesNotRestartPromotedTask(t *testing.T) {
	store := newFakeStore()
	task := core.Task{
		ID: "T-PROMO", Status: core.TaskAvailable,
		Scope: []core.ScopePath{core.ScopeFile("promo.go")},
	}
	store.ready = []core.Task{task}
	disp := &Dispatcher{
		Source: &fakeSource{tasks: []core.Task{task}}, Planner: NewPlanner(nil),
		Workspace: &fakeWorkspace{}, Harness: &fakeHarness{}, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 4, MaxActiveAgents: 2},
	}
	promoter := &guardStartPromoter{store: store, ticks: make(chan struct{}, 4)}
	r := &Reconciler{
		Store: store, Interval: time.Millisecond,
		Promoter: promoter, Dispatch: disp,
		AutoDispatchEnabled: true, DispatchOpen: true,
	}
	stop := runGuardTicks(t, r, promoter.ticks, 2)
	stop()

	var started int
	for _, run := range store.runs {
		if run.TaskID == "T-PROMO" {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("active runs for T-PROMO = %d, want 1", started)
	}
}

func agedRun(id core.RunID, state core.RunState, reason string) *core.Run {
	run := makeRun(id, 7, state)
	run.TaskID = "T-STUCK"
	run.Reason = reason
	run.UpdatedAt = time.Now().UTC().Add(-2 * time.Hour)
	return run
}

func guardWatchdog(store *fakeStore, notifier *guardNotifier, sink *memoryDecisionSink) *Reconciler {
	return &Reconciler{
		Store: store, Notifier: notifier,
		WatchdogEnabled: true,
		WatchdogAges:    map[core.RunState]time.Duration{core.RunWaitingCI: time.Hour},
		Decisions:       DecisionLogger{Sink: sink},
	}
}

func TestStateAgeDoesNotAlertUnderDefaultOrDisabled(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		notifier := &guardNotifier{}
		sink := &memoryDecisionSink{}
		r := guardWatchdog(newFakeStore(), notifier, sink)
		r.WatchdogEnabled = false
		run := agedRun("r-quiet", core.RunWaitingCI, "checks pending")
		r.checkStateAges(context.Background(), []*core.Run{run})
		if len(notifier.snapshot()) != 0 || len(sink.decisions) != 0 {
			t.Fatal("watchdog alerted while disabled")
		}
	})
	t.Run("young", func(t *testing.T) {
		notifier := &guardNotifier{}
		sink := &memoryDecisionSink{}
		r := guardWatchdog(newFakeStore(), notifier, sink)
		run := agedRun("r-young", core.RunWaitingCI, "checks pending")
		run.UpdatedAt = time.Now().UTC().Add(-time.Minute)
		r.checkStateAges(context.Background(), []*core.Run{run})
		if len(notifier.snapshot()) != 0 || len(sink.decisions) != 0 {
			t.Fatal("watchdog alerted before the configured age")
		}
	})
}

func TestStateAgeAlertsOnceUntilRunLeavesState(t *testing.T) {
	ctx := context.Background()
	notifier := &guardNotifier{}
	sink := &memoryDecisionSink{}
	r := guardWatchdog(newFakeStore(), notifier, sink)
	run := agedRun("r-stuck", core.RunWaitingCI, "checks pending")
	runs := []*core.Run{run}

	r.checkStateAges(ctx, runs)
	notes := notifier.snapshot()
	if len(notes) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notes))
	}
	for _, want := range []string{"r-stuck", "T-STUCK", "waiting_ci", "checks pending"} {
		if !strings.Contains(notes[0].Body, want) {
			t.Errorf("notification body %q missing %q", notes[0].Body, want)
		}
	}
	if len(sink.decisions) != 1 || sink.decisions[0].Kind != "watchdog.state_age" {
		t.Fatalf("decisions = %+v, want one watchdog.state_age row", sink.decisions)
	}
	if sink.decisions[0].Fields["age"] == nil || sink.decisions[0].Fields["limit"] != "1h0m0s" {
		t.Fatalf("age fields = %+v, want the spent age and one hour limit", sink.decisions[0].Fields)
	}

	r.checkStateAges(ctx, runs)
	if len(notifier.snapshot()) != 1 || len(sink.decisions) != 1 {
		t.Fatal("next tick repeated the age alert")
	}
}

func TestStateAgeAlertRearmsAfterReturnToState(t *testing.T) {
	ctx := context.Background()
	notifier := &guardNotifier{}
	sink := &memoryDecisionSink{}
	r := guardWatchdog(newFakeStore(), notifier, sink)
	run := agedRun("r-cycle", core.RunWaitingCI, "checks pending")
	runs := []*core.Run{run}

	r.checkStateAges(ctx, runs)
	if len(notifier.snapshot()) != 1 {
		t.Fatalf("first alert count = %d, want 1", len(notifier.snapshot()))
	}

	if err := run.Apply(core.Event{Kind: core.EvBehind}); err != nil {
		t.Fatalf(`run.Apply(core.Event{Kind: core.EvBehind}): %v`, err)
	}
	run.UpdatedAt = time.Now().UTC()
	r.checkStateAges(ctx, runs)

	if err := run.Apply(core.Event{Kind: core.EvChecksPassed}); err != nil {
		t.Fatalf(`run.Apply(core.Event{Kind: core.EvChecksPassed}): %v`, err)
	}
	run.UpdatedAt = time.Now().UTC().Add(-2 * time.Hour)
	r.checkStateAges(ctx, runs)

	if got := len(notifier.snapshot()); got != 2 {
		t.Fatalf("notifications after return = %d, want 2", got)
	}
	if got := len(sink.decisions); got != 2 {
		t.Fatalf("watchdog decisions after return = %d, want 2", got)
	}
}

func TestStateAgeClaimReleasedOnFailedSend(t *testing.T) {
	ctx := context.Background()
	notifier := &guardNotifier{err: errors.New("notify down")}
	store := newFakeStore()
	r := guardWatchdog(store, notifier, &memoryDecisionSink{})
	run := agedRun("r-retry", core.RunWaitingCI, "checks pending")

	r.checkStateAges(ctx, []*core.Run{run})
	key := watchdogDeliveryKey(run.ID, run.State)
	if store.deliveries[key] {
		t.Fatal("failed notification left a delivery claim")
	}

	notifier.err = nil
	r.checkStateAges(ctx, []*core.Run{run})
	if got := len(notifier.snapshot()); got != 1 {
		t.Fatalf("notifications after retry = %d, want 1", got)
	}
}
