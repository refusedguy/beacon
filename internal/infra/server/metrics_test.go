package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func findSeries(t *testing.T, m *Metrics, attrs Attributes) Series {
	t.Helper()
	want := attrs.Normalize()
	for _, s := range m.Snapshot() {
		if s.Attributes == want {
			return s
		}
	}
	t.Fatalf("серия для %+v не найдена", want)
	return Series{}
}

func TestSeriesCountDoesNotGrowWithPRs(t *testing.T) {
	m := NewMetrics()
	want := SeriesCount()
	if got := len(m.Snapshot()); got != want {
		t.Fatalf("начальное число серий = %d, ожидалось %d", got, want)
	}

	stable := Attributes{Signal: "wake", Reason: "ok", Adapter: "opencode"}
	for i := 0; i < 200; i++ {
		// Идентификатор вотча и номер PR меняются каждый проход. Число серий
		// должно оставаться прежним: эти значения не попали в label-ы.
		changing := fmt.Sprintf("watch-%d", i)
		attrs := Attributes{Signal: "wake", Reason: "ok", Adapter: changing}
		m.IncCall(attrs)
		m.ObserveDuration(attrs, time.Millisecond)
		m.IncCall(stable)
		m.ObserveDuration(stable, time.Millisecond)
	}

	if got := len(m.Snapshot()); got != want {
		t.Fatalf("число серий выросло до %d, ожидалось %d", got, want)
	}

	counterLines := 0
	sumLines := 0
	for _, line := range strings.Split(string(m.Prometheus()), "\n") {
		if strings.HasPrefix(line, "beacon_"+CounterCalls+"{") {
			counterLines++
		}
		if strings.HasPrefix(line, "beacon_"+HistogramCallDuration+"_sum{") {
			sumLines++
		}
	}
	if counterLines != want || sumLines != want {
		t.Errorf("в выводе %d линий счётчика и %d линий sum, ожидалось по %d",
			counterLines, sumLines, want)
	}
}

func TestLabelSetIsFixedAndHasNoHighCardinalityValues(t *testing.T) {
	m := NewMetrics()
	m.IncCall(Attributes{Signal: "watch-42", Reason: "pr-999", Adapter: "ses_abc"})
	m.ObserveDuration(Attributes{Signal: "watch-42", Reason: "pr-999", Adapter: "ses_abc"}, time.Second)

	text := string(m.Prometheus())
	for _, forbidden := range []string{"watch-42", "pr-999", "ses_abc", "watchID", "sessionID", "pr="} {
		if strings.Contains(text, forbidden) {
			t.Errorf("в выводе метрик есть высококардинальное значение %q:\n%s", forbidden, text)
		}
	}

	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "beacon_") {
			continue
		}
		open := strings.Index(line, "{")
		if open < 0 {
			continue
		}
		close := strings.LastIndex(line, "}")
		if close < open {
			t.Fatalf("незакрытые label-ы в строке %q", line)
		}
		for _, part := range strings.Split(line[open+1:close], ",") {
			key := strings.SplitN(part, "=", 2)[0]
			switch key {
			case LabelSignal, LabelReason, LabelAdapter, "le":
			default:
				t.Errorf("неожиданная метка %q в строке %q", key, line)
			}
		}
	}
}

func TestNormalizeMapsUnknownValuesToOther(t *testing.T) {
	got := Attributes{Signal: "wake", Reason: "pr-99", Adapter: "ses_abc"}.Normalize()
	want := Attributes{Signal: "wake", Reason: Other, Adapter: Other}
	if got != want {
		t.Errorf("Normalize = %+v, ожидалось %+v", got, want)
	}

	known := Attributes{Signal: "merge", Reason: "retry", Adapter: "cursor"}.Normalize()
	if known != (Attributes{Signal: "merge", Reason: "retry", Adapter: "cursor"}) {
		t.Errorf("известные значения изменились: %+v", known)
	}
}

