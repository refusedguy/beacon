package observe

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsIncrementUnlabeledAndLabelledCounters(t *testing.T) {
	m := NewMetrics()
	m.Inc(CounterPollCycles, "")
	m.Inc(CounterPollCycles, "")
	m.Inc(CounterVerdictPublished, "ready")
	m.Inc(CounterVerdictPublished, "ready")
	m.Inc(CounterVerdictPublished, "retry")
	m.Inc(CounterWakeDelivered, "opencode")

	var got map[string]any
	if err := json.Unmarshal(m.JSON(), &got); err != nil {
		t.Fatalf("невалидный JSON метрик: %v", err)
	}
	if v, ok := got[CounterPollCycles].(float64); !ok || int(v) != 2 {
		t.Errorf("poll_cycles = %v, ожидалось 2", got[CounterPollCycles])
	}
	if v, ok := got[CounterMergeAttempt].(float64); !ok || int(v) != 0 {
		t.Errorf("merge_attempt = %v, ожидалось 0", got[CounterMergeAttempt])
	}
	verdicts, ok := got[CounterVerdictPublished].(map[string]any)
	if !ok {
		t.Fatalf("verdict_published не объект: %v", got[CounterVerdictPublished])
	}
	if v, ok := verdicts["ready"].(float64); !ok || int(v) != 2 {
		t.Errorf("verdict_published[ready] = %v, ожидалось 2", verdicts["ready"])
	}
	if v, ok := verdicts["retry"].(float64); !ok || int(v) != 1 {
		t.Errorf("verdict_published[retry] = %v, ожидалось 1", verdicts["retry"])
	}
	adapters, ok := got[CounterWakeDelivered].(map[string]any)
	if !ok || adapters["opencode"] != float64(1) {
		t.Errorf("wake_delivered = %v", got[CounterWakeDelivered])
	}
}

func TestMetricsReset(t *testing.T) {
	m := NewMetrics()
	m.Inc(CounterWakeFailed, "")
	m.Inc(CounterWakeFailed, "")
	m.Reset()
	var got map[string]any
	_ = json.Unmarshal(m.JSON(), &got)
	if v, ok := got[CounterWakeFailed].(float64); !ok || int(v) != 0 {
		t.Errorf("после Reset wake_failed = %v", got[CounterWakeFailed])
	}
}

func TestMetricsPrometheusFormat(t *testing.T) {
	m := NewMetrics()
	m.Inc(CounterMergeAttempt, "")
	m.Inc(CounterMergeSuccess, "")
	m.Inc(CounterWakeDelivered, `weird"adapter`)

	text := string(m.Prometheus())
	for _, want := range []string{
		"# HELP beacon_merge_attempt Beacon counter merge_attempt.",
		"# TYPE beacon_merge_attempt counter",
		"beacon_merge_attempt 1",
		"beacon_merge_success 1",
		`beacon_wake_delivered{adapter="weird\"adapter"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в Prometheus-выводе нет %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "beacon_verdict_published{") {
		t.Errorf("пустой счётчик с меткой не должен давать sample:\n%s", text)
	}
}

func TestMetricsHandlerSetsPrometheusContentType(t *testing.T) {
	m := NewMetrics()
	m.Inc(CounterRebaseRequested, "")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "beacon_rebase_requested 1") {
		t.Errorf("в теле нет счётчика:\n%s", rec.Body.String())
	}
}
