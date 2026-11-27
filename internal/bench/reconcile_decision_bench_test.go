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

// This benchmark covers the reconcile decision lookup branch that the other
// benchmarks in this package miss. e2eSink deliberately omits DecisionLookup,
// so logReconcileDecision always takes the sink write path there. Here the sink
// adds the optional read side, and every tick presents the same observation so
// the signature of each reconcile decision stays equal.
//
// One variant builds a new Reconciler per tick, which empties the in-memory
// decision cache and forces a LastDecision read on every tick. The other
// variant reuses one Reconciler, so the second and later ticks hit the cache
// and perform no read at all. The assertions after the timed loops fail if the
// lookup or the cache stops being used.

const (
	decisionLookupPrefix = "decision-lookup-dedup"

	decisionLookupLimiterName = "sqlite LastDecision query + decisions insert"
	decisionLookupLimiterSlug = "sqlite-lastdecision-query-plus-decision-insert"

	decisionLookupHeadSHA = "1111111111111111111111111111111111111111"
	decisionLookupBaseSHA = "2222222222222222222222222222222222222222"
	decisionLookupPR      = 6000
	decisionLookupCheck   = "go test"
)

// decisionLookupSCM returns a fixed head and base pair. A run that already
// recorded the pair reaches sameObservation, so no wake or rebase work enters
// the timed section and each tick produces exactly one reconcile decision.
type decisionLookupSCM struct {
	*e2eSCM
	observes atomic.Int64
}

func (s *decisionLookupSCM) Observe(_ context.Context, pr int) (core.Observation, error) {
	s.observes.Add(1)
	return core.Observation{
		PRNumber: pr,
		HeadSHA:  decisionLookupHeadSHA,
		BaseSHA:  decisionLookupBaseSHA,
		PRState:  "open",
		Checks:   []core.CheckResult{{Name: decisionLookupCheck, State: core.CheckFailure}},
	}, nil
}

type decisionLookupPolicy struct{}

func (decisionLookupPolicy) Evaluate(core.Observation) core.MergeDecision {
	return core.MergeDecision{Verdict: core.VerdictNeedsAgentFix, Reason: "checks failed", Criterion: "invariant"}
}

func (decisionLookupPolicy) ReviewRequired(core.ReviewMode, core.Observation) bool { return false }

// lookupSink embeds e2eSink for the decision insert and adds the DecisionLookup
// read side. The counters name the two operations the dedup path spends: the
// sqlite lookup and the record write.
type lookupSink struct {
	*e2eSink
	lookups    atomic.Int64
	lookupErrs atomic.Int64
	recordErrs atomic.Int64
}

func (s *lookupSink) LastDecision(ctx context.Context, runID string) (*app.Decision, error) {
	s.lookups.Add(1)
	d, err := s.store.LastDecision(ctx, runID)
	if err != nil {
		s.lookupErrs.Add(1)
	}
	return d, err
}

func (s *lookupSink) RecordDecision(ctx context.Context, d app.Decision) error {
	err := s.e2eSink.RecordDecision(ctx, d)
	if err != nil {
		s.recordErrs.Add(1)
	}
	return err
}

type decisionLookupEnv struct {
	ctx     context.Context
	store   *sqlite.Store
	sink    *lookupSink
	scm     *decisionLookupSCM
	harness *e2eHarness
	runID   core.RunID
	pr      int
	warm    *app.Reconciler
}

