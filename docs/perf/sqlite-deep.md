# Deep SQLite snapshot performance

This note extends `dossier.md`, which measured the snapshot path with 120 runs
and 400 decisions. That fixture was too small to expose the real limiter: the
dossier's 11.37 ms median came from six queries over a few hundred rows, and the
named limiter was "WAL page reads". This note scales every table a status
snapshot reads and records what actually bounds the path.

## Protocol

Commands, in order, on the benchmark machine (`Intel(R) Core(TM) i5-8250U CPU
@ 1.60GHz`, linux/amd64, eight logical CPUs, load average 7 to 9 during the
later runs):

```sh
gofmt -w internal/infra/sqlite/store.go internal/infra/sqlite/store_test.go
go test -bench=BenchmarkSQLiteSnapshot -benchmem -count=5 ./internal/bench/
BEACON_DEEP_CPUPROFILE=/tmp/opencode/deep-cpu.out \
BEACON_DEEP_MEMPROFILE=/tmp/opencode/deep-mem.out \
  go test -bench=BenchmarkSQLiteSnapshot -benchtime=20x -count=1 ./internal/bench/
go tool pprof -top -nodecount=15 /tmp/opencode/deep-cpu.out
go tool pprof -top -nodecount=15 -alloc_space /tmp/opencode/deep-mem.out
go test -bench=BenchmarkSnapshotApproaches -benchtime=1x ./internal/bench/
```

Each `BenchmarkSQLiteSnapshot` invocation pins `GOMAXPROCS` to one, seeds the
fixture in one transaction with set-based inserts, runs one untimed warmup pass,
then times full snapshot reads. `-count=5` repeats the invocation five times,
each with a fresh database. Go's calibration sometimes raises `b.N` to two,
three or four when a pass finishes inside the one-second target; each such pass
is a timed snapshot read. A pass issues the eight store reads the snapshot
service performs and reports rows returned (`work`) and failed calls (`errs`).
The report prints median, mean, min, max and range across the timed passes of
one invocation. The five final reports are the five alternating runs. The
profiles start after the fixture and warmup, so they describe the timed loop
rather than millions of fixture inserts.

The fixture is fixed and seeded:

| Table | Rows | Distribution |
| --- | --- | --- |
| `runs` | 100,000 | 1,000 nonterminal, 20 closed, rest merged |
| `decisions` | 500,000 | 25% orphan (`run_id = ''`), rest spread over all runs |
| `run_events` | 2,000,000 | round robin over all runs, six kinds |
| snapshot `run_id` set | 1,020 | 1,000 active plus 20 recent |

No task cards are seeded, so the queue and ready-task reads return zero rows.
That keeps the measured work in the three tables that scale. Every pass in
every run returned 25,348 rows with zero errors.

## Baseline

Five final reports from `-count=5` against the original queries:

| Run | Median |
| --- | --- |
| 1 | 1.697 s |
| 2 | 2.690 s |
| 3 | 3.167 s |
| 4 | 2.032 s |
| 5 | 1.620 s |

Median of the five medians: **2.032 s**. Range across the five: **1.547 s**.
The spread is machine noise from other work on the shared host, so the
comparison below uses medians and looks for a change much larger than the
range.

A per-query trace of one baseline pass gives the cause:

| Store read | Duration | Rows |
| --- | --- | --- |
| `ListDecisions` | 1.53 s / 0.84 s | 3,925 |
| `DecisionCounts` | 538 ms / 527 ms | 3 |
| `ListRecent` | 553 ms / 317 ms | 20 |
| `ListRunEvents` | 188 ms / 152 ms | 20,400 |
| `ListActive` | 102 ms / 57 ms | 1,000 |
| `ListQueueTasks` | 9 ms / 0.4 ms | 0 |
| `ListReadyTasks` | 1.7 ms | 0 |
| `CurrentIncident` | 0.15 ms | 0 |

Three reads dominate. `ListDecisions` numbers 125,000 orphan rows with a window
function before applying `rn <= 100`. `DecisionCounts` scans all 500,000
decision rows because the `run_id IN (...) OR run_id = ''` predicate cannot use
an index, and the scan reads `reason` and `fields_json` that the aggregate never
uses. `ListRecent` sorts nearly 98,000 terminal runs to find 20.

