package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/pkg/core"
)

// The end-to-end harness always returns a session and always reports it alive,
// so a failed start never reaches the cleanup branch of handleStartFailure.
// This benchmark sends one Dispatch with 20 cards through a harness whose Spawn
// fails. Every failed start must leave one closed run, one Harness.Stop call and
// one Workspace.ForceRemove call. Those three writes are the limiter.
const (
	failureCards    = 20
	failureInFlight = 32
	failureAgents   = 32

	// nameDispatchFailure is the perf key for this benchmark.
	nameDispatchFailure = "dispatch-failure"

	failureLimiter = "cancel + Stop + ForceRemove IO after failed Spawn"
)

// failureHarness fails every Spawn. It counts the cleanup calls so a reader can
// check that one failed start produced exactly one Stop. A liveness check for a
// session this fixture did not create is counted as an error; the injected case
// is a dead session, reported as (false, nil).
type failureHarness struct {
	spawns      atomic.Int64
	stops       atomic.Int64
	liveness    atomic.Int64
	livenessErr atomic.Int64
}

func (h *failureHarness) Spawn(context.Context, core.SpawnRequest) (core.Session, error) {
	h.spawns.Add(1)
	return core.Session{}, errors.New("spawn exploded")
}

func (h *failureHarness) Wake(context.Context, string, core.WakeContext) error { return nil }

func (h *failureHarness) Stop(context.Context, string) error {
	h.stops.Add(1)
	return nil
}

func (h *failureHarness) IsAlive(_ context.Context, sessionID string) (bool, error) {
	h.liveness.Add(1)
	if strings.HasPrefix(sessionID, failureSessionPrefix) {
		return false, nil
	}
	h.livenessErr.Add(1)
	return true, fmt.Errorf("liveness check for unknown session %q", sessionID)
}

func (h *failureHarness) NotifyParent(context.Context, string, string) error { return nil }
func (h *failureHarness) Events() <-chan core.HarnessEvent                   { return nil }

const failureSessionPrefix = "sess-fail-cleanup-"

// failureStore decorates the store with the session reference a failed Spawn
// would otherwise leave unset. Dispatcher.handleStartFailure checks the session
// before it stops the worker, and startOne assigns SessionID only after a
// successful Spawn. Restoring the reference on Save keeps the whole cleanup
// path in the benchmark instead of skipping Stop on an empty ID.
type failureStore struct {
	*sqlite.Store
}

func (s *failureStore) Save(ctx context.Context, run *core.Run) error {
	if run.SessionID == "" {
		run.SessionID = failureSessionPrefix + string(run.ID)
	}
	return s.Store.Save(ctx, run)
}

// failureWorkspace counts ForceRemove calls. Create and the other operations
// come from the end-to-end fake, so no worktree is touched on disk.
type failureWorkspace struct {
	e2eWorkspace
	forced atomic.Int64
}

func (w *failureWorkspace) ForceRemove(ctx context.Context, ws core.Workspace) error {
	w.forced.Add(1)
	return w.e2eWorkspace.ForceRemove(ctx, ws)
}

// failureEnv holds one fresh store, harness and workspace per pass. The 20
// cards are seeded outside the timed section.
type failureEnv struct {
	ctx        context.Context
	store      *failureStore
	harness    *failureHarness
	workspace  *failureWorkspace
	dispatcher *app.Dispatcher
}

