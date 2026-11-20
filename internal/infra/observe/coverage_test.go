package observe

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEveryCounterIsExportedAndIncrements(t *testing.T) {
	m := NewMetrics()
	for _, name := range CounterNames() {
		m.Inc(name, "")
	}
	var got map[string]any
	if err := json.Unmarshal(m.JSON(), &got); err != nil {
		t.Fatalf("невалидный JSON: %v", err)
	}
	labelled := map[string]string{
		CounterVerdictPublished: LabelVerdict,
		CounterWakeDelivered:    LabelAdapter,
		CounterWakeErrors:       LabelAdapter,
		CounterFixTotal:         LabelRole,
	}
	for _, name := range CounterNames() {
		if label, ok := labelled[name]; ok {
			byValue, ok := got[name].(map[string]any)
			if !ok || byValue[""] != float64(1) {
				t.Errorf("%s = %v, ожидалось 1 по метке %s", name, got[name], label)
			}
			continue
		}
		if v, ok := got[name].(float64); !ok || int(v) != 1 {
			t.Errorf("%s = %v, ожидалось 1", name, got[name])
		}
	}
	want := []string{
		"poll_cycles_total", "verdict_published_total", "merge_attempts_total",
		"merge_success_total", "merge_failures_total", "wake_delivered_total",
		"wake_errors_total", "wake_skipped_total", "rebase_requests_total",
		"dependency_waits_total", "blocks_total", "actor_errors_total",
		"fix_total",
	}
	if len(want) != len(CounterNames()) {
		t.Fatalf(" счётчиков %d, ожидалось %d", len(CounterNames()), len(want))
	}
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("в выводе нет счётчика %s", name)
		}
	}
	text := string(m.Prometheus())
	for _, name := range want {
		if !strings.Contains(text, "beacon_"+name+" ") {
			t.Errorf("в Prometheus нет beacon_%s:\n%s", name, text)
		}
	}
}

