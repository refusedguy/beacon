package observe

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedactCoversLogSecretPatterns(t *testing.T) {
	in := []byte(`token ghp_abcdefghijklmnopqrstuvwxyz0123456789 ` +
		`auth="Authorization: Bearer supersecretvalue123" ` +
		`url=https://alice:hunter2@github.com/org/repo.git`)
	out := string(Redact(in))
	for _, secret := range []string{
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"supersecretvalue123",
		"alice:hunter2",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q survived log redaction: %q", secret, out)
		}
	}
	for _, keep := range []string{"Authorization", "https://", "github.com/org/repo.git"} {
		if !strings.Contains(out, keep) {
			t.Errorf("missing %q in redacted line: %q", keep, out)
		}
	}
}

// TestServerLogRedactsSecrets proves no credential reaches beacon.mcp.jsonl:
// the JSONL logger's writer applies the same filter as the store.
func TestServerLogRedactsSecrets(t *testing.T) {
	t.Setenv(LogLevelEnv, "info")
	run, err := OpenServerLog(t.TempDir())
	if err != nil {
		t.Fatalf("OpenServerLog: %v", err)
	}
	logger := run.Logger()
	if logger == nil {
		t.Fatal("server log has no preconfigured logger")
	}
	logger.Info("published",
		"token", "ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"auth", "Authorization: Bearer supersecretvalue123",
		"url", "https://alice:hunter2@github.com/org/repo.git")
	if err := run.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(run.Path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	for _, secret := range []string{
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"supersecretvalue123",
		"alice:hunter2",
	} {
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("secret %q reached %s: %s", secret, run.Path, data)
		}
	}
	if !bytes.Contains(data, []byte("[REDACTED]")) {
		t.Errorf("no redaction marker in log: %s", data)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &record); err != nil {
		t.Errorf("redacted log line is not valid JSON: %v (%s)", err, data)
	}
}

func TestTextHandlerIsReadableAndKeepsAttributes(t *testing.T) {
	buf := new(bytes.Buffer)
	rec := NewRecorder(NewLogger("text", LevelInfo, buf), NewMetrics())

	rec.Log(LevelInfo, "verdict_published", Fields{
		FieldWatchID: "watch:1",
		FieldPR:      12,
		FieldVerdict: "ready",
		FieldAdapter: "opencode",
		FieldError:   "boom",
	})

	line := buf.String()
	for _, want := range []string{
		"level=INFO",
		"event=verdict_published",
		"watchID=watch:1",
		"pr=12",
		"verdict=ready",
		"adapter=opencode",
		"error=boom",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("в текстовом логе нет %q: %q", want, line)
		}
	}
	if strings.Contains(line, "msg=") {
		t.Errorf("пустое сообщение slog не должно попадать в лог: %q", line)
	}
}

func TestJSONHandlerEmitsValidJSONWithAttributes(t *testing.T) {
	buf := new(bytes.Buffer)
	rec := NewRecorder(NewLogger("json", LevelInfo, buf), NewMetrics())

	rec.Log(LevelInfo, "verdict_published", Fields{
		FieldWatchID: "watch:1",
		FieldPR:      12,
		FieldVerdict: "ready",
	})

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("невалидный JSON %q: %v", buf.String(), err)
	}
	for key, want := range map[string]any{
		FieldEvent:   "verdict_published",
		FieldWatchID: "watch:1",
		FieldVerdict: "ready",
		"level":      "INFO",
	} {
		if got[key] != want {
			t.Errorf("поле %s = %v, ожидалось %v", key, got[key], want)
		}
	}
	if pr, ok := got[FieldPR].(float64); !ok || int(pr) != 12 {
		t.Errorf("поле pr = %v, ожидалось 12", got[FieldPR])
	}
	if _, ok := got["time"].(string); !ok {
		t.Error("нет поля time")
	}
}

func TestHandlerFiltersBelowMinimumLevel(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		buf := new(bytes.Buffer)
		rec := NewRecorder(NewLogger(format, LevelWarn, buf), NewMetrics())
		rec.Log(LevelDebug, "debug", nil)
		rec.Log(LevelInfo, "info", nil)
		if buf.Len() != 0 {
			t.Errorf("%s: debug и info не должны попасть в лог: %q", format, buf.String())
		}
		rec.Log(LevelError, "error", nil)
		if strings.Contains(buf.String(), "debug") || strings.Contains(buf.String(), "info") {
			t.Errorf("%s: в логе есть отфильтрованные уровни: %q", format, buf.String())
		}
		if !strings.Contains(buf.String(), "event=error") && !strings.Contains(buf.String(), `"event":"error"`) {
			t.Errorf("%s: error должен попасть в лог: %q", format, buf.String())
		}
	}
}

