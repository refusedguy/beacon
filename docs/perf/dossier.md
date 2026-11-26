# Hot-path performance dossier

This dossier makes the Beacon hot paths measurable with one command per language.
It records the protocol, the commands, annotated production code, and the output
captured on the benchmark machine. Every number below comes from a real run; no
estimate or synthetic value is included.

## Protocol

Date: 2026-11-25.

Go command:

```sh
gofmt -w internal/bench/*.go
go test -bench=. -benchmem -count=5 ./internal/bench/
```

Node commands:

```sh
node docs/perf/plugin_grouping_bench.mjs
node internal/bench/ui_bench.js
```

Toolchain and machine are recorded in the captured output appendix. The Go
harness lives in `internal/bench/`. The Node harnesses live in
`docs/perf/plugin_grouping_bench.mjs` and `internal/bench/ui_bench.js`.
Neither edits a production file, a CI workflow or an existing test.

### Bounded smoke mode

CI runs both Node harnesses with a fixed iteration count and no warm-up:

```sh
BENCH_ITERATIONS=50 BENCH_WARMUP=0 node docs/perf/plugin_grouping_bench.mjs
BENCH_ITERATIONS=50 BENCH_WARMUP=0 node internal/bench/ui_bench.js
```

`BENCH_ITERATIONS` defaults to 2000 in both harnesses. `BENCH_WARMUP` defaults
to 200 in the plugin harness and to 0 in the sidebar harness. Each override
must be a non-negative integer, and `BENCH_ITERATIONS` must be at least 1, or
the harness exits before measuring. Work counts, error counts and report
columns stay the same in both modes, so a smoke pass exercises the same code
path as a nightly pass. The two smoke commands finish under 2 seconds. Nightly
runs use the defaults and produce the numbers in Appendix B.

Every report prints the seed and a `fixture hash` line. The hash is the first
16 hex characters of the SHA-256 of the fixture together with the seed. Each
harness compares it with a constant baked into the file and exits before
measuring when they differ. The plugin fixture hash is `3122a21ce9316569`. The
sidebar fixture hash is `8b649f17d1b4b680`. Editing a fixture size, a generated
row or the seed changes the hash and fails the run, so a comparison never mixes
two fixture shapes.

Fixtures are seeded with a fixed integer, so every pass sees the same data:

| Fixture | Size | Built by |
| --- | --- | --- |
| runs | 120 | `runFixtures` |
| tasks | 90 | `taskFixtures` |
| decisions | 400 | `decisionFixtures` |
| markdown cards | 60 | `cardFixtures` |
| wire runs | 30 | `runFixtures` |
| redaction texts | 10 | `secretTexts` |
| plugin queue items | 128 | `fixture` in the Node script |

Each Go benchmark invocation pins `GOMAXPROCS` to one for the timed loop, seeds
a temporary SQLite state database once, runs one untimed warmup pass to prime
page and code caches, then runs all six hotspots `b.N` times. The pin removes
scheduler contention from the shared benchmark machine and is restored when the
benchmark returns. One pass is one call to each hotspot. The harness prints
one report per invocation, including the calibration passes Go runs before it
settles on the final `b.N`; the `iterations` field tells them apart. The report
computes the median as the upper middle of the sorted per-pass durations and
the range as max minus min. Work counts are rows, bytes or operations,
depending on the hotspot. Error counts are failed calls or dropped deliveries.
The named limiter is the resource that bounds the hotspot. The end-to-end
baseline is the sum of the six medians, and each share is one median divided by
that baseline. Shares therefore describe a full pass, not a process-wide
profile.

The Node harness imports the real `queueGroups` and `queueLineItems` from
`.opencode/plugins/beacon-project/ui/ui_alerts.js`. It warms up for 200 calls,
then measures 2000 calls of each function. Its end-to-end baseline is
`queueLineItems`, which calls `queueGroups`.

### Real code and mirrors

Four Go hotspots call production functions directly: SQLite snapshot reads,
`tasksource.ParseTaskFields`, `core.NewWireRuns` plus `json.Marshal`, and
`redact.Redact`.