func TestHelpersCoverEveryTransition(t *testing.T) {
	sink := newCaptureSink()
	SetDefault(sink)
	defer SetDefault(nil)

	PollCycleStart(4)
	PollCycleEnd(2, 1, 3*time.Millisecond)
	DependenciesResolved("w1", 10, []string{"#9"})
	DependencyWait("w2", 11, "#12 не зелёный")
	BlockDetected("w3", 12, "needs input")
	MergeAttempt("w1", 10)
	MergeSuccess("w1", 10)
	MergeFailure("w1", 10, errors.New("refused"))
	WakeSkipped("opencode", "w2", "нет подписчика")
	ActorError("daemon", "w3", errors.New("boom"))
	WatchRegistered("w1", 10, 3, "opencode")
	ResultRead("w1", "ready", 4)
	Command("daemon", []string{"--once"})
	RunEnd(CycleStats{Watches: 4, Merges: 1, Duration: time.Second}, nil)

	wantEvents := []string{
		"poll_cycle_start", "poll_cycle_end", "deps_resolved", "dependency_wait",
		"blocked_detected", "merge_attempt", "merge_success", "merge_fail",
		"wake_skipped", "actor_error", "watch_registered", "result_read",
		"command_dispatch", "run_end",
	}
	for _, event := range wantEvents {
		found := false
		for _, e := range sink.events {
			if e.name == event {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("нет события %s", event)
		}
	}
	for name, want := range map[string]uint64{
		CounterPollCycles: 1, CounterDependencyWaits: 1, CounterBlocks: 1,
		CounterMergeAttempts: 1, CounterMergeSuccess: 1, CounterMergeFailures: 1,
		CounterWakeSkipped: 1, CounterActorErrors: 1,
	} {
		if sink.counts[name] != want {
			t.Errorf("%s = %d, ожидалось %d", name, sink.counts[name], want)
		}
	}
	for _, e := range sink.events {
		if e.fields[FieldEvent] != nil {
			t.Errorf("event не должен дублироваться в полях: %+v", e.fields)
		}
	}
}

func TestCycleSummaryIsOneCompactEvent(t *testing.T) {
	buf := &bytes.Buffer{}
	rec := NewRecorder(NewLogger("json", LevelInfo, buf), NewMetrics())
	SetDefault(rec)
	defer SetDefault(nil)

	Cycle(CycleStats{Watches: 7, Ready: 3, Retry: 2, Merges: 1, Wakes: 2, Duration: 250 * time.Millisecond})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("цикл дал %d строк, ожидалась 1: %q", len(lines), buf.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("невалидный JSON: %q", buf.String())
	}
	for key, want := range map[string]any{
		FieldEvent: "cycle", FieldWatches: float64(7), FieldReady: float64(3),
		FieldRetry: float64(2), FieldMerges: float64(1), FieldWakes: float64(2),
		FieldDuration: "250ms",
	} {
		if got[key] != want {
			t.Errorf("поле %s = %v, ожидалось %v", key, got[key], want)
		}
	}
}

func TestDaemonCycleSummaryAggregatesTransitions(t *testing.T) {
	SetDefault(nil)
	FlushCycle() // reset any state left by another test without emitting into the buffer
	buf := &bytes.Buffer{}
	SetDefault(NewRecorder(NewLogger("json", LevelInfo, buf), NewMetrics()))
	defer SetDefault(nil)

	PollCycleStart(5)
	VerdictPublished("ready", "w1", 1)
	VerdictPublished("retry", "w2", 2)
	MergeSuccess("w1", 1)
	WakeDelivered("opencode", "w2")
	WakeSkipped("", "w3", "нет подписчика")
	FlushCycle()

	lines := cycleLines(t, buf.String())
	if len(lines) != 1 {
		t.Fatalf("ожидалась одна строка цикла, получено %d: %q", len(lines), buf.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("невалидный JSON: %v", err)
	}
	for key, want := range map[string]any{
		FieldEvent: "cycle", FieldWatches: float64(5), FieldReady: float64(1),
		FieldRetry: float64(1), FieldMerges: float64(1), FieldWakes: float64(2),
	} {
		if got[key] != want {
			t.Errorf("поле %s = %v, ожидалось %v", key, got[key], want)
		}
	}

	FlushCycle() // after a flush an empty pass must stay silent
	if got := cycleLines(t, buf.String()); len(got) != 1 {
		t.Errorf("пустой цикл не должен логироваться: %q", buf.String())
	}
}

func TestIdleDaemonCycleStillLogsSummary(t *testing.T) {
	SetDefault(nil)
	FlushCycle()
	buf := &bytes.Buffer{}
	SetDefault(NewRecorder(NewLogger("json", LevelInfo, buf), NewMetrics()))
	defer SetDefault(nil)

	DaemonPass()
	FlushCycle()

	lines := cycleLines(t, buf.String())
	if len(lines) != 1 {
		t.Fatalf("холостой цикл дал %d строк, ожидалась 1: %q", len(lines), buf.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatal(err)
	}
	if got[FieldWatches] != float64(0) || got[FieldEvent] != "cycle" {
		t.Errorf("сводка холостого цикла = %v", got)
	}
}

func cycleLines(t *testing.T, raw string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("невалидная строка лога %q: %v", line, err)
		}
		if event[FieldEvent] == "cycle" {
			out = append(out, line)
		}
	}
	return out
}

func TestHistogramRecordsDurationsAndExportsThem(t *testing.T) {
	m := NewMetrics()
	m.Observe(HistogramPollCycle, 0.002)
	m.Observe(HistogramPollCycle, 0.2)
	m.Observe(HistogramMerge, 1.5)

	var got map[string]any
	if err := json.Unmarshal(m.JSON(), &got); err != nil {
		t.Fatalf("невалидный JSON: %v", err)
	}
	histograms, ok := got["histograms"].(map[string]any)
	if !ok {
		t.Fatalf("нет histograms: %v", got)
	}
	poll, ok := histograms[HistogramPollCycle].(map[string]any)
	if !ok {
		t.Fatalf("нет гистограммы poll: %v", histograms)
	}
	if poll["count"] != float64(2) {
		t.Errorf("count = %v, ожидалось 2", poll["count"])
	}
	buckets := poll["buckets"].(map[string]any)
	if buckets["0.005"] != float64(1) || buckets["0.5"] != float64(2) || buckets["+Inf"] != float64(2) {
		t.Errorf("buckets = %v", buckets)
	}
	text := string(m.Prometheus())
	for _, want := range []string{
		"# TYPE beacon_poll_cycle_duration_seconds histogram",
		`beacon_poll_cycle_duration_seconds_bucket{le="+Inf"} 2`,
		"beacon_poll_cycle_duration_seconds_count 2",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в Prometheus нет %q:\n%s", want, text)
		}
	}
}
