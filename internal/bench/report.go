package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

// perfPrefix marks one canonical row in a benchmark log. The payload is JSON
// so limiter strings may contain spaces.
const perfPrefix = "beacon-perf: "

// perfRow is the fixed payload of every canonical row. Consumers validate the
// field set, so no harness may add another field or rename one. Cache names the
// cache policy the measurement ran under, so two rows from different policies
// can never be read as the same sample.
type perfRow struct {
	Key      string  `json:"key"`
	MedianMS float64 `json:"median_ms"`
	MinMS    float64 `json:"min_ms"`
	MaxMS    float64 `json:"max_ms"`
	Work     int64   `json:"work"`
	Errs     int64   `json:"errs"`
	Limiter  string  `json:"limiter"`
	Cache    string  `json:"cache"`
}

// benchCache returns the cache state stamped on every canonical row. CI sets
// BEACON_BENCH_CACHE when it runs a harness under a named policy; without an
// override the row reports "unset" instead of omitting the field.
func benchCache() string {
	if value := os.Getenv("BEACON_BENCH_CACHE"); value != "" {
		return value
	}
	return "unset"
}

// perfLine renders one canonical row. Benchmarks time in nanoseconds and the
// advisory report reads in milliseconds, so the published values are ms.
func perfLine(key string, median, min, max time.Duration, work, errs int64, limiter string) string {
	data, err := json.Marshal(perfRow{
		Key:      key,
		MedianMS: float64(median) / float64(time.Millisecond),
		MinMS:    float64(min) / float64(time.Millisecond),
		MaxMS:    float64(max) / float64(time.Millisecond),
		Work:     work,
		Errs:     errs,
		Limiter:  limiter,
		Cache:    benchCache(),
	})
	if err != nil {
		return ""
	}
	return perfPrefix + string(data)
}

// printPerfRow writes one canonical row after the human-readable line. Every
// harness prints both so a reader and a parser see the same measurement.
func printPerfRow(w io.Writer, key string, median, min, max time.Duration, work, errs int64, limiter string) {
	fmt.Fprintln(w, perfLine(key, median, min, max, work, errs, limiter))
}

// Hotspot names are fixed so every report has the same row order.
const (
	nameSQLite       = "sqlite-snapshot"
	nameCard         = "card-parse"
	nameWire         = "wire-encode"
	nameRedact       = "redaction"
	nameJournal      = "mcp-journal"
	nameFanout       = "sse-fanout"
	nameSCM          = "scm-observe"
	nameCardLocal    = "card-local-parse"
	nameHTTPSnapshot = "http-snapshot-assembly"
	nameSpawn        = "harness-spawn"
)

var hotspotOrder = []string{
	nameSQLite, nameCard, nameWire, nameRedact, nameJournal, nameFanout,
	nameSCM, nameCardLocal, nameHTTPSnapshot, nameSpawn,
}

// namePrompt names the standalone prompt benchmark. Prompt construction is not
// one of the ten alternating hotspots, so it gets its own report line.
const namePrompt = "prompt-build"

// limiters name the resource that bounds each hotspot. The dossier explains
// why each one is the limiter and points at the annotated production code.
var limiters = map[string]string{
	nameSQLite:       "indexed snapshot reads + WAL pages",
	nameCard:         "yaml.Unmarshal + line scan",
	nameWire:         "encoding/json reflection",
	nameRedact:       "regexp pass chain",
	nameJournal:      "mutex + indexed LRU",
	nameFanout:       "json.Marshal + subscriber maps + slow-view drop",
	nameSCM:          "loopback PR + compare + checks + files round trips",
	nameCardLocal:    "yaml.Unmarshal + per-scope os.Stat",
	nameHTTPSnapshot: "groupHistory + json reflection",
	nameSpawn:        "loopback create + model POST + deferred move",
}

type sample struct {
	name string
	d    time.Duration
	work int64
	errs int64
}

type summary struct {
	Name    string
	Median  time.Duration
	Min     time.Duration
	Max     time.Duration
	Work    int64
	Errors  int64
	Limiter string
	Share   float64
}

