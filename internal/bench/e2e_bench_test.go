package bench

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/pkg/core"
)

// The end-to-end harness measures the two operator paths that the micro
// dossier cannot see: one reconcile tick over active pull requests and one
// dispatch call from tool entry to the prompt handed to the harness.
//
// Fixtures are fixed and seeded. The SQLite store is real; the SCM and the
// agent harness are fakes, so no network or subprocess work is timed. A fresh
// state database is built for every sample, outside the timed section, so work
// counts do not drift as run and decision rows accumulate.
const (
	e2eSeed          = 20261125
	e2eActivePRs     = 20
	e2eDispatchTasks = 4

	pathReconcile = "reconcile-tick"
	pathDispatch  = "dispatch-prompt"

	reconcileLimiter = "SQLite Save + decision writes"
	dispatchLimiter  = "SQLite run + task status writes"
)

// e2eSCM is a fake provider. Every observation carries a fresh head SHA so a
// waiting run persists its observation on each pass and the work per sample
// stays constant. The decision log in this harness deliberately does not
// deduplicate, for the same reason.
type e2eSCM struct{ seq atomic.Uint64 }

func (s *e2eSCM) Observe(_ context.Context, pr int) (core.Observation, error) {
	n := s.seq.Add(1)
	return core.Observation{
		PRNumber: pr,
		HeadSHA:  fmt.Sprintf("%040x", n),
		BaseSHA:  fmt.Sprintf("%040x", n/2+1),
		PRState:  "open",
		Checks:   []core.CheckResult{{Name: "go test", State: core.CheckPending}},
	}, nil
}

func (s *e2eSCM) UpdateBranch(context.Context, int, string) (string, error) { return "", nil }
func (s *e2eSCM) Merge(context.Context, int, string, string) error          { return nil }
func (s *e2eSCM) BaseBranchStatus(context.Context, string) (core.BaseStatus, error) {
	return core.BaseStatus{}, nil
}
func (s *e2eSCM) FindOpenPRByBranch(context.Context, string) (int, error) { return 0, nil }
func (s *e2eSCM) OpenPR(context.Context, string, string, string) (int, error) {
	return 0, nil
}

// e2ePolicy holds every PR in wait, the path an operator sees most often. It
// keeps the observation and decision writes in the tick without merge,
// rebase, wake or incident work.
type e2ePolicy struct{}

func (e2ePolicy) Evaluate(core.Observation) core.MergeDecision {
	return core.MergeDecision{Verdict: core.VerdictWait, Reason: "checks pending", Criterion: "invariant"}
}

// e2eHarness captures the prompt from Spawn. Capturing the request is the
// tool-call-to-prompt endpoint: the MCP wrapper calls Dispatcher.Dispatch and
// does not touch the prompt.
type e2eHarness struct {
	mu      sync.Mutex
	prompts []string
	events  chan core.HarnessEvent
}

func newE2EHarness() *e2eHarness {
	return &e2eHarness{events: make(chan core.HarnessEvent, 16)}
}

func (h *e2eHarness) Spawn(_ context.Context, req core.SpawnRequest) (core.Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prompts = append(h.prompts, req.Prompt)
	return core.Session{ID: "sess-" + string(req.RunID)}, nil
}

func (h *e2eHarness) Wake(context.Context, string, core.WakeContext) error { return nil }
func (h *e2eHarness) Stop(context.Context, string) error                   { return nil }
func (h *e2eHarness) IsAlive(context.Context, string) (bool, error)        { return true, nil }
func (h *e2eHarness) NotifyParent(context.Context, string, string) error   { return nil }
func (h *e2eHarness) Events() <-chan core.HarnessEvent                     { return h.events }

func (h *e2eHarness) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.prompts...)
}

func (h *e2eHarness) count() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return int64(len(h.prompts))
}

type e2eSource struct{ tasks []core.Task }

func (s *e2eSource) ListAvailable(context.Context) ([]core.Task, error) { return s.tasks, nil }
func (s *e2eSource) Claim(context.Context, core.TaskID) error           { return nil }
func (s *e2eSource) Complete(context.Context, core.TaskID) error        { return nil }
func (s *e2eSource) MarkInWorkspace(context.Context, core.TaskID, string, string) error {
	return nil
}
func (s *e2eSource) Create(_ context.Context, t core.Task) (core.Task, error) { return t, nil }

type e2eWorkspace struct{}