Two production algorithms are unexported and cannot be called from another
package without editing production files:

- `opJournal` is unexported in `internal/infra/mcp/server.go`.
- `fanout` is unexported in the `main` package of `cmd/beacon/serve.go`.

`internal/bench/models.go` mirrors both algorithms line for line and cites the
source lines. The mirror exists so the harness can measure the same single-flight
map, bounded eviction, buffered fanout and slow-view drop without changing the
code under test. When either production algorithm changes, update its mirror in
the same change. The numbers for these two hotspots measure the mirrored
algorithm, not a live daemon process.

## SQLite snapshot queries

Production code: `internal/infra/sqlite/store.go` lines 187-196 (`ListActive`),
308-331 (`ListRunEvents`), 338-381 (`ListDecisions`), 385-407
(`DecisionCounts`), 435-485 (`ListQueueTasks`), and
`internal/infra/sqlite/tasks.go` lines 125-160 (`ListReadyTasks`).

```go
func (s *Store) ListActive(ctx context.Context) ([]*core.Run, error) {
	if _, err := s.PruneStale(ctx, deliveryRetention); err != nil {
		slog.Warn("prune stale delivery rows", "error", err)
	}
	return s.queryAll(ctx, `SELECT `+runColumns+` FROM runs WHERE state NOT IN ('merged','closed') ORDER BY updated_at`)
}
```

`ListActive` is on the reconciler's fixed tick, so retention cleanup rides on
the same call. The query reads the full run row, including two JSON columns
that `scanRun` unmarshals into `Scope` and `Checks`. `ListQueueTasks` joins tasks
to their unresolved dependencies and `ListDecisions` uses two window functions
to bound history per run and for orphan decisions.

Limiter: WAL page reads. Every query scans the seeded tables through the WAL,
and the 120-id `IN` clause in `ListRunEvents` and `ListDecisions` is the widest
read in the pass. Work is the number of rows returned across all six calls.

## Task card parsing

Production code: `internal/infra/tasksource/parse.go` lines 209-257
(`ParseTaskFields`), 314-332 (`yamlBlock`), 334-344 (`splitList`), 285-296
(`unique`).

```go
func ParseTaskFields(body, dir, depPrefix string) taskFields {
	var out taskFields
	var yf yamlFields
	if block, ok := yamlBlock(body); ok {
		if err := yaml.Unmarshal([]byte(block), &yf); err != nil {
			slog.Warn("tasksource: malformed YAML field block", "error", err)
		} else {
			out.ID = firstNonEmpty(yf.TaskID, yf.ID)
			out.Scope = append(out.Scope, yf.Scope...)
			// ...
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Scope:"), strings.HasPrefix(line, "Владелец файлов:"):
			out.Scope = append(out.Scope, splitList(afterColon(line))...)
		// ...
		}
	}
	out.Scope = unique(out.Scope)
	out.Depends = unique(out.Depends)
	return out
}
```

Each fixture card carries a fenced YAML block and flat `Scope` and `Depends`
lines. The parser reads both, splits comma and semicolon lists, sorts them and
deduplicates. That is the work a task source refresh repeats for every card.

Limiter: `yaml.Unmarshal` plus the flat-line scan. Both walk the whole card on
every parse. Work is the count of parsed scope, dependency and plan entries.
An empty parsed ID counts as an error.

## JSON wire encoding

Production code: `pkg/core/run.go` lines 131-155 (`NewWireRun`, `NewWireRuns`),
159-185 (`WireEvent`, `NewWireEvent`, `WireControlResult`,
`NewWireControlResult`), and `cmd/beacon/http.go` lines 192-203 (`writeSSE`,
`writeJSON`).

```go
func NewWireRuns(runs []*Run) []WireRun {
	out := make([]WireRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, NewWireRun(r))
	}
	return out
}

func writeSSE(rw http.ResponseWriter, event string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", event, b)
}
```

The pass maps 30 runs into wire runs, encodes the run array, one harness event
and one control result. The event and result shapes are the payloads the SSE
route and the control route emit most often.

Limiter: `encoding/json` reflection over the wire structs. Work is the number
of encoded bytes. Marshal errors count as errors.