// summarize groups samples by hotspot. Median is the upper middle duration of
// the sorted samples. Range is max minus min. Work and errors are totals over
// the whole benchmark invocation, so they stay comparable between runs.
func summarize(samples []sample) []summary {
	durations := make(map[string][]time.Duration)
	work := make(map[string]int64)
	errs := make(map[string]int64)
	for _, s := range samples {
		durations[s.name] = append(durations[s.name], s.d)
		work[s.name] += s.work
		errs[s.name] += s.errs
	}
	out := make([]summary, 0, len(hotspotOrder))
	for _, name := range hotspotOrder {
		ds := durations[name]
		if len(ds) == 0 {
			continue
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		out = append(out, summary{
			Name:    name,
			Median:  ds[len(ds)/2],
			Min:     ds[0],
			Max:     ds[len(ds)-1],
			Work:    work[name],
			Errors:  errs[name],
			Limiter: limiters[name],
		})
	}
	return out
}

// endToEnd is the sum of the per-hotspot medians. It is the baseline for each
// share, so shares describe one full pass over all six hotspots.
func endToEnd(sums []summary) time.Duration {
	var total time.Duration
	for _, s := range sums {
		total += s.Median
	}
	return total
}

// printReport writes the human-readable part of the benchmark report. It goes
// to stdout so `go test -bench=. -benchmem -count=5` emits it without -v.
func printReport(w io.Writer, sums []summary, total time.Duration, iterations int) {
	fmt.Fprintf(w, "hot paths: seed=%d runs=%d tasks=%d decisions=%d cards=%d wire=%d iterations=%d\n",
		fixtureSeed, fixtureRuns, fixtureTasks, fixtureDecisions, fixtureCards, fixtureWireRuns, iterations)
	fmt.Fprintln(w, "hot path             median      range       work    errs  share  limiter")
	for _, s := range sums {
		fmt.Fprintf(w, "%-20s %-11s %-11s %8d %6d %5.1f%%  %s\n",
			s.Name, s.Median, s.Max-s.Min, s.Work, s.Errors, s.Share, s.Limiter)
		printPerfRow(w, s.Name, s.Median, s.Min, s.Max, s.Work, s.Errors, s.Limiter)
	}
	marshal, delivered, dropped := takeFanoutCounts()
	fmt.Fprintf(w, "sse-fanout (8 and 64 subscribers): marshal=%d delivery=%d drop=%d\n", marshal, delivered, dropped)
	fmt.Fprintf(w, "end-to-end median: %s (sum of hotspot medians)\n", total)
}

// Full snapshot phases. Unlike the hotspot rows above, these two phases are
// additive: reads are the SQL work and assembly is the in-memory work plus
// encoding, so their medians sum to one full snapshot CPU.
const (
	phaseReads    = "snapshot-reads"
	phaseAssembly = "snapshot-assembly"
	phaseTotal    = "snapshot-total"
)

var snapshotLimiters = map[string]string{
	phaseReads:    "WAL page reads",
	phaseAssembly: "groupHistory + json reflection",
	phaseTotal:    "sum of both phases",
}

// phaseSample is one timed phase of one alternating pass.
type phaseSample struct {
	d    time.Duration
	work int64
	errs int64
}

// snapshotPhase is the measured result of one phase over all passes.
type snapshotPhase struct {
	Name   string
	Median time.Duration
	Min    time.Duration
	Max    time.Duration
	Work   int64
	Errs   int64
	Bytes  int
	Allocs float64
	Share  float64
}

// medianDuration returns the upper middle value of ds without reordering the
// caller's slice.
func medianDuration(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// summarizePhase folds the samples of one phase into a row. Work and errors
// are totals over every pass so they stay comparable between runs.
func summarizePhase(name string, samples []phaseSample) (snapshotPhase, []time.Duration) {
	ds := make([]time.Duration, 0, len(samples))
	var work, errs int64
	var min, max time.Duration
	for i, s := range samples {
		ds = append(ds, s.d)
		work += s.work
		errs += s.errs
		if i == 0 || s.d < min {
			min = s.d
		}
		if s.d > max {
			max = s.d
		}
	}
	out := snapshotPhase{
		Name:   name,
		Median: medianDuration(ds),
		Min:    min,
		Max:    max,
		Work:   work,
		Errs:   errs,
	}
	return out, ds
}

// snapshotShares sets each phase's share of one full snapshot CPU. The total
// row is the baseline and reports 100 percent; it is excluded from the sum so
// the two real phases cannot double count it.
func snapshotShares(phases []snapshotPhase) []snapshotPhase {
	out := make([]snapshotPhase, len(phases))
	copy(out, phases)
	var total time.Duration
	for _, p := range out {
		if p.Name != phaseTotal {
			total += p.Median
		}
	}
	if total <= 0 {
		return out
	}
	for i := range out {
		if out[i].Name == phaseTotal {
			out[i].Share = 100
			continue
		}
		out[i].Share = 100 * float64(out[i].Median) / float64(total)
	}
	return out
}

// printSnapshotReport writes the full snapshot report to stdout so
// `go test -bench=BenchmarkFullSnapshot -benchmem -count=5` emits it without -v.
func printSnapshotReport(w io.Writer, phases []snapshotPhase, fixture string, passes int) {
	fmt.Fprintf(w, "full snapshot: %s passes=%d\n", fixture, passes)
	fmt.Fprintln(w, "phase                median      range       work    errs   bytes  allocs  share  limiter")
	for _, p := range phases {
		fmt.Fprintf(w, "%-20s %-11s %-11s %8d %6d %7d %7.1f %5.1f%%  %s\n",
			p.Name, p.Median, p.Max-p.Min, p.Work, p.Errs, p.Bytes, p.Allocs, p.Share, snapshotLimiters[p.Name])
		printPerfRow(w, p.Name, p.Median, p.Min, p.Max, p.Work, p.Errs, snapshotLimiters[p.Name])
	}
	fmt.Fprintln(w, "shares are of one full snapshot CPU: reads plus assembly, not the hot-path micro benchmark")
}

// printSnapshotRepeatability writes the spread across the five alternating
// passes so a reader can judge repeatability without re-reading every report.
func printSnapshotRepeatability(w io.Writer, name string, ds []time.Duration) {
	median := medianDuration(ds)
	var min, max time.Duration
	if len(ds) > 0 {
		min, max = ds[0], ds[0]
		for _, d := range ds {
			if d < min {
				min = d
			}
			if d > max {
				max = d
			}
		}
	}
	pct := float64(0)
	if median > 0 {
		pct = 100 * float64(max-min) / float64(median)
	}
	fmt.Fprintf(w, "%s: five alternating passes median=%s range=%s repeatability=%.1f%% of median\n",
		name, median, max-min, pct)
}

// printPromptReport writes the standalone prompt benchmark line to stdout so
// `go test -bench=BenchmarkSpawnPrompt -benchmem` emits it without -v. Work is
// the total prompt bytes and does not depend on timer resolution.
func printPromptReport(w io.Writer, work, errs int64, tasks, iterations int) {
	fmt.Fprintf(w, "%s: tasks=%d iterations=%d work=%d errs=%d\n",
		namePrompt, tasks, iterations, work, errs)
}