## Attempts and results

### 1. Covering indexes (kept)

Three indexes are created on open, so an older state database gains them without
a schema version bump:

```sql
CREATE INDEX runs_state_updated_idx ON runs(state, updated_at DESC);
CREATE INDEX decisions_run_kind_verdict_action_idx
  ON decisions(run_id, kind, verdict, action);
CREATE INDEX decisions_orphan_at_idx ON decisions(run_id, at DESC)
  WHERE run_id = '';
```

The first turns `ListRecent` into three top-N index scans. The second lets
counts be read from index entries. The third gives the orphan bucket a direct
path to its newest rows.

### 2. Orphan rows by LIMIT, not ROW_NUMBER (kept)

The old orphan CTE numbered all `run_id = ''` rows, then kept the newest 100.
The new CTE keeps them directly:

```sql
SELECT <cols> FROM decisions
WHERE run_id = '' ORDER BY at DESC, id DESC LIMIT 100
```

The outer `ORDER BY at, id` is unchanged, so the returned rows and their order
are identical. The limit is a literal because `modernc.org/sqlite` returns
`datatype mismatch` when this `LIMIT` takes a bound parameter.

After attempts 1 and 2, `ListDecisions` fell from 1.53 s / 0.84 s to 91 ms /
100 ms. The orphan sort was the largest single cost in the baseline.

### 3. Counting from the decisions table with a covering index (rejected)

Counting the two buckets separately removed the `OR` scan:

```sql
SELECT kind, verdict, action, SUM(n) FROM (
  SELECT kind, verdict, action, COUNT(*) AS n FROM decisions
    WHERE run_id IN (...) GROUP BY kind, verdict, action
  UNION ALL
  SELECT kind, verdict, action, COUNT(*) AS n FROM decisions
    WHERE run_id = '' GROUP BY kind, verdict, action
) GROUP BY kind, verdict, action
```

This kept the covering index and never touched the wide text columns. At full
scale it took **65 ms / 79 ms**, down from 538 ms / 527 ms. It was rejected in
favour of attempt 4, which took 11 ms / 12 ms on the same fixture and left the
decision rows untouched.

### 4. Maintained summary table (kept)

`decision_summary` holds one row per `(run_id, kind, verdict, action)` with a
count. A trigger maintains it on every decision insert, and an idempotent
backfill on open makes an older state database correct. `DecisionCounts` reads
only this small table:

```sql
SELECT kind, verdict, action, SUM(n) FROM (
  SELECT kind, verdict, action, n FROM decision_summary WHERE run_id IN (...)
  UNION ALL
  SELECT kind, verdict, action, n FROM decision_summary WHERE run_id = ''
) GROUP BY kind, verdict, action ORDER BY kind, verdict, action
```

At full scale this took **12 ms / 11 ms**. The five-pass comparison on the
smaller fixture put it at **10.8 ms** against **32.0 ms** for attempt 3, with the
same 19 rows. The cost is one UPSERT per decision insert; the backfill test
covers a database written before the table existed.

### 5. ListRecent union (kept)

Each terminal state is already ordered by `updated_at` in the covering index.
Taking `limit` rows from each state and merging them avoids sorting every
finished run:

```sql
SELECT <cols> FROM (
  SELECT * FROM (SELECT <cols> FROM runs WHERE state='merged'
                 ORDER BY updated_at DESC LIMIT 20)
  UNION ALL ... 'closed' ... UNION ALL ... 'dropped' ...
) ORDER BY updated_at DESC LIMIT 20
```

The overall newest 20 rows are always inside that three-way union. `ListRecent`
fell from **553 ms / 317 ms** to **2.4 ms / 2.0 ms**.

### 6. Streaming rows through a channel (rejected)

A variant moved `ListRunEvents` rows through a buffered channel before
collecting them. On five alternating passes over 400,000 events it took
**204.9 ms** against **224.0 ms** for the direct read. Both returned 102,000
rows with zero failures. The 19 ms difference sits inside the 68 ms range of
the direct read, and the variant only scanned raw columns: building the
`app.RunEvent` structs the snapshot service needs would add work back. There is
no result to stream to, because the caller groups the full slice by run.

### 7. Bounded cache with invalidation (rejected)

