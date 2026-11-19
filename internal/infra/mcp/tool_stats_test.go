package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func callStats(t *testing.T, handler *StatsHandler) StatsOutput {
	t.Helper()
	result, out, err := handler.Handle(context.Background(), nil, StatsInput{})
	if err != nil {
		t.Fatalf("beacon_stats: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("beacon_stats returned an error result: %#v", result)
	}
	return out
}

func TestStatsResponseHasEveryField(t *testing.T) {
	reader := &snapshotReaderFake{snap: sampleSnapshot()}
	out := callStats(t, NewStatsHandler(reader))

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal stats output: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal stats output: %v", err)
	}

	for _, key := range []string{
		"pointsThisHour", "budget", "activeWatches", "persist", "loadErrors", "ladder", "version",
	} {
		if _, ok := fields[key]; !ok {
			t.Errorf("stats output missing field %q: %s", key, raw)
		}
	}
	for key, sub := range map[string][]string{
		"persist": {"seq", "lastError"},
		"ladder":  {"pending", "escalated"},
	} {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(fields[key], &nested); err != nil {
			t.Fatalf("unmarshal %s: %v", key, err)
		}
		for _, name := range sub {
			if _, ok := nested[name]; !ok {
				t.Errorf("stats.%s missing field %q: %s", key, name, fields[key])
			}
		}
	}
	if bytes.Equal(bytes.TrimSpace(fields["loadErrors"]), []byte("null")) {
		t.Errorf("loadErrors is JSON null, want an array")
	}
}

func TestStatsValuesComeFromSnapshot(t *testing.T) {
	snap := sampleSnapshot()
	reader := &snapshotReaderFake{snap: snap}
	out := callStats(t, NewStatsHandler(reader))

	if out.PointsThisHour != snap.PointsThisHour {
		t.Errorf("pointsThisHour = %d, want %d", out.PointsThisHour, snap.PointsThisHour)
	}
	if out.Budget != snap.Budget {
		t.Errorf("budget = %d, want %d", out.Budget, snap.Budget)
	}
	if out.ActiveWatches != len(snap.ActiveWatches) {
		t.Errorf("activeWatches = %d, want %d", out.ActiveWatches, len(snap.ActiveWatches))
	}
	if out.Persist.Seq != snap.PersistSeq {
		t.Errorf("persist.seq = %d, want %d", out.Persist.Seq, snap.PersistSeq)
	}
	if out.Persist.LastError != snap.LastPersistErr {
		t.Errorf("persist.lastError = %q, want %q", out.Persist.LastError, snap.LastPersistErr)
	}
	if !reflect.DeepEqual(out.LoadErrors, snap.LoadErrors) {
		t.Errorf("loadErrors = %v, want %v", out.LoadErrors, snap.LoadErrors)
	}
	if out.Ladder.Pending != snap.LadderPending || out.Ladder.Escalated != snap.LadderEscalated {
		t.Errorf("ladder = %+v, want pending %d escalated %d", out.Ladder, snap.LadderPending, snap.LadderEscalated)
	}
	if out.Version != snap.Version {
		t.Errorf("version = %q, want %q", out.Version, snap.Version)
	}
}

func TestStatsDoesNotWriteState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"seq":1}`), 0600); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	before, beforeMod := stateFingerprint(t, path)

	reader := &snapshotReaderFake{path: path, snap: sampleSnapshot()}
	handler := NewStatsHandler(reader)
	callStats(t, handler)
	callStats(t, handler)

	after, afterMod := stateFingerprint(t, path)
	if !bytes.Equal(before, after) {
		t.Errorf("state file bytes changed: %q -> %q", before, after)
	}
	if !beforeMod.Equal(afterMod) {
		t.Errorf("state file mtime changed: %v -> %v", beforeMod, afterMod)
	}
	if reader.calls != 2 {
		t.Errorf("snapshot reads = %d, want 2", reader.calls)
	}
}

func TestStatsRepeatable(t *testing.T) {
	reader := &snapshotReaderFake{snap: sampleSnapshot()}
	handler := NewStatsHandler(reader)

	first := callStats(t, handler)
	second := callStats(t, handler)

	if !reflect.DeepEqual(first, second) {
		t.Errorf("two calls with unchanged state differ:\n%#v\n%#v", first, second)
	}
}

func TestStatsEmptySlicesStayArrays(t *testing.T) {
	snap := sampleSnapshot()
	snap.ActiveWatches = nil
	snap.LoadErrors = nil
	reader := &snapshotReaderFake{snap: snap}
	out := callStats(t, NewStatsHandler(reader))

	if out.LoadErrors == nil {
		t.Error("loadErrors is nil, want an empty array")
	}
	if out.ActiveWatches != 0 {
		t.Errorf("activeWatches = %d, want 0", out.ActiveWatches)
	}
}

func TestStatsPropagatesReadError(t *testing.T) {
	want := errors.New("store unavailable")
	reader := &snapshotReaderFake{err: want}
	handler := NewStatsHandler(reader)

	_, _, err := handler.Handle(context.Background(), nil, StatsInput{})
	if !errors.Is(err, want) {
		t.Fatalf("handler error = %v, want it to wrap %v", err, want)
	}
}
