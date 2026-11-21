package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

// graphIndexFake serves a fixed graph status and counts reads, so a test can
// prove beacon_stats asks the port once per call.
type graphIndexFake struct {
	status domain.GraphStatus
	err    error
	calls  int
}

func (f *graphIndexFake) GraphStatus(context.Context) (domain.GraphStatus, error) {
	f.calls++
	if f.err != nil {
		return domain.GraphStatus{}, f.err
	}
	return f.status, nil
}

// TestGraphStatusReadyRule pins the domain rule behind graphReady: an index
// is ready only when it exists and its hash still matches the card tree.
func TestGraphStatusReadyRule(t *testing.T) {
	cases := []struct {
		name   string
		status domain.GraphStatus
		want   bool
	}{
		{"fresh", domain.GraphStatus{Built: true, IndexHash: "h1", SourceHash: "h1"}, true},
		{"stale", domain.GraphStatus{Built: true, IndexHash: "old", SourceHash: "new"}, false},
		{"not built", domain.GraphStatus{Built: false, IndexHash: "h1", SourceHash: "h1"}, false},
		{"empty source hash", domain.GraphStatus{Built: true, IndexHash: "", SourceHash: ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.status.Ready(); got != tc.want {
				t.Errorf("GraphStatus(%+v).Ready() = %t, want %t", tc.status, got, tc.want)
			}
		})
	}
}

// TestStatsGraphReadyFreshAndStale checks the marker an agent reads from
// beacon_stats for both a current index and one built from an older card set.
func TestStatsGraphReadyFreshAndStale(t *testing.T) {
	cases := []struct {
		name      string
		status    domain.GraphStatus
		wantEntry string
		wantReady bool
	}{
		{
			name:      "fresh index",
			status:    domain.GraphStatus{Built: true, IndexHash: "abc", SourceHash: "abc"},
			wantEntry: graphReadyPrefix + "true",
			wantReady: true,
		},
		{
			name:      "stale index",
			status:    domain.GraphStatus{Built: true, IndexHash: "old", SourceHash: "abc"},
			wantEntry: graphReadyPrefix + "false",
			wantReady: false,
		},
		{
			name:      "missing index",
			status:    domain.GraphStatus{},
			wantEntry: graphReadyPrefix + "false",
			wantReady: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &graphIndexFake{status: tc.status}
			handler := NewStatsHandler(&snapshotReaderFake{snap: sampleSnapshot()})
			handler.SetGraphIndexReader(fake)

			out := callStats(t, handler)

			if got := graphReadyEntry(out); got != tc.wantEntry {
				t.Errorf("graphReady entry = %q, want %q (loadErrors %v)", got, tc.wantEntry, out.LoadErrors)
			}
			if got := graphReady(out); got != tc.wantReady {
				t.Errorf("graphReady = %t, want %t (loadErrors %v)", got, tc.wantReady, out.LoadErrors)
			}
			if fake.calls != 1 {
				t.Errorf("graph index reads = %d, want 1", fake.calls)
			}
		})
	}
}

// TestStatsGraphReadyFailsClosed covers the two cases where the agent cannot
// verify the graph: no reader bound and a failing reader. Both must report
// false, and a failed read leaves a diagnostic behind.
func TestStatsGraphReadyFailsClosed(t *testing.T) {
	t.Run("no reader", func(t *testing.T) {
		handler := NewStatsHandler(&snapshotReaderFake{snap: sampleSnapshot()})
		out := callStats(t, handler)
		if graphReady(out) {
			t.Errorf("graphReady = true with no graph index reader, want false")
		}
		if got := graphReadyEntry(out); got != "" {
			t.Errorf("graphReady entry = %q with no reader, want no entry", got)
		}
	})

	t.Run("failed read", func(t *testing.T) {
		fake := &graphIndexFake{err: errors.New("index unreadable")}
		handler := NewStatsHandler(&snapshotReaderFake{snap: sampleSnapshot()})
		handler.SetGraphIndexReader(fake)

		out := callStats(t, handler)

		if graphReady(out) {
			t.Errorf("graphReady = true after a failed index read, want false")
		}
		found := false
		for _, entry := range out.LoadErrors {
			if len(entry) >= len(graphIndexErrorPrefix) && entry[:len(graphIndexErrorPrefix)] == graphIndexErrorPrefix {
				found = true
			}
		}
		if !found {
			t.Errorf("no %q diagnostic in loadErrors %v", graphIndexErrorPrefix, out.LoadErrors)
		}
	})
}