A cache keyed by the `run_id` set was tried against `DecisionCounts`. One
decision was written per pass to model the reconcile stream, and the write
invalidated the cache. Five passes produced **0 hits out of 5** and took
**30.4 ms** against **32.0 ms** for the uncached read. A cache that skipped
invalidation would return stale counts and change the verdicts in the snapshot.
With invalidation it has no hits to serve, so it only adds bookkeeping.

## Result

Five final reports from `-count=5` after all kept changes:

| Run | Median | Per-pass range |
| --- | --- | --- |
| 1 | 569 ms | 197 ms |
| 2 | 432 ms | 113 ms |
| 3 | 394 ms | 54 ms |
| 4 | 319 ms | 49 ms |
| 5 | 1.229 s | 69 ms |

Median of the five medians: **432 ms**. That is **4.7x** faster than the 2.032 s
baseline median. Run 5 landed during a load spike; the trace and the profiles
below come from quieter invocations. Every pass returned 25,348 rows with zero
errors, the same work and error counts as the baseline.

The final per-query trace:

| Store read | Duration | Rows |
| --- | --- | --- |
| `ListRunEvents` | 280 ms / 276 ms | 20,400 |
| `ListDecisions` | 96 ms / 113 ms | 3,925 |
| `ListActive` | 71 ms / 92 ms | 1,000 |
| `DecisionCounts` | 12 ms / 11 ms | 3 |
| `ListRecent` | 2.4 ms / 2.0 ms | 20 |
| `ListQueueTasks` | 2.7 ms / 0.3 ms | 0 |
| `ListReadyTasks` | 2 ms | 0 |
| `CurrentIncident` | 0.13 ms | 0 |

## Profiles

CPU profile, 15.12 s over 20 snapshot reads, 14.36 s of samples:

```
      flat  flat%   cum   cum%
    1920ms 13.37%  8150ms 56.75%  modernc.org/sqlite/lib._sqlite3VdbeExec
    1730ms 12.05%  1730ms 12.05%  internal/runtime/syscall/linux.Syscall6
     570ms  3.97%   570ms  3.97%  runtime.memmove
     440ms  3.06%  1620ms 11.28%  modernc.org/sqlite/lib._sqlite3BtreeTableMoveto
     280ms  1.95%   550ms  3.83%  modernc.org/libc.Xpthread_mutex_unlock
     230ms  1.60%   350ms  2.44%  modernc.org/sqlite/lib._pcache1FetchNoMutex
     170ms  1.18%   520ms  3.62%  modernc.org/sqlite/lib._vdbeSorterMerge
```

Allocation profile, 622.63 MB across 20 reads:

```
      flat  flat%   cum   cum%
  353.49MB 56.77% 450.54MB 72.36%  sqlite.(*Store).ListRunEvents
   74.23MB 11.92% 121.82MB 19.57%  sqlite.(*Store).ListDecisions
   57.50MB  9.24%   113MB 18.15%  modernc.org/sqlite.(*rows).Next
   55.50MB  8.91%  55.50MB  8.91%  modernc.org/sqlite.(*conn).columnText
   23.51MB  3.78%  27.51MB  4.42%  sqlite.scanRun
```

The CPU profile is dominated by the SQLite virtual machine and the syscall time
that reads WAL pages. `_vdbeSorterMerge` is the global event sort. The allocation
profile puts 72% of all bytes in `ListRunEvents`, which reads and decodes every
event for the active set.

## Remaining limiter

`ListRunEvents` is now the floor at about **276 ms** per pass. It returns 20,400
events for 1,020 runs, ordered by `at, id`. The index on `(run_id, at)` already
gives each per-run lookup in order; the remaining work is the global merge sort
and the construction of 20,400 `app.RunEvent` values. Neither can be removed
without changing what the caller receives:

- The snapshot groups these events into per-run timelines and puts them on the
  wire in `at, id` order. Dropping the sort is a wire change.
- Capping the events per run would leave timelines incomplete. That is the
  hidden result reduction the task forbids.
- The eight store reads already run in one pass with batched `IN` queries. There
  is no extra round trip to remove.

The same argument holds for the next two reads. `ListActive` must return all
1,000 active runs with their scope and checks, because the wire run shape
carries both. `ListDecisions` must return up to 20 decisions per run plus 100
orphans, because the operator panel shows that history. The floor is the cost
of reading, decoding and ordering the rows the snapshot contract requires.