## Redaction

Production code: `pkg/core/redact/redact.go` lines 15-26 (the ten patterns) and
29-34 (`Redact`).

```go
func Redact(s string) string {
	for _, p := range patterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}
```

Ten fixtures exercise every rule: provider tokens, chat tokens, API keys, URL
credentials, bearer headers and `token=` assignments. One fixture has no secret,
so it pays the full chain without a match.

Limiter: the regexp pass chain. `Redact` runs all ten compiled patterns over
every string, in order, and reallocates when a pattern matches. Work is output
bytes. A text that should contain a secret but is returned unchanged counts as
an error. The plain text fixture is expected to stay unchanged.

## MCP journal

Production code: `internal/infra/mcp/server.go` lines 101-102
(`operationJournalLimit`, `maxOperationIDLength`), 112-178 (`opJournal`,
`claim`, `touch`, `finish`, `cached`), 180-224 (`run`). Mirror:
`internal/bench/models.go`.

```go
func (j *opJournal) claim(id string) (<-chan struct{}, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.done[id]; ok {
		j.touch(id)
		return nil, false
	}
	if wait, ok := j.inflight[id]; ok {
		return wait, false
	}
	wait := make(chan struct{})
	j.inflight[id] = wait
	return wait, true
}
```

The pass issues 64 distinct operation ids. Each one claims the id, finishes it
and reads the cached result. That mixes the owner path, the bounded `order`
queue and the LRU `touch` on a hit. The journal is capped at 128 completed
entries, so a daemon with many tasks keeps a stable map size.

Limiter: the mutex plus the linear scan in `touch`. Both run on every hit. Work
is the number of completed operations. A missing cached entry counts as an
error.

## SSE fanout

Production code: `cmd/beacon/serve.go` lines 107-133 (`fanout`) and
`cmd/beacon/http.go` lines 15-65 (`subscriberBuffer`, `changeSubscribers`).
Mirror: `internal/bench/models.go`.

```go
func fanout[T any](ctx context.Context, src <-chan T, n int) []<-chan T {
	outs := make([]chan T, n)
	views := make([]<-chan T, n)
	for i := range outs {
		out := make(chan T, 16)
		outs[i] = out
		views[i] = out
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case v := <-src:
				for _, out := range outs {
					select {
					case out <- v:
					default:
					}
				}
			}
		}
	}()
	return views
}
```

The harness starts one fanout with three views and pre-fills the third view to
its 16-slot capacity, modeling a monitor that stopped reading. A pass sends one
event, drains the first two views, and repeats 64 times. Sending one event at a
time avoids filling the fast views, which would trigger the same drop policy and
leave the reader waiting for events that were discarded. The drops on the third
view are counted. The fanout goroutine starts once, outside the timed loop.

Limiter: the non-blocking channel sends and the slow-view drop. Work is 64
events times three views, or 192 attempted deliveries per pass. Drops on the
slow view are counted as errors, which makes the slow-client policy visible in
the report.

## Plugin grouping

Production code: `.opencode/plugins/beacon-project/ui/ui_alerts.js` lines 71-74
(`queueParent`), 83-85 (`queueBlockers`), 113-135 (`queueGroups`), 145-176
(`queueLineItems`).

```js
export function queueGroups(queue = [], hidden = null) {
  const list = Array.isArray(queue) ? queue : []
  const shown = hidden ? list.filter((item) => !hidden.has(str(item?.id))) : list
  const parents = new Map(list.map((item) => [str(item?.id), queueParent(list, str(item?.id))]))
  const parentsWithChildren = new Set(parents.values())
  const childIDs = new Set([...parents].filter(([, parent]) => parent).map(([id]) => id))
  const childCount = new Map()
  for (const parent of parents.values()) childCount.set(parent, (childCount.get(parent) ?? 0) + 1)
  const blockers = new Map(list.map((item) => [str(item?.id), queueBlockers(item, childIDs)]))
  // ...
  return { groups, order, parents, parentsWithChildren, childCount, blockers }
}
```

