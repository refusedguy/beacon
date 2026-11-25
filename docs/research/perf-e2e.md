# End-to-end performance dossier: operator paths

This dossier measures the two paths an operator waits on. An earlier note named
likely limiters from source and ran no measurement. This one runs the paths and
prints the numbers. Every value below comes from the captured output at the end
of this file. There is no estimate and no speedup claim.

## Protocol

Date: 2026-11-25.

Machine: Linux x86_64, 8 logical CPUs, Intel Core i5-8250U at 1.60 GHz, 7.6 GiB
RAM. Toolchain: `go version go1.27.4-X:nodwarf5 linux/amd64`, `GOOS=linux`,
`GOARCH=amd64`, `CGO_ENABLED=1`.

Run both the operator paths and the older micro baseline with:

```sh
bash scripts/bench-e2e.sh
```

To keep a copy of the output, pass a path:

```sh
bash scripts/bench-e2e.sh /tmp/opencode/e2e-sample.txt
```

The script formats the benchmark file, then runs two Go commands:

```sh
go test -run '^$' -bench '^BenchmarkE2EOperatorPaths$' -benchtime=5x -count=1 ./internal/bench/
go test -run '^$' -bench '^BenchmarkHotPaths$' -benchtime=1x -count=1 ./internal/bench/
```

One `E2EOperatorPaths` invocation is five alternating pairs. Pair zero measures
reconcile first and dispatch second; pair one reverses the order, and so on.
Alternating removes a fixed ordering advantage from a shared machine. Go runs a
calibration pass at `b.N=1` before the requested `b.N=5`; both reports are in
the captured output. Only the `pairs=5` report is used below.

Fixtures are fixed and seeded:

| Fixture | Size | Built by |
| --- | --- | --- |
| active PRs | 20 | `newE2EEnv` |
| dispatch tasks | 4 | `newE2EEnv` |
| reconcile seed | 20261125 | `e2eSeed` |
| micro fixture runs | 120 | `runFixtures` in `internal/bench/` |

The SQLite store is real. The SCM and the agent harness are fakes. No network
call, subprocess or worktree is timed. A fresh state database is built outside
the timed section for every sample, so the work per sample does not drift as
runs and decisions accumulate. `GOMAXPROCS` is pinned to one for the timed
loop and restored afterwards. An untimed warmup pass primes the page cache and
the compiled query paths. The default `slog` handler is discarded during the
benchmark so log writes do not enter the timing or the captured output.

The decision sink records to SQLite but does not implement `DecisionLookup`.
Production deduplication would drop a repeated decision after the first pass
and cut the write count in later samples. Hiding the lookup keeps 20 persisted
decisions per tick in every sample. This is a harness choice, not a production
change.

Definitions used in the report:

- `median` is the upper middle of the five durations; `range` is max minus min.
- `work` is 20 decisions per reconcile tick and 4 captured prompts per dispatch
  call, summed over five pairs.
- `errs` counts failed `Reconcile` and `Dispatch` calls plus dispatch failures.
- `share`, the micro share, is one path's median divided by the sum of the two
  path medians. It says how much of one operator cycle each path occupies.
- The named limiter is the resource that bounds the path. The substep lines
  under each report support the name. They time representative calls, not every
  call, so they identify the dominant class rather than a complete profile.

## Reconcile: tick to decision

Production path, `internal/app/reconciler.go:59-91`:

```go
func (r *Reconciler) Reconcile(ctx context.Context, pr int) (decision core.MergeDecision, err error) {
	run, err := r.Store.FindByPR(ctx, pr)
	...
	obs, err := r.SCM.Observe(ctx, pr)
	...
	run.Checks = obs.Checks
	if err := r.Store.Save(ctx, run); err != nil {
		return core.MergeDecision{}, err
	}
	defer r.Decisions.logReconcileDecisionDeferred(ctx, pr, run, obs, &decision)
	...
	decision = r.Policy.Evaluate(obs)
	...
	return r.applyDecision(ctx, run, obs, decision)
}
```

