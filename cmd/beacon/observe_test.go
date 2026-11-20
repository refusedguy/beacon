package main

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

func TestSetupObserveWritesStructuredRecordsToUniqueFiles(t *testing.T) {
	state := t.TempDir()
	t.Setenv("BEACON_STATE_DIR", state)
	t.Setenv("BEACON_LOG_FORMAT", "json")
	t.Setenv("BEACON_LOG_LEVEL", "info")

	previousSink := observe.Default()
	previousOutput := log.Writer()
	defer func() {
		observe.SetDefault(previousSink)
		log.SetOutput(previousOutput)
	}()

	var files []string
	for run := 0; run < 2; run++ {
		cleanup := setupObserve()
		observe.VerdictPublished("ready", "watch:1", 7)
		observe.WakeDelivered("opencode", "watch:1")
		log.Printf("legacy line %d", run)
		cleanup()

		latest := filepath.Join(state, "logs", "beacon.latest.log")
		target, err := os.Readlink(latest)
		if err != nil {
			t.Fatalf("beacon.latest.log не symlink: %v", err)
		}
		path := filepath.Join(state, "logs", target)
		if len(files) != 0 && files[len(files)-1] == path {
			t.Fatal("перезапуск перезаписал тот же файл")
		}
		files = append(files, path)

		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		events := make(map[string]bool)
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatalf("невалидная JSON-строка лога %q: %v", line, err)
			}
			event, ok := record[observe.FieldEvent].(string)
			if !ok {
				t.Fatalf("в записи нет поля event: %q", line)
			}
			events[event] = true
		}
		for _, want := range []string{"verdict_published", "wake_delivered", "legacy_log", "cycle"} {
			if !events[want] {
				t.Errorf("в логе нет события %s: %q", want, raw)
			}
		}
	}
}

func TestRunMetricsExportsAllCounters(t *testing.T) {
	metrics := observe.NewMetrics()
	observe.SetDefault(observe.NewRecorder(nil, metrics))
	defer observe.SetDefault(nil)
	for _, name := range observe.CounterNames() {
		metrics.Inc(name, "")
	}

	var stdout, stderr bytes.Buffer
	code := runMetrics(&CLI{out: &stdout, errOut: &stderr}, []string{"--format", "prometheus"})
	if code != 0 {
		t.Fatalf("код %d, stderr %q", code, stderr.String())
	}
	text := stdout.String()
	for _, want := range []string{
		"beacon_poll_cycles_total 1",
		"beacon_merge_failures_total 1",
		`beacon_wake_delivered_total{adapter=""} 1`,
		"# TYPE beacon_poll_cycle_duration_seconds histogram",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в метриках нет %q:\n%s", want, text)
		}
	}
}