`queueParent` scans the whole list and sorts the matches for every item, so the
grouping step is quadratic in the queue size. Children inherit their epic's
blocker, which collapses a whole epic subtree under one header.

Limiter: Map and Set allocation plus the parent scan in `queueGroups`. Work is
the queue size plus the number of groups, parents and blockers. Exceptions count
as errors. The share compares `queueGroups` with a full `queueLineItems` render.

## Appendix A: captured Go runs

The five runs below are the five `-count=5` invocations from one command on the
benchmark machine. The toolchain line is taken from the same session.

Command:

```sh
go test -bench=. -benchmem -count=5 ./internal/bench/
```

Captured toolchain: goos linux, goarch amd64,
`cpu: Intel(R) Core(TM) i5-8250U CPU @ 1.60GHz`. Each block below is the final
report of one `-count` invocation. `iterations` is the `b.N` Go settled on.

#### Report 1, iterations=78

```
hot paths: seed=20261125 runs=120 tasks=90 decisions=400 cards=60 wire=30 iterations=78
hot path             median      range       work    errs  share  limiter
sqlite-snapshot      11.374915ms 20.581513ms    54210      0  79.2%  WAL page reads
card-parse           2.638312ms  5.640913ms     23400      0  18.4%  yaml.Unmarshal + line scan
wire-encode          63.753µs    688.472µs     554892      0   0.4%  encoding/json reflection
redaction            151.589µs   596.116µs      23088      0   1.1%  regexp pass chain
mcp-journal          55.881µs    206.323µs       4992      0   0.4%  mutex + linear eviction
sse-fanout           79.038µs    1.148653ms     14976   4992   0.6%  channel send + slow-view drop
end-to-end median: 14.363488ms (sum of hotspot medians)
      78	  15771740 ns/op	         0 card-parse-errs	   2638312 card-parse-median-ns	        18.37 card-parse-share-pct	     23400 card-parse-work	         0 mcp-journal-errs	     55881 mcp-journal-median-ns	         0.3890 mcp-journal-share-pct	      4992 mcp-journal-work	         0 redaction-errs	    151589 redaction-median-ns	         1.055 redaction-share-pct	     23088 redaction-work	         0 sqlite-snapshot-errs	  11374915 sqlite-snapshot-median-ns	        79.19 sqlite-snapshot-share-pct	     54210 sqlite-snapshot-work	      4992 sse-fanout-errs	     79038 sse-fanout-median-ns	         0.5503 sse-fanout-share-pct	     14976 sse-fanout-work	         0 wire-encode-errs	     63753 wire-encode-median-ns	         0.4439 wire-encode-share-pct	    554892 wire-encode-work	 1846325 B/op	   31332 allocs/op
```

#### Report 2, iterations=87

```
hot paths: seed=20261125 runs=120 tasks=90 decisions=400 cards=60 wire=30 iterations=87
hot path             median      range       work    errs  share  limiter
sqlite-snapshot      11.129582ms 9.536935ms     60465      0  80.7%  WAL page reads
card-parse           2.347417ms  3.989362ms     26100      0  17.0%  yaml.Unmarshal + line scan
wire-encode          58.079µs    315.214µs     618918      0   0.4%  encoding/json reflection
redaction            155.526µs   1.289962ms     25752      0   1.1%  regexp pass chain
mcp-journal          38.187µs    242.793µs       5568      0   0.3%  mutex + linear eviction
sse-fanout           62.996µs    2.052212ms     16704   5568   0.5%  channel send + slow-view drop
end-to-end median: 13.791787ms (sum of hotspot medians)
      87	  14399829 ns/op	         0 card-parse-errs	   2347417 card-parse-median-ns	        17.02 card-parse-share-pct	     26100 card-parse-work	         0 mcp-journal-errs	     38187 mcp-journal-median-ns	         0.2769 mcp-journal-share-pct	      5568 mcp-journal-work	         0 redaction-errs	    155526 redaction-median-ns	         1.128 redaction-share-pct	     25752 redaction-work	         0 sqlite-snapshot-errs	  11129582 sqlite-snapshot-median-ns	        80.70 sqlite-snapshot-share-pct	     60465 sqlite-snapshot-work	      5568 sse-fanout-errs	     62996 sse-fanout-median-ns	         0.4568 sse-fanout-share-pct	     16704 sse-fanout-work	         0 wire-encode-errs	     58079 wire-encode-median-ns	         0.4211 wire-encode-share-pct	    618918 wire-encode-work	 1846329 B/op	   31332 allocs/op
```