func (e2eWorkspace) Create(_ context.Context, t core.Task) (core.Workspace, error) {
	return core.Workspace{Path: "/ws/" + string(t.ID), Branch: "beacon/" + string(t.ID)}, nil
}
func (e2eWorkspace) Clean(context.Context, core.Workspace) (bool, error) { return true, nil }
func (e2eWorkspace) Remove(context.Context, core.Workspace) error        { return nil }
func (e2eWorkspace) ForceRemove(context.Context, core.Workspace) error   { return nil }
func (e2eWorkspace) HasChanges(context.Context, core.Workspace, string) (bool, error) {
	return false, nil
}
func (e2eWorkspace) CommitPath(context.Context, core.Workspace, string, string) error {
	return nil
}

// e2eSink records into the real store and counts persisted decisions. It does
// not implement DecisionLookup, so production deduplication cannot hide a
// repeated decision and change the work count between samples.
type e2eSink struct {
	store   *sqlite.Store
	records atomic.Int64
}

func (s *e2eSink) RecordDecision(ctx context.Context, d app.Decision) error {
	err := s.store.RecordDecision(ctx, d)
	if err == nil {
		s.records.Add(1)
	}
	return err
}

type e2eEnv struct {
	ctx      context.Context
	store    *sqlite.Store
	prs      []int
	runs     []*core.Run
	tasks    []core.Task
	recon    *app.Reconciler
	dispatch *app.Dispatcher
	scm      *e2eSCM
	harness  *e2eHarness
	sink     *e2eSink
}

