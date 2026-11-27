package bench

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
)

// Deep snapshot fixture sizes. The dossier used 120 runs, 400 decisions and
// no run_events fixture. This harness scales every table that a status
// snapshot reads so the measured floor is not an artefact of a tiny fixture.
const (
	deepRuns      = 100000
	deepDecisions = 500000
	deepEvents    = 2000000
	deepActive    = 1000
	deepRecent    = 20
	deepSeed      = 20261126

	// Smoke sizes keep the same seed and modulo distributions as the deep
	// fixture, so one bounded PR pass reads a fixed amount of work in seconds.
	smokeRuns      = 2000
	smokeDecisions = 10000
	smokeEvents    = 40000
	smokeActive    = 200
	smokeRecent    = 20
)

// deepDBFlags pins the SQLite settings that change page-cache and WAL work.
// cache_size is negative, so its magnitude is a fixed KiB limit. Autocheckpoint
// stays off until seeding finishes and the explicit checkpoint below runs.
const deepDBFlags = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-65536)&_pragma=wal_autocheckpoint(0)&_pragma=temp_store(MEMORY)"

// deepTmpDir keeps the seeded database on tmpfs when the machine has it. A
// fixed filesystem removes dentry and page-cache variance between base and
// head. Machines without tmpfs fall back to the OS temp directory.
func deepTmpDir() string {
	if info, err := os.Stat("/dev/shm"); err == nil && info.IsDir() {
		return "/dev/shm"
	}
	return os.TempDir()
}

// snapshotEnv holds one seeded state database and the ids a snapshot reads.
type snapshotEnv struct {
	ctx       context.Context
	store     *sqlite.Store
	ids       []string
	runs      int
	decisions int
	events    int
}

// deepSize returns the fixture sizes from the environment when set, so a
// quick smoke run can use smaller numbers without editing the default check.
func deepSize(name string, fallback int) int {
	if raw := os.Getenv(name); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// newSnapshotEnv builds the deep fixture once, outside the timed loop. All
// inserts share one transaction and use prepared statements so fixture setup
// measures seconds, not minutes, on the benchmark machine.
func newSnapshotEnv(b *testing.B) *snapshotEnv {
	b.Helper()
	fallbackRuns, fallbackDecisions, fallbackEvents := deepRuns, deepDecisions, deepEvents
	fallbackActive, fallbackRecent := deepActive, deepRecent
	if smokeMode() {
		fallbackRuns, fallbackDecisions, fallbackEvents = smokeRuns, smokeDecisions, smokeEvents
		fallbackActive, fallbackRecent = smokeActive, smokeRecent
	}
	// deepSize reads the environment first, so explicit BEACON_DEEP_* values
	// still win over the smoke fallback.
	runs := deepSize("BEACON_DEEP_RUNS", fallbackRuns)
	decisions := deepSize("BEACON_DEEP_DECISIONS", fallbackDecisions)
	events := deepSize("BEACON_DEEP_EVENTS", fallbackEvents)
	active := deepSize("BEACON_DEEP_ACTIVE", fallbackActive)
	recent := deepSize("BEACON_DEEP_RECENT", fallbackRecent)
	if active > runs {
		active = runs
	}
	if recent > runs-active {
		recent = runs - active
	}

	dir, err := os.MkdirTemp(deepTmpDir(), "beacon-deep-snapshot-")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "state.db")
	store, err := sqlite.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })

	db, err := sql.Open("sqlite", "file:"+path+"?"+deepDBFlags)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	seedDeepFixture(b, db, runs, decisions, events, active, recent)

	ids := make([]string, 0, active+recent)
	for i := 0; i < active+recent; i++ {
		ids = append(ids, fmt.Sprintf("run-%06d", i))
	}
	return &snapshotEnv{
		ctx:       context.Background(),
		store:     store,
		ids:       ids,
		runs:      runs,
		decisions: decisions,
		events:    events,
	}
}