Each tick finds the run, observes the PR, saves the checks, evaluates policy,
applies the verdict and logs the decision on return. The fixture policy returns
`VerdictWait`, so `applyDecision` reaches `handlePending`
(`internal/app/reconcile_observation.go:148-164`):

```go
if err := run.Apply(core.Event{Kind: core.EvChecksPending, HeadSHA: obs.HeadSHA}); err != nil {
	return decision, err
}
if run.ObserveSHA(obs.HeadSHA, obs.BaseSHA) {
	return decision, r.Store.Save(ctx, run)
}
```

The fake SCM returns a fresh head SHA on every observation, so that second
`Save` runs on every pass. The decision write happens in
`internal/app/decisionlog.go:170-199` and ends in
`Store.RecordDecision` at `internal/infra/sqlite/store.go:306-325`.

Limiter: SQLite writes, both the two `Save` calls per PR and the decision
insert. In sample 1, twenty `Save` calls cost 2.218 ms and twenty decision
inserts cost 1.057 ms, against 0.009 ms for twenty `Observe` calls and 0.387 ms
for twenty approval reads. Fake provider work is not the bound. Work is 100
decisions over five pairs. Both samples report zero errors.

## Dispatch: tool call to prompt

`beacon_dispatch` is a thin wrapper. It reads the parent session, then calls
`Dispatcher.Dispatch` (`internal/infra/mcp/tool_dispatch.go:33-45`). The prompt
is built inside `startOne` and handed to `Harness.Spawn`
(`internal/app/dispatcher.go:195-238`):

```go
session, err := d.Harness.Spawn(ctx, core.SpawnRequest{
	Task: task, Workspace: ws, RunID: run.ID,
	ParentSessionID: run.ParentSessionID, IsFixer: run.IsFixer,
	ModelRef: d.modelRef(run.ParentSessionID),
	Prompt:   spawnPrompt(task, run.ID, ws.Path, d.WorkerInstructions),
})
```

`spawnPrompt` (`internal/app/dispatcher.go:349-359`) is a `strings.Builder`
with no I/O. The fake harness captures the request, which is the endpoint of
this measurement. Before the spawn, `Dispatch` syncs four cards, lists ready
tasks and active runs, selects non-overlapping tasks, saves one run and marks
the card in the workspace. After the spawn it saves the run, sets task status
and logs transitions.

Limiter: SQLite writes for the four new runs and their task statuses. Sample 1
measures 0.595 ms for four run `Save` calls and 0.141 ms for four status
updates. The three read calls together cost about 0.8 ms, and the four harness
spawns are in-memory appends. The harness captured 8220 prompt bytes across
four spawns in each five-pair run, or 1644 bytes in a single call. Work is 20
prompts over five pairs. Both samples report zero errors and zero dispatch
failures.

## Micro share

The two paths are the micro components of one operator cycle. In both samples
reconcile takes 70 percent of the cycle and dispatch takes 30 percent. The
older six-hotspot benchmark is captured alongside each run for context:
10.152945 ms in sample 1 and 10.945302 ms in sample 2 on its own 120-run
fixture. That fixture is different from the 20-PR fixture above, so the two
totals are not subtracted or divided into a speedup claim.

## Sample 1

Captured with `bash scripts/bench-e2e.sh /tmp/opencode/e2e-sample-1.txt`.

