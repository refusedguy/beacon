package observe

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type capturedEvent struct {
	name   string
	level  Level
	fields Fields
}

type captureSink struct {
	events []capturedEvent
	counts map[string]uint64
	labels map[string]string
}

func newCaptureSink() *captureSink {
	return &captureSink{counts: make(map[string]uint64), labels: make(map[string]string)}
}

func (s *captureSink) Emit(event string, level Level, fields Fields) {
	s.events = append(s.events, capturedEvent{name: event, level: level, fields: fields})
}

func (s *captureSink) Inc(name, labelValue string) {
	s.counts[name]++
	s.labels[name] = labelValue
}

func TestHelpersEmitEventsAndIncrementCounters(t *testing.T) {
	sink := newCaptureSink()
	SetDefault(sink)
	defer SetDefault(nil)

	PollCycle()
	PollCycleDuration(3 * time.Millisecond)
	VerdictPublished("ready", "watch:1", 7)
	MergeAttempt("watch:1", 7)
	MergeSuccess("watch:1", 7)
	WakeDelivered("opencode", "watch:1")
	WakeFailed("opencode", "watch:1", errors.New("refused"))
	RebaseRequested("watch:1", 7)

	for _, name := range []string{
		CounterPollCycles, CounterVerdictPublished, CounterMergeAttempt,
		CounterMergeSuccess, CounterWakeDelivered, CounterWakeFailed,
		CounterRebaseRequested,
	} {
		if sink.counts[name] != 1 {
			t.Errorf("счётчик %s = %d, ожидалось 1", name, sink.counts[name])
		}
	}
	if sink.labels[CounterVerdictPublished] != "ready" {
		t.Errorf("метка verdict = %q", sink.labels[CounterVerdictPublished])
	}
	if sink.labels[CounterWakeDelivered] != "opencode" {
		t.Errorf("метка adapter = %q", sink.labels[CounterWakeDelivered])
	}
	if len(sink.events) != 7 {
		t.Fatalf("событий %d, ожидалось 7", len(sink.events))
	}
	if got := sink.events[0]; got.name != "poll_cycle" || got.fields[FieldDuration] != "3ms" {
		t.Errorf("событие poll_cycle = %+v", got)
	}
	failed := sink.events[5]
	if failed.name != "wake_failed" || failed.level != LevelWarn || failed.fields[FieldError] != "refused" {
		t.Errorf("событие wake_failed = %+v", failed)
	}
}

func TestDefaultSinkIsNoOpWithoutWiring(t *testing.T) {
	SetDefault(nil)
	PollCycle()
	VerdictPublished("ready", "watch:1", 1)
	WakeFailed("", "watch:1", errors.New("boom"))
	if Default() == nil {
		t.Fatal("Default не должен возвращать nil")
	}
}

func TestRecorderRoutesToLoggerAndMetrics(t *testing.T) {
	buf := &strings.Builder{}
	metrics := NewMetrics()
	rec := NewRecorder(NewLogger(LevelDebug, buf), metrics)
	SetDefault(rec)
	defer SetDefault(nil)

	VerdictPublished("retry", "watch:2", 9)
	if MetricsOf(Default()) != metrics {
		t.Fatal("MetricsOf не вернул установленный счётчик")
	}
	if metrics == nil || len(buf.String()) == 0 {
		t.Fatal("Recorder не направил событие в логгер")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(buf.String()), &got); err != nil {
		t.Fatalf("невалидный JSON: %q", buf.String())
	}
	if got[FieldVerdict] != "retry" || got[FieldPR] != float64(9) {
		t.Errorf("событие = %v", got)
	}
}
