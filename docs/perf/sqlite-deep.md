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
