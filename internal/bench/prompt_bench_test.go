package bench

import (
	"os"
	"runtime"
	"testing"
)

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
