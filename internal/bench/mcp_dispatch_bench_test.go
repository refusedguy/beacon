package bench

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

const (
	// nameMCPDispatchEncode is the canonical advisory key for the dispatch
	// response encoding. The registry owns the limiter text.
	nameMCPDispatchEncode = "mcp-dispatch-encode"

	// The fixture follows the card scenario: 500 ready cards and two free
	// slots. Two runs start and the other 498 candidates are skipped.
	mcpDispatchReady = 500
	mcpDispatchSlots = 2
)

// mcpDispatchOutcome mirrors the compact outcome the dispatch tool builds for
// its summary field. Only the on-the-wire fields matter: this benchmark prices
// the duplicate bytes, not the mapping from a particular production type.
type mcpDispatchOutcome struct {
	ID       string `json:"id"`
	TaskID   string `json:"task_id"`
	State    string `json:"state"`
	Reason   string `json:"reason"`
	Launch   string `json:"launch_source,omitempty"`
	Identity string `json:"agent_identity,omitempty"`
}

// mcpDispatchTyped mirrors the typed response the dispatch tool emits. Its
// field order matches json.Marshal's alphabetical map-key order, so its bytes
// must equal the map's. The fixture check below proves that identity.
type mcpDispatchTyped struct {
	Failures []app.DispatchFailure `json:"failures"`
	Runs     []*core.Run           `json:"runs"`
	Skipped  []app.SkippedTask     `json:"skipped"`
	Summary  []mcpDispatchOutcome  `json:"summary"`
}

type mcpDispatchFixture struct {
	typed        mcpDispatchTyped
	summary      []mcpDispatchOutcome
	work         int64
	mapBytes     int
	summaryBytes int
}

// newMCPDispatchFixture builds the response once outside the timed loop. The
// compact summary and the full runs and skipped items stay separate so the
// byte counts expose how much of one response repeats the same candidates.
func newMCPDispatchFixture(tb testing.TB) mcpDispatchFixture {
	tb.Helper()
	f := mcpDispatchFixture{}
	runs := make([]*core.Run, 0, mcpDispatchSlots)
	skipped := make([]app.SkippedTask, 0, mcpDispatchReady-mcpDispatchSlots)
	f.summary = make([]mcpDispatchOutcome, 0, mcpDispatchReady)

	for i := 0; i < mcpDispatchReady; i++ {
		taskID := core.TaskID(fmt.Sprintf("T-DISPATCH-%03d", i))
		if i < mcpDispatchSlots {
			run := core.NewRun(core.RunID(fmt.Sprintf("run-dispatch-%03d", i)), taskID, fixtureNow)
			run.State = core.RunRunning
			run.LaunchSource = core.LaunchMCP
			run.AgentIdentity = "worker"
			runs = append(runs, run)
			f.summary = append(f.summary, mcpDispatchOutcome{
				ID: string(run.ID), TaskID: string(run.TaskID), State: string(run.State),
				Reason: string(run.State), Launch: string(run.LaunchSource), Identity: run.AgentIdentity,
			})
			continue
		}
		skip := app.SkippedTask{
			TaskID: taskID, Code: app.SkipSelectedScopeOverlap,
			Reason: fmt.Sprintf("scope overlaps T-DISPATCH-%03d on f:internal/infra/mcp/tool_dispatch.go", i%mcpDispatchSlots),
		}
		skipped = append(skipped, skip)
		f.summary = append(f.summary, mcpDispatchOutcome{
			ID: string(skip.TaskID), TaskID: string(skip.TaskID),
			State: "skipped", Reason: skip.Reason,
		})
	}
	f.typed = mcpDispatchTyped{Runs: runs, Skipped: skipped, Summary: f.summary}
	f.work = int64(len(f.summary))

	// The map is the shape this encoding replaced. Both must emit identical
	// bytes; the summary alone is the lower bound with no duplicate candidates.
	legacy := map[string]any{
		"runs":     runs,
		"skipped":  skipped,
		"failures": f.typed.Failures,
		"summary":  f.summary,
	}
	mapBytes, err := json.Marshal(legacy)
	if err != nil {
		tb.Fatalf("json.Marshal(map): %v", err)
	}
	typedBytes, err := json.Marshal(f.typed)
	if err != nil {
		tb.Fatalf("json.Marshal(typed): %v", err)
	}
	if !bytes.Equal(mapBytes, typedBytes) {
		tb.Fatalf("typed bytes differ from map bytes\nmap  %d bytes\ntyped %d bytes", len(mapBytes), len(typedBytes))
	}
	summaryBytes, err := json.Marshal(f.summary)
	if err != nil {
		tb.Fatalf("json.Marshal(summary): %v", err)
	}
	f.mapBytes = len(mapBytes)
	f.summaryBytes = len(summaryBytes)
	if f.summaryBytes >= f.mapBytes {
		tb.Fatalf("summary bytes %d are not below map bytes %d", f.summaryBytes, f.mapBytes)
	}
	return f
}

