package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/pkg/core"
)

// newSmallSnapshotEnv seeds a store small enough for unit tests. It uses the
// same fixtures as the hot-path harness, so the assembly reads real rows.
func newSmallSnapshotEnv(t *testing.T) *snapshotEnv {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	runs := runFixtures(4)
	for _, run := range runs {
		if err := store.Save(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	decisions := decisionFixtures(12, runIDs(runs))
	for _, decision := range decisions {
		if err := store.RecordDecision(ctx, decision); err != nil {
			t.Fatal(err)
		}
	}
	return &snapshotEnv{
		ctx:       ctx,
		store:     store,
		ids:       runIDs(runs),
		runs:      len(runs),
		decisions: len(decisions),
		events:    len(runs),
	}
}

func TestSnapshotWork(t *testing.T) {
	snap := &app.Snapshot{
		Active: []app.RunSnapshot{{
			ID:        "run-1",
			Timeline:  []app.RunEvent{{}, {}},
			Decisions: []app.Decision{{}},
			Checks:    []app.CheckSnapshot{{}},
		}},
		Recent: []app.RunSnapshot{{ID: "run-2"}},
		Queue:  []app.QueueItemSnapshot{{ID: "T-1"}},
	}
	// One active run with two events, one decision and one check, one recent
	// run and one queue item make seven counted objects.
	if got, want := snapshotWork(snap), int64(7); got != want {
		t.Fatalf("snapshotWork: got %d, want %d", got, want)
	}
	if got := snapshotWork(&app.Snapshot{}); got != 0 {
		t.Fatalf("snapshotWork on empty snapshot: got %d, want 0", got)
	}
}

func TestReadAssemblyRowsAndAssembleSnapshot(t *testing.T) {
	e := newSmallSnapshotEnv(t)
	rows, ids, err := readAssemblyRows(e.ctx, e.store)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != len(e.ids) {
		t.Fatalf("readAssemblyRows returned %d ids, want %d", len(ids), len(e.ids))
	}
	if len(rows.active) != 4 {
		t.Fatalf("readAssemblyRows returned %d active runs, want 4", len(rows.active))
	}
	if len(rows.events) == 0 || len(rows.decisions) == 0 {
		t.Fatalf("readAssemblyRows returned no history: events=%d decisions=%d", len(rows.events), len(rows.decisions))
	}

	out := assembleSnapshot(e.ctx, app.SnapshotService{Store: rows})
	if out.errs != 0 {
		t.Fatalf("assembleSnapshot errs=%d", out.errs)
	}
	if out.work <= 0 {
		t.Fatalf("assembleSnapshot work=%d, want > 0", out.work)
	}
	if len(out.body) == 0 {
		t.Fatal("assembleSnapshot returned an empty body")
	}
	var snap app.Snapshot
	if err := json.Unmarshal(out.body, &snap); err != nil {
		t.Fatalf("assembleSnapshot body is not a snapshot: %v", err)
	}
	if len(snap.Active) != 4 {
		t.Fatalf("encoded snapshot has %d active runs, want 4", len(snap.Active))
	}
}

func TestAssembleSnapshotWithFailingStore(t *testing.T) {
	svc := app.SnapshotService{Store: failingSnapshotStore{}}
	out := assembleSnapshot(context.Background(), svc)
	if out.errs != 1 {
		t.Fatalf("assembleSnapshot errs=%d, want 1", out.errs)
	}
	if out.body != nil || out.work != 0 {
		t.Fatalf("failed assembly returned work=%d body=%d", out.work, len(out.body))
	}
}

// failingSnapshotStore makes every snapshot read fail so the error path of
// assembleSnapshot is covered without a broken database.
type failingSnapshotStore struct{}

func (failingSnapshotStore) ListActive(context.Context) ([]*core.Run, error) {
	return nil, fmt.Errorf("boom")
}

func (failingSnapshotStore) ListRecent(context.Context, int) ([]*core.Run, error) {
	return nil, fmt.Errorf("boom")
}

func (failingSnapshotStore) ListRunEvents(context.Context, []string) ([]app.RunEvent, error) {
	return nil, fmt.Errorf("boom")
}

func (failingSnapshotStore) ListDecisions(context.Context, []string, int, int) ([]app.Decision, error) {
	return nil, fmt.Errorf("boom")
}

func (failingSnapshotStore) DecisionCounts(context.Context, []string) ([]app.DecisionCount, error) {
	return nil, fmt.Errorf("boom")
}

func TestMedianDuration(t *testing.T) {
	ds := []time.Duration{4 * time.Second, time.Second, 3 * time.Second, 2 * time.Second}
	if got, want := medianDuration(ds), 3*time.Second; got != want {
		t.Fatalf("medianDuration: got %s, want %s", got, want)
	}
	if ds[0] != 4*time.Second {
		t.Fatal("medianDuration reordered its input")
	}
	if got := medianDuration(nil); got != 0 {
		t.Fatalf("medianDuration on empty input: got %s, want 0", got)
	}
}

func TestSummarizePhaseAndShares(t *testing.T) {
	samples := []phaseSample{
		{d: 10 * time.Millisecond, work: 100, errs: 0},
		{d: 30 * time.Millisecond, work: 200, errs: 1},
		{d: 20 * time.Millisecond, work: 300, errs: 0},
	}
	phase, ds := summarizePhase(phaseAssembly, samples)
	if len(ds) != 3 {
		t.Fatalf("summarizePhase returned %d durations, want 3", len(ds))
	}
	if phase.Median != 20*time.Millisecond {
		t.Fatalf("median: got %s, want 20ms", phase.Median)
	}
	if phase.Min != 10*time.Millisecond || phase.Max != 30*time.Millisecond {
		t.Fatalf("range: got %s to %s, want 10ms to 30ms", phase.Min, phase.Max)
	}
	if phase.Work != 600 || phase.Errs != 1 {
		t.Fatalf("totals: got work=%d errs=%d, want 600 and 1", phase.Work, phase.Errs)
	}

	shares := snapshotShares([]snapshotPhase{
		{Name: phaseReads, Median: 30 * time.Millisecond},
		phase,
		{Name: phaseTotal, Median: 50 * time.Millisecond},
	})
	if shares[0].Share != 60 || shares[1].Share != 40 || shares[2].Share != 100 {
		t.Fatalf("shares: got %v, want 60, 40, 100", []float64{shares[0].Share, shares[1].Share, shares[2].Share})
	}
	if shares[1].Work != phase.Work {
		t.Fatal("snapshotShares dropped the work count")
	}
}

func TestPrintSnapshotReport(t *testing.T) {
	phases := snapshotShares([]snapshotPhase{
		{Name: phaseReads, Median: 30 * time.Millisecond, Max: 32 * time.Millisecond, Min: 28 * time.Millisecond},
		{Name: phaseAssembly, Median: 70 * time.Millisecond, Bytes: 4096, Allocs: 1234},
		{Name: phaseTotal, Median: 100 * time.Millisecond},
	})
	var buf bytes.Buffer
	printSnapshotReport(&buf, phases, "runs=10 active=2 recent=1", 5)
	got := buf.String()
	for _, want := range []string{
		"full snapshot: runs=10 active=2 recent=1 passes=5",
		phaseReads, phaseAssembly, phaseTotal,
		"4096", "1234.0", "30.0%", "70.0%", "100.0%",
		"shares are of one full snapshot CPU",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("report does not contain %q:\n%s", want, got)
		}
	}
}

func TestPrintSnapshotRepeatability(t *testing.T) {
	ds := []time.Duration{10 * time.Millisecond, 12 * time.Millisecond, 11 * time.Millisecond}
	var buf bytes.Buffer
	printSnapshotRepeatability(&buf, phaseAssembly, ds)
	got := buf.String()
	if !strings.Contains(got, "five alternating passes") {
		t.Fatalf("repeatability line missing pass count: %s", got)
	}
	if !strings.Contains(got, "median=11ms range=2ms repeatability=18.2%") {
		t.Fatalf("unexpected repeatability line: %s", got)
	}
}

// timeSnapshotReads and timeSnapshotAssembly wrap one timed phase of one pass.
func timeSnapshotReads(e *snapshotEnv) phaseSample {
	start := time.Now()
	work, errs := runSnapshotQueries(e)
	return phaseSample{d: time.Since(start), work: work, errs: errs}
}

func timeSnapshotAssembly(fn func() snapshotAssembly) phaseSample {
	start := time.Now()
	out := fn()
	return phaseSample{d: time.Since(start), work: out.work, errs: out.errs}
}

// BenchmarkFullSnapshot measures the CPU work BenchmarkSQLiteSnapshot omits.
// One pass times the eight store reads and the full SnapshotService.Snapshot
// plus json.Marshal assembly over the same rows. Five passes alternate the
// order of the two phases so slow drift on a shared machine cancels out.
//
// The default fixture is 100,000 runs with 1,000 active and 20 recent, 500,000
// decisions and 2,000,000 events. BEACON_DEEP_* variables override the sizes
// for a bounded local check; the report header prints the sizes actually used.
func BenchmarkFullSnapshot(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	e := newSnapshotEnv(b)
	rows, ids, err := readAssemblyRows(e.ctx, e.store)
	if err != nil {
		b.Fatal(err)
	}
	// Align the reads phase with the rows the assembly phase works on.
	e.ids = ids
	svc := app.SnapshotService{Store: rows}
	assemblyFn := func() snapshotAssembly { return assembleSnapshot(e.ctx, svc) }

	// Warm both paths outside the timed loop so calibration pays neither cold
	// page cache nor first-call reflection cost.
	_, _ = runSnapshotQueries(e)
	_ = assemblyFn()
	runtime.GC()

	// Allocations use a dedicated untimed pass. Bytes are the exact encoded
	// length and do not depend on timer resolution.
	allocs := testing.AllocsPerRun(1, func() { _ = assemblyFn() })
	bytesPerAssembly := len(assemblyFn().body)

	const passes = 5
	reads := make([]phaseSample, 0, passes*b.N)
	assembly := make([]phaseSample, 0, passes*b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for pass := 0; pass < passes; pass++ {
			if pass%2 == 0 {
				reads = append(reads, timeSnapshotReads(e))
				assembly = append(assembly, timeSnapshotAssembly(assemblyFn))
			} else {
				assembly = append(assembly, timeSnapshotAssembly(assemblyFn))
				reads = append(reads, timeSnapshotReads(e))
			}
		}
	}
	b.StopTimer()

	readPhase, readDurations := summarizePhase(phaseReads, reads)
	assemblyPhase, assemblyDurations := summarizePhase(phaseAssembly, assembly)
	assemblyPhase.Bytes = bytesPerAssembly
	assemblyPhase.Allocs = allocs
	totalPhase := snapshotPhase{
		Name:   phaseTotal,
		Median: readPhase.Median + assemblyPhase.Median,
		Min:    readPhase.Min + assemblyPhase.Min,
		Max:    readPhase.Max + assemblyPhase.Max,
		Work:   readPhase.Work + assemblyPhase.Work,
		Errs:   readPhase.Errs + assemblyPhase.Errs,
		Bytes:  bytesPerAssembly,
		Allocs: allocs,
	}
	phases := snapshotShares([]snapshotPhase{readPhase, assemblyPhase, totalPhase})

	fixture := fmt.Sprintf("runs=%d decisions=%d events=%d active=%d recent=%d",
		e.runs, e.decisions, e.events, len(rows.active), len(rows.recent))
	printSnapshotReport(os.Stdout, phases, fixture, passes)
	printSnapshotRepeatability(os.Stdout, phaseReads, readDurations)
	printSnapshotRepeatability(os.Stdout, phaseAssembly, assemblyDurations)

	if errs := readPhase.Errs + assemblyPhase.Errs; errs > 0 {
		b.Errorf("snapshot phases reported %d errors", errs)
	}
	b.ReportMetric(float64(readPhase.Median.Nanoseconds()), "snapshot-reads-median-ns")
	b.ReportMetric(float64(assemblyPhase.Median.Nanoseconds()), "snapshot-assembly-median-ns")
	b.ReportMetric(float64(totalPhase.Median.Nanoseconds()), "snapshot-total-median-ns")
	b.ReportMetric(phases[0].Share, "snapshot-reads-share-pct")
	b.ReportMetric(phases[1].Share, "snapshot-assembly-share-pct")
	b.ReportMetric(float64(readPhase.Work), "snapshot-reads-work")
	b.ReportMetric(float64(assemblyPhase.Work), "snapshot-assembly-work")
	b.ReportMetric(float64(readPhase.Errs+assemblyPhase.Errs), "snapshot-errs")
	b.ReportMetric(float64(bytesPerAssembly), "snapshot-assembly-bytes")
	b.ReportMetric(allocs, "snapshot-assembly-allocs")
}
