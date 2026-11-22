package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/github"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// TestReconcilePassObservesRunDuration proves that one daemon cycle records a
// run_duration_seconds observation in the metrics snapshot without starting
// the loop or the orchestrator pass (PF-06).
func TestReconcilePassObservesRunDuration(t *testing.T) {
	metrics := observe.NewMetrics()
	previous := observe.Default()
	observe.SetDefault(observe.NewRecorder(nil, metrics))
	t.Cleanup(func() { observe.SetDefault(previous) })

	db := autoSpawnTestDB(t, 0)
	defer db.Close()
	cfg := reconcileConfig{
		dir:   t.TempDir(),
		db:    db,
		root:  t.TempDir(),
		batch: &github.BatchProvider{},
	}
	ReconcilePass(context.Background(), cfg)

	var got map[string]any
	if err := json.Unmarshal(metrics.SnapshotJSON(), &got); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	histograms, ok := got["histograms"].(map[string]any)
	if !ok {
		t.Fatalf("no histograms in snapshot: %v", got)
	}
	run, ok := histograms[observe.HistogramRun].(map[string]any)
	if !ok {
		t.Fatalf("no %s histogram: %v", observe.HistogramRun, histograms)
	}
	count, _ := run["count"].(float64)
	sum, _ := run["sum"].(float64)
	if count < 1 {
		t.Errorf("%s count = %v, want >= 1", observe.HistogramRun, count)
	}
	if sum <= 0 {
		t.Errorf("%s sum = %v, want > 0", observe.HistogramRun, sum)
	}
}
