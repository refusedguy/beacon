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

func TestSetupObserveWritesStructuredRecordsToServerLog(t *testing.T) {
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

	// A server command owns the rotating log and both latest links.
	cleanup := setupObserve("mcp")
	observe.VerdictPublished("ready", "watch:1", 7)
	observe.WakeDelivered("opencode", "watch:1")
	log.Printf("legacy line")
	cleanup()

	for _, link := range []string{"beacon.latest.log", "beacon.mcp.latest.log"} {
		target, err := os.Readlink(filepath.Join(state, "logs", link))
		if err != nil {
			t.Fatalf("%s не symlink: %v", link, err)
		}
		if target != "beacon.mcp.jsonl" {
			t.Fatalf("%s -> %q, ожидался beacon.mcp.jsonl", link, target)
		}
	}

	raw, err := os.ReadFile(filepath.Join(state, "logs", "beacon.mcp.jsonl"))
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

func TestSetupObserveCLILogLeavesServerLinksAlone(t *testing.T) {
	state := t.TempDir()
	t.Setenv("BEACON_STATE_DIR", state)
	t.Setenv("BEACON_LOG_FORMAT", "json")
	previousSink := observe.Default()
	previousOutput := log.Writer()
	defer func() {
		observe.SetDefault(previousSink)
		log.SetOutput(previousOutput)
	}()

	cleanup := setupObserve("mcp")
	observe.Count("beacon_poll_cycles", "")
	cleanup()

	links := make(map[string]string)
	for _, name := range []string{"beacon.latest.log", "beacon.mcp.latest.log"} {
		target, err := os.Readlink(filepath.Join(state, "logs", name))
		if err != nil {
			t.Fatalf("%s не symlink: %v", name, err)
		}
		links[name] = target
	}

	cleanup = setupObserve("list")
	observe.Count("beacon_poll_cycles", "")
	cleanup()

	for name, want := range links {
		got, err := os.Readlink(filepath.Join(state, "logs", name))
		if err != nil || got != want {
			t.Fatalf("короткая команда изменила %s: %q, ожидалось %q", name, got, want)
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

func TestIsServerCommandOnlySelectsLongLivedProcesses(t *testing.T) {
	for _, name := range []string{"mcp", "daemon", " mcp "} {
		if !isServerCommand(name) {
			t.Errorf("%q should use the server log", name)
		}
	}
	for _, name := range []string{"logs", "inspect", "reset", "list", ""} {
		if isServerCommand(name) {
			t.Errorf("%q must not move the server latest link", name)
		}
	}
}