func newFailureEnv(b *testing.B) *failureEnv {
	b.Helper()
	store, err := sqlite.Open(filepath.Join(b.TempDir(), "failure.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	tasks := make([]core.Task, 0, failureCards)
	for i := 0; i < failureCards; i++ {
		id := core.TaskID(fmt.Sprintf("T-FAIL-%02d", i))
		tasks = append(tasks, core.Task{
			ID:            id,
			Title:         fmt.Sprintf("Start failure fixture %d", i),
			Body:          "A fixed card used by the dispatcher start-failure cleanup benchmark.",
			Priority:      100 - i,
			Status:        core.TaskAvailable,
			SourceRef:     fmt.Sprintf("docs/tasks/%s.md", id),
			Kind:          core.TaskKindTask,
			BaseBranch:    "main",
			RequireReview: core.ReviewModeLLM,
			WorkerAgent:   "worker",
			WorkerPrompt:  "Implement the task.",
			Scope:         []core.ScopePath{core.ScopeFile(fmt.Sprintf("bench/failure/%02d.go", i))},
		})
	}

	e := &failureEnv{
		ctx:       ctx,
		store:     &failureStore{Store: store},
		harness:   &failureHarness{},
		workspace: &failureWorkspace{},
	}
	e.dispatcher = &app.Dispatcher{
		Source:             &e2eSource{tasks: tasks},
		Planner:            app.NewPlanner(nil),
		Workspace:          e.workspace,
		Harness:            e.harness,
		Store:              e.store,
		Concurrency:        core.Concurrency{MaxInFlight: failureInFlight, MaxActiveAgents: failureAgents},
		MaxAttempts:        3,
		ParentSessionID:    "ses_bench",
		WorkerInstructions: "Operator instructions from the session.",
		Decisions:          app.DecisionLogger{},
	}
	return e
}

type failurePass struct {
	closed, stops, forced, spawns, failed, work, errs int64
}

// pass runs one Dispatch. Work is the three cleanup writes named by the
// limiter: closed runs, Stop calls and ForceRemove calls. Errs counts a Dispatch
// error, a malformed saved run, an unexpected liveness error and a Save error.
func (e *failureEnv) pass(b *testing.B) failurePass {
	b.Helper()
	var res failurePass

	out, err := e.dispatcher.Dispatch(e.ctx, failureCards)
	if err != nil {
		res.errs++
	}
	res.failed = int64(len(out.Failures))
	res.spawns = e.harness.spawns.Load()

	recent, err := e.store.ListRecent(e.ctx, failureCards*2)
	if err != nil {
		res.errs++
	} else {
		for _, run := range recent {
			if run.State == core.RunClosed && strings.HasPrefix(string(run.TaskID), "T-FAIL-") {
				res.closed++
			}
		}
	}

	res.stops = e.harness.stops.Load()
	res.forced = e.workspace.forced.Load()
	res.errs += e.harness.livenessErr.Load()
	res.work = res.closed + res.stops + res.forced
	return res
}

func printDispatchFailureReport(w io.Writer, r failurePass, passes int) {
	fmt.Fprintf(w, "dispatch failure cleanup: cards=%d passes=%d closed=%d stops=%d force=%d spawns=%d failed=%d\n",
		failureCards, passes, r.closed, r.stops, r.forced, r.spawns, r.failed)
	fmt.Fprintf(w, "dispatch failure cleanup: work=%d errs=%d limiter=%s\n", r.work, r.errs, failureLimiter)
}

// BenchmarkDispatchFailureCleanup runs one Dispatch with 20 cards whose Spawn
// fails. Every iteration uses a fresh store, harness and workspace. It reports
// closed runs, Stop calls and ForceRemove calls as work, and any unexpected
// Dispatch, Save, Cancel or liveness error as errs. Smoke mode skips the warm-up
// pass; fixture sizes and assertions are the same in both modes.
//
//	BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' -bench '^BenchmarkDispatchFailureCleanup$' -benchtime=1x -count=1 -benchmem ./internal/bench/
func BenchmarkDispatchFailureCleanup(b *testing.B) {
	previousLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(previousLog)
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	if !smokeMode() {
		warm := newFailureEnv(b)
		_ = warm.pass(b)
	}

	var sums failurePass
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		e := newFailureEnv(b)
		b.StartTimer()
		r := e.pass(b)
		b.StopTimer()

		sums.closed += r.closed
		sums.stops += r.stops
		sums.forced += r.forced
		sums.spawns += r.spawns
		sums.failed += r.failed
		sums.work += r.work
		sums.errs += r.errs

		if r.failed != failureCards {
			b.Fatalf("failed starts = %d, want %d", r.failed, failureCards)
		}
		if r.spawns != failureCards {
			b.Fatalf("spawn calls = %d, want %d", r.spawns, failureCards)
		}
		if r.closed != failureCards {
			b.Fatalf("closed runs = %d, want %d; every failed start must be closed", r.closed, failureCards)
		}
		if r.stops != failureCards {
			b.Fatalf("Stop calls = %d, want %d; every failed start must stop its worker", r.stops, failureCards)
		}
		if r.forced != failureCards {
			b.Fatalf("ForceRemove calls = %d, want %d; every failed start must drop its worktree", r.forced, failureCards)
		}
		if r.errs != 0 {
			b.Fatalf("dispatch, Save, Cancel and liveness errors = %d, want zero", r.errs)
		}
	}

	b.ReportMetric(float64(sums.work), nameDispatchFailure+"-work")
	b.ReportMetric(float64(sums.errs), nameDispatchFailure+"-errs")
	b.ReportMetric(float64(sums.closed), nameDispatchFailure+"-closed")
	b.ReportMetric(float64(sums.stops), nameDispatchFailure+"-stops")
	b.ReportMetric(float64(sums.forced), nameDispatchFailure+"-force-removes")
	b.ReportMetric(float64(sums.spawns), nameDispatchFailure+"-spawns")
	b.ReportMetric(float64(sums.failed), nameDispatchFailure+"-failed-starts")
	printDispatchFailureReport(os.Stdout, sums, b.N)

	if b.N == 0 {
		b.Fatal("benchmark ran zero iterations")
	}
}