```text
e2e paths: seed=20261125 active_prs=20 dispatch_tasks=4 pairs=1
path                 median      range       work    errs  share  limiter
reconcile-tick       9.055686ms  0s                20      0  60.0%  SQLite Save + decision writes
dispatch-prompt      4.887166ms  0s                 4      0  40.0%  SQLite run + task status writes
e2e cycle median: 13.942852ms (sum of path medians; share is each path's micro share)
reconcile substeps (median ms): observe=0.017 save=2.636 approved=0.429 decision=1.148
dispatch substeps (median ms): sync=0.177 ready=0.279 active=0.461 save=0.606 status=0.147 prompt-bytes=1644
goos: linux
goarch: amd64
pkg: github.com/refusedguy/beacon/internal/bench
cpu: Intel(R) Core(TM) i5-8250U CPU @ 1.60GHz
BenchmarkE2EOperatorPaths-8   	e2e paths: seed=20261125 active_prs=20 dispatch_tasks=4 pairs=5
path                 median      range       work    errs  share  limiter
reconcile-tick       9.969625ms  2.981254ms       100      0  70.0%  SQLite Save + decision writes
dispatch-prompt      4.490494ms  982.292µs         20      0  30.0%  SQLite run + task status writes
e2e cycle median: 14.460119ms (sum of path medians; share is each path's micro share)
reconcile substeps (median ms): observe=0.009 save=2.218 approved=0.387 decision=1.057
dispatch substeps (median ms): sync=0.187 ready=0.280 active=0.331 save=0.595 status=0.141 prompt-bytes=8220
       5	  36222660 ns/op	         0 dispatch-prompt-errs	   4490494 dispatch-prompt-median-ns	        30.00 dispatch-prompt-micro-share-pct	        20.00 dispatch-prompt-work	         0 reconcile-tick-errs	   9969625 reconcile-tick-median-ns	        70.00 reconcile-tick-micro-share-pct	       100.0 reconcile-tick-work
PASS
ok  	github.com/refusedguy/beacon/internal/bench	0.254s
hot paths: seed=20261125 runs=120 tasks=90 decisions=400 cards=60 wire=30 iterations=1
hot path             median      range       work    errs  share  limiter
sqlite-snapshot      7.582753ms  0s               695      0  74.7%  WAL page reads
card-parse           1.855978ms  0s               300      0  18.3%  yaml.Unmarshal + line scan
wire-encode          49.457µs    0s              7114      0   0.5%  encoding/json reflection
redaction            150.53µs    0s               296      0   1.5%  regexp pass chain
mcp-journal          112.167µs   0s                64      0   1.1%  mutex + linear eviction
sse-fanout           402.06µs    0s               192     64   4.0%  channel send + slow-view drop
end-to-end median: 10.152945ms (sum of hotspot medians)
goos: linux
goarch: amd64
pkg: github.com/refusedguy/beacon/internal/bench
cpu: Intel(R) Core(TM) i5-8250U CPU @ 1.60GHz
BenchmarkHotPaths-8   	       1	  10154522 ns/op	         0 card-parse-errs	   1855978 card-parse-median-ns	        18.28 card-parse-share-pct	       300.0 card-parse-work	         0 mcp-journal-errs	    112167 mcp-journal-median-ns	         1.105 mcp-journal-share-pct	        64.00 mcp-journal-work	         0 redaction-errs	    150530 redaction-median-ns	         1.483 redaction-share-pct	       296.0 redaction-work	         0 sqlite-snapshot-errs	   7582753 sqlite-snapshot-median-ns	        74.69 sqlite-snapshot-share-pct	       695.0 sqlite-snapshot-work	        64.00 sse-fanout-errs	    402060 sse-fanout-median-ns	         3.960 sse-fanout-share-pct	       192.0 sse-fanout-work	         0 wire-encode-errs	     49457 wire-encode-median-ns	         0.4871 wire-encode-share-pct	      7114 wire-encode-work
PASS
ok  	github.com/refusedguy/beacon/internal/bench	0.076s
```

## Sample 2

Captured with `bash scripts/bench-e2e.sh /tmp/opencode/e2e-sample-2.txt` on the
same machine, shortly after sample 1.

