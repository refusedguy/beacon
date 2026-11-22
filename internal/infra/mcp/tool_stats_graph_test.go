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
		wantSrc   GraphReadySource
	}{
		{
			name:      "fresh index",
			status:    domain.GraphStatus{Built: true, IndexHash: "abc", SourceHash: "abc"},
			wantEntry: graphReadyPrefix + "true",
			wantReady: true,
			wantSrc:   GraphReadyFresh,
		},
		{
			name:      "stale index",
			status:    domain.GraphStatus{Built: true, IndexHash: "old", SourceHash: "abc"},
			wantEntry: graphReadyPrefix + "false",
			wantReady: false,
			wantSrc:   GraphReadyStale,
		},
		{
			name:      "missing index",
			status:    domain.GraphStatus{},
			wantEntry: graphReadyPrefix + "false",
			wantReady: false,
			wantSrc:   GraphReadyStale,
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
			signal := ReadGraphReady(out)
			if signal.Ready != tc.wantReady {
				t.Errorf("ReadGraphReady.Ready = %t, want %t (loadErrors %v)", signal.Ready, tc.wantReady, out.LoadErrors)
			}
			if signal.Source != tc.wantSrc {
				t.Errorf("ReadGraphReady.Source = %q, want %q (loadErrors %v)", signal.Source, tc.wantSrc, out.LoadErrors)
			}
			if got := out.GraphReady(); got != signal {
				t.Errorf("out.GraphReady() = %+v, want %+v", got, signal)
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
		signal := ReadGraphReady(out)
		if signal.Ready || signal.Source != GraphReadyNoReader {
			t.Errorf("ReadGraphReady = %+v with no reader, want not ready and source %q", signal, GraphReadyNoReader)
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
		signal := ReadGraphReady(out)
		if signal.Ready || signal.Source != GraphReadyReadError {
			t.Errorf("ReadGraphReady = %+v after a failed read, want not ready and source %q", signal, GraphReadyReadError)
		}
	})
}

// TestGraphReadyLegacyMarkerStillReadable proves the string marker remains a
// compatible output. An older reader that only parses loadErrors still sees
// the same entry, while the typed signal reports the same readiness.
func TestGraphReadyLegacyMarkerStillReadable(t *testing.T) {
	cases := []struct {
		name      string
		entry     string
		wantReady bool
		wantSrc   GraphReadySource
	}{
		{"ready marker", graphReadyPrefix + "true", true, GraphReadyFresh},
		{"not ready marker", graphReadyPrefix + "false", false, GraphReadyStale},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := StatsOutput{LoadErrors: []string{tc.entry}}
			if got := graphReadyEntry(out); got != tc.entry {
				t.Errorf("graphReadyEntry = %q, want %q", got, tc.entry)
			}
			signal := ReadGraphReady(out)
			if signal.Ready != tc.wantReady || signal.Source != tc.wantSrc {
				t.Errorf("ReadGraphReady = %+v, want ready %t and source %q", signal, tc.wantReady, tc.wantSrc)
			}
			if got := out.GraphReady(); got != signal {
				t.Errorf("out.GraphReady() = %+v, want %+v", got, signal)
			}
		})
	}
}
