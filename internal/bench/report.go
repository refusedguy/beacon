package bench

import (
	"fmt"
	"io"
	"sort"
	"time"
)

// Hotspot names are fixed so every report has the same row order.
const (
	nameSQLite  = "sqlite-snapshot"
	nameCard    = "card-parse"
	nameWire    = "wire-encode"
	nameRedact  = "redaction"
	nameJournal = "mcp-journal"
	nameFanout  = "sse-fanout"
)

var hotspotOrder = []string{nameSQLite, nameCard, nameWire, nameRedact, nameJournal, nameFanout}

// limiters name the resource that bounds each hotspot. The dossier explains
// why each one is the limiter and points at the annotated production code.
var limiters = map[string]string{
	nameSQLite:  "WAL page reads",
	nameCard:    "yaml.Unmarshal + line scan",
	nameWire:    "encoding/json reflection",
	nameRedact:  "regexp pass chain",
	nameJournal: "mutex + linear eviction",
	nameFanout:  "channel send + slow-view drop",
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

// printReport writes the human-readable part of the benchmark report. It goes
// to stdout so `go test -bench=. -benchmem -count=5` emits it without -v.
func printReport(w io.Writer, sums []summary, total time.Duration, iterations int) {
	fmt.Fprintf(w, "hot paths: seed=%d runs=%d tasks=%d decisions=%d cards=%d wire=%d iterations=%d\n",
		fixtureSeed, fixtureRuns, fixtureTasks, fixtureDecisions, fixtureCards, fixtureWireRuns, iterations)
	fmt.Fprintln(w, "hot path             median      range       work    errs  share  limiter")
	for _, s := range sums {
		fmt.Fprintf(w, "%-20s %-11s %-11s %8d %6d %5.1f%%  %s\n",
			s.Name, s.Median, s.Max-s.Min, s.Work, s.Errors, s.Share, s.Limiter)
	}
	fmt.Fprintf(w, "end-to-end median: %s (sum of hotspot medians)\n", total)
}