// TestMCPDispatchEncodeWorkStable pins the fixture. The summary count and both
// byte counts must be identical across builds; otherwise a change to the
// fixture would move a number a reader treats as fixed across base and head.
func TestMCPDispatchEncodeWorkStable(t *testing.T) {
	first := newMCPDispatchFixture(t)
	second := newMCPDispatchFixture(t)
	if first.work != second.work || first.mapBytes != second.mapBytes || first.summaryBytes != second.summaryBytes {
		t.Fatalf("dispatch fixture is not stable: %+v then %+v", first, second)
	}
	if first.work != mcpDispatchReady || first.mapBytes <= 0 || first.summaryBytes <= 0 {
		t.Fatalf("dispatch fixture = work %d, map %d bytes, summary %d bytes", first.work, first.mapBytes, first.summaryBytes)
	}
}

// BenchmarkMCPDispatchEncode times the typed response encoding for one dispatch
// with 500 ready cards and two free slots. It prints the map bytes and the
// summary-only bytes so the duplicate portion is visible, then emits one
// canonical row with fixed work and zero errors. Smoke mode uses the same
// fixture and assertions as a full run.
//
//	BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' -bench '^BenchmarkMCPDispatchEncode$' -benchtime=1x -count=1 -benchmem ./internal/bench/
func BenchmarkMCPDispatchEncode(b *testing.B) {
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	f := newMCPDispatchFixture(b)
	_ = f // built outside the timer; the byte counts are reported below

	b.ReportAllocs()
	durations := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		encoded, err := json.Marshal(f.typed)
		durations = append(durations, time.Since(start))
		if err != nil {
			b.Fatalf("json.Marshal(typed): %v", err)
		}
		if len(encoded) != f.mapBytes {
			b.Fatalf("encoded %d bytes, want %d", len(encoded), f.mapBytes)
		}
	}
	b.StopTimer()

	if b.N == 0 {
		b.Fatal("benchmark ran no passes")
	}
	median := medianDuration(durations)
	min, max := durations[0], durations[0]
	for _, d := range durations[1:] {
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
	}
	var errs int64

	fmt.Fprintf(os.Stdout, "dispatch encode: ready=%d slots=%d work=%d map_bytes=%d summary_bytes=%d duplicate_bytes=%d\n",
		mcpDispatchReady, mcpDispatchSlots, f.work, f.mapBytes, f.summaryBytes, f.mapBytes-f.summaryBytes)
	printPerfRow(os.Stdout, nameMCPDispatchEncode, median, min, max, f.work, errs, mcpDispatchEncodeLimiter)
	b.ReportMetric(float64(f.work), nameMCPDispatchEncode+"-work")
	b.ReportMetric(float64(errs), nameMCPDispatchEncode+"-errs")
	b.ReportMetric(float64(f.mapBytes), nameMCPDispatchEncode+"-map-bytes")
	b.ReportMetric(float64(f.summaryBytes), nameMCPDispatchEncode+"-summary-bytes")
}