func newE2EEnv(b *testing.B) *e2eEnv {
	b.Helper()
	store, err := sqlite.Open(filepath.Join(b.TempDir(), "state.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	e := &e2eEnv{ctx: ctx, store: store, scm: &e2eSCM{}, harness: newE2EHarness(), sink: &e2eSink{store: store}}

	for i := 0; i < e2eActivePRs; i++ {
		pr := 3000 + i
		run := core.NewRun(core.RunID(fmt.Sprintf("run-e2e-%04d", i)), core.TaskID(fmt.Sprintf("T-E2E-%04d", i)), now)
		run.State = core.RunWaitingCI
		run.PRNumber = pr
		run.HeadSHA = fmt.Sprintf("%040x", i+1)
		run.SessionID = fmt.Sprintf("sess-e2e-%04d", i)
		run.WorkspacePath = fmt.Sprintf("/ws/T-E2E-%04d", i)
		run.Branch = fmt.Sprintf("beacon/T-E2E-%04d", i)
		run.Scope = []core.ScopePath{core.ScopeFile(fmt.Sprintf("internal/bench/e2e_%02d.go", i))}
		if err := store.Save(ctx, run); err != nil {
			b.Fatal(err)
		}
		e.prs = append(e.prs, pr)
		e.runs = append(e.runs, run)
	}
	for i := 0; i < e2eDispatchTasks; i++ {
		id := core.TaskID(fmt.Sprintf("T-E2E-TASK-%04d", i))
		e.tasks = append(e.tasks, core.Task{
			ID:            id,
			Title:         fmt.Sprintf("Dispatch fixture %d", i),
			Body:          "A fixed card used by the end-to-end dispatch benchmark.",
			Priority:      100 - i,
			Status:        core.TaskAvailable,
			SourceRef:     fmt.Sprintf("docs/tasks/%s.md", id),
			Kind:          core.TaskKindTask,
			BaseBranch:    "main",
			RequireReview: core.ReviewModeLLM,
			WorkerAgent:   "worker",
			WorkerPrompt:  "Implement the task, open a PR, call beacon_watch_pr.",
			Scope:         []core.ScopePath{core.ScopeFile(fmt.Sprintf("internal/bench/dispatch_%02d.go", i))},
		})
	}

	source := &e2eSource{tasks: e.tasks}
	workspace := e2eWorkspace{}
	logger := app.DecisionLogger{Sink: e.sink}
	e.recon = &app.Reconciler{
		Store:      store,
		SCM:        e.scm,
		Policy:     e2ePolicy{},
		Harness:    e.harness,
		Source:     source,
		Workspace:  workspace,
		Decisions:  logger,
		Repo:       "example/beacon",
		BaseBranch: "main",
		Interval:   time.Hour,
	}
	e.dispatch = &app.Dispatcher{
		Source:             source,
		Planner:            app.NewPlanner(nil),
		Workspace:          workspace,
		Harness:            e.harness,
		Store:              store,
		Concurrency:        core.Concurrency{MaxInFlight: 64, MaxActiveAgents: 64},
		MaxAttempts:        3,
		ParentSessionID:    "ses_operator",
		WorkerInstructions: "Operator instructions from the session.",
		Decisions:          logger,
	}
	return e
}

func (e *e2eEnv) reconcileTick() (int64, int64) {
	before := e.sink.records.Load()
	var errs int64
	for _, pr := range e.prs {
		if _, err := e.recon.Reconcile(e.ctx, pr); err != nil {
			errs++
		}
	}
	return e.sink.records.Load() - before, errs
}

func (e *e2eEnv) dispatchCall() (int64, int64) {
	before := e.harness.count()
	res, err := e.dispatch.Dispatch(e.ctx, e2eDispatchTasks)
	var errs int64
	if err != nil {
		errs++
	}
	errs += int64(len(res.Failures))
	return e.harness.count() - before, errs
}

type e2eSubsteps struct {
	observe, save, approved, decision time.Duration
	sync, ready, active, taskSave     time.Duration
	status                            time.Duration
	promptBytes                       int
}

func e2eTime(n int, fn func(int)) time.Duration {
	start := time.Now()
	for i := 0; i < n; i++ {
		fn(i)
	}
	return time.Since(start)
}

func (e *e2eEnv) reconcileSubsteps() e2eSubsteps {
	return e2eSubsteps{
		observe: e2eTime(e2eActivePRs, func(int) {
			_, _ = e.scm.Observe(e.ctx, e.prs[0])
		}),
		save: e2eTime(e2eActivePRs, func(i int) {
			_ = e.store.Save(e.ctx, e.runs[i])
		}),
		approved: e2eTime(e2eActivePRs, func(i int) {
			_, _ = e.store.Approved(e.ctx, e.runs[i].ID, "sha")
		}),
		decision: e2eTime(e2eActivePRs, func(i int) {
			_ = e.sink.RecordDecision(e.ctx, app.Decision{
				CycleID: "substep", Kind: "substep", RunID: string(e.runs[i].ID),
				Reason: "substep", Fields: map[string]any{"i": i},
			})
		}),
	}
}

func (e *e2eEnv) dispatchSubsteps() e2eSubsteps {
	s := e2eSubsteps{
		sync: e2eTime(1, func(int) { _ = e.store.SyncTasks(e.ctx, e.tasks) }),
		ready: e2eTime(1, func(int) {
			_, _ = e.store.ListReadyTasks(e.ctx)
		}),
		active: e2eTime(1, func(int) {
			_, _ = e.store.ListActive(e.ctx)
		}),
		taskSave: e2eTime(e2eDispatchTasks, func(i int) {
			run := core.NewRun(core.RunID(fmt.Sprintf("run-substep-%d", i)), e.tasks[i].ID, time.Now().UTC())
			_ = e.store.Save(e.ctx, run)
		}),
		status: e2eTime(e2eDispatchTasks, func(i int) {
			_ = e.store.SetTaskStatus(e.ctx, e.tasks[i].ID, "claimed")
		}),
	}
	for _, prompt := range e.harness.snapshot() {
		s.promptBytes += len(prompt)
	}
	return s
}

type e2eSummary struct {
	Name    string
	Median  time.Duration
	Min     time.Duration
	Max     time.Duration
	Work    int64
	Errors  int64
	Limiter string
	Share   float64
}

func e2eSummarize(samples []sample) []e2eSummary {
	durations := map[string][]time.Duration{}
	work := map[string]int64{}
	errs := map[string]int64{}
	limiters := map[string]string{pathReconcile: reconcileLimiter, pathDispatch: dispatchLimiter}
	for _, s := range samples {
		durations[s.name] = append(durations[s.name], s.d)
		work[s.name] += s.work
		errs[s.name] += s.errs
	}
	out := make([]e2eSummary, 0, 2)
	for _, name := range []string{pathReconcile, pathDispatch} {
		ds := durations[name]
		if len(ds) == 0 {
			continue
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		out = append(out, e2eSummary{
			Name: name, Median: ds[len(ds)/2], Min: ds[0], Max: ds[len(ds)-1],
			Work: work[name], Errors: errs[name], Limiter: limiters[name],
		})
	}
	return out
}

func e2eMedianDuration(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

type substepTotals struct {
	observe, save, approved, decision []time.Duration
	sync, ready, active, taskSave     []time.Duration
	status                            []time.Duration
	promptBytes                       int
}

func (t *substepTotals) add(s e2eSubsteps) {
	t.observe = append(t.observe, s.observe)
	t.save = append(t.save, s.save)
	t.approved = append(t.approved, s.approved)
	t.decision = append(t.decision, s.decision)
	t.sync = append(t.sync, s.sync)
	t.ready = append(t.ready, s.ready)
	t.active = append(t.active, s.active)
	t.taskSave = append(t.taskSave, s.taskSave)
	t.status = append(t.status, s.status)
	t.promptBytes += s.promptBytes
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func printE2EReport(w io.Writer, sums []e2eSummary, total time.Duration, pairs int, recon, disp substepTotals) {
	fmt.Fprintf(w, "e2e paths: seed=%d active_prs=%d dispatch_tasks=%d pairs=%d\n",
		e2eSeed, e2eActivePRs, e2eDispatchTasks, pairs)
	fmt.Fprintln(w, "path                 median      range       work    errs  share  limiter")
	for _, s := range sums {
		fmt.Fprintf(w, "%-20s %-11s %-11s %8d %6d %5.1f%%  %s\n",
			s.Name, s.Median, s.Max-s.Min, s.Work, s.Errors, s.Share, s.Limiter)
	}
	fmt.Fprintf(w, "e2e cycle median: %s (sum of path medians; share is each path's micro share)\n", total)
	fmt.Fprintf(w, "reconcile substeps (median ms): observe=%.3f save=%.3f approved=%.3f decision=%.3f\n",
		ms(e2eMedianDuration(recon.observe)), ms(e2eMedianDuration(recon.save)),
		ms(e2eMedianDuration(recon.approved)), ms(e2eMedianDuration(recon.decision)))
	fmt.Fprintf(w, "dispatch substeps (median ms): sync=%.3f ready=%.3f active=%.3f save=%.3f status=%.3f prompt-bytes=%d\n",
		ms(e2eMedianDuration(disp.sync)), ms(e2eMedianDuration(disp.ready)),
		ms(e2eMedianDuration(disp.active)), ms(e2eMedianDuration(disp.taskSave)),
		ms(e2eMedianDuration(disp.status)), disp.promptBytes)
}

// BenchmarkE2EOperatorPaths runs five alternating pairs by default. Pair zero
// measures reconcile first, pair one dispatch first, and so on, so a change in
// machine load cannot always favour the same path. Each pair gets two fresh
// state databases, built outside the timed section.
func BenchmarkE2EOperatorPaths(b *testing.B) {
	previousLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(previousLog)
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	warm := newE2EEnv(b)
	_, _ = warm.reconcileTick()
	warm = newE2EEnv(b)
	_, _ = warm.dispatchCall()

	samples := make([]sample, 0, b.N*2)
	var reconSubs, dispSubs substepTotals
	for pair := 0; pair < b.N; pair++ {
		reconEnv := newE2EEnv(b)
		dispatchEnv := newE2EEnv(b)
		if pair%2 == 0 {
			samples = append(samples, timed(pathReconcile, reconEnv.reconcileTick))
			samples = append(samples, timed(pathDispatch, dispatchEnv.dispatchCall))
		} else {
			samples = append(samples, timed(pathDispatch, dispatchEnv.dispatchCall))
			samples = append(samples, timed(pathReconcile, reconEnv.reconcileTick))
		}
		reconSubs.add(reconEnv.reconcileSubsteps())
		dispSubs.add(dispatchEnv.dispatchSubsteps())
	}

	sums := e2eSummarize(samples)
	var total time.Duration
	for _, s := range sums {
		total += s.Median
	}
	for i := range sums {
		if total > 0 {
			sums[i].Share = 100 * math.Round(10*float64(sums[i].Median)/float64(total)) / 10
		}
	}
	for _, s := range sums {
		b.ReportMetric(float64(s.Median.Nanoseconds()), s.Name+"-median-ns")
		b.ReportMetric(s.Share, s.Name+"-micro-share-pct")
		b.ReportMetric(float64(s.Work), s.Name+"-work")
		b.ReportMetric(float64(s.Errors), s.Name+"-errs")
	}
	printE2EReport(os.Stdout, sums, total, b.N, reconSubs, dispSubs)
}
