package bench

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
)

// Two reads bound a status snapshot. ListRunEvents merges every event for the
// active set into one global at, id order. ListDecisions numbers decisions per
// run with ROW_NUMBER and caps the orphan bucket separately. This benchmark
// times those two reads alone, without the other six snapshot reads, so CI can
// see the cost of the event merge sort and the per-run decision window.
//
// The fixture comes from newSnapshotEnv, which already uses deepSize for the
// deep and smoke sizes. Reusing both helpers keeps this file compiling on the
// base and head trees that the A B B A comparison copies it into.
const (
	nameEventDecisionReads = "sqlite-event-decision-reads"

	limiterEventDecisionReads = "sqlite-event-decision-reads-limiter-event-sort-plus-decision-window"
)

// runEventDecisionReads performs exactly the two reads under measurement. Work
// is the event and decision rows they return. Errs counts failed calls, so a
// query that regresses into an error cannot pass as a fast pass.
func runEventDecisionReads(e *snapshotEnv) (int64, int64) {
	var work, errs int64
	events, err := e.store.ListRunEvents(e.ctx, e.ids)
	if err != nil {
		errs++
	} else {
		work += int64(len(events))
	}
	decisions, err := e.store.ListDecisions(e.ctx, e.ids,
		app.DefaultDecisionsLimit, app.DefaultOrphanDecisionsLimit)
	if err != nil {
		errs++
	} else {
		work += int64(len(decisions))
	}
	return work, errs
}

// BenchmarkSQLiteEventDecisionReads times the global event merge sort and the
// per-run decision window as one pass. The fixture is built once outside the
// timed loop. Every pass prints the sizes, work, errs and the named limiter, so
// a bounded smoke run and a full run report the same work.
//
// CI runs a bounded smoke pass with BEACON_BENCH_SMOKE=1 and -benchtime=1x:
//
//	BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' -bench '^BenchmarkSQLiteEventDecisionReads$' -benchtime=1x -count=1 -benchmem ./internal/bench/
func BenchmarkSQLiteEventDecisionReads(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	e := newSnapshotEnv(b)
	if !smokeMode() {
		_, _ = runEventDecisionReads(e) // warm page cache and prepared plans
	}

	durations := make([]time.Duration, 0, b.N)
	var work, errs int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		w, er := runEventDecisionReads(e)
		durations = append(durations, time.Since(start))
		work += w
		errs += er
	}
	b.StopTimer()

	median := medianDuration(durations)
	fmt.Printf("%s: runs=%d decisions=%d events=%d active_ids=%d iterations=%d\n",
		nameEventDecisionReads, e.runs, e.decisions, e.events, len(e.ids), b.N)
	fmt.Printf("%s: median=%s work=%d errs=%d limiter=%s\n",
		nameEventDecisionReads, median, work, errs, limiterEventDecisionReads)
	b.ReportMetric(float64(median.Nanoseconds()), nameEventDecisionReads+"-median-ns")
	b.ReportMetric(float64(work), nameEventDecisionReads+"-work")
	b.ReportMetric(float64(errs), nameEventDecisionReads+"-errs")
	b.ReportMetric(1, limiterEventDecisionReads)
}
