package observe

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type capturedEvent struct {
	name   string
	level  slog.Level
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

func (s *captureSink) Log(level Level, event string, fields Fields) {
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
	if failed.name != "wake_error" || failed.level != LevelWarn || failed.fields[FieldError] != "refused" {
		t.Errorf("событие wake_error = %+v", failed)
	}
}

func TestFixDeliveredCountsOnlyFixerPath(t *testing.T) {
	sink := newCaptureSink()
	SetDefault(sink)
	defer SetDefault(nil)

	WakeDelivered("opencode", "watch:exec")
	FixDelivered("watch:fix")

	if sink.counts[CounterFixTotal] != 1 {
		t.Errorf("счётчик %s = %d, ожидалось 1", CounterFixTotal, sink.counts[CounterFixTotal])
	}
	if sink.labels[CounterFixTotal] != "fixer" {
		t.Errorf("метка role = %q", sink.labels[CounterFixTotal])
	}
	if sink.counts[CounterWakeDelivered] != 1 {
		t.Errorf("счётчик %s = %d, ожидалось 1", CounterWakeDelivered, sink.counts[CounterWakeDelivered])
	}
	delivered := false
	for _, e := range sink.events {
		if e.name == "fix_delivered" && e.fields[FieldRole] == "fixer" && e.fields[FieldWatchID] == "watch:fix" {
			delivered = true
		}
	}
	if !delivered {
		t.Errorf("событие fix_delivered не найдено: %+v", sink.events)
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

func TestParseLevel(t *testing.T) {
	for raw, want := range map[string]Level{
		"debug": LevelDebug, "INFO": LevelInfo, " warn ": LevelWarn,
		"error": LevelError, "": LevelInfo,
	} {
		got, ok := ParseLevel(raw)
		if !ok || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v", raw, got, ok)
		}
	}
	if _, ok := ParseLevel("trace"); ok {
		t.Error("trace не должен распознаваться")
	}
}

func TestRecorderRoutesToLoggerAndMetrics(t *testing.T) {
	buf := &strings.Builder{}
	metrics := NewMetrics()
	rec := NewRecorder(NewLogger("json", LevelDebug, buf), metrics)
	SetDefault(rec)
	defer SetDefault(nil)

	VerdictPublished("retry", "watch:2", 9)
	if MetricsOf(Default()) != metrics {
		t.Fatal("MetricsOf не вернул установленный счётчик")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(buf.String()), &got); err != nil {
		t.Fatalf("невалидный JSON: %q", buf.String())
	}
	if got[FieldVerdict] != "retry" || got[FieldPR] != float64(9) {
		t.Errorf("событие = %v", got)
	}
}

// withContentCapture restores the previous mode when the test ends so global
// state does not leak into other tests in the package.
func withContentCapture(t *testing.T, mode ContentCaptureMode) {
	t.Helper()
	prev := ContentCapture()
	SetContentCapture(mode)
	t.Cleanup(func() { SetContentCapture(prev) })
}

func TestContentCaptureDefaultsToNoContent(t *testing.T) {
	SetContentCapture("")
	if got := ContentCapture(); got != ContentCaptureNoContent {
		t.Fatalf("режим после сброса = %q, ожидалось %q", got, ContentCaptureNoContent)
	}
}

func TestParseContentCaptureMode(t *testing.T) {
	for raw, want := range map[string]ContentCaptureMode{
		"":           ContentCaptureNoContent,
		"  ":         ContentCaptureNoContent,
		"no_content": ContentCaptureNoContent,
		"NONE":       ContentCaptureNoContent,
		"disabled":   ContentCaptureNoContent,
		"misspelled": ContentCaptureNoContent,
		"enabled":    ContentCaptureEnabled,
		" ALL ":      ContentCaptureEnabled,
		"content":    ContentCaptureEnabled,
	} {
		if got := ParseContentCaptureMode(raw); got != want {
			t.Errorf("ParseContentCaptureMode(%q) = %q, ожидалось %q", raw, got, want)
		}
	}
}

func TestSanitizeContentNoContentKeepsOnlyRef(t *testing.T) {
	withContentCapture(t, ContentCaptureNoContent)
	ref := "s3://beacon-content/watch-1"
	in := Fields{
		FieldWatchID:            "watch:1",
		FieldWakeText:           "WAKE-SECRET",
		FieldToolArguments:      "ARG-SECRET",
		FieldSystemInstructions: "SYSTEM-SECRET",
		FieldPRDiff:             "DIFF-SECRET",
		FieldContentRef:         ref,
	}

	out := SanitizeContent(in)

	for _, key := range []string{
		FieldWakeText, FieldToolArguments, FieldSystemInstructions, FieldPRDiff,
	} {
		if _, ok := out[key]; ok {
			t.Errorf("поле %s попало в телеметрию: %v", key, out)
		}
		if _, ok := in[key]; !ok {
			t.Errorf("SanitizeContent изменил входную карту: нет ключа %s", key)
		}
	}
	if out[FieldContentRef] != ref {
		t.Errorf("ссылка = %v, ожидалось %q", out[FieldContentRef], ref)
	}
	if out[FieldWatchID] != "watch:1" {
		t.Errorf("обычное поле потеряно: %v", out)
	}
}

func TestSanitizeContentNoContentWithoutRefMarksWithheld(t *testing.T) {
	withContentCapture(t, ContentCaptureNoContent)
	out := SanitizeContent(Fields{FieldWakeText: "WAKE-SECRET"})
	if out[FieldContentRef] != "withheld" {
		t.Errorf("ссылка = %v, ожидалось %q", out[FieldContentRef], "withheld")
	}
	if _, ok := out[FieldWakeText]; ok {
		t.Errorf("текст wake попал в телеметрию: %v", out)
	}
}

func TestSanitizeContentEnabledKeepsContent(t *testing.T) {
	withContentCapture(t, ContentCaptureEnabled)
	in := Fields{
		FieldWakeText:   "WAKE-SECRET",
		FieldPRDiff:     "DIFF-SECRET",
		FieldContentRef: "s3://beacon-content/watch-1",
	}
	out := SanitizeContent(in)
	if len(out) != len(in) {
		t.Fatalf("полей %d, ожидалось %d: %v", len(out), len(in), out)
	}
	for k, v := range in {
		if out[k] != v {
			t.Errorf("поле %s изменено при opt-in: %v", k, out[k])
		}
	}
}

func TestRecorderLogDropsContentWithoutOptIn(t *testing.T) {
	withContentCapture(t, ContentCaptureNoContent)
	buf := &strings.Builder{}
	rec := NewRecorder(NewLogger("json", LevelDebug, buf), NewMetrics())

	rec.Log(LevelInfo, "content_probe", Fields{
		FieldWatchID:            "watch:1",
		FieldWakeText:           "WAKE-SECRET",
		FieldToolArguments:      "ARG-SECRET",
		FieldSystemInstructions: "SYSTEM-SECRET",
		FieldPRDiff:             "DIFF-SECRET",
		FieldContentRef:         "s3://beacon-content/watch-1",
	})

	raw := buf.String()
	for _, secret := range []string{"WAKE-SECRET", "ARG-SECRET", "SYSTEM-SECRET", "DIFF-SECRET"} {
		if strings.Contains(raw, secret) {
			t.Errorf("секрет %q попал в лог: %q", secret, raw)
		}
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("невалидный JSON: %q", raw)
	}
	if got[FieldContentRef] != "s3://beacon-content/watch-1" {
		t.Errorf("ссылка = %v", got[FieldContentRef])
	}
	if got[FieldWatchID] != "watch:1" {
		t.Errorf("обычное поле = %v", got[FieldWatchID])
	}
}

func TestRecorderLogKeepsContentWithOptIn(t *testing.T) {
	withContentCapture(t, ContentCaptureEnabled)
	buf := &strings.Builder{}
	rec := NewRecorder(NewLogger("json", LevelDebug, buf), NewMetrics())

	rec.Log(LevelInfo, "content_probe", Fields{FieldWakeText: "WAKE-SECRET"})

	if !strings.Contains(buf.String(), "WAKE-SECRET") {
		t.Errorf("текст не попал в лог при opt-in: %q", buf.String())
	}
}

func TestAttemptToWriteContentWithoutOptInKeepsOnlyRef(t *testing.T) {
	withContentCapture(t, ContentCaptureNoContent)
	buf := &strings.Builder{}
	rec := NewRecorder(NewLogger("json", LevelDebug, buf), NewMetrics())
	SetDefault(rec)
	defer SetDefault(nil)

	// This is the boundary an unwitting caller hits: it passes all four
	// content categories with no reference. The write must keep only the
	// "withheld" marker, never the text.
	Event(LevelInfo, "content_probe", Fields{
		FieldWakeText:           "WAKE-SECRET",
		FieldToolArguments:      "ARG-SECRET",
		FieldSystemInstructions: "SYSTEM-SECRET",
		FieldPRDiff:             "DIFF-SECRET",
	})

	raw := buf.String()
	for _, secret := range []string{"WAKE-SECRET", "ARG-SECRET", "SYSTEM-SECRET", "DIFF-SECRET"} {
		if strings.Contains(raw, secret) {
			t.Errorf("секрет %q попал в лог: %q", secret, raw)
		}
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("невалидный JSON: %q", raw)
	}
	if got[FieldContentRef] != "withheld" {
		t.Errorf("ссылка = %v, ожидалось %q", got[FieldContentRef], "withheld")
	}
	if got[FieldEvent] != "content_probe" {
		t.Errorf("событие = %v", got[FieldEvent])
	}
	if got := ContentCapture(); got != ContentCaptureNoContent {
		t.Fatalf("режим изменился при попытке записи: %q", got)
	}
}