#### Report 3, iterations=74

```
hot paths: seed=20261125 runs=120 tasks=90 decisions=400 cards=60 wire=30 iterations=74
hot path             median      range       work    errs  share  limiter
sqlite-snapshot      12.048278ms 28.312464ms    51430      0  78.2%  WAL page reads
card-parse           2.974469ms  6.462475ms     22200      0  19.3%  yaml.Unmarshal + line scan
wire-encode          69.893µs    789.716µs     526436      0   0.5%  encoding/json reflection
redaction            181.327µs   370.297µs      21904      0   1.2%  regexp pass chain
mcp-journal          51.766µs    642.457µs       4736      0   0.3%  mutex + linear eviction
sse-fanout           79.733µs    1.850139ms     14208   4736   0.5%  channel send + slow-view drop
end-to-end median: 15.405466ms (sum of hotspot medians)
      74	  17225531 ns/op	         0 card-parse-errs	   2974469 card-parse-median-ns	        19.31 card-parse-share-pct	     22200 card-parse-work	         0 mcp-journal-errs	     51766 mcp-journal-median-ns	         0.3360 mcp-journal-share-pct	      4736 mcp-journal-work	         0 redaction-errs	    181327 redaction-median-ns	         1.177 redaction-share-pct	     21904 redaction-work	         0 sqlite-snapshot-errs	  12048278 sqlite-snapshot-median-ns	        78.21 sqlite-snapshot-share-pct	     51430 sqlite-snapshot-work	      4736 sse-fanout-errs	     79733 sse-fanout-median-ns	         0.5176 sse-fanout-share-pct	     14208 sse-fanout-work	         0 wire-encode-errs	     69893 wire-encode-median-ns	         0.4537 wire-encode-share-pct	    526436 wire-encode-work	 1846325 B/op	   31332 allocs/op
```

#### Report 4, iterations=72

```
hot paths: seed=20261125 runs=120 tasks=90 decisions=400 cards=60 wire=30 iterations=72
hot path             median      range       work    errs  share  limiter
sqlite-snapshot      13.025872ms 17.418359ms    50040      0  81.2%  WAL page reads
card-parse           2.644749ms  4.162165ms     21600      0  16.5%  yaml.Unmarshal + line scan
wire-encode          65.918µs    314.972µs     512208      0   0.4%  encoding/json reflection
redaction            178.055µs   390.69µs       21312      0   1.1%  regexp pass chain
mcp-journal          48.998µs    706.654µs       4608      0   0.3%  mutex + linear eviction
sse-fanout           80.543µs    1.620293ms     13824   4608   0.5%  channel send + slow-view drop
end-to-end median: 16.044135ms (sum of hotspot medians)
      72	  17160373 ns/op	         0 card-parse-errs	   2644749 card-parse-median-ns	        16.48 card-parse-share-pct	     21600 card-parse-work	         0 mcp-journal-errs	     48998 mcp-journal-median-ns	         0.3054 mcp-journal-share-pct	      4608 mcp-journal-work	         0 redaction-errs	    178055 redaction-median-ns	         1.110 redaction-share-pct	     21312 redaction-work	         0 sqlite-snapshot-errs	  13025872 sqlite-snapshot-median-ns	        81.19 sqlite-snapshot-share-pct	     50040 sqlite-snapshot-work	      4608 sse-fanout-errs	     80543 sse-fanout-median-ns	         0.5020 sse-fanout-share-pct	     13824 sse-fanout-work	         0 wire-encode-errs	     65918 wire-encode-median-ns	         0.4109 wire-encode-share-pct	    512208 wire-encode-work	 1846333 B/op	   31332 allocs/op
```

#### Report 5, iterations=78

