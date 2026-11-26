package bench

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/sqlite"
)

func newFanoutBench(n int) *fanoutBench {
	ctx, cancel := context.WithCancel(context.Background())
	src := make(chan int, 64)
	views := fanout(ctx, src, n)
	for i := 0; i < fanoutBuffer; i++ {
		views[2] <- -1
	}
	return &fanoutBench{src: src, views: views, cancel: cancel}
}

func newEnv(b *testing.B) *env {
	dir, err := os.MkdirTemp("", "beacon-bench-")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = os.RemoveAll(dir) })
	store, err := sqlite.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	runs := runFixtures(fixtureRuns)
	for _, run := range runs {
		if err := store.Save(ctx, run); err != nil {
			b.Fatal(err)
		}
	}
	tasks := taskFixtures(fixtureTasks)
	if err := store.SyncTasks(ctx, tasks); err != nil {
		b.Fatal(err)
	}
	for _, decision := range decisionFixtures(fixtureDecisions, runIDs(runs)) {
		if err := store.RecordDecision(ctx, decision); err != nil {
			b.Fatal(err)
		}
	}

	fan := newFanoutBench(3)
	b.Cleanup(fan.close)
	return &env{
		ctx:     ctx,
		store:   store,
		runs:    runs,
		ids:     runIDs(runs),
		cards:   cardFixtures(fixtureCards),
		wire:    runFixtures(fixtureWireRuns),
		texts:   secretTexts(),
		journal: newOpJournal(),
		fanout:  fan,
	}
}

// warmup runs every hotspot once outside the timed loop so the first
// calibration pass does not pay cold page-cache and regexp-init costs.
func warmup(e *env) {
	_, _ = runSQLiteSnapshot(e)
	_, _ = runCardParse(e)
	_, _ = runWireEncode(e)
	_, _ = runRedact(e)
	_, _ = runJournal(e)
	_, _ = runFanout(e)
}

func timed(name string, fn func() (int64, int64)) sample {
	start := time.Now()
	work, errs := fn()
	return sample{name: name, d: time.Since(start), work: work, errs: errs}
}

func withShares(sums []summary, total time.Duration) []summary {
	out := make([]summary, len(sums))
	copy(out, sums)
	for i := range out {
		if total > 0 {
			out[i].Share = 100 * float64(out[i].Median) / float64(total)
		}
	}
	return out
}

// BenchmarkHotPaths is the single repeatable command for the whole dossier.
// One pass runs all six hotspots, so the shares describe one end-to-end pass.
//
// CI runs a bounded smoke pass with BEACON_BENCH_SMOKE=1 and -benchtime=1x:
//
//	BEACON_BENCH_SMOKE=1 GOMAXPROCS=1 go test -run '^$' -bench '^BenchmarkHotPaths$' -benchtime=1x -count=1 ./internal/bench/
//
// Smoke mode skips the warm-up pass; the report and every count are unchanged.
func BenchmarkHotPaths(b *testing.B) {
	// One P removes scheduler contention from a shared machine. The timings
	// then describe the hot paths instead of neighbouring builds.
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	e := newEnv(b)
	if !smokeMode() {
		warmup(e)
	}
	b.ResetTimer()
	samples := make([]sample, 0, b.N*len(hotspotOrder))
	for i := 0; i < b.N; i++ {
		samples = append(samples, timed(nameSQLite, func() (int64, int64) { return runSQLiteSnapshot(e) }))
		samples = append(samples, timed(nameCard, func() (int64, int64) { return runCardParse(e) }))
		samples = append(samples, timed(nameWire, func() (int64, int64) { return runWireEncode(e) }))
		samples = append(samples, timed(nameRedact, func() (int64, int64) { return runRedact(e) }))
		samples = append(samples, timed(nameJournal, func() (int64, int64) { return runJournal(e) }))
		samples = append(samples, timed(nameFanout, func() (int64, int64) { return runFanout(e) }))
	}
	b.StopTimer()
	sums := summarize(samples)
	total := endToEnd(sums)
	sums = withShares(sums, total)
	printReport(os.Stdout, sums, total, b.N)
	for _, s := range sums {
		b.ReportMetric(float64(s.Median.Nanoseconds()), s.Name+"-median-ns")
		b.ReportMetric(s.Share, s.Name+"-share-pct")
		b.ReportMetric(float64(s.Work), s.Name+"-work")
		b.ReportMetric(float64(s.Errors), s.Name+"-errs")
	}
}
