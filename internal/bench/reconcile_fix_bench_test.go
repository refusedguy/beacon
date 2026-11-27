package bench

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/pkg/core"
)

// This file measures the needs-agent-fix wake path. reconcile_bench_test.go
// counts lists, observes and gets, and e2e_bench_test.go holds runs in wait or
// reuses an observation so the SHA guard skips the wake. Here a policy returns
// needs-agent-fix and every observation carries a fresh head SHA, so each tick
// reaches hasAgentSlot and Harness.Wake.
//
// Work is wake calls, slot checks and ticks that woke a session. Errs counts
// reconcile failures and wake failures. The limiter metrics count the two
// calls named as the bound on this path: AgentSlots.Available and Harness.Wake.

const (
	nameAgentFixWake    = "agent-fix-wake"
	limiterAgentFixWake = "agent-slots-available-plus-harness-wake"
	agentFixWakePRs     = 20
)

// wakeSlotCounter counts AgentSlots.Available calls and always offers one free
// slot, so the wake path continues past hasAgentSlot on every tick.
type wakeSlotCounter struct{ checks atomic.Int64 }

func (s *wakeSlotCounter) Available(context.Context) int {
	s.checks.Add(1)
	return 1
}

// wakeHarnessCounter wraps e2eHarness and counts Wake calls. It records a wake
// error when an injected one is returned, so errs covers both reconcile and
// wake failures.
type wakeHarnessCounter struct {
	*e2eHarness
	wakes    atomic.Int64
	wakeErrs atomic.Int64
	err      error
}

func (h *wakeHarnessCounter) Wake(ctx context.Context, id string, wc core.WakeContext) error {
	h.wakes.Add(1)
	if h.err != nil {
		h.wakeErrs.Add(1)
		return h.err
	}
	return h.e2eHarness.Wake(ctx, id, wc)
}

// fixWakeSCM returns a fresh head SHA on every observation. The changing SHA
// keeps sameObservation false in both handleNeedsAgentFix and wake, so the
// wake is not skipped as a duplicate.
type fixWakeSCM struct{ seq atomic.Uint64 }

func (s *fixWakeSCM) Observe(_ context.Context, pr int) (core.Observation, error) {
	n := s.seq.Add(1)
	return core.Observation{
		PRNumber: pr,
		HeadSHA:  fmt.Sprintf("%040x", n),
		BaseSHA:  fmt.Sprintf("%040x", n/2+1),
		PRState:  "open",
		Checks:   []core.CheckResult{{Name: benchFailCheck, State: core.CheckFailure}},
	}, nil
}

func (s *fixWakeSCM) UpdateBranch(context.Context, int, string) (string, error) { return "", nil }
func (s *fixWakeSCM) Merge(context.Context, int, string, string) error          { return nil }
func (s *fixWakeSCM) BaseBranchStatus(context.Context, string) (core.BaseStatus, error) {
	return core.BaseStatus{}, nil
}
func (s *fixWakeSCM) FindOpenPRByBranch(context.Context, string) (int, error) { return 0, nil }
func (s *fixWakeSCM) OpenPR(context.Context, string, string, string) (int, error) {
	return 0, nil
}

// fixWakePolicy puts every observation in needs-agent-fix, the verdict whose
// handler calls wake after the SHA guard.
type fixWakePolicy struct{}

func (fixWakePolicy) Evaluate(core.Observation) core.MergeDecision {
	return core.MergeDecision{Verdict: core.VerdictNeedsAgentFix, Reason: "checks failed", Criterion: "invariant"}
}

func (fixWakePolicy) ReviewRequired(core.ReviewMode, core.Observation) bool { return false }

type fixWakeEnv struct {
	ctx     context.Context
	prs     []int
	recon   *app.Reconciler
	slots   *wakeSlotCounter
	harness *wakeHarnessCounter
}