func seedDeepFixture(b *testing.B, db *sql.DB, runs, decisions, events, active, recent int) {
	b.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	// Set-based inserts keep fixture setup to seconds on the benchmark machine.
	// A recursive CTE produces the same seeded rows the loop did, in one pass
	// through the SQLite engine instead of millions of driver round trips.
	seed := fmt.Sprintf("WITH RECURSIVE seq(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM seq WHERE i < %d) ", runs-1)
	execSeed := func(query string) {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			b.Fatal(err)
		}
	}
	execSeed(seed + `INSERT INTO runs
(id, task_id, state, pr_number, head_sha, observed_sha, branch, scope_json,
 is_fixer, rebase_attempts, fix_attempts, max_attempts, reason, checks_json, created_at, updated_at)
SELECT printf('run-%06d', i), printf('T-%06d', i),
 CASE WHEN i < ` + fmt.Sprint(active) + ` THEN
   CASE i%5 WHEN 0 THEN 'running' WHEN 1 THEN 'waiting_ci' WHEN 2 THEN 'needs_rebase'
            WHEN 3 THEN 'needs_agent_fix' ELSE 'blocked' END
 WHEN i < ` + fmt.Sprint(active+recent) + ` THEN 'closed' ELSE 'merged' END,
 100000+i, printf('sha%040d', i), printf('sha%040d', i), printf('beacon/T-%06d', i),
 '["f:pkg/core/run.go"]', 0, 0, 0, 12, 'checks failed on main',
 '[{"Name":"go test","State":"success"}]',
 (` + fmt.Sprint(deepSeed) + `+i)*1000000000, (` + fmt.Sprint(deepSeed) + `+i)*1000000000+1
FROM seq`)

	seed = fmt.Sprintf("WITH RECURSIVE seq(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM seq WHERE i < %d) ", decisions-1)
	execSeed(seed + `INSERT INTO decisions
(at, cycle_id, kind, run_id, task_id, pr_number, verdict, action, reason, fields_json)
SELECT (` + fmt.Sprint(deepSeed) + `+i)*1000000000, printf('cycle-%04d', i%16),
 CASE i%3 WHEN 0 THEN 'dispatch' WHEN 1 THEN 'reconcile' ELSE 'control' END,
 CASE WHEN i%4 = 0 THEN '' ELSE printf('run-%06d', i%` + fmt.Sprint(runs) + `) END,
 printf('T-%06d', i%` + fmt.Sprint(runs) + `), 100000+i%` + fmt.Sprint(runs) + `,
 CASE i%3 WHEN 0 THEN 'ready' WHEN 1 THEN 'retry' ELSE 'keep_working' END,
 'admit', 'snapshot decision reason', '{"attempt":1}'
FROM seq`)

	seed = fmt.Sprintf("WITH RECURSIVE seq(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM seq WHERE i < %d) ", events-1)
	execSeed(seed + `INSERT INTO run_events (run_id, kind, state, at, head_sha, pr_number, reason)
SELECT printf('run-%06d', i%` + fmt.Sprint(runs) + `),
 CASE i%6 WHEN 0 THEN 'start' WHEN 1 THEN 'started' WHEN 2 THEN 'checks_pending'
          WHEN 3 THEN 'behind' WHEN 4 THEN 'checks_failed' ELSE 'observed' END,
 CASE i%5 WHEN 0 THEN 'running' WHEN 1 THEN 'waiting_ci' WHEN 2 THEN 'needs_rebase'
          WHEN 3 THEN 'needs_agent_fix' ELSE 'blocked' END,
 (` + fmt.Sprint(deepSeed) + `+i)*1000000, printf('sha%040d', i),
 100000+i%` + fmt.Sprint(runs) + `, 'snapshot event reason'
FROM seq`)

	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	// Move every seeded page out of the WAL before the first timed read. A
	// checkpoint at a fixed point keeps the warm pass and the timed passes
	// reading the same file state.
	var busy, log, checkpointed int
	if err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &checkpointed); err != nil {
		b.Fatal(err)
	}
	if busy != 0 {
		b.Fatalf("wal_checkpoint(TRUNCATE) reported %d busy pages", busy)
	}
}

// runSnapshotQueries issues the store reads a status snapshot performs. Work
// is rows returned across every read; errors are failed calls.
func runSnapshotQueries(e *snapshotEnv) (int64, int64) {
	var work, errs int64
	trace := os.Getenv("BEACON_DEEP_TRACE") != ""
	start := func(name string) func(int, error) {
		t := time.Now()
		return func(rows int, err error) {
			if trace {
				fmt.Printf("  %-18s %s rows=%d err=%v\n", name, time.Since(t), rows, err)
			}
			if err != nil {
				errs++
				return
			}
			work += int64(rows)
		}
	}
	done := start("ListActive")
	active, err := e.store.ListActive(e.ctx)
	done(len(active), err)
	done = start("ListRecent")
	recent, err := e.store.ListRecent(e.ctx, app.DefaultRecentLimit)
	done(len(recent), err)
	done = start("ListQueueTasks")
	queue, err := e.store.ListQueueTasks(e.ctx)
	done(len(queue), err)
	done = start("ListDecisions")
	decisions, err := e.store.ListDecisions(e.ctx, e.ids, app.DefaultDecisionsLimit, app.DefaultOrphanDecisionsLimit)
	done(len(decisions), err)
	done = start("DecisionCounts")
	counts, err := e.store.DecisionCounts(e.ctx, e.ids)
	done(len(counts), err)
	done = start("ListRunEvents")
	events, err := e.store.ListRunEvents(e.ctx, e.ids)
	done(len(events), err)
	done = start("ListReadyTasks")
	ready, err := e.store.ListReadyTasks(e.ctx)
	done(len(ready), err)
	done = start("CurrentIncident")
	incident, err := e.store.CurrentIncident(e.ctx)
	if err != nil {
		done(0, err)
	} else if incident != nil {
		done(1, nil)
	} else {
		done(0, nil)
	}
	return work, errs
}

