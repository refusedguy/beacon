package bench

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/sqlite"
)

const (
	nameOpenPrepared    = "open-prepared"
	limiterOpenPrepared = "open-prepared-limiter-ddl-scans"
)

// openPreparedEnv holds one seeded database that already carries the current
// schema. Every Open in the timed loop therefore measures the prepared path:
// a legacy file would take the migration path on its first open only.
type openPreparedEnv struct {
	path string
	work int64
}

// openPreparedState counts the schema objects and the rows two migrations
// backfill. All three counts are fixed by the seed, so base and head report
// the same work even when head skips the DDL that base replays on each open.
func openPreparedState(path string) (objects, events, summary int64, err error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return 0, 0, 0, err
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_autoindex_%'`).Scan(&objects); err != nil {
		return 0, 0, 0, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM run_events`).Scan(&events); err != nil {
		return 0, 0, 0, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM decision_summary`).Scan(&summary); err != nil {
		return 0, 0, 0, err
	}
	return objects, events, summary, nil
}

func newOpenPreparedEnv(tb testing.TB) *openPreparedEnv {
	tb.Helper()
	dir, err := os.MkdirTemp("", "beacon-open-prepared-")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "state.db")

	store, err := sqlite.Open(path)
	if err != nil {
		tb.Fatal(err)
	}
	ctx := context.Background()
	runs := runFixtures(fixtureRuns)
	for _, run := range runs {
		if err := store.Save(ctx, run); err != nil {
			tb.Fatal(err)
		}
	}
	for _, d := range decisionFixtures(fixtureDecisions, runIDs(runs)) {
		if err := store.RecordDecision(ctx, d); err != nil {
			tb.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		tb.Fatal(err)
	}

	objects, events, summary, err := openPreparedState(path)
	if err != nil {
		tb.Fatal(err)
	}
	return &openPreparedEnv{
		path: path,
		work: objects + events + summary,
	}
}

func printOpenPreparedReport(w io.Writer, d time.Duration, work, errs int64) {
	fmt.Fprintf(w, "%s: median=%s work=%d errs=%d limiter=%s\n",
		nameOpenPrepared, d, work, errs, limiterOpenPrepared)
}

// TestOpenPreparedWorkStable pins the counts the CI comparison reads. One
// seeded file gives positive, identical work before and after an Open and no
// errors, so a smoke pass and a full pass agree.
func TestOpenPreparedWorkStable(t *testing.T) {
	e := newOpenPreparedEnv(t)
	objects, events, summary, err := openPreparedState(e.path)
	if err != nil {
		t.Fatal(err)
	}
	work := objects + events + summary
	if work <= 0 || work != e.work {
		t.Fatalf("open prepared work = %d, setup = %d, want positive and equal", work, e.work)
	}

	store, err := sqlite.Open(e.path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	objects, events, summary, err = openPreparedState(e.path)
	if err != nil {
		t.Fatal(err)
	}
	againWork := objects + events + summary
	if againWork != work {
		t.Fatalf("work after Open = %d, want %d", againWork, work)
	}
}

// BenchmarkOpenPreparedDB times repeated opens of an already prepared state
// database. The seed and the reported counts are fixed, so a smoke pass and a
// full pass print the same work and errs. CI compares those counts between the
// base and head trees; the named limiter is the DDL and presence scans an
// unprepared open replays.
//
// CI runs a bounded smoke pass with BEACON_BENCH_SMOKE=1 and -benchtime=1x:
//
//	BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' -bench '^BenchmarkOpenPreparedDB$' -benchtime=1x -count=1 -benchmem ./internal/bench/
func BenchmarkOpenPreparedDB(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	e := newOpenPreparedEnv(b)
	if !smokeMode() {
		// Warm the page cache once outside the measured pass.
		store, err := sqlite.Open(e.path)
		if err != nil {
			b.Fatal(err)
		}
		if err := store.Close(); err != nil {
			b.Fatal(err)
		}
	}

	durations := make([]time.Duration, 0, b.N)
	var errs int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		store, err := sqlite.Open(e.path)
		durations = append(durations, time.Since(start))
		if err != nil {
			errs++
			continue
		}
		if err := store.Close(); err != nil {
			errs++
		}
	}
	b.StopTimer()

	median := medianDuration(durations)
	printOpenPreparedReport(os.Stdout, median, e.work, errs)
	b.ReportMetric(float64(median.Nanoseconds()), nameOpenPrepared+"-median-ns")
	b.ReportMetric(float64(e.work), nameOpenPrepared+"-work")
	b.ReportMetric(float64(errs), nameOpenPrepared+"-errs")
	b.ReportMetric(float64(e.work), limiterOpenPrepared)
}
