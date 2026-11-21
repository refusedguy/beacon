package mcp

import (
	"strings"
	"testing"
)

func TestStatsReportPollerStatus(t *testing.T) {
	t.Cleanup(ClearPollerStatus)
	SetPollerStatus(PollerStatus{Active: true, PID: 4242})

	out := callStats(t, NewStatsHandler(&snapshotReaderFake{snap: sampleSnapshot()}))
	entry := pollerStatsEntry(out)
	if !strings.Contains(entry, "pollerActive=true") {
		t.Fatalf("stats poller entry = %q, want pollerActive=true", entry)
	}
	if !strings.Contains(entry, "pollerPID=4242") {
		t.Fatalf("stats poller entry = %q, want pollerPID=4242", entry)
	}
}

func TestStatsReportInactivePollerWithoutPID(t *testing.T) {
	t.Cleanup(ClearPollerStatus)
	SetPollerStatus(PollerStatus{Active: false, PID: 4242})

	out := callStats(t, NewStatsHandler(&snapshotReaderFake{snap: sampleSnapshot()}))
	entry := pollerStatsEntry(out)
	if !strings.Contains(entry, "pollerActive=false") {
		t.Fatalf("stats poller entry = %q, want pollerActive=false", entry)
	}
	if !strings.Contains(entry, "pollerPID=0") {
		t.Fatalf("stats poller entry = %q, want pollerPID=0", entry)
	}
}

func TestStatsOmitPollerEntryBeforePublication(t *testing.T) {
	ClearPollerStatus()
	out := callStats(t, NewStatsHandler(&snapshotReaderFake{snap: sampleSnapshot()}))
	if entry := pollerStatsEntry(out); entry != "" {
		t.Fatalf("stats poller entry = %q, want no entry", entry)
	}
}