type snapshotSample struct {
	d    time.Duration
	work int64
	errs int64
}

// BenchmarkSQLiteSnapshot is the repeatable deep-snapshot command. It reports
// the median and range of a full snapshot read at 500k decisions, 100k runs
// and 2M run events, with work and error counts from the final pass.
//
// CI runs a bounded smoke pass with BEACON_BENCH_SMOKE=1 and -benchtime=1x:
//
//	BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' -bench '^BenchmarkSQLiteSnapshot$' -benchtime=1x -count=1 -benchmem ./internal/bench/
//
// Smoke mode uses smaller fixture sizes, skips the warm pass and both
// profiles, and still prints the sizes actually used so base and head can be
// checked for identical work. The seed, modulo distributions, report and
// counts stay the same.
func BenchmarkSQLiteSnapshot(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	e := newSnapshotEnv(b)
	if !smokeMode() {
		_, _ = runSnapshotQueries(e) // warm page cache and prepared plans
	}

	// Profiles start after the fixture and warmup so they describe the timed
	// snapshot loop rather than millions of fixture inserts. Smoke mode skips
	// them so one bounded pass stays well under the CI limit.
	var cpuFile, heapFile *os.File
	if !smokeMode() {
		var err error
		if path := os.Getenv("BEACON_DEEP_CPUPROFILE"); path != "" {
			cpuFile, err = os.Create(path)
			if err != nil {
				b.Fatal(err)
			}
			if err := pprof.StartCPUProfile(cpuFile); err != nil {
				b.Fatal(err)
			}
			defer func() {
				pprof.StopCPUProfile()
				_ = cpuFile.Close()
			}()
		}
		if path := os.Getenv("BEACON_DEEP_MEMPROFILE"); path != "" {
			heapFile, err = os.Create(path)
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = heapFile.Close() }()
		}
	}

	b.ResetTimer()
	samples := make([]snapshotSample, 0, b.N)
	for i := 0; i < b.N; i++ {
		start := time.Now()
		work, errs := runSnapshotQueries(e)
		samples = append(samples, snapshotSample{d: time.Since(start), work: work, errs: errs})
	}
	b.StopTimer()
	if heapFile != nil {
		runtime.GC()
		if err := pprof.WriteHeapProfile(heapFile); err != nil {
			b.Fatal(err)
		}
	}

	ds := make([]time.Duration, 0, len(samples))
	var total, min, max time.Duration
	var work, errs int64
	for i, s := range samples {
		ds = append(ds, s.d)
		total += s.d
		work += s.work
		errs += s.errs
		if i == 0 || s.d < min {
			min = s.d
		}
		if s.d > max {
			max = s.d
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	median := ds[(len(ds)-1)/2]
	avg := total / time.Duration(len(ds))
	fmt.Printf("sqlite snapshot deep: runs=%d decisions=%d events=%d active_ids=%d iterations=%d\n", e.runs, e.decisions, e.events, len(e.ids), b.N)
	fmt.Printf("sqlite snapshot deep: median=%s mean=%s min=%s max=%s range=%s work=%d errs=%d\n",
		median, avg, min, max, max-min, work, errs)
	// The canonical row exposes min and max directly instead of only the
	// range, so a consumer never has to infer the sample spread.
	printPerfRow(os.Stdout, "snapshot", median, min, max, work, errs,
		"SQLite VM, ListRunEvents sort and JSON reflection")
	b.ReportMetric(float64(median.Nanoseconds()), "snapshot-median-ns")
	b.ReportMetric(float64(min.Nanoseconds()), "snapshot-min-ns")
	b.ReportMetric(float64(max.Nanoseconds()), "snapshot-max-ns")
	b.ReportMetric(float64((max - min).Nanoseconds()), "snapshot-range-ns")
	b.ReportMetric(float64(work), "snapshot-work")
	b.ReportMetric(float64(errs), "snapshot-errs")
}
