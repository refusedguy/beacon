package observe

import (
	"encoding/json"
	"testing"
)

func TestSnapshotJSONRoundTrip(t *testing.T) {
	original := NewMetrics()
	original.Inc(CounterPollCycles, "")
	original.Inc(CounterPollCycles, "")
	original.Inc(CounterVerdictPublished, "ready")
	original.Inc(CounterVerdictPublished, "retry")
	original.Inc(CounterWakeDelivered, "opencode")
	original.Inc(CounterFixersSpawned, "")
	original.Observe(HistogramPollCycle, 0.002)
	original.Observe(HistogramPollCycle, 0.2)
	original.Observe(HistogramMerge, 1.5)

	raw := original.SnapshotJSON()
	if !json.Valid(raw) {
		t.Fatalf("снапшот невалидный JSON: %q", raw)
	}

	restored := MetricsFromSnapshotJSON(raw)
	var want, got map[string]any
	if err := json.Unmarshal(original.JSON(), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(restored.JSON(), &got); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		CounterPollCycles, CounterVerdictPublished, CounterWakeDelivered, CounterFixersSpawned,
	} {
		if got[name] == nil {
			t.Errorf("в снимке нет счётчика %s", name)
		}
	}
	wantCounters, _ := want[CounterPollCycles].(float64)
	gotCounters, _ := got[CounterPollCycles].(float64)
	if gotCounters != wantCounters {
		t.Errorf("%s = %v, ожидалось %v", CounterPollCycles, gotCounters, wantCounters)
	}
	wantVerdicts, _ := want[CounterVerdictPublished].(map[string]any)
	gotVerdicts, _ := got[CounterVerdictPublished].(map[string]any)
	for _, label := range []string{"ready", "retry"} {
		if gotVerdicts[label] != wantVerdicts[label] {
			t.Errorf("verdict_published[%s] = %v, ожидалось %v", label, gotVerdicts[label], wantVerdicts[label])
		}
	}

	wantHist, _ := want["histograms"].(map[string]any)
	gotHist, _ := got["histograms"].(map[string]any)
	wantPoll, _ := wantHist[HistogramPollCycle].(map[string]any)
	gotPoll, _ := gotHist[HistogramPollCycle].(map[string]any)
	if gotPoll["count"] != wantPoll["count"] || gotPoll["sum"] != wantPoll["sum"] {
		t.Errorf("гистограмма = %v, ожидалась %v", gotPoll, wantPoll)
	}
	wantBuckets, _ := wantPoll["buckets"].(map[string]any)
	gotBuckets, _ := gotPoll["buckets"].(map[string]any)
	if gotBuckets["0.005"] != wantBuckets["0.005"] || gotBuckets["+Inf"] != wantBuckets["+Inf"] {
		t.Errorf("бакеты = %v, ожидались %v", gotBuckets, wantBuckets)
	}
}

func TestMetricsFromSnapshotJSONEmptyAndMalformed(t *testing.T) {
	for name, raw := range map[string][]byte{
		"nil":         nil,
		"empty":       []byte("   "),
		"garbage":     []byte("{это не json"),
		"wrong shape": []byte(`{"poll_cycles_total":"не число"}`),
	} {
		m := MetricsFromSnapshotJSON(raw)
		if m == nil {
			t.Fatalf("%s: ожидался пустой набор, а не nil", name)
		}
		var got map[string]any
		if err := json.Unmarshal(m.JSON(), &got); err != nil {
			t.Fatalf("%s: невалидный JSON: %v", name, err)
		}
		if got[CounterPollCycles] != float64(0) {
			t.Errorf("%s: счётчик = %v, ожидался 0", name, got[CounterPollCycles])
		}
	}
}
