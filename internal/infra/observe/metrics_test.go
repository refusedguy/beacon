package observe

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestWakeOutcomeCounterBreaksDownAdapterAndOutcome(t *testing.T) {
	previous := Default()
	defer SetDefault(previous)

	m := NewMetrics()
	SetDefault(NewRecorder(nil, m))

	WakeFailedOutcome("opencode", "w1", WakeOutcomeTransportFailed, errors.New("boom"), 3)
	WakeFailedOutcome("pi", "w2", WakeOutcomeTransportFailed, errors.New("refused"))
	WakeDeliveredOutcome("opencode", "w3", WakeOutcomePosted)
	WakeDuplicate("w4", 5)

	var got map[string]any
	if err := json.Unmarshal(m.JSON(), &got); err != nil {
		t.Fatalf("невалидный JSON метрик: %v", err)
	}
	byPair, ok := got[CounterWakeOutcome].(map[string]any)
	if !ok {
		t.Fatalf("%s не объект: %v", CounterWakeOutcome, got[CounterWakeOutcome])
	}
	want := map[string]float64{
		"opencode:" + WakeOutcomeTransportFailed: 1,
		"pi:" + WakeOutcomeTransportFailed:       1,
		"opencode:" + WakeOutcomePosted:          1,
		":" + WakeOutcomeDuplicate:               1,
	}
	for key, count := range want {
		if byPair[key] != count {
			t.Errorf("%s[%q] = %v, ожидалось %v", CounterWakeOutcome, key, byPair[key], count)
		}
	}
	if len(byPair) != len(want) {
		t.Errorf("в %s %d ключей, ожидалось %d: %v", CounterWakeOutcome, len(byPair), len(want), byPair)
	}

	text := string(m.Prometheus())
	for _, line := range []string{
		`beacon_wake_outcome_total{adapter_outcome="opencode:transport-failed"} 1`,
		`beacon_wake_outcome_total{adapter_outcome="pi:transport-failed"} 1`,
		`beacon_wake_outcome_total{adapter_outcome=":duplicate"} 1`,
	} {
		if !strings.Contains(text, line) {
			t.Errorf("в Prometheus-выводе нет %q:\n%s", line, text)
		}
	}
}

func TestWakeOutcomeKeepsExistingJSONShape(t *testing.T) {
	m := NewMetrics()
	m.Inc(CounterWakeDelivered, "opencode")
	m.Inc(CounterWakeErrors, "opencode")
	m.Inc(CounterWakeOutcome, outcomeKey("opencode", WakeOutcomeTransportFailed))

	var got map[string]any
	if err := json.Unmarshal(m.JSON(), &got); err != nil {
		t.Fatalf("невалидный JSON метрик: %v", err)
	}
	for _, name := range []string{CounterWakeDelivered, CounterWakeErrors} {
		byAdapter, ok := got[name].(map[string]any)
		if !ok || byAdapter["opencode"] != float64(1) {
			t.Errorf("%s = %v, ожидалось {opencode: 1}", name, got[name])
		}
	}
	hist, ok := got["histograms"].(map[string]any)
	if !ok {
		t.Fatalf("нет histograms: %v", got)
	}
	if len(hist) != len(HistogramNames()) {
		t.Errorf("гистограмм %d, ожидалось %d: %v", len(hist), len(HistogramNames()), hist)
	}
	if _, ok := hist[HistogramPollCycle]; !ok {
		t.Errorf("гистограмма цикла потеряна: %v", hist)
	}
}