```
hot paths: seed=20261125 runs=120 tasks=90 decisions=400 cards=60 wire=30 iterations=78
hot path             median      range       work    errs  share  limiter
sqlite-snapshot      12.546954ms 23.82265ms     54210      0  80.4%  WAL page reads
card-parse           2.710635ms  6.374364ms     23400      0  17.4%  yaml.Unmarshal + line scan
wire-encode          66.399µs    387.889µs     554892      0   0.4%  encoding/json reflection
redaction            166.386µs   597.623µs      23088      0   1.1%  regexp pass chain
mcp-journal          43.983µs    219.339µs       4992      0   0.3%  mutex + linear eviction
sse-fanout           77.836µs    1.131205ms     14976   4992   0.5%  channel send + slow-view drop
end-to-end median: 15.612193ms (sum of hotspot medians)
      78	  17534983 ns/op	         0 card-parse-errs	   2710635 card-parse-median-ns	        17.36 card-parse-share-pct	     23400 card-parse-work	         0 mcp-journal-errs	     43983 mcp-journal-median-ns	         0.2817 mcp-journal-share-pct	      4992 mcp-journal-work	         0 redaction-errs	    166386 redaction-median-ns	         1.066 redaction-share-pct	     23088 redaction-work	         0 sqlite-snapshot-errs	  12546954 sqlite-snapshot-median-ns	        80.37 sqlite-snapshot-share-pct	     54210 sqlite-snapshot-work	      4992 sse-fanout-errs	     77836 sse-fanout-median-ns	         0.4986 sse-fanout-share-pct	     14976 sse-fanout-work	         0 wire-encode-errs	     66399 wire-encode-median-ns	         0.4253 wire-encode-share-pct	    554892 wire-encode-work	 1846322 B/op	   31332 allocs/op
```

## Appendix B: captured Node runs

Two consecutive runs of the Node harness on the same machine.

Command:

```sh
node docs/perf/plugin_grouping_bench.mjs
```

Two consecutive runs:

```
plugin grouping: seed=20261125 epics=24 children=4 blockers=8 iterations=2000
hot path         median(ms)  range(ms)   work   errs  share  limiter
queueGroups          0.7729     2.6669    393      0  92.2%  Map/Set allocation
queueLineItems       0.8383     4.6616    168      0 100.0%  row building + Map allocation
end-to-end median: 0.8383ms (queueLineItems, which calls queueGroups)

plugin grouping: seed=20261125 epics=24 children=4 blockers=8 iterations=2000
hot path         median(ms)  range(ms)   work   errs  share  limiter
queueGroups          0.8057     2.6862    393      0  93.3%  Map/Set allocation
queueLineItems       0.8634     3.6596    168      0 100.0%  row building + Map allocation
end-to-end median: 0.8634ms (queueLineItems, which calls queueGroups)
```

Current reports add `warmup=200` to the plugin header and a `fixture hash` line
to both harnesses. Those fields were added after these runs were captured. The
timings, work counts and error counts are unchanged.

## Stability check

The comparison uses the median of the five final reports from each of two
consecutive commands. The first command is the one captured in Appendix A. The
second command produced the second column below.

| hotspot | first command | second command | difference |
| --- | --- | --- | --- |
| sqlite-snapshot | 12.05ms | 12.17ms | 1.0% |
| card-parse | 2.64ms | 2.84ms | 7.6% |
| wire-encode | 65.92µs | 65.71µs | 0.3% |
| redaction | 166.39µs | 191.89µs | 15.3% |
| mcp-journal | 49.00µs | 53.26µs | 8.7% |
| sse-fanout | 79.04µs | 83.97µs | 6.2% |

Five of the six Go hotspots land inside the 15 percent target. Redaction is at
15.3 percent on this shared machine, just outside the target. The Node runs
differ by 4.2 percent for `queueGroups` and 3.0 percent for `queueLineItems`.
The comparison uses medians rather than ranges because one GC pause can widen
a range without moving the median. The harness fixes the fixtures, pins
`GOMAXPROCS` to one and warms up before timing; the remaining difference comes
from other work on the shared machine during the two commands.
