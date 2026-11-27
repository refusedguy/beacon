package bench

import (
	"context"
	"fmt"
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

const benchFailCheck = "go test"

type countSource struct {
	*e2eSource
	lists atomic.Int64
}

func (s *countSource) ListAvailable(ctx context.Context) ([]core.Task, error) {
	s.lists.Add(1)
	return s.e2eSource.ListAvailable(ctx)
}

type countSCM struct {
	*e2eSCM
	red      bool
	seq      atomic.Uint64
	observes atomic.Int64
}

func (s *countSCM) Observe(_ context.Context, pr int) (core.Observation, error) {
	s.observes.Add(1)
	n := s.seq.Add(1)
	return core.Observation{PRNumber: pr, HeadSHA: fmt.Sprintf("%040x", n),
		BaseSHA: fmt.Sprintf("%040x", n/2+1), PRState: "open",
		Checks: []core.CheckResult{{Name: benchFailCheck, State: core.CheckFailure}}}, nil
}

func (s *countSCM) BaseBranchStatus(_ context.Context, branch string) (core.BaseStatus, error) {
	out := core.BaseStatus{BaseBranch: branch}
	if s.red {
		out.FailedChecks = []core.CheckResult{{Name: benchFailCheck, State: core.CheckFailure}}
		out.FailureExcerpt = "base check failed"
	}
	return out, nil
}

type countStore struct {
	*sqlite.Store
	gets atomic.Int64
}

func (s *countStore) Get(ctx context.Context, id core.RunID) (*core.Run, error) {
	s.gets.Add(1)
	return s.Store.Get(ctx, id)
}

type tickEnv struct {
	ctx    context.Context
	store  *countStore
	source *countSource
	scm    *countSCM
	recon  *app.Reconciler
	prs    []int
}

func newTickEnv(b *testing.B, prs, candidates int, red bool) *tickEnv {
	b.Helper()
	raw, err := sqlite.Open(filepath.Join(b.TempDir(), "tick.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = raw.Close() })
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	e := &tickEnv{ctx: ctx, store: &countStore{Store: raw},
		source: &countSource{e2eSource: &e2eSource{tasks: taskFixtures(4)}},
		scm:    &countSCM{e2eSCM: &e2eSCM{}, red: red}}
	e.recon = &app.Reconciler{Store: e.store, SCM: e.scm, Policy: dedupPolicy{},
		Harness: newE2EHarness(), Source: e.source, Workspace: e2eWorkspace{},
		Decisions: app.DecisionLogger{Sink: &e2eSink{store: raw}},
		Repo:      "example/beacon", BaseBranch: "main", Interval: time.Hour}
	for i := 0; i < prs+candidates; i++ {
		pr := 5000 + i
		run := core.NewRun(core.RunID(fmt.Sprintf("run-tick-%04d", i)),
			core.TaskID(fmt.Sprintf("T-TICK-%04d", i)), now)
		run.State = core.RunWaitingCI
		run.PRNumber = pr
		run.HeadSHA = fmt.Sprintf("%040x", i+1)
		run.SessionID = fmt.Sprintf("sess-tick-%04d", i)
		run.WorkspacePath = fmt.Sprintf("/ws/T-TICK-%04d", i)
		run.Branch = fmt.Sprintf("beacon/T-TICK-%04d", i)
		if err := raw.Save(ctx, run); err != nil {
			b.Fatal(err)
		}
		if i < prs {
			e.prs = append(e.prs, pr)
		}
	}
	return e
}

func (e *tickEnv) tick(b *testing.B) (int64, int64) {
	b.Helper()
	var work, errs int64
	for _, pr := range e.prs {
		if _, err := e.recon.Reconcile(e.ctx, pr); err != nil {
			errs++
			continue
		}
		work++
	}
	return work, errs
}

// canonicalKey is empty for the two base-check variants; only the reconcile
// tick plan publishes an advisory row. Its work is one successful tick per PR
// and its errs stay zero, so a no-change comparison sees equal counts.
func runTickBench(b *testing.B, prefix, slug string, prs, candidates int, red bool, canonicalKey string) {
	prevProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prevProcs)
	var work, errs, lists, observes, gets int64
	var ds []time.Duration
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		e := newTickEnv(b, prs, candidates, red)
		b.StartTimer()
		start := time.Now()
		w, er := e.tick(b)
		d := time.Since(start)
		b.StopTimer()
		work += w
		errs += er
		lists += e.source.lists.Load()
		observes += e.scm.observes.Load()
		gets += e.store.gets.Load()
		ds = append(ds, d)
	}
	if errs != 0 || work == 0 || lists < int64(b.N) || observes < int64(b.N) {
		b.Fatalf("work=%d errs=%d lists=%d observes=%d ticks=%d", work, errs, lists, observes, b.N)
	}
	n := float64(b.N)
	b.ReportMetric(float64(work), prefix+"-work")
	b.ReportMetric(float64(errs), prefix+"-errs")
	b.ReportMetric(float64(medianDuration(ds).Nanoseconds()), prefix+"-median-ns")
	b.ReportMetric(float64(lists)/n, prefix+"-lists-per-tick")
	b.ReportMetric(float64(observes)/n, prefix+"-observes-per-tick")
	b.ReportMetric(float64(gets)/n, prefix+"-gets-per-tick")
	b.ReportMetric(1, prefix+"-limiter-"+slug)
	if canonicalKey != "" {
		var min, max time.Duration
		for i, d := range ds {
			if i == 0 || d < min {
				min = d
			}
			if d > max {
				max = d
			}
		}
		printPerfRow(os.Stdout, canonicalKey, medianDuration(ds), min, max,
			int64(prs), errs, reconcileTickLimiter)
	}
}

func BenchmarkReconcileTickCounts(b *testing.B) {
	runTickBench(b, "reconcile-tick-counts", "source-listavailable-plus-sqlite-get", 20, 0, false,
		"reconcile-tick-counts")
}

func BenchmarkBaseRedCheck(b *testing.B) {
	runTickBench(b, "base-red-check", "source-listavailable-io", 1, 0, true, "")
}

func BenchmarkBaseRedBlockFanout(b *testing.B) {
	runTickBench(b, "base-red-block", "scm-observe-roundtrips", 1, 24, true, "")
}