func TestDurationIsRecordedInSeconds(t *testing.T) {
	m := NewMetrics()
	attrs := Attributes{Signal: "wake", Reason: "ok", Adapter: "opencode"}
	m.ObserveDuration(attrs, 1500*time.Millisecond)

	s := findSeries(t, m, attrs)
	if s.Duration.Count != 1 {
		t.Errorf("count = %d, ожидалось 1", s.Duration.Count)
	}
	if s.Duration.Sum != 1.5 {
		t.Errorf("sum = %v, ожидалось 1.5 секунды", s.Duration.Sum)
	}
	if s.Duration.Buckets[len(durationBounds)-1] != 1 {
		t.Errorf("последняя корзина = %d, ожидалось 1", s.Duration.Buckets[len(durationBounds)-1])
	}
	if s.Duration.Buckets[6] != 0 {
		t.Errorf("корзина le=1 для 1.5 c = %d, ожидалось 0", s.Duration.Buckets[6])
	}

	text := string(m.Prometheus())
	sumLine := fmt.Sprintf("beacon_%s_sum{%s} 1.5", HistogramCallDuration, labelString(attrs))
	if !strings.Contains(text, sumLine) {
		t.Errorf("в Prometheus-выводе нет %q:\n%s", sumLine, text)
	}
	bucketLine := fmt.Sprintf("beacon_%s_bucket{%s,le=\"5\"} 1", HistogramCallDuration, labelString(attrs))
	if !strings.Contains(text, bucketLine) {
		t.Errorf("в Prometheus-выводе нет %q:\n%s", bucketLine, text)
	}

	var decoded metricsJSON
	if err := json.Unmarshal(m.JSON(), &decoded); err != nil {
		t.Fatalf("невалидный JSON метрик: %v", err)
	}
	for _, js := range decoded.Series {
		if js.Signal == attrs.Signal && js.Reason == attrs.Reason && js.Adapter == attrs.Adapter {
			if js.Duration.Sum != 1.5 {
				t.Errorf("sum_seconds в JSON = %v, ожидалось 1.5", js.Duration.Sum)
			}
			return
		}
	}
	t.Error("серия с длительностью отсутствует в JSON")
}

func TestCountersAdvanceOncePerCall(t *testing.T) {
	m := NewMetrics()
	stable := Attributes{Signal: "wake", Reason: "ok", Adapter: "opencode"}
	for i := 0; i < 3; i++ {
		m.IncCall(stable)
	}
	retry := Attributes{Signal: "wake", Reason: "retry", Adapter: "opencode"}
	m.IncCall(retry)

	if got := findSeries(t, m, stable).Calls; got != 3 {
		t.Errorf("call_total для ok = %d, ожидалось 3", got)
	}
	if got := findSeries(t, m, retry).Calls; got != 1 {
		t.Errorf("call_total для retry = %d, ожидалось 1", got)
	}

	text := string(m.Prometheus())
	wantLine := fmt.Sprintf("beacon_%s{%s} 3", CounterCalls, labelString(stable))
	if !strings.Contains(text, wantLine) {
		t.Errorf("в Prometheus-выводе нет %q:\n%s", wantLine, text)
	}
}

func TestResetClearsCountersAndDurations(t *testing.T) {
	m := NewMetrics()
	attrs := Attributes{Signal: "merge", Reason: "ok", Adapter: "claude"}
	m.IncCall(attrs)
	m.ObserveDuration(attrs, 250*time.Millisecond)
	m.Reset()

	s := findSeries(t, m, attrs)
	if s.Calls != 0 || s.Duration.Count != 0 || s.Duration.Sum != 0 {
		t.Errorf("после Reset серия = %+v", s)
	}
}

func TestConcurrentRecording(t *testing.T) {
	m := NewMetrics()
	attrs := Attributes{Signal: "poll", Reason: "ok", Adapter: "opencode"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.IncCall(attrs)
				m.ObserveDuration(attrs, time.Millisecond)
			}
		}()
	}
	wg.Wait()

	s := findSeries(t, m, attrs)
	if s.Calls != 800 || s.Duration.Count != 800 {
		t.Errorf("после 800 вызовов calls=%d, count=%d", s.Calls, s.Duration.Count)
	}
	if got := len(m.Snapshot()); got != SeriesCount() {
		t.Errorf("число серий после параллельных вызовов = %d, ожидалось %d", got, SeriesCount())
	}
}

func TestRSSGaugeFormat(t *testing.T) {
	m := NewMetrics()
	value := uint64(101 * 1024 * 1024)
	m.SetRSS(value)

	text := string(m.Prometheus())
	for _, want := range []string{
		"# HELP beacon_rss_bytes Resident set size of the process in bytes.",
		"# TYPE beacon_rss_bytes gauge",
		fmt.Sprintf("beacon_rss_bytes %d", value),
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в Prometheus-выводе нет %q:\n%s", want, text)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "beacon_rss_bytes ") && strings.Contains(line, "{") {
			t.Errorf("rss_bytes не должен нести метки: %s", line)
		}
	}

	var decoded metricsJSON
	if err := json.Unmarshal(m.JSON(), &decoded); err != nil {
		t.Fatalf("невалидный JSON метрик: %v", err)
	}
	if decoded.RSSBytes != value {
		t.Errorf("rssBytes в JSON = %d, ожидалось %d", decoded.RSSBytes, value)
	}

	m.Reset()
	if m.RSSBytes() != 0 {
		t.Errorf("Reset не обнулил rss_bytes: %d", m.RSSBytes())
	}
}