## Correctness

`TestSnapshotReadsKeepExactRows` pins the exact decision IDs and order for
`ListDecisions`, and the exact groups and counts from `DecisionCounts`, including
the case where a `(kind, verdict, action)` group appears in both the run bucket
and the orphan bucket. `TestDecisionSummaryBackfillOnOpen` drops the summary
table and reopens the database to check that the backfill restores the counts.
`TestListRecent` pins the recent-run order. The benchmark reports the same
25,348 rows and zero errors before and after every change. No verdict value,
wire field or result cap was changed.

## Retention and reverse-edge indexes (T-3bd24007)

The prune tick filters `deliveries` and `approvals` on `created_at`. The
ready-task recursive step joins `task_deps` on `depends_on`. Neither column had
an index. This note records the read benefit and the write cost of three new
indexes created in `schema.go`:

```sql
CREATE INDEX deliveries_created_at_idx ON deliveries(created_at);
CREATE INDEX approvals_created_at_idx ON approvals(created_at);
CREATE INDEX task_deps_depends_on_task_idx ON task_deps(depends_on, task_id);
```

### Protocol

One process opened two SQLite databases, one with the three `CREATE INDEX`
statements and one without. It seeded identical fixtures into both, then ran
the two variants in an alternating order, old then new, for five passes each.
Each pass recorded elapsed time, rows touched and errors. The machine had eight
logical CPUs and a load average near 2 to 3, so both variants saw roughly the
same noise. The harness was temporary and is not part of the source tree.

The prune fixture holds 20,000 rows in each of `deliveries` and `approvals`.
Most rows have `created_at` at the current time. The last 2,000 rows are 31 days
old, and the prune cutoff is 30 days. Each `PruneStale` pass removes 1,000 rows
from each table. The harness replenishes those rows outside the timed region.

### Results

| Experiment | Old median | New median | Old range | New range | Work | Errors |
| --- | --- | --- | --- | --- | --- | --- |
| Prune filter only | 3.565 ms | 544 us | 3.226 to 6.284 ms | 416 to 695 us | 1,000 rows per pass | 0 |
| Full `PruneStale` | 9.531 ms | 10.391 ms | 8.912 to 12.754 ms | 7.783 to 12.948 ms | 2,000 rows per pass | 0 |
| Dependency walk | 525.971 ms | 446.666 ms | 433.066 to 565.751 ms | 430.066 to 491.188 ms | 1 ready task | 0 |
| Write cost, 10,000 inserts | 22.297 ms | 31.030 ms | 21.090 to 27.401 ms | 25.805 to 36.313 ms | 10,000 rows per pass | 0 |

Values are medians and ranges over five passes per side. The filter and the
full prune read the same fixture. The filter isolates the two `created_at`
subqueries, and the full prune includes the deletes.

### Read effect

The filter is faster on every pass. `EXPLAIN QUERY PLAN` shows the cause. The
old database scans the table and skips the recent rows before it finds 1,000
matches. The new database searches `deliveries_created_at_idx` and
`approvals_created_at_idx` and reads the first 1,000 matching entries. The
median falls from 3.565 ms to 544 us, about 6.5x, with no overlap between the
two ranges.

The full `PruneStale` call does not show a measurable gain. Its median moves
from 9.531 ms to 10.391 ms, and the ranges overlap. Deleting 2,000 rows
dominates the call, so the faster filter sits inside that cost. The limiter is
the delete work, and the index does not change it.

The dependency walk also shows no clear gain. `EXPLAIN QUERY PLAN` shows that
the old database already builds an automatic covering index on `depends_on`
for the recursive step. The new database uses the persistent index instead and
skips that build. The recursive step and the union dedup dominate the walk, so
the saved index build is small against them. The ranges overlap, so this result
is inconclusive rather than a speedup.

### Measured write cost

Both `deliveries` and `approvals` take an insert on every reconcile tick, so the
two `created_at` indexes add work to a write-hot path. The harness inserted
10,000 rows per pass and alternated sides for five passes. The old database had
a median of 22.297 ms. The new database had a median of 31.030 ms, about 39%
slower. The new side was slower in all five alternating pairs. That cost is the
price of maintaining the two indexes and is recorded here as the requirement
for shipping them.

