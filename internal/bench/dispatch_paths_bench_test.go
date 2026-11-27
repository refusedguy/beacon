package bench

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/pkg/core"
)

// This benchmark covers the two dispatch paths the end-to-end fixture never
// reaches. Its store starts with pending runs, so PromotePending has queued work,
// and two Dispatch calls overlap inside Spawn, so their slot reservations coexist.
// The harness parks the first four spawns behind a barrier and samples the free
// agent slots from Dispatcher.Available while the reservations are held. That
// read is the limiter: ListActive under slotsMu plus the reservation map.
const (
	slotsPendingRuns = 20
	slotsFreeTasks   = 4
	slotsMaxInFlight = 40
	slotsMaxAgents   = 4
	slotsPerDispatch = 2
	slotsTimeout     = 5 * time.Second

	slotsLimiter = "ListActive reads under slotsMu + reservation map"
)

// slotsHarness parks the first four spawns so two dispatch batches hold their
// reservations at the same time. It records every start by task, which makes a
// double start visible, and it reads the free agent slots from inside Spawn
// while the reservations are live.
type slotsHarness struct {
	arrived    chan struct{}
	release    chan struct{}
	park       atomic.Bool
	mu         sync.Mutex
	starts     map[core.TaskID]int
	doubles    int64
	peak       int64
	freeTotal  int64
	freeCount  int64
	dispatcher *app.Dispatcher
}

func newSlotsHarness() *slotsHarness {
	h := &slotsHarness{
		arrived: make(chan struct{}, slotsMaxAgents*2),
		release: make(chan struct{}),
		starts:  make(map[core.TaskID]int),
	}
	h.park.Store(true)
	return h
}

func (h *slotsHarness) Spawn(_ context.Context, req core.SpawnRequest) (core.Session, error) {
	h.mu.Lock()
	h.starts[req.Task.ID]++
	if h.starts[req.Task.ID] > 1 {
		h.doubles++
	}
	h.mu.Unlock()

	if h.dispatcher != nil {
		free := h.dispatcher.Available(context.Background())
		h.mu.Lock()
		h.freeTotal += int64(free)
		h.freeCount++
		if reserved := int64(slotsMaxAgents - free); reserved > h.peak {
			h.peak = reserved
		}
		h.mu.Unlock()
	}

	if h.park.Load() {
		h.arrived <- struct{}{}
		<-h.release
	}
	return core.Session{ID: "sess-" + string(req.RunID)}, nil
}

func (h *slotsHarness) Wake(context.Context, string, core.WakeContext) error { return nil }
func (h *slotsHarness) Stop(context.Context, string) error                   { return nil }
func (h *slotsHarness) IsAlive(context.Context, string) (bool, error)        { return true, nil }
func (h *slotsHarness) NotifyParent(context.Context, string, string) error   { return nil }
func (h *slotsHarness) Events() <-chan core.HarnessEvent                     { return nil }

func (h *slotsHarness) startCount() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	var n int64
	for _, c := range h.starts {
		n += int64(c)
	}
	return n
}

func (h *slotsHarness) doubleCount() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.doubles
}

func (h *slotsHarness) peakReservations() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.peak
}

func (h *slotsHarness) meanFreeSlots() float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.freeCount == 0 {
		return 0
	}
	return float64(h.freeTotal) / float64(h.freeCount)
}

// slotsEnv holds one fresh store and dispatcher per benchmark pass. Pending
// runs and their cards are seeded outside the timed section.
type slotsEnv struct {
	ctx        context.Context
	store      *sqlite.Store
	dispatcher *app.Dispatcher
	harness    *slotsHarness
	runs       []*core.Run
	tasks      []core.Task
}

