package bench

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
)

// BenchmarkSnapshotApproaches compares the four candidate optimisations on one
// shared fixture. It exists so the deep dossier can reject three of them with
// numbers instead of prose. The fixture is smaller than BenchmarkSQLiteSnapshot
// so the four variants can be alternated many times in one command.
func BenchmarkSnapshotApproaches(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	runs := deepSize("BEACON_APPROACH_RUNS", 20000)
	decisions := deepSize("BEACON_APPROACH_DECISIONS", 100000)
	events := deepSize("BEACON_APPROACH_EVENTS", 400000)
	active := deepSize("BEACON_APPROACH_ACTIVE", 1000)

	dir, err := os.MkdirTemp("", "beacon-approach-")
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
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	seedDeepFixture(b, db, runs, decisions, events, active, deepRecent)

	ids := make([]string, 0, active+deepRecent)
	for i := 0; i < active+deepRecent; i++ {
		ids = append(ids, fmt.Sprintf("run-%06d", i))
	}
	ctx := context.Background()
	_, _ = store.DecisionCounts(ctx, ids)
	_, _ = store.ListRunEvents(ctx, ids)

	summary := buildSummaryTable(b, ctx, db)
	cache := &countsCache{key: fmt.Sprint(ids)}
	var cacheHits int
	seq := int64(0)

	type variant struct {
		name string
		fn   func() (int64, bool)
	}
	variants := []variant{
		{"cover", func() (int64, bool) {
			counts, err := store.DecisionCounts(ctx, ids)
			return int64(len(counts)), err == nil
		}},
		{"summary", func() (int64, bool) {
			rows, err := summary.QueryContext(ctx,
				`SELECT kind, verdict, action, SUM(n) FROM decision_counts_summary
WHERE run_id IN (`+placeholders(len(ids))+`) OR run_id = ''
GROUP BY kind, verdict, action`, idsArgs(ids)...)
			if err != nil {
				return 0, false
			}
			n := 0
			for rows.Next() {
				n++
			}
			err = rows.Err()
			rows.Close()
			return int64(n), err == nil
		}},
		{"events", func() (int64, bool) {
			events, err := store.ListRunEvents(ctx, ids)
			return int64(len(events)), err == nil
		}},
		{"stream", func() (int64, bool) {
			n, err := streamRunEvents(ctx, db, ids)
			return n, err == nil
		}},
		{"cache", func() (int64, bool) {
			// Reconcile writes between snapshots invalidate a naive cache. One
			// decision per pass models the steady write stream on a live daemon.
			// A cache that skipped invalidation would return stale counts and
			// change the verdicts in the snapshot.
			seq++
			if err := store.RecordDecision(ctx, app.Decision{
				Kind: "bench.write", RunID: ids[int(seq)%len(ids)],
			}); err != nil {
				return 0, false
			}
			cache.invalidate()
			if v, ok := cache.get(fmt.Sprint(ids)); ok {
				cacheHits++
				return int64(len(v)), true
			}
			counts, err := store.DecisionCounts(ctx, ids)
			if err != nil {
				return 0, false
			}
			cache.put(fmt.Sprint(ids), counts)
			return int64(len(counts)), true
		}},
	}

	const passes = 5
	type result struct {
		name           string
		d              []time.Duration
		work, failures int64
	}
	results := make([]result, len(variants))
	for pass := 0; pass < passes; pass++ {
		for i, v := range variants {
			results[i].name = v.name
			start := time.Now()
			work, ok := v.fn()
			results[i].d = append(results[i].d, time.Since(start))
			results[i].work += work
			if !ok {
				results[i].failures++
			}
		}
	}
	fmt.Printf("snapshot approaches: runs=%d decisions=%d events=%d active_ids=%d passes=%d cache_hits=%d\n", runs, decisions, events, len(ids), passes, cacheHits)
	for _, r := range results {
		ds := append([]time.Duration(nil), r.d...)
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		median := ds[(len(ds)-1)/2]
		fmt.Printf("  %-8s median=%-12s range=%-12s work=%-8d fails=%d\n",
			r.name, median, ds[len(ds)-1]-ds[0], r.work, r.failures)
	}
}

func buildSummaryTable(b *testing.B, ctx context.Context, db *sql.DB) *sql.DB {
	b.Helper()
	if _, err := db.ExecContext(ctx, `CREATE TABLE decision_counts_summary (
run_id TEXT NOT NULL, kind TEXT NOT NULL, verdict TEXT NOT NULL, action TEXT NOT NULL,
n INTEGER NOT NULL, PRIMARY KEY (run_id, kind, verdict, action))`); err != nil {
		b.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO decision_counts_summary
SELECT run_id, kind, verdict, action, COUNT(*) FROM decisions
GROUP BY run_id, kind, verdict, action`); err != nil {
		b.Fatal(err)
	}
	// The maintained variant needs a write path. A trigger keeps the summary
	// in step with RecordDecision, which is what production would have to do.
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER decision_counts_summary_insert
AFTER INSERT ON decisions BEGIN
INSERT INTO decision_counts_summary (run_id, kind, verdict, action, n)
VALUES (NEW.run_id, NEW.kind, NEW.verdict, NEW.action, 1)
ON CONFLICT (run_id, kind, verdict, action) DO UPDATE SET n = n + 1;
END`); err != nil {
		b.Fatal(err)
	}
	return db
}

func placeholders(n int) string {
	out := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		out += "?"
	}
	return out
}

func idsArgs(ids []string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// streamRunEvents mirrors ListRunEvents but hands rows through a channel
// before collecting them, which is the shape a "streaming" change would take.
func streamRunEvents(ctx context.Context, db *sql.DB, ids []string) (int64, error) {
	query := `SELECT id, run_id, kind, state, at, head_sha, pr_number, reason
FROM run_events WHERE run_id IN (` + placeholders(len(ids)) + `) ORDER BY at, id`
	rows, err := db.QueryContext(ctx, query, idsArgs(ids)...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type item struct{ id int64 }
	ch := make(chan item, 256)
	go func() {
		defer close(ch)
		for rows.Next() {
			var id, at int64
			var runID, kind, state, headSHA, reason string
			var pr int
			if err := rows.Scan(&id, &runID, &kind, &state, &at, &headSHA, &pr, &reason); err != nil {
				return
			}
			ch <- item{id: id}
		}
	}()
	var n int64
	for range ch {
		n++
	}
	return n, rows.Err()
}

type countsCache struct {
	key   string
	value []app.DecisionCount
}

func (c *countsCache) get(key string) ([]app.DecisionCount, bool) {
	return c.value, key == c.key
}

func (c *countsCache) put(key string, value []app.DecisionCount) {
	c.key, c.value = key, value
}

func (c *countsCache) invalidate() {
	c.key, c.value = "", nil
}