### Correctness

The indexes add no rows, columns or caps. `PruneStale`, `ListReadyTasks` and the
snapshot reads keep their result shapes. Every recorded pass returned the
expected row count with zero errors, so the work counts match across sides.

## Composite run read indexes (T-2c814a94)

`FindByPR`, `FindActiveByTask`, `ListActive` and `ListByIncident` each filter on
one column and sort by `updated_at`, but only single-column indexes served them.
This note records the read gain from four composite indexes and the write cost
of carrying them.

```sql
CREATE INDEX runs_pr_updated_idx ON runs(pr_number, updated_at DESC);
CREATE INDEX runs_task_updated_idx ON runs(task_id, updated_at DESC);
CREATE INDEX runs_active_updated_idx ON runs(updated_at)
  WHERE state NOT IN ('merged','closed');
CREATE INDEX runs_incident_state_idx ON runs(blocked_incident_id, state);
```

`runs_state_idx` was removed from the base schema and is dropped by an
idempotent `DROP INDEX IF EXISTS` on open. It was a strict prefix of
`runs_state_updated_idx`, which the snapshot indexes already supply, so it only
added write cost. The drop runs after `openDB` and before the store is used, so
an older state database loses the index on the next open.

### Protocol

A temporary harness opened two SQLite databases with the same 20,000 run rows.
The old side kept the pre-task indexes (`runs_pr_idx`, `runs_task_idx`,
`runs_state_updated_idx` and the now-dropped `runs_state_idx`). The new side
added the four indexes above. Each pass ran the four store queries in the same
order on both sides, old then new, five times. The harness recorded elapsed
time and rows returned. It is not part of the source tree.

`EXPLAIN QUERY PLAN` on the new side confirms each query takes its intended
path. No `USE TEMP B-TREE FOR ORDER BY` step remains on any of the four:

| Store read | Plan |
| --- | --- |
| `FindByPR` | `SEARCH runs USING INDEX runs_pr_updated_idx (pr_number=?)` |
| `FindActiveByTask` | `SEARCH runs USING INDEX runs_task_updated_idx (task_id=?)` |
| `ListActive` | `SCAN runs USING INDEX runs_active_updated_idx` |
| `ListByIncident` | `SEARCH runs USING INDEX runs_incident_state_idx (blocked_incident_id=? AND state=?)` |

The partial index matches `ListActive` because its predicate is exactly
`state NOT IN ('merged','closed')`. A scan of that index yields rows in
`updated_at` order, so the query no longer sorts the active set.

### Results

Values are medians and ranges over five alternating passes. The work column is
rows returned per pass by the four queries combined. Every pass returned the
same count with zero errors.

| Experiment | Old median | New median | Old range | New range | Work | Errors |
| --- | --- | --- | --- | --- | --- | --- |
| Four filtered reads | 16.789 ms | 10.068 ms | 15.876 to 18.887 ms | 9.068 to 14.820 ms | 8,400 rows per pass | 0 |
| Write cost, 10,000 inserts | 209.234 ms | 259.202 ms | 162.675 to 278.349 ms | 237.967 to 285.072 ms | 10,000 rows per pass | 0 |

### Read effect

The new side was faster on every one of the five alternating pairs. The gain
comes from removing two kinds of work. `FindByPR` and `FindActiveByTask` seek
the composite index and stop at the first matching row, instead of seeking the
single-column index and sorting the matches by `updated_at`. `ListActive` scans
the partial index in key order and skips the sort of the active rows.
`ListByIncident` satisfies both predicates from one index entry.

The write side shows the price. Each run insert now maintains four extra
indexes and no longer maintains the dropped prefix, a net gain of three. The
new side was slower in all five pairs, with a median about 24% above the old
one. Run inserts are frequent on the orchestrator path, so this cost is
recorded here as the condition for shipping the indexes.

### Correctness

The indexes add no rows, columns or caps. The four queries keep their result
shapes and order. The migration test opens a database that still carries
`runs_state_idx` and confirms the index is gone after `Open`. Every recorded
pass returned the same row count with zero errors, so the work counts match
across sides.

## perf-smoke timeout (T-be8aa55b)