func newSlotsEnv(b *testing.B) *slotsEnv {
	b.Helper()
	store, err := sqlite.Open(filepath.Join(b.TempDir(), "slots.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	e := &slotsEnv{ctx: ctx, store: store, harness: newSlotsHarness()}

	for i := 0; i < slotsPendingRuns; i++ {
		id := core.TaskID(fmt.Sprintf("T-PEND-%02d", i))
		task := core.Task{
			ID: id, Title: fmt.Sprintf("Pending fixture %d", i),
			Body:     "A fixed pending card used by the dispatch slot benchmark.",
			Priority: 100 - i, Status: core.TaskAvailable,
			SourceRef: fmt.Sprintf("docs/tasks/%s.md", id),
			Kind:      core.TaskKindTask, BaseBranch: "main",
			RequireReview: core.ReviewModeLLM, WorkerAgent: "worker",
			WorkerPrompt: "Implement the task.",
			Scope:        []core.ScopePath{core.ScopeFile(fmt.Sprintf("bench/pending/%02d.go", i))},
		}
		e.tasks = append(e.tasks, task)
		run := core.NewRun(core.RunID(fmt.Sprintf("run-pend-%02d", i)), id, now)
		run.State = core.RunPending
		run.Scope = task.Scope
		if err := store.Save(ctx, run); err != nil {
			b.Fatal(err)
		}
		e.runs = append(e.runs, run)
	}
	for i := 0; i < slotsFreeTasks; i++ {
		id := core.TaskID(fmt.Sprintf("T-FREE-%02d", i))
		e.tasks = append(e.tasks, core.Task{
			ID: id, Title: fmt.Sprintf("Free fixture %d", i),
			Body:     "A fixed free card used by the dispatch slot benchmark.",
			Priority: 200 - i, Status: core.TaskAvailable,
			SourceRef: fmt.Sprintf("docs/tasks/%s.md", id),
			Kind:      core.TaskKindTask, BaseBranch: "main",
			RequireReview: core.ReviewModeLLM, WorkerAgent: "worker",
			WorkerPrompt: "Implement the task.",
			Scope:        []core.ScopePath{core.ScopeFile(fmt.Sprintf("bench/free/%02d.go", i))},
		})
	}

	e.dispatcher = &app.Dispatcher{
		Source:             &e2eSource{tasks: e.tasks},
		Planner:            app.NewPlanner(nil),
		Workspace:          e2eWorkspace{},
		Harness:            e.harness,
		Store:              store,
		Concurrency:        core.Concurrency{MaxInFlight: slotsMaxInFlight, MaxActiveAgents: slotsMaxAgents},
		MaxAttempts:        3,
		ParentSessionID:    "ses_bench",
		WorkerInstructions: "Operator instructions from the session.",
		Decisions:          app.DecisionLogger{},
	}
	e.harness.dispatcher = e.dispatcher
	return e
}

type slotsPass struct {
	starts       int64
	doubles      int64
	peak         int64
	freeSlots    float64
	promoted     int64
	activeAgents int64
	activeRuns   int64
	errs         int64
	work         int64
}

// pass runs two overlapping Dispatch calls, then promotes queued runs after
// the dispatched workers move out of the agent pool. Counts are collected for
// the whole pass so a reader can check them against the fixture sizes.
func (e *slotsEnv) pass(b *testing.B) slotsPass {
	b.Helper()
	var res slotsPass

	type outcome struct {
		runs []*core.Run
		err  error
	}
	results := make([]outcome, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := e.dispatcher.Dispatch(e.ctx, slotsPerDispatch)
			results[i] = outcome{runs: r.Runs, err: err}
		}(i)
	}

	wantArrivals := 2 * slotsPerDispatch
	arrived, timedOut := 0, false
	for arrived < wantArrivals {
		select {
		case <-e.harness.arrived:
			arrived++
		case <-time.After(slotsTimeout):
			timedOut = true
			arrived = wantArrivals
		}
	}
	close(e.harness.release)
	wg.Wait()
	e.harness.park.Store(false)
	if timedOut {
		b.Fatal("overlapping dispatches did not reach Spawn together")
	}

	for _, o := range results {
		if o.err != nil {
			res.errs++
		}
	}

	// Registering a PR moves a running run to waiting_ci, which frees its
	// agent slot without ending the run. PromotePending can then claim it.
	for _, o := range results {
		for _, run := range o.runs {
			if err := run.Apply(core.Event{Kind: core.EvPRRegistered, PRNumber: 4000}); err != nil {
				res.errs++
				continue
			}
			if err := e.store.Save(e.ctx, run); err != nil {
				res.errs++
			}
		}
	}

	promoted, err := e.dispatcher.PromotePending(e.ctx)
	if err != nil {
		res.errs++
	}
	res.promoted = int64(len(promoted))

	res.starts = e.harness.startCount()
	res.doubles = e.harness.doubleCount()
	res.peak = e.harness.peakReservations()
	res.freeSlots = e.harness.meanFreeSlots()
	res.work = res.starts

	active, err := e.store.ListActive(e.ctx)
	if err != nil {
		res.errs++
	} else {
		res.activeRuns = int64(len(active))
		for _, run := range active {
			if run.State.ActiveAgent() {
				res.activeAgents++
			}
		}
	}
	return res
}

func printDispatchSlotsReport(w io.Writer, r slotsPass, passes int) {
	fmt.Fprintf(w, "dispatch promotion: pending=%d free_tasks=%d passes=%d\n",
		slotsPendingRuns, slotsFreeTasks, passes)
	fmt.Fprintln(w, "path                 starts  double  peak  free  promoted  agents  active  work  errs  limiter")
	fmt.Fprintf(w, "%-20s %7d %7d %5d %5.1f %9d %7d %7d %5d %5d  %s\n",
		"promotion-slots", r.starts, r.doubles, r.peak, r.freeSlots, r.promoted,
		r.activeAgents, r.activeRuns, r.work, r.errs, slotsLimiter)
}

// BenchmarkDispatchPromotionSlots runs one pass per iteration. Each pass builds
// a fresh store, overlaps two Dispatch calls inside Spawn, and then promotes
// queued runs. Work, errors and the named limiter are reported in stdout and as
// metrics so CI can read them. Smoke mode skips the warm-up pass; fixture sizes
// and assertions stay the same.
//
//	BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' -bench '^BenchmarkDispatchPromotionSlots$' -benchtime=1x -count=1 -benchmem ./internal/bench/
func BenchmarkDispatchPromotionSlots(b *testing.B) {
	previousLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(previousLog)
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	if !smokeMode() {
		warm := newSlotsEnv(b)
		_ = warm.pass(b)
	}

	var sums slotsPass
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		e := newSlotsEnv(b)
		b.StartTimer()
		r := e.pass(b)
		b.StopTimer()

		sums.starts += r.starts
		sums.doubles += r.doubles
		sums.peak += r.peak
		sums.freeSlots += r.freeSlots
		sums.promoted += r.promoted
		sums.activeAgents += r.activeAgents
		sums.activeRuns += r.activeRuns
		sums.errs += r.errs
		sums.work += r.work

		wantStarts := int64(2*slotsPerDispatch + slotsMaxAgents)
		if r.starts != wantStarts {
			b.Fatalf("starts = %d, want %d; the fixture is not stable", r.starts, wantStarts)
		}
		if r.promoted != slotsMaxAgents {
			b.Fatalf("promoted = %d, want %d; promotion is not filling the freed slots", r.promoted, slotsMaxAgents)
		}
		if r.doubles != 0 {
			b.Fatalf("double starts = %d, want zero; a task was started twice", r.doubles)
		}
		if r.errs != 0 {
			b.Fatalf("dispatch and promotion errors = %d, want zero", r.errs)
		}
		if r.peak < 2 || r.peak > slotsMaxAgents {
			b.Fatalf("peak reservations = %d, want between 2 and %d", r.peak, slotsMaxAgents)
		}
		if r.activeAgents != slotsMaxAgents {
			b.Fatalf("active agents = %d, want %d; agent cap does not hold", r.activeAgents, slotsMaxAgents)
		}
		if r.activeRuns > slotsMaxInFlight {
			b.Fatalf("active runs = %d, want at most %d; in-flight cap does not hold", r.activeRuns, slotsMaxInFlight)
		}
	}

	b.ReportMetric(float64(sums.starts), "dispatch-starts")
	b.ReportMetric(float64(sums.doubles), "dispatch-double-starts")
	b.ReportMetric(float64(sums.peak), "dispatch-reservations-peak")
	b.ReportMetric(sums.freeSlots, "dispatch-free-slots")
	b.ReportMetric(float64(sums.promoted), "dispatch-promoted")
	b.ReportMetric(float64(sums.work), "dispatch-work")
	b.ReportMetric(float64(sums.errs), "dispatch-errs")
	printDispatchSlotsReport(os.Stdout, sums, b.N)
}