```text
e2e paths: seed=20261125 active_prs=20 dispatch_tasks=4 pairs=1
path                 median      range       work    errs  share  limiter
reconcile-tick       10.127699ms 0s                20      0  70.0%  SQLite Save + decision writes
dispatch-prompt      4.483156ms  0s                 4      0  30.0%  SQLite run + task status writes
e2e cycle median: 14.610855ms (sum of path medians; share is each path's micro share)
reconcile substeps (median ms): observe=0.020 save=2.349 approved=0.440 decision=1.164
dispatch substeps (median ms): sync=0.242 ready=0.681 active=0.782 save=0.667 status=0.146 prompt-bytes=1644
goos: linux
goarch: amd64
pkg: github.com/refusedguy/beacon/internal/bench
cpu: Intel(R) Core(TM) i5-8250U CPU @ 1.60GHz
BenchmarkE2EOperatorPaths-8   	e2e paths: seed=20261125 active_prs=20 dispatch_tasks=4 pairs=5
path                 median      range       work    errs  share  limiter
reconcile-tick       10.427926ms 2.907965ms       100      0  70.0%  SQLite Save + decision writes
dispatch-prompt      5.410388ms  1.792278ms        20      0  30.0%  SQLite run + task status writes
e2e cycle median: 15.838314ms (sum of path medians; share is each path's micro share)
reconcile substeps (median ms): observe=0.010 save=2.442 approved=0.417 decision=1.262
dispatch substeps (median ms): sync=0.199 ready=0.309 active=0.343 save=0.632 status=0.146 prompt-bytes=8220
       5	  43207416 ns/op	         0 dispatch-prompt-errs	   5410388 dispatch-prompt-median-ns	        30.00 dispatch-prompt-micro-share-pct	        20.00 dispatch-prompt-work	         0 reconcile-tick-errs	  10427926 reconcile-tick-median-ns	        70.00 reconcile-tick-micro-share-pct	       100.0 reconcile-tick-work
PASS
ok  	github.com/refusedguy/beacon/internal/bench	0.293s
hot paths: seed=20261125 runs=120 tasks=90 decisions=400 cards=60 wire=30 iterations=1
hot path             median      range       work    errs  share  limiter
sqlite-snapshot      8.172649ms  0s               695      0  74.7%  WAL page reads
card-parse           1.965432ms  0s               300      0  18.0%  yaml.Unmarshal + line scan
wire-encode          55.229µs    0s              7114      0   0.5%  encoding/json reflection
redaction            142.174µs   0s               296      0   1.3%  regexp pass chain
mcp-journal          111.222µs   0s                64      0   1.0%  mutex + linear eviction
sse-fanout           498.596µs   0s               192     64   4.6%  channel send + slow-view drop
end-to-end median: 10.945302ms (sum of hotspot medians)
goos: linux
goarch: amd64
pkg: github.com/refusedguy/beacon/internal/bench
cpu: Intel(R) Core(TM) i5-8250U CPU @ 1.60GHz
BenchmarkHotPaths-8   	       1	  10946971 ns/op	         0 card-parse-errs	   1965432 card-parse-median-ns	        17.96 card-parse-share-pct	       300.0 card-parse-work	         0 mcp-journal-errs	    111222 mcp-journal-median-ns	         1.016 mcp-journal-share-pct	        64.00 mcp-journal-work	         0 redaction-errs	    142174 redaction-median-ns	         1.299 redaction-share-pct	       296.0 redaction-work	         0 sqlite-snapshot-errs	   8172649 sqlite-snapshot-median-ns	        74.67 sqlite-snapshot-share-pct	       695.0 sqlite-snapshot-work	        64.00 sse-fanout-errs	    498596 sse-fanout-median-ns	         4.555 sse-fanout-share-pct	       192.0 sse-fanout-work	         0 wire-encode-errs	     55229 wire-encode-median-ns	         0.5046 wire-encode-share-pct	      7114 wire-encode-work
PASS
ok  	github.com/refusedguy/beacon/internal/bench	0.083s
```

## Scope of the measurement

This is a controller benchmark on a weak shared machine. It does not call
GitHub, start a real agent, render the monitor or touch a worktree. The SCM and
harness are fakes; the SQLite store is real. Dispatch is timed at
`Dispatcher.Dispatch`, one call below the MCP wrapper, because the wrapper only
extracts metadata and builds the result JSON. Use the path split and the
write-bound limiter from this dossier. Do not read the absolute latency as a
production-daemon number.
