package worktree

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

const gcDurationListing = "worktree /main\n" +
	"HEAD aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
	"branch refs/heads/main\n\n" +
	"worktree /clean\n" +
	"HEAD 1111111111111111111111111111111111111111\n" +
	"branch refs/heads/clean\n"

// gcDurationHistogram reads the observe histogram through the same JSON the
// daemon saves to runtime_stats. A test asserts the public number, not the
// internal recorder.
func gcDurationHistogram(t *testing.T, m *observe.Metrics) (uint64, float64) {
	t.Helper()
	var doc struct {
		Histograms map[string]struct {
			Count uint64  `json:"count"`
			Sum   float64 `json:"sum"`
		} `json:"histograms"`
	}
	raw := m.JSON()
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode observe metrics: %v", err)
	}
	h, ok := doc.Histograms[HistogramGCDuration]
	if !ok {
		t.Fatalf("no %s histogram in %s", HistogramGCDuration, raw)
	}
	return h.Count, h.Sum
}

// TestCollectRecordsOneGCDurationObservation proves one Collect call, which is
// what the daemon WorktreeGC port calls, adds exactly one observation with a
// positive duration. A second call adds a second observation.
func TestCollectRecordsOneGCDurationObservation(t *testing.T) {
	previous := observe.Default()
	metrics := observe.NewMetrics()
	observe.SetDefault(observe.NewRecorder(nil, metrics))
	t.Cleanup(func() { observe.SetDefault(previous) })

	git := &fakeGit{listOut: gcDurationListing}
	slow := func(dir string, args ...string) (string, error) {
		time.Sleep(time.Millisecond)
		return git.run(dir, args...)
	}

	res, err := Collect(slow, nil)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	assertRemoved(t, res, "/clean")

	count, sum := gcDurationHistogram(t, metrics)
	if count != 1 {
		t.Fatalf("%s count = %d, want 1", HistogramGCDuration, count)
	}
	if sum <= 0 {
		t.Fatalf("%s sum = %v, want > 0", HistogramGCDuration, sum)
	}

	if _, err := Collect(slow, nil); err != nil {
		t.Fatalf("second Collect() error = %v", err)
	}
	count, _ = gcDurationHistogram(t, metrics)
	if count != 2 {
		t.Fatalf("%s count after two calls = %d, want 2", HistogramGCDuration, count)
	}
}
