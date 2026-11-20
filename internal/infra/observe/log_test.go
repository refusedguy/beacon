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