func newDecisionLookupEnv(b *testing.B) *decisionLookupEnv {
	b.Helper()
	store, err := sqlite.Open(filepath.Join(b.TempDir(), "decision-lookup.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	e := &decisionLookupEnv{
		ctx:     ctx,
		store:   store,
		sink:    &lookupSink{e2eSink: &e2eSink{store: store}},
		scm:     &decisionLookupSCM{e2eSCM: &e2eSCM{}},
		harness: newE2EHarness(),
		runID:   core.RunID("run-decision-lookup"),
		pr:      decisionLookupPR,
	}
	run := core.NewRun(e.runID, core.TaskID("T-DECISION-LOOKUP"), now)
	run.State = core.RunNeedsAgentFix
	run.PRNumber = e.pr
	run.HeadSHA = decisionLookupHeadSHA
	run.ObservedSHA = decisionLookupHeadSHA
	run.ObservedBaseSHA = decisionLookupBaseSHA
	run.SessionID = "sess-decision-lookup"
	run.WorkspacePath = "/ws/T-DECISION-LOOKUP"
	run.Branch = "beacon/T-DECISION-LOOKUP"
	run.Scope = []core.ScopePath{core.ScopeFile("internal/bench/decision_lookup.go")}
	if err := store.Save(ctx, run); err != nil {
		b.Fatal(err)
	}
	e.warm = e.newReconciler()
	return e
}

// newReconciler returns a logger with an empty decision cache. The warm variant
// keeps one of these; the cold variant builds one per tick.
func (e *decisionLookupEnv) newReconciler() *app.Reconciler {
	return &app.Reconciler{
		Store:      e.store,
		SCM:        e.scm,
		Policy:     decisionLookupPolicy{},
		Harness:    e.harness,
		Source:     &e2eSource{},
		Workspace:  e2eWorkspace{},
		Decisions:  app.DecisionLogger{Sink: e.sink},
		Repo:       "example/beacon",
		BaseBranch: "main",
		Interval:   time.Hour,
	}
}

func (e *decisionLookupEnv) tick(recon *app.Reconciler) error {
	_, err := recon.Reconcile(e.ctx, e.pr)
	return err
}

func (e *decisionLookupEnv) resetCounters() {
	e.sink.lookups.Store(0)
	e.sink.lookupErrs.Store(0)
	e.sink.recordErrs.Store(0)
	e.sink.records.Store(0)
	e.scm.observes.Store(0)
}

// lookupCounts is the measured result of one variant. writes counts the rows
// the sink stored; skips are derived as ticks minus writes because each tick
// emits exactly one reconcile decision.
type lookupCounts struct {
	lookups, writes, skips, errs int64
}

func (e *decisionLookupEnv) counts(ticks int) lookupCounts {
	writes := e.sink.records.Load()
	return lookupCounts{
		lookups: e.sink.lookups.Load(),
		writes:  writes,
		skips:   int64(ticks) - writes,
		errs:    e.sink.lookupErrs.Load() + e.sink.recordErrs.Load(),
	}
}

func printDecisionLookupReport(w io.Writer, ticks int, cold, warm lookupCounts, work, errs int64) {
	fmt.Fprintf(w, "decision lookup dedup: ticks=%d work=%d errs=%d limiter=%s\n",
		ticks, work, errs, decisionLookupLimiterName)
	fmt.Fprintln(w, "variant  reads/tick writes/tick skips/tick errs")
	for _, row := range []struct {
		name  string
		count lookupCounts
	}{{"cold", cold}, {"warm", warm}} {
		fmt.Fprintf(w, "%-8s %11.1f %11.1f %11.1f %4d\n", row.name,
			float64(row.count.lookups)/float64(ticks),
			float64(row.count.writes)/float64(ticks),
			float64(row.count.skips)/float64(ticks), row.count.errs)
	}
}

// BenchmarkDecisionLookupDedup measures the sqlite LastDecision read and the
// decision insert on repeated reconcile decisions. CI runs a bounded smoke
// pass with BEACON_BENCH_SMOKE=1 and -benchtime=1x:
//
//	BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' -bench '^BenchmarkDecisionLookupDedup$' -benchtime=1x -count=1 -benchmem ./internal/bench/
//
// Smoke mode skips no fixture work. The counts and assertions are the same in
// both modes; only the number of measured ticks changes with -benchtime.
func BenchmarkDecisionLookupDedup(b *testing.B) {
	previousLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(previousLog)
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	e := newDecisionLookupEnv(b)

	// Cold variant: a fresh logger on every tick, so no cache hit can hide the
	// lookup. Tick one finds no stored decision and writes one; later ticks find
	// the equal signature and skip the write after a lookup.
	var ds []time.Duration
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		if err := e.tick(e.newReconciler()); err != nil {
			b.Fatal(err)
		}
		ds = append(ds, time.Since(start))
	}
	b.StopTimer()
	cold := e.counts(b.N)

	// Prime the warm logger outside the timed section. The first tick stores a
	// decision and fills the cache; every measured tick then hits the cache.
	e.resetCounters()
	if err := e.tick(e.warm); err != nil {
		b.Fatal(err)
	}
	e.resetCounters()

	b.StartTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		if err := e.tick(e.warm); err != nil {
			b.Fatal(err)
		}
		ds = append(ds, time.Since(start))
	}
	b.StopTimer()
	warm := e.counts(b.N)

	if cold.lookups != int64(b.N) {
		b.Fatalf("cold LastDecision reads = %d, want %d ticks", cold.lookups, b.N)
	}
	if warm.lookups != 0 {
		b.Fatalf("warm LastDecision reads = %d, want zero with an unchanged observation", warm.lookups)
	}
	if cold.writes == 0 || warm.writes != 0 {
		b.Fatalf("decision writes: cold=%d warm=%d, want one cold write and no warm write",
			cold.writes, warm.writes)
	}
	errs := cold.errs + warm.errs
	if errs != 0 {
		b.Fatalf("lookup and record errors = %d, want zero", errs)
	}

	// Work is one reconcile decision per tick for each variant. It is fixed by
	// the fixture and matches in smoke and full mode.
	work := cold.writes + cold.skips + warm.writes + warm.skips
	n := float64(b.N)
	b.ReportMetric(float64(work), decisionLookupPrefix+"-work")
	b.ReportMetric(float64(errs), decisionLookupPrefix+"-errs")
	b.ReportMetric(float64(medianDuration(ds).Nanoseconds()), decisionLookupPrefix+"-median-ns")
	b.ReportMetric(float64(cold.lookups)/n, decisionLookupPrefix+"-cold-reads-per-tick")
	b.ReportMetric(float64(cold.writes)/n, decisionLookupPrefix+"-cold-writes-per-tick")
	b.ReportMetric(float64(cold.skips)/n, decisionLookupPrefix+"-cold-skips-per-tick")
	b.ReportMetric(float64(warm.lookups)/n, decisionLookupPrefix+"-warm-reads-per-tick")
	b.ReportMetric(float64(warm.skips)/n, decisionLookupPrefix+"-warm-skips-per-tick")
	b.ReportMetric(1, decisionLookupPrefix+"-limiter-"+decisionLookupLimiterSlug)
	printDecisionLookupReport(os.Stdout, b.N, cold, warm, work, errs)
}
