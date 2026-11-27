package bench

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

const (
	nameSSEParse       = "sse-parse"
	sseParseStreamName = "fixed CRLF SSE stream"
	sseParseLimiter    = "bufio.Scanner 64 KB buffer plus per-line string ops"
)

// sseParseStreamLines is a fixed stream. It carries comments, repeated data
// fields, CRLF endings and a final frame with no terminating blank line, so
// the parser must run with SSEKeepPartial.
var sseParseStreamLines = []string{
	": heartbeat",
	"id: 1",
	"event: run.updated",
	`data: {"id":1}`,
	`data: {"n":2}`,
	"",
	"id: 2",
	"event: ready",
	`data: {"type":"ready"}`,
	"",
	": comment between frames",
	"id: 3",
	"data: alpha",
	"data: beta",
	"",
	"id: 4",
	"event: done",
	`data: {"type":"partial"}`,
}

var sseParseWant = []core.SSEFrame{
	{ID: "1", Event: "run.updated", Data: "{\"id\":1}\n{\"n\":2}"},
	{ID: "2", Event: "ready", Data: `{"type":"ready"}`},
	{ID: "3", Data: "alpha\nbeta"},
	{ID: "4", Event: "done", Data: `{"type":"partial"}`},
}

func sseParseStream() string { return strings.Join(sseParseStreamLines, "\r\n") }

// runSSEParse parses one fixed stream and checks every frame against the
// fixture. Work is frames delivered plus data bytes emitted. Errs is parse
// errors plus frames whose ID, Event or Data differs from the fixture. The
// limiter count is the number of lines the scanner reads.
func runSSEParse() (work, errs, lines int64) {
	got := make([]core.SSEFrame, 0, len(sseParseWant))
	err := core.ParseSSE(strings.NewReader(sseParseStream()), core.SSEKeepPartial, func(f core.SSEFrame) {
		got = append(got, f)
		work++
		work += int64(len(f.Data))
	})
	if err != nil {
		errs++
	}
	if len(got) > len(sseParseWant) {
		errs += int64(len(got) - len(sseParseWant))
	} else if len(got) < len(sseParseWant) {
		errs += int64(len(sseParseWant) - len(got))
	}
	for i := 0; i < len(got) && i < len(sseParseWant); i++ {
		if got[i] != sseParseWant[i] {
			errs++
		}
	}
	return work, errs, int64(len(sseParseStreamLines))
}

// printSSEReport writes the SSE parse line to stdout so a benchmark run emits
// work, errs and the named limiter without -v.
func printSSEReport(w io.Writer, work, errs, lines int64, iterations int) {
	fmt.Fprintf(w, "%s: stream=%s frames=%d data-bytes=%d lines=%d iterations=%d work=%d errs=%d limiter=%s\n",
		nameSSEParse, sseParseStreamName, len(sseParseWant), work-int64(len(sseParseWant)), lines, iterations, work, errs, sseParseLimiter)
}

// TestParseSSEFramesWorkStable pins the counts the benchmark reports. Two
// passes over the same fixture produce the same work, error and line counts,
// so a parse change that drops a frame cannot pass unnoticed.
func TestParseSSEFramesWorkStable(t *testing.T) {
	work, errs, lines := runSSEParse()
	againWork, againErrs, againLines := runSSEParse()
	if work != againWork || errs != againErrs || lines != againLines {
		t.Fatalf("sse parse counts are not stable: first=%d/%d/%d second=%d/%d/%d", work, errs, lines, againWork, againErrs, againLines)
	}
	if work <= 0 || errs != 0 || lines <= 0 {
		t.Fatalf("sse parse work=%d errs=%d lines=%d, want positive work and lines with no errors", work, errs, lines)
	}
}

// BenchmarkParseSSEFrames times ParseSSE on the fixed stream. The loopback
// monitor and the OpenCode harness both call it, but only the fanout mirror
// has a timing row. Fixtures are seeded once outside the timed loop and work
// is the total frames plus emitted data bytes of one pass, so smoke and full
// modes report the same counts.
//
// CI runs a bounded smoke pass with BEACON_BENCH_SMOKE=1 and -benchtime=1x:
//
//	BEACON_BENCH_SMOKE=1 GOMAXPROCS=1 go test -vet=off -run '^$' -bench '^BenchmarkParseSSEFrames$' -benchtime=1x -count=1 -benchmem ./internal/bench/
//
// Smoke mode skips the warm-up parse. The fixture and every count stay the
// same.
func BenchmarkParseSSEFrames(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	if !smokeMode() {
		_, _, _ = runSSEParse()
	}

	b.ReportAllocs()
	b.ResetTimer()
	ds := make([]time.Duration, 0, b.N)
	var totalWork, totalErrs, totalLines int64
	for i := 0; i < b.N; i++ {
		start := time.Now()
		work, errs, lines := runSSEParse()
		ds = append(ds, time.Since(start))
		totalWork += work
		totalErrs += errs
		totalLines += lines
	}
	b.StopTimer()

	passes := int64(b.N)
	if passes == 0 {
		b.Fatal("benchmark ran no passes")
	}
	work := totalWork / passes
	errs := totalErrs / passes
	lines := totalLines / passes
	median := medianDuration(ds)

	printSSEReport(os.Stdout, work, errs, lines, int(passes))
	b.ReportMetric(float64(median.Nanoseconds()), nameSSEParse+"-median-ns")
	b.ReportMetric(float64(work), nameSSEParse+"-work")
	b.ReportMetric(float64(errs), nameSSEParse+"-errs")
	b.ReportMetric(float64(lines), nameSSEParse+"-limiter-scanner-lines")
}
