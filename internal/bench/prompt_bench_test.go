package bench

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

const nameWake = "wake-prompts"

// wakeBenchContext is the fixture for the correction-prompt benchmark. The
// excerpt is long enough to be cut, so the shared truncator runs on every
// iteration.
func wakeBenchContext() core.WakeContext {
	return core.WakeContext{
		RunID:     "run-0042",
		PRNumber:  17,
		HeadSHA:   "abc123def456",
		Reason:    "review blocked by a failed gate",
		Excerpt:   strings.Repeat("gate output line\n", 16),
		RebaseTry: 2,
		FixTry:    3,
	}
}

const wakeBenchLimit = 40

// TestPromptBuildWorkStable pins the property the checkpoint asks for. Two
// passes over the same seeded fixtures produce the same byte and error counts,
// so a change that silently drops part of the prompt cannot pass unnoticed.
func TestPromptBuildWorkStable(t *testing.T) {
	tasks := promptFixtures()
	work, errs := runPromptBuild(tasks)
	againWork, againErrs := runPromptBuild(tasks)
	if work != againWork || errs != againErrs {
		t.Fatalf("prompt work is not stable: first=%d/%d second=%d/%d", work, errs, againWork, againErrs)
	}
	if work <= 0 || errs != 0 {
		t.Fatalf("prompt work=%d errs=%d, want positive work and no errors", work, errs)
	}
}

// BenchmarkSpawnPrompt times prompt construction on its own. The dispatch
// end-to-end benchmark includes a store write and a harness spawn, so it cannot
// isolate the strings.Builder work. Fixtures are seeded and built once outside
// the timed loop. Work is the total prompt bytes and stays fixed across runs.
func BenchmarkSpawnPrompt(b *testing.B) {
	// One P keeps scheduler contention off a shared machine.
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	tasks := promptFixtures()
	// A warm pass pays first-call costs before the timer starts.
	_, _ = runPromptBuild(tasks)

	b.ReportAllocs()
	b.ResetTimer()
	var work, errs int64
	for i := 0; i < b.N; i++ {
		w, e := runPromptBuild(tasks)
		work += w
		errs += e
	}
	b.StopTimer()

	printPromptReport(os.Stdout, work, errs, len(tasks), b.N)
	b.ReportMetric(float64(work), namePrompt+"-work")
	b.ReportMetric(float64(errs), namePrompt+"-errs")
}

// BenchmarkWakePrompts times correction-prompt construction. Both harnesses
// build one of these per wake, through fmt today. Work is the total prompt
// bytes and errs counts empty prompts, so both stay fixed across runs.
func BenchmarkWakePrompts(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	w := wakeBenchContext()
	// Warm passes pay first-call costs before the timer starts.
	_ = core.WakeText(w)
	_ = core.WakeTextLimited(w, wakeBenchLimit)

	b.ReportAllocs()
	b.ResetTimer()
	var work, errs int64
	for i := 0; i < b.N; i++ {
		full := core.WakeText(w)
		limited := core.WakeTextLimited(w, wakeBenchLimit)
		work += int64(len(full) + len(limited))
		if full == "" || limited == "" {
			errs++
		}
	}
	b.StopTimer()

	fmt.Fprintf(os.Stdout, "%s: iterations=%d work=%d errs=%d\n", nameWake, b.N, work, errs)
	b.ReportMetric(float64(work), nameWake+"-work")
	b.ReportMetric(float64(errs), nameWake+"-errs")
}