PRs #2618, #2619 and #2620 failed perf-smoke with exit 124 after exactly 30 s.
The step runs `timeout 30s bash scripts/bench-e2e.sh --smoke`. Timing the stages
on the benchmark machine separated two costs.

| Stage | Measured cost |
| --- | --- |
| Cold `go test` compile of `./internal/bench/` | 71.63 s, or 138 s under load |
| Warm smoke, whole script, five runs | 4.03, 2.99, 3.77, 2.89, 3.59 s |
| One measured pass of both benchmarks | about 0.2 s |

The cold compile is the stage that crosses 30 s. At the time of those PRs the
perf-smoke job restored the Go build cache with `cache-dependency-path: go.sum`,
while build, vet, test and lint used `go.sum` plus `ui/tui/go.sum`. The two keys
did not match, so perf-smoke recompiled the dependency graph, including
`modernc.org/sqlite`. That workflow change is not made here. It landed on main
in PR #2620. A CI run after that fix still hit exit 124 because of the
single-P compile described below.

The script had a smaller defect of its own. T-c894c2fa added the
`BEACON_BENCH_SMOKE` override so each benchmark skips its warm-up pass, and both
benchmarks document a smoke command that sets it. `scripts/bench-e2e.sh --smoke`
never set the variable. `BenchmarkHotPaths` therefore ran all six hotspots a
second time, and `BenchmarkE2EOperatorPaths` built two extra state databases and
ran one reconcile tick and one dispatch call before the measured pair. The
smoke branch now exports `BEACON_BENCH_SMOKE=1` and passes `-vet=off` before the
`go test` call. That removes the redundant passes and the vet run that the CI
vet job already performs. It does not set `GOMAXPROCS`. An earlier version
of this change did, and the first CI run after it still hit exit 124. Setting
`GOMAXPROCS=1` serializes the `go test` compile. In two fresh caches on the same
machine, the default compile finished in 138 s while the single-P compile was
still running after 160 s and was stopped. The benchmarks already pin P to one
inside the timed loop, so the external setting only slows the build.

Five runs after the final change finished in 4.03, 2.99, 3.77, 2.89 and 3.59 s
and reported identical counts: sqlite-snapshot 695/0, card-parse 300/0,
wire-encode 7114/0, redaction 296/0, mcp-journal 64/0, sse-fanout 192/64,
reconcile-tick 20/0 and dispatch-prompt 4/0. The 64 on sse-fanout is the
designed slow-view drop, not a failed call. Fixture sizes, the nightly gate and
the 30 s step timeout are unchanged.

## Audit table retention (T-a5ae7a69)

`decisions` and `run_events` had no prune while deliveries and approvals were capped at 30 days. This change adds both tables to the `PruneStale` tick and indexes their `at` column so the filter searches the oldest rows. The window stays at 30 days because the status snapshot reads recent history from both tables. No result cap or wire field changes.

```sql
CREATE INDEX IF NOT EXISTS run_events_at_idx ON run_events(at);
CREATE INDEX IF NOT EXISTS decisions_at_idx ON decisions(at);
CREATE TRIGGER IF NOT EXISTS decision_summary_delete
AFTER DELETE ON decisions BEGIN
  UPDATE decision_summary SET n = n - 1
  WHERE run_id = OLD.run_id AND kind = OLD.kind
    AND verdict = OLD.verdict AND action = OLD.action;
  DELETE FROM decision_summary WHERE run_id = OLD.run_id AND kind = OLD.kind
    AND verdict = OLD.verdict AND action = OLD.action AND n <= 0;
END
```

The trigger keeps `decision_summary` equal to the rows still stored. Without it, `DecisionCounts` would keep counting decisions that retention removed. `PruneStale` gains two bounded deletes:

```sql
DELETE FROM decisions WHERE id IN (SELECT id FROM decisions WHERE at < ? LIMIT ?);
DELETE FROM run_events WHERE id IN (SELECT id FROM run_events WHERE at < ? LIMIT ?);
```

Each table loses at most `retentionBatchSize` rows per tick, as the delivery and approval deletes already do.

### Protocol

One process opened two SQLite databases through `Open`. The old side dropped the two `at` indexes after open, which reproduces the pre-change write path. Both sides kept the summary insert and delete triggers, so the measured difference is the two indexes. The machine had eight logical CPUs and a load average near 1.8.