func TestWakeDeliveredSourceLabelExportsJSONAndPrometheus(t *testing.T) {
	m := NewMetrics()
	m.IncSource(CounterWakeDelivered, "opencode", WakeSourceMCP)
	m.IncSource(CounterWakeDelivered, "opencode", WakeSourceDaemon)
	m.IncSource(CounterWakeDelivered, "opencode", WakeSourceDaemon)
	m.Inc(CounterWakeDelivered, "pi")

	var got map[string]any
	if err := json.Unmarshal(m.JSON(), &got); err != nil {
		t.Fatalf("невалидный JSON метрик: %v", err)
	}
	byLabel, ok := got[CounterWakeDelivered].(map[string]any)
	if !ok {
		t.Fatalf("%s не объект: %v", CounterWakeDelivered, got[CounterWakeDelivered])
	}
	wantJSON := map[string]float64{
		sourceLabelKey("opencode", WakeSourceMCP):    1,
		sourceLabelKey("opencode", WakeSourceDaemon): 2,
		"pi": 1,
	}
	for key, want := range wantJSON {
		if byLabel[key] != want {
			t.Errorf("%s[%q] = %v, ожидалось %v", CounterWakeDelivered, key, byLabel[key], want)
		}
	}
	if len(byLabel) != len(wantJSON) {
		t.Errorf("в %s %d ключей, ожидалось %d: %v", CounterWakeDelivered, len(byLabel), len(wantJSON), byLabel)
	}

	text := string(m.Prometheus())
	for _, want := range []string{
		`beacon_wake_delivered_total{adapter="opencode",source="mcp"} 1`,
		`beacon_wake_delivered_total{adapter="opencode",source="daemon"} 2`,
		`beacon_wake_delivered_total{adapter="pi"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в Prometheus-выводе нет %q:\n%s", want, text)
		}
	}
}

func TestWakeDeliveredSourceLabelSurvivesSnapshot(t *testing.T) {
	m := NewMetrics()
	m.IncSource(CounterWakeDelivered, "opencode", WakeSourceDaemon)
	m.Inc(CounterWakeDelivered, "pi")

	restored := MetricsFromSnapshotJSON(m.SnapshotJSON())
	if got, want := string(restored.Prometheus()), string(m.Prometheus()); got != want {
		t.Errorf("снапшот изменил метрику с source:\n%s\n----\n%s", got, want)
	}
}

func TestWakeDeliveredSourceWritesLabelThroughRecorder(t *testing.T) {
	previous := Default()
	defer SetDefault(previous)

	m := NewMetrics()
	SetDefault(NewRecorder(nil, m))
	WakeDeliveredSource("opencode", "w1", WakeSourceDaemon)
	want := `beacon_wake_delivered_total{adapter="opencode",source="daemon"} 1`
	if text := string(m.Prometheus()); !strings.Contains(text, want) {
		t.Errorf("в Prometheus-выводе нет %q:\n%s", want, text)
	}

	plain := newCaptureSink()
	SetDefault(plain)
	WakeDeliveredSource("opencode", "w2", WakeSourceDaemon)
	if plain.counts[CounterWakeDelivered] != 1 || plain.labels[CounterWakeDelivered] != "opencode" {
		t.Errorf("старый sink получил %d вызовов с меткой %q, ожидалось 1 и %q",
			plain.counts[CounterWakeDelivered], plain.labels[CounterWakeDelivered], "opencode")
	}
}

func TestMetricsReset(t *testing.T) {
	m := NewMetrics()
	m.Inc(CounterWakeErrors, "opencode")
	m.Inc(CounterWakeErrors, "opencode")
	m.Reset()
	var got map[string]any
	_ = json.Unmarshal(m.JSON(), &got)
	byAdapter, ok := got[CounterWakeErrors].(map[string]any)
	if !ok || len(byAdapter) != 0 {
		t.Errorf("после Reset wake_errors = %v", got[CounterWakeErrors])
	}
}

func TestMetricsPrometheusFormat(t *testing.T) {
	m := NewMetrics()
	m.Inc(CounterMergeAttempt, "")
	m.Inc(CounterMergeSuccess, "")
	m.Inc(CounterWakeDelivered, `weird"adapter`)

	text := string(m.Prometheus())
	for _, want := range []string{
		"# HELP beacon_merge_attempts_total Beacon counter merge_attempts_total.",
		"# TYPE beacon_merge_attempts_total counter",
		"beacon_merge_attempts_total 1",
		"beacon_merge_success_total 1",
		`beacon_wake_delivered_total{adapter="weird\"adapter"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в Prometheus-выводе нет %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "beacon_verdict_published_total{") {
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
	if !strings.Contains(rec.Body.String(), "beacon_rebase_requests_total 1") {
		t.Errorf("в теле нет счётчика:\n%s", rec.Body.String())
	}
}

func TestMetricsGaugesFormatAndValues(t *testing.T) {
	m := NewMetrics()
	m.SetGauge(GaugeActiveWatches, 4)
	m.SetGauge(GaugeOutboxDepth, 7)
	m.SetGauge("unknown_gauge", 99)

	text := string(m.Prometheus())
	for _, want := range []string{
		"# HELP beacon_active_watches Beacon gauge active_watches.",
		"# TYPE beacon_active_watches gauge",
		"beacon_active_watches 4",
		"# TYPE beacon_outbox_depth gauge",
		"beacon_outbox_depth 7",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в Prometheus-выводе нет %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "beacon_unknown_gauge") {
		t.Errorf("неизвестный gauge попал в вывод:\n%s", text)
	}

	var got map[string]any
	if err := json.Unmarshal(m.JSON(), &got); err != nil {
		t.Fatalf("невалидный JSON метрик: %v", err)
	}
	if got[GaugeActiveWatches] != float64(4) || got[GaugeOutboxDepth] != float64(7) {
		t.Errorf("gauges в JSON = %v, %v", got[GaugeActiveWatches], got[GaugeOutboxDepth])
	}
	if _, ok := got["unknown_gauge"]; ok {
		t.Errorf("неизвестный gauge попал в JSON: %v", got)
	}

	m.Reset()
	if m.Gauge(GaugeActiveWatches) != 0 || m.Gauge(GaugeOutboxDepth) != 0 {
		t.Errorf("Reset не обнулил gauges: %d, %d", m.Gauge(GaugeActiveWatches), m.Gauge(GaugeOutboxDepth))
	}
}

func TestPackageGaugeHelpersUseInstalledRecorder(t *testing.T) {
	previous := Default()
	defer SetDefault(previous)

	m := NewMetrics()
	SetDefault(NewRecorder(nil, m))
	ActiveWatches(3)
	OutboxDepth(5)
	if m.Gauge(GaugeActiveWatches) != 3 || m.Gauge(GaugeOutboxDepth) != 5 {
		t.Errorf("gauges = %d, %d, ожидалось 3, 5", m.Gauge(GaugeActiveWatches), m.Gauge(GaugeOutboxDepth))
	}

	SetDefault(nil)
	ActiveWatches(9)
	if m.Gauge(GaugeActiveWatches) != 3 {
		t.Errorf("без sink gauge изменился: %d", m.Gauge(GaugeActiveWatches))
	}
}

func TestStartMetricsServerServesPrometheusOnLoopback(t *testing.T) {
	m := NewMetrics()
	m.SetGauge(GaugeActiveWatches, 2)
	m.SetGauge(GaugeOutboxDepth, 9)

	srv, err := StartMetricsServer("0", m)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	host, port, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("адрес %q: %v", srv.Addr(), err)
	}
	if host != "127.0.0.1" || port == "0" || srv.Port() == 0 {
		t.Fatalf("сервер слушает не на свободном loopback-порту: %s", srv.Addr())
	}

	resp, err := http.Get("http://" + srv.Addr() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("статус /metrics = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"beacon_active_watches 2", "beacon_outbox_depth 9"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("в scrape нет %q:\n%s", want, body)
		}
	}

	other, err := http.Get("http://" + srv.Addr() + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Body.Close()
	if other.StatusCode != http.StatusNotFound {
		t.Errorf("чужой путь = %d, ожидался 404", other.StatusCode)
	}
}

func TestStartMetricsServerDisabledAndInvalidPort(t *testing.T) {
	s, err := StartMetricsServer("", NewMetrics())
	if err != nil {
		t.Fatal(err)
	}
	if s.Addr() != "" || s.Port() != 0 {
		t.Errorf("отключённый сервер имеет адрес %q и порт %d", s.Addr(), s.Port())
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close отключённого сервера: %v", err)
	}
	if _, err := StartMetricsServer("не-порт", NewMetrics()); err == nil {
		t.Error("невалидный порт должен отклоняться")
	}
}

func TestRetryOpenHistogramExportsBucketsSumAndCount(t *testing.T) {
	previous := Default()
	defer SetDefault(previous)

	m := NewMetrics()
	SetDefault(NewRecorder(nil, m))
	RetryOpen("w1", 2*time.Second)

	text := string(m.Prometheus())
	for _, want := range []string{
		"# TYPE beacon_retry_open_duration_seconds histogram",
		`beacon_retry_open_duration_seconds_bucket{le="1"} 0`,
		`beacon_retry_open_duration_seconds_bucket{le="5"} 1`,
		`beacon_retry_open_duration_seconds_bucket{le="+Inf"} 1`,
		"beacon_retry_open_duration_seconds_sum 2",
		"beacon_retry_open_duration_seconds_count 1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в Prometheus-выводе нет %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "watchID") || strings.Contains(text, "sessionID") {
		t.Errorf("в метрике есть высококардинальная метка:\n%s", text)
	}

	var got map[string]any
	if err := json.Unmarshal(m.JSON(), &got); err != nil {
		t.Fatalf("невалидный JSON метрик: %v", err)
	}
	hist, ok := got["histograms"].(map[string]any)
	if !ok {
		t.Fatalf("нет histograms: %v", got)
	}
	retry, ok := hist[HistogramRetryOpen].(map[string]any)
	if !ok {
		t.Fatalf("нет гистограммы %s: %v", HistogramRetryOpen, hist)
	}
	if retry["count"] != float64(1) || retry["sum"] != float64(2) {
		t.Errorf("%s count/sum = %v/%v, ожидалось 1/2", HistogramRetryOpen, retry["count"], retry["sum"])
	}
	buckets, ok := retry["buckets"].(map[string]any)
	if !ok || buckets["1"] != float64(0) || buckets["5"] != float64(1) || buckets["+Inf"] != float64(1) {
		t.Errorf("%s buckets = %v", HistogramRetryOpen, retry["buckets"])
	}
}

func TestRetryOpenHistogramRegisteredAtZero(t *testing.T) {
	m := NewMetrics()
	text := string(m.Prometheus())
	for _, want := range []string{
		"# TYPE beacon_retry_open_duration_seconds histogram",
		"beacon_retry_open_duration_seconds_bucket{le=\"+Inf\"} 0",
		"beacon_retry_open_duration_seconds_count 0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("свежая метрика не содержит %q:\n%s", want, text)
		}
	}
}
