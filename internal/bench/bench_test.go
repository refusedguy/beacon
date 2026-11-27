package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// canonicalFields is the fixed payload shared by every perf harness.
var canonicalFields = []string{"key", "median_ms", "min_ms", "max_ms", "work", "errs", "limiter"}

// perfRowsFrom reads canonical rows from a report and rejects a prefix line
// that is not one JSON object with exactly the shared field set.
func perfRowsFrom(t *testing.T, out string) []perfRow {
	t.Helper()
	var rows []perfRow
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, perfPrefix) {
			continue
		}
		payload := strings.TrimPrefix(line, perfPrefix)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &fields); err != nil {
			t.Fatalf("canonical row is not JSON: %q: %v", payload, err)
		}
		if len(fields) != len(canonicalFields) {
			t.Fatalf("canonical row has %d fields, want %d: %s", len(fields), len(canonicalFields), payload)
		}
		for _, name := range canonicalFields {
			if _, ok := fields[name]; !ok {
				t.Fatalf("canonical row is missing %q: %s", name, payload)
			}
		}
		var row perfRow
		if err := json.Unmarshal([]byte(payload), &row); err != nil {
			t.Fatalf("canonical row does not decode: %q: %v", payload, err)
		}
		rows = append(rows, row)
	}
	return rows
}

func TestCanonicalPerfRowFields(t *testing.T) {
	line := perfLine(nameJournal, 2*time.Millisecond, time.Millisecond, 3*time.Millisecond, 7, 0, limiters[nameJournal])
	rows := perfRowsFrom(t, line)
	if len(rows) != 1 {
		t.Fatalf("got %d canonical rows, want 1: %s", len(rows), line)
	}
	row := rows[0]
	if row.Key != nameJournal || row.MedianMS != 2 || row.MinMS != 1 || row.MaxMS != 3 ||
		row.Work != 7 || row.Errs != 0 || row.Limiter != limiters[nameJournal] {
		t.Fatalf("canonical row = %+v, want %s 2/1/3 7/0 %q", row, nameJournal, limiters[nameJournal])
	}
}

func TestCanonicalRowsOnePerHotspot(t *testing.T) {
	sums := withShares(summarize([]sample{
		{name: nameCard, d: 2 * time.Millisecond, work: 10},
		{name: nameCard, d: 4 * time.Millisecond, work: 10},
		{name: nameWire, d: time.Millisecond, work: 3},
	}), 5*time.Millisecond)
	var buf bytes.Buffer
	printReport(&buf, sums, 5*time.Millisecond, 2)
	rows := perfRowsFrom(t, buf.String())
	if len(rows) != len(sums) {
		t.Fatalf("got %d canonical rows, want %d:\n%s", len(rows), len(sums), buf.String())
	}
	registryKeys := make(map[string]bool, len(Registry()))
	for _, spec := range Registry() {
		registryKeys[spec.Key] = true
	}
	seen := make(map[string]bool, len(rows))
	for i, row := range rows {
		if row.Key != sums[i].Name {
			t.Errorf("row %d key = %q, want %q", i, row.Key, sums[i].Name)
		}
		if seen[row.Key] {
			t.Errorf("duplicate canonical row %q", row.Key)
		}
		seen[row.Key] = true
		if !registryKeys[row.Key] {
			t.Errorf("canonical key %q is not in the registry", row.Key)
		}
		if row.MinMS <= 0 || row.MaxMS < row.MinMS || row.MedianMS < row.MinMS || row.MedianMS > row.MaxMS {
			t.Errorf("row %q has invalid min/max/median: %+v", row.Key, row)
		}
	}
}

func TestCanonicalRowsOnePerSnapshotPhase(t *testing.T) {
	phases := snapshotShares([]snapshotPhase{
		{Name: phaseReads, Median: 30 * time.Millisecond, Min: 28 * time.Millisecond, Max: 32 * time.Millisecond, Work: 3},
		{Name: phaseAssembly, Median: 70 * time.Millisecond, Min: 60 * time.Millisecond, Max: 80 * time.Millisecond, Work: 4, Errs: 1},
		{Name: phaseTotal, Median: 100 * time.Millisecond, Min: 88 * time.Millisecond, Max: 112 * time.Millisecond},
	})
	var buf bytes.Buffer
	printSnapshotReport(&buf, phases, "runs=10 active=2 recent=1", 5)
	rows := perfRowsFrom(t, buf.String())
	if len(rows) != len(phases) {
		t.Fatalf("got %d canonical rows, want %d:\n%s", len(rows), len(phases), buf.String())
	}
	registryKeys := make(map[string]bool, len(Registry()))
	for _, spec := range Registry() {
		registryKeys[spec.Key] = true
	}
	for i, row := range rows {
		if row.Key != phases[i].Name {
			t.Errorf("row %d key = %q, want %q", i, row.Key, phases[i].Name)
		}
		if !registryKeys[row.Key] {
			t.Errorf("canonical key %q is not in the registry", row.Key)
		}
		if row.MinMS <= 0 || row.MaxMS < row.MinMS {
			t.Errorf("row %q is missing min and max: %+v", row.Key, row)
		}
	}
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
// One pass runs all ten hotspots, so the shares describe one end-to-end pass.
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
	scm := newSCMObserveHarness(b)
	cards := newCardLocalBench(b)
	snapshot := newHTTPSnapshotBench(b, e)
	spawn := newSpawnHarnessBench(b)
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
		samples = append(samples, timed(nameSCM, func() (int64, int64) { return runSCMObserve(scm) }))
		samples = append(samples, timed(nameCardLocal, func() (int64, int64) { return runCardStoreLocalScan(cards) }))
		samples = append(samples, timed(nameHTTPSnapshot, func() (int64, int64) { return runHTTPSnapshotAssembly(snapshot) }))
		samples = append(samples, timed(nameSpawn, func() (int64, int64) { return runHarnessSpawn(spawn) }))
	}
	b.StopTimer()
	waitSpawnMoves(spawn, int64(b.N*spawnCount))
	if got := spawn.moves.Load(); got != int64(b.N*spawnCount) {
		b.Errorf("harness spawn deferred moves = %d, want %d", got, b.N*spawnCount)
	}
	b.ReportMetric(float64(snapshot.bytes), nameHTTPSnapshot+"-body-bytes")
	sums := summarize(samples)
	total := endToEnd(sums)
	sums = withShares(sums, total)
	printReport(os.Stdout, sums, total, b.N)
	for _, s := range sums {
		b.ReportMetric(float64(s.Median.Nanoseconds()), s.Name+"-median-ns")
		b.ReportMetric(float64(s.Min.Nanoseconds()), s.Name+"-min-ns")
		b.ReportMetric(float64(s.Max.Nanoseconds()), s.Name+"-max-ns")
		b.ReportMetric(s.Share, s.Name+"-share-pct")
		b.ReportMetric(float64(s.Work), s.Name+"-work")
		b.ReportMetric(float64(s.Errors), s.Name+"-errs")
	}
}