For the write harness, each pass inserted 5,000 decisions and 5,000 run events in one transaction. The timed region covered the inserts and the commit. The two sides ran in alternating order, old then new, for five passes each. The harness counted inserted rows and errors.

For the prune harness, each database held 20,000 decisions and 20,000 run events with `at` 31 days old. Each prune removed up to 1,000 rows per table, and the harness replenished 1,000 rows of each kind outside the timed region. The two sides alternated for five passes each. The harness counted removed rows and errors.

### Results

Values are medians and ranges over five passes per side. Every pass returned the counted rows with zero errors.

| Experiment | Old median | New median | Old range | New range | Work | Errors |
| --- | --- | --- | --- | --- | --- | --- |
| Write cost, 5,000 decisions + 5,000 events | 346.9 ms | 403.3 ms | 309.1 to 381.0 ms | 353.2 to 421.8 ms | 10,000 rows per pass | 0 |
| Full `PruneStale` | 11.63 ms | 13.34 ms | 10.57 to 13.16 ms | 12.82 to 18.53 ms | 2,000 rows per pass | 0 |

### Write cost

The two indexes cost writes. The new side was slower in four of the five alternating insert pairs. The medians are 346.9 ms and 403.3 ms, about 16% apart. The ranges overlap between 353.2 ms and 381.0 ms, so the size of the gap is approximate and the direction is the reliable result. Each insert maintains one extra B-tree entry in each table, and that work is the limiter.

The prune call was slower on the new side in all five pairs, with medians 11.63 ms and 13.34 ms, about 15% apart. `EXPLAIN QUERY PLAN` shows the new side searching `decisions_at_idx` and `run_events_at_idx` for the filter. The old side scans both tables. Deleting 2,000 rows and their index entries dominates the filter saving, the same result the delivery and approval prune showed in the earlier section.

### Correctness

A temporary test seeded five old and three recent decisions and run events. One prune removed the ten old rows and left the three recent ones. `ListDecisions`, `LastDecision` and `DecisionCounts` all returned the recent decisions, and `decision_summary` fell from 8 to 3 to match the remaining rows. A second prune removed nothing. The prune bounds row counts and lets SQLite reuse freed pages. It does not compact the file. `PRAGMA page_count` stayed at 164 while `PRAGMA freelist_count` rose from 0 to 57. No verdict, cap or wire field changed.

## CI placement (T-b186b563)

Where a benchmark runs matters more than its absolute number. The nightly job
on the `perf` runner supplies every comparison used as a gate. No number in
this note is re-measured on a GitHub-hosted PR runner.

| Path | Plans | Gate |
| --- | --- | --- |
| Every PR | bounded `bench-e2e.sh --smoke` inside the root bench test leg | compile and hang signal only, no timing gate |
| PR labeled `perf` | six cheap plans: planner, prompt, MCP journal, redaction, TUI view, HTTP frames | advisory two-tree comparison, always exits zero |
| Nightly `schedule` | HotPaths and E2EOperatorPaths against the fixed `bench-baseline` artifact | 2 percent noise floor, 15 and 20 percent limits |
| `workflow_dispatch` | the ten heavy plans, including SQLiteSnapshot, FullSnapshot, the 1001-tick plugin watcher and a bounded git-worktree latency proxy | full suite on the perf runner |

The labeled PR job checks out two detached worktrees at the merge base and at
`github.event.pull_request.head.sha`, copies harness changes onto both sides,
and runs A B B A B passes. It writes per-side CSVs and a comparison CSV, keeps
raw logs and CSVs for seven days, and updates one comment behind the
`<!-- beacon-pr-perf -->` marker. Rows whose ranges overlap are reported as
inconclusive rather than as gains. GitHub-hosted and self-hosted numbers are
never compared.

The nightly comparison script rejects a baseline whose observed median is more
than `BEACON_BASELINE_SANITY_FACTOR` times the recorded median, with a default
factor of three. A missing baseline fails the job explicitly instead of leaving
a green run with no comparison. A regressed run cannot publish a new baseline
because the publish step follows the guarded comparison. No required Criterion
name is added for any advisory job.

Wall clock for a normal PR is reported from GitHub Actions, not from a local
run. Where the measured ranges overlap, the result is inconclusive. Local
checks on a weak machine stay limited to format and the targeted package tests.