func TestLogWriterTurnsLegacyLineIntoStructuredRecord(t *testing.T) {
	buf := new(bytes.Buffer)
	logger := NewLogger("json", LevelInfo, buf)
	writer := NewLogWriter(logger)
	if n, err := writer.Write([]byte("cibridge: verdict published\n")); err != nil || n != len("cibridge: verdict published\n") {
		t.Fatalf("Write = %d, %v", n, err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("невалидный JSON %q: %v", buf.String(), err)
	}
	if got[FieldEvent] != "legacy_log" || got["message"] != "cibridge: verdict published" {
		t.Errorf("запись = %v", got)
	}
	if _, err := writer.Write(nil); err != nil {
		t.Errorf("пустая запись не должна ошибку: %v", err)
	}
}

func TestOpenRunLogCreatesUniqueFileEachInitialization(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 20, 12, 34, 56, 123456789, time.UTC)

	first, err := OpenRunLog(dir, now, 4242)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Writer().Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := OpenRunLog(dir, now, 4242)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	if _, err := second.Writer().Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}

	if first.Path == second.Path {
		t.Fatalf("две инициализации получили один файл: %s", first.Path)
	}
	wantPrefix := "beacon-20261120T123456.123456789Z-4242"
	for _, path := range []string{first.Path, second.Path} {
		if filepath.Dir(path) != filepath.Join(dir, "logs") {
			t.Errorf("файл не в logs: %s", path)
		}
		if !strings.HasPrefix(filepath.Base(path), wantPrefix) || !strings.HasSuffix(path, ".log") {
			t.Errorf("имя файла %s не начинается с %s", path, wantPrefix)
		}
	}

	latest := filepath.Join(dir, "logs", "beacon.latest.log")
	target, err := os.Readlink(latest)
	if err != nil {
		t.Fatalf("beacon.latest.log не symlink: %v", err)
	}
	if target != filepath.Base(second.Path) {
		t.Errorf("symlink указывает на %q, ожидалось %q", target, filepath.Base(second.Path))
	}

	raw, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "first\n" {
		t.Errorf("в первом файле %q, ожидалось только \"first\\n\"", raw)
	}
}

// TestCycleCorrelationChainByCycleID proves the correlation fields survive the
// write boundary under the default NO_CONTENT policy. One cycleID selects the
// Watch event, the verdict and the delivery receipt. A foreign cycleID stays
// out of the selection. Wake text is dropped and only a content reference is
// kept, so the receipt carries identifiers but no message body.
func TestCycleCorrelationChainByCycleID(t *testing.T) {
	if got := ContentCapture(); got != ContentCaptureNoContent {
		t.Fatalf("capture mode = %q, want %q by default", got, ContentCaptureNoContent)
	}

	sink := newCaptureSink()
	SetDefault(sink)
	defer SetDefault(nil)

	const cycleID = "20261121T120000.000000000Z-7"
	chain := []struct {
		event  string
		fields Fields
	}{
		{"cycle_start", Fields{FieldWatches: 1}},
		{"observe", Fields{
			FieldWatchID: "watch:o/r:4:sha", FieldRevision: uint64(3), FieldVerdict: "watching",
		}},
		{"verdict", Fields{
			FieldWatchID: "watch:o/r:4:sha", FieldRevision: uint64(3), FieldVerdict: "ready",
		}},
		{"wake", Fields{
			FieldWatchID: "watch:o/r:4:sha", FieldObligationID: "obligation:1",
			FieldRevision: uint64(3), FieldRole: "executor", FieldAdapter: "opencode",
			FieldVerdict: "ready", FieldAttempt: 1,
			FieldWakeText: "keep_working: конкретные failedChecks: build",
		}},
	}
	for _, e := range chain {
		Event(LevelInfo, e.event, mergeCycleFields(cycleID, e.fields))
	}
	// Negative control: this wake belongs to another cycle and must not join.
	Event(LevelInfo, "wake", mergeCycleFields("foreign-cycle", Fields{FieldWatchID: "watch:other"}))

	var selected []capturedEvent
	for _, e := range sink.events {
		if e.fields[FieldCycleID] == cycleID {
			selected = append(selected, e)
		}
	}
	if len(selected) != len(chain) {
		t.Fatalf("selected %d events for %s, want %d: %+v", len(selected), cycleID, len(chain), sink.events)
	}
	for i, e := range selected {
		if e.name != chain[i].event {
			t.Errorf("selected[%d] = %q, want %q", i, e.name, chain[i].event)
		}
	}

	receipt := selected[len(selected)-1]
	for _, key := range []string{
		FieldWatchID, FieldObligationID, FieldRevision, FieldRole,
		FieldAdapter, FieldVerdict, FieldAttempt,
	} {
		if _, ok := receipt.fields[key]; !ok {
			t.Errorf("receipt has no %s: %+v", key, receipt.fields)
		}
	}
	if got := receipt.fields[FieldRole]; got != "executor" {
		t.Errorf("receipt role = %v, want executor", got)
	}
	if got := receipt.fields[FieldAdapter]; got != "opencode" {
		t.Errorf("receipt adapter = %v, want opencode", got)
	}
	if _, ok := receipt.fields[FieldWakeText]; ok {
		t.Errorf("receipt kept wake text under NO_CONTENT: %+v", receipt.fields)
	}
	if got := receipt.fields[FieldContentRef]; got != "withheld" {
		t.Errorf("receipt contentRef = %v, want withheld", got)
	}
}

// mergeCycleFields attaches a cycle id without mutating the caller's map, the
// same contract cycleEvent uses in cibridge.
func mergeCycleFields(cycleID string, fields Fields) Fields {
	out := make(Fields, len(fields)+1)
	for k, v := range fields {
		out[k] = v
	}
	out[FieldCycleID] = cycleID
	return out
}