func newFixWakeEnv(tb testing.TB) *fixWakeEnv {
	tb.Helper()
	raw, err := sqlite.Open(filepath.Join(tb.TempDir(), "agent-fix-wake.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = raw.Close() })

	ctx := context.Background()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	e := &fixWakeEnv{
		ctx:     ctx,
		slots:   &wakeSlotCounter{},
		harness: &wakeHarnessCounter{e2eHarness: newE2EHarness()},
	}
	for i := 0; i < agentFixWakePRs; i++ {
		pr := 6000 + i
		run := core.NewRun(core.RunID(fmt.Sprintf("run-wake-%04d", i)),
			core.TaskID(fmt.Sprintf("T-WAKE-%04d", i)), now)
		run.State = core.RunWaitingCI
		run.PRNumber = pr
		run.HeadSHA = fmt.Sprintf("%040x", i+1)
		run.SessionID = fmt.Sprintf("sess-wake-%04d", i)
		run.WorkspacePath = fmt.Sprintf("/ws/T-WAKE-%04d", i)
		run.Branch = fmt.Sprintf("beacon/T-WAKE-%04d", i)
		if err := raw.Save(ctx, run); err != nil {
			tb.Fatal(err)
		}
		e.prs = append(e.prs, pr)
	}
	e.recon = &app.Reconciler{
		Store:      raw,
		SCM:        &fixWakeSCM{},
		Policy:     fixWakePolicy{},
		Harness:    e.harness,
		Source:     &e2eSource{},
		Workspace:  e2eWorkspace{},
		Decisions:  app.DecisionLogger{Sink: &e2eSink{store: raw}},
		AgentSlots: e.slots,
		Repo:       "example/beacon",
		BaseBranch: "main",
		Interval:   time.Hour,
	}
	return e
}

// tick runs one reconcile pass over every PR and returns the number of
// reconcile errors. Each pass gets a fresh environment, so fix attempts never
// run out and every PR wakes.
func (e *fixWakeEnv) tick() int64 {
	var errs int64
	for _, pr := range e.prs {
		if _, err := e.recon.Reconcile(e.ctx, pr); err != nil {
			errs++
		}
	}
	return errs
}

func printAgentFixWakeReport(w io.Writer, median time.Duration, work, errs, wakes, checks, wokeTicks int64) {
	fmt.Fprintf(w, "%s: median=%s work=%d errs=%d wakes=%d slot-checks=%d woke-ticks=%d limiter=%s\n",
		nameAgentFixWake, median, work, errs, wakes, checks, wokeTicks, limiterAgentFixWake)
}

// TestAgentFixWakeWorkStable pins the counts the CI parsing compares between
// the base and head sides. Two fresh environments must report the same wakes,
// slot checks and no errors.
func TestAgentFixWakeWorkStable(t *testing.T) {
	runOnce := func() (int64, int64, int64, int64) {
		e := newFixWakeEnv(t)
		errs := e.tick()
		return e.harness.wakes.Load(), e.slots.checks.Load(), errs, e.harness.wakeErrs.Load()
	}
	wakes, checks, errs, wakeErrs := runOnce()
	againWakes, againChecks, againErrs, againWakeErrs := runOnce()
	if wakes != againWakes || checks != againChecks || errs != againErrs || wakeErrs != againWakeErrs {
		t.Fatalf("wake work is not stable: first=%d/%d/%d/%d second=%d/%d/%d/%d",
			wakes, checks, errs, wakeErrs, againWakes, againChecks, againErrs, againWakeErrs)
	}
	if wakes < 1 || checks != wakes || errs != 0 || wakeErrs != 0 {
		t.Fatalf("wakes=%d checks=%d errs=%d wake-errs=%d, want equal positive wakes and checks with no errors",
			wakes, checks, errs, wakeErrs)
	}
}

// BenchmarkAgentFixWake times one needs-agent-fix tick over 20 PRs. Every tick
// wakes a session, so wakes, slot checks and woke ticks all stay at or above
// the tick count. CI runs a bounded smoke pass with BEACON_BENCH_SMOKE=1:
//
//	BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' -bench '^BenchmarkAgentFixWake$' -benchtime=1x -count=1 -benchmem ./internal/bench/
func BenchmarkAgentFixWake(b *testing.B) {
	previousLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(previousLog)
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	if !smokeMode() {
		e := newFixWakeEnv(b)
		_ = e.tick()
	}

	var work, errs, wakes, checks, wokeTicks int64
	durations := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		e := newFixWakeEnv(b)
		before := e.harness.wakes.Load()
		b.StartTimer()
		start := time.Now()
		tickErrs := e.tick()
		d := time.Since(start)
		b.StopTimer()

		iterationWakes := e.harness.wakes.Load() - before
		errs += tickErrs + e.harness.wakeErrs.Load()
		wakes += iterationWakes
		checks += e.slots.checks.Load()
		if iterationWakes > 0 {
			wokeTicks++
		}
		durations = append(durations, d)
	}

	ticks := int64(b.N)
	work = wakes + checks + wokeTicks
	if errs != 0 || wakes < ticks || checks < ticks || wokeTicks != ticks {
		b.Fatalf("wakes=%d checks=%d woke-ticks=%d errs=%d ticks=%d",
			wakes, checks, wokeTicks, errs, ticks)
	}

	median := medianDuration(durations)
	printAgentFixWakeReport(os.Stdout, median, work, errs, wakes, checks, wokeTicks)
	b.ReportMetric(float64(median.Nanoseconds()), nameAgentFixWake+"-median-ns")
	b.ReportMetric(float64(work), nameAgentFixWake+"-work")
	b.ReportMetric(float64(errs), nameAgentFixWake+"-errs")
	b.ReportMetric(float64(wakes)/float64(ticks), nameAgentFixWake+"-wakes-per-tick")
	b.ReportMetric(float64(checks)/float64(ticks), nameAgentFixWake+"-slot-checks-per-tick")
	b.ReportMetric(float64(wokeTicks), nameAgentFixWake+"-woke-ticks")
	b.ReportMetric(float64(wakes+checks), limiterAgentFixWake)
}
