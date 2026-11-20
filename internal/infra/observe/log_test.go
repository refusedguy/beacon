package observe

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggerWritesValidJSONToEveryOutput(t *testing.T) {
	first, second := new(bytes.Buffer), new(bytes.Buffer)
	log := NewLogger(LevelInfo, first, second)

	log.Write(Event{Level: LevelInfo, Fields: Fields{
		FieldEvent:   "verdict_published",
		FieldWatchID: "watch:1",
		FieldPR:      12,
		FieldVerdict: "ready",
		FieldAdapter: "opencode",
		FieldError:   "boom",
	}})

	for name, buf := range map[string]*bytes.Buffer{"first": first, "second": second} {
		line := buf.String()
		if !strings.HasSuffix(line, "\n") {
			t.Fatalf("%s: нет перевода строки: %q", name, line)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("%s: невалидный JSON %q: %v", name, line, err)
		}
		for key, want := range map[string]any{
			FieldEvent:   "verdict_published",
			FieldWatchID: "watch:1",
			FieldVerdict: "ready",
			FieldAdapter: "opencode",
			FieldError:   "boom",
		} {
			if got[key] != want {
				t.Errorf("%s: поле %s = %v, ожидалось %v", name, key, got[key], want)
			}
		}
		if pr, ok := got[FieldPR].(float64); !ok || int(pr) != 12 {
			t.Errorf("%s: поле pr = %v, ожидалось 12", name, got[FieldPR])
		}
		if _, ok := got["time"].(string); !ok {
			t.Errorf("%s: нет поля time", name)
		}
	}
}

func TestLoggerFiltersBelowMinimumLevel(t *testing.T) {
	buf := new(bytes.Buffer)
	log := NewLogger(LevelWarn, buf)

	log.Write(Event{Level: LevelDebug, Fields: Fields{FieldEvent: "debug"}})
	log.Write(Event{Level: LevelInfo, Fields: Fields{FieldEvent: "info"}})
	if buf.Len() != 0 {
		t.Fatalf("debug и info не должны попасть в лог: %q", buf.String())
	}
	log.Write(Event{Level: LevelError, Fields: Fields{FieldEvent: "error"}})
	if !strings.Contains(buf.String(), "error") {
		t.Fatalf("error должен попасть в лог: %q", buf.String())
	}
}

func TestOpenLogFileAppendsToRequestedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "beacon.log")
	f, err := OpenLogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	log := NewLogger(LevelInfo, f)
	log.Write(Event{Level: LevelInfo, Fields: Fields{FieldEvent: "first"}})
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	f, err = OpenLogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	log = NewLogger(LevelInfo, f)
	log.Write(Event{Level: LevelInfo, Fields: Fields{FieldEvent: "second"}})
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"event":"first"`) || !strings.Contains(string(raw), `"event":"second"`) {
		t.Fatalf("в файле нет обеих записей: %q", raw)
	}
}