func TestFootprintCheckAtFifteenWatches(t *testing.T) {
	m := NewMetrics()
	var warnings []string
	warn := func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}
	over := func() (uint64, bool) { return uint64(FootprintLimitBytes) + 1, true }
	under := func() (uint64, bool) { return uint64(FootprintLimitBytes) - 1, true }

	if !FootprintCheck(m, FootprintWatchThreshold, over, warn) {
		t.Fatal("15 вотчей выше лимита должны предупреждать")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "WARNING") {
		t.Fatalf("предупреждения = %v", warnings)
	}
	if !strings.Contains(warnings[0], fmt.Sprintf("%d", FootprintLimitBytes)) {
		t.Errorf("в предупреждении нет лимита: %s", warnings[0])
	}
	if got := m.RSSBytes(); got != uint64(FootprintLimitBytes)+1 {
		t.Errorf("rss_bytes = %d, ожидалось %d", got, uint64(FootprintLimitBytes)+1)
	}

	warnings = nil
	if FootprintCheck(m, FootprintWatchThreshold, under, warn) {
		t.Fatal("RSS ниже лимита не должен предупреждать")
	}
	if len(warnings) != 0 {
		t.Errorf("ниже лимита получены предупреждения: %v", warnings)
	}

	warnings = nil
	if FootprintCheck(m, FootprintWatchThreshold-1, over, warn) {
		t.Fatal("меньше 15 вотчей не должны предупреждать")
	}
	if len(warnings) != 0 {
		t.Errorf("при 14 вотчах получены предупреждения: %v", warnings)
	}

	warnings = nil
	if FootprintCheck(m, FootprintWatchThreshold, func() (uint64, bool) { return 0, false }, warn) {
		t.Fatal("неизвестный RSS не должен предупреждать")
	}
	if len(warnings) != 0 {
		t.Errorf("при неизвестном RSS получены предупреждения: %v", warnings)
	}
}

func TestCheckLaunchFootprintDecidesAtThreshold(t *testing.T) {
	over := func() (uint64, bool) { return uint64(FootprintLimitBytes) + 1, true }
	under := func() (uint64, bool) { return uint64(FootprintLimitBytes) - 1, true }

	m := NewMetrics()
	got := CheckLaunchFootprint(m, FootprintWatchThreshold, over, nil)
	if got.Allow {
		t.Fatal("при превышении порога запуск должен быть запрещён")
	}
	if got.Reason != LaunchReasonFootprint {
		t.Errorf("причина отказа = %q, ожидалось %q", got.Reason, LaunchReasonFootprint)
	}
	if value := m.RSSBytes(); value != uint64(FootprintLimitBytes)+1 {
		t.Errorf("rss_bytes = %d, ожидалось %d", value, uint64(FootprintLimitBytes)+1)
	}

	got = CheckLaunchFootprint(m, FootprintWatchThreshold, under, nil)
	if !got.Allow {
		t.Fatal("ниже лимита запуск должен быть разрешён")
	}
	if got.Reason != LaunchReasonOK {
		t.Errorf("причина = %q, ожидалось %q", got.Reason, LaunchReasonOK)
	}

	got = CheckLaunchFootprint(m, FootprintWatchThreshold-1, over, nil)
	if !got.Allow {
		t.Fatal("при 14 вотчах запуск должен быть разрешён")
	}
}

func TestCheckLaunchFootprintWithoutReadRSS(t *testing.T) {
	unreadable := func() (uint64, bool) { return 0, false }
	cases := []struct {
		name   string
		reader RSSReader
	}{
		{name: "nil reader", reader: nil},
		{name: "unreadable RSS", reader: unreadable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMetrics()
			got := CheckLaunchFootprint(m, FootprintWatchThreshold, tc.reader, nil)
			if got.Allow {
				t.Fatal("без RSS запуск должен быть запрещён")
			}
			if got.Reason != LaunchReasonRSSUnknown {
				t.Errorf("причина = %q, ожидалось %q", got.Reason, LaunchReasonRSSUnknown)
			}
		})
	}
}
