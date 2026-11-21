package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPollerStateRoundTrip(t *testing.T) {
	m := NewMetrics()
	m.SetPoller(true, 4242)

	want := PollerState{Active: true, PID: 4242}
	if got := m.Poller(); got != want {
		t.Fatalf("Poller() = %+v, want %+v", got, want)
	}

	text := string(m.Prometheus())
	for _, line := range []string{
		"# HELP beacon_poller_active Whether the CI poller is active.",
		"# TYPE beacon_poller_active gauge",
		"beacon_poller_active 1",
		"# TYPE beacon_poller_pid gauge",
		"beacon_poller_pid 4242",
	} {
		if !strings.Contains(text, line) {
			t.Fatalf("Prometheus output missing %q:\n%s", line, text)
		}
	}
}

func TestPollerStateInJSON(t *testing.T) {
	m := NewMetrics()
	m.SetPoller(true, 4242)

	var decoded metricsJSON
	if err := json.Unmarshal(m.JSON(), &decoded); err != nil {
		t.Fatalf("decode metrics JSON: %v", err)
	}
	if decoded.PollerState != (PollerState{Active: true, PID: 4242}) {
		t.Fatalf("JSON poller state = %+v", decoded.PollerState)
	}

	raw := string(m.JSON())
	for _, field := range []string{`"pollerActive": true`, `"pollerPID": 4242`} {
		if !strings.Contains(raw, field) {
			t.Fatalf("metrics JSON missing %s: %s", field, raw)
		}
	}
}

func TestPollerStateInactiveDropsPID(t *testing.T) {
	m := NewMetrics()
	m.SetPoller(true, 4242)
	m.SetPoller(false, 4242)

	if got := m.Poller(); got != (PollerState{}) {
		t.Fatalf("inactive Poller() = %+v, want zero state", got)
	}
	text := string(m.Prometheus())
	if !strings.Contains(text, "beacon_poller_active 0") || !strings.Contains(text, "beacon_poller_pid 0") {
		t.Fatalf("inactive poller gauges missing:\n%s", text)
	}

	m.SetPoller(true, 4242)
	m.Reset()
	if got := m.Poller(); got != (PollerState{}) {
		t.Fatalf("Reset Poller() = %+v, want zero state", got)
	}
}
