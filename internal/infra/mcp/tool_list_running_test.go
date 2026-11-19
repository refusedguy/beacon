package mcp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// snapshotReaderFake serves a fixed snapshot. When path is set it reads that
// file on every call, so a test can compare the file before and after a tool
// call. The fake has no method that writes, just like SnapshotReader.
type snapshotReaderFake struct {
	snap  StateSnapshot
	err   error
	path  string
	calls int
}

func (f *snapshotReaderFake) Snapshot() (StateSnapshot, error) {
	f.calls++
	if f.path != "" {
		if _, err := os.ReadFile(f.path); err != nil {
			return StateSnapshot{}, err
		}
	}
	if f.err != nil {
		return StateSnapshot{}, f.err
	}
	return f.snap, nil
}

func sampleWatch(id string, verdict domain.Verdict, subscribers int) domain.Watch {
	subs := make([]domain.Subscriber, subscribers)
	for i := range subs {
		subs[i] = domain.Subscriber{ID: id + "-s" + string(rune('a'+i)), Role: domain.RoleExecutor}
	}
	return domain.Watch{
		ID:          id,
		Repo:        "refusedguy/beacon",
		PR:          1,
		Status:      domain.StatusWatching,
		Verdict:     verdict,
		Subscribers: subs,
	}
}

func sampleSnapshot() StateSnapshot {
	return StateSnapshot{
		ActiveWatches: []domain.Watch{
			sampleWatch("w1", domain.VerdictReady, 2),
			sampleWatch("w2", domain.VerdictWatching, 1),
			sampleWatch("w3", domain.VerdictReady, 0),
		},
		PointsThisHour:  42,
		Budget:          500,
		PersistSeq:      7,
		LastPersistErr:  "disk full",
		LoadErrors:      []string{"line 3: bad payload"},
		LadderPending:   2,
		LadderEscalated: 1,
		Version:         "4",
	}
}

func stateFingerprint(t *testing.T, path string) ([]byte, time.Time) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}
	return data, info.ModTime()
}

func TestListRunningCountsMatchWatches(t *testing.T) {
	reader := &snapshotReaderFake{snap: sampleSnapshot()}
	handler := NewListRunningHandler(reader)

	out := callListRunning(t, handler, ListRunningInput{})

	if out.Counts.Watches != len(out.Watches) {
		t.Errorf("counts.watches = %d, len(watches) = %d", out.Counts.Watches, len(out.Watches))
	}
	sum := 0
	for _, n := range out.Counts.ByVerdict {
		sum += n
	}
	if sum != out.Counts.Watches {
		t.Errorf("byVerdict sum = %d, counts.watches = %d", sum, out.Counts.Watches)
	}
	subs := 0
	for _, w := range out.Watches {
		subs += len(w.Subscribers)
	}
	if out.Counts.Subscribers != subs {
		t.Errorf("counts.subscribers = %d, want %d", out.Counts.Subscribers, subs)
	}
	if got := out.Counts.ByVerdict[string(domain.VerdictReady)]; got != 2 {
		t.Errorf("byVerdict[ready] = %d, want 2", got)
	}
}

func TestListRunningFiltersKeepCountsConsistent(t *testing.T) {
	reader := &snapshotReaderFake{snap: sampleSnapshot()}
	handler := NewListRunningHandler(reader)

	in := ListRunningInput{Filters: &ListRunningFilters{Verdict: string(domain.VerdictReady)}}
	out := callListRunning(t, handler, in)

	if len(out.Watches) != 2 {
		t.Fatalf("filtered watches = %d, want 2", len(out.Watches))
	}
	for _, w := range out.Watches {
		if w.Verdict != domain.VerdictReady {
			t.Errorf("watch %s has verdict %s, want ready", w.ID, w.Verdict)
		}
	}
	if out.Counts.Watches != len(out.Watches) {
		t.Errorf("counts.watches = %d, len(watches) = %d", out.Counts.Watches, len(out.Watches))
	}
	if out.Counts.ByVerdict[string(domain.VerdictWatching)] != 0 {
		t.Errorf("byVerdict still includes watching watches: %v", out.Counts.ByVerdict)
	}
	if out.Counts.Subscribers != 2 {
		t.Errorf("counts.subscribers = %d, want 2", out.Counts.Subscribers)
	}
}

func TestListRunningDoesNotWriteState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"seq":1}`), 0600); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	before, beforeMod := stateFingerprint(t, path)

	reader := &snapshotReaderFake{path: path, snap: sampleSnapshot()}
	handler := NewListRunningHandler(reader)
	callListRunning(t, handler, ListRunningInput{})
	callListRunning(t, handler, ListRunningInput{})

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

func TestListRunningRepeatable(t *testing.T) {
	reader := &snapshotReaderFake{snap: sampleSnapshot()}
	handler := NewListRunningHandler(reader)

	first := callListRunning(t, handler, ListRunningInput{})
	second := callListRunning(t, handler, ListRunningInput{})

	if !reflect.DeepEqual(first, second) {
		t.Errorf("two calls with unchanged state differ:\n%#v\n%#v", first, second)
	}
}

func TestListRunningPropagatesReadError(t *testing.T) {
	want := errors.New("store unavailable")
	reader := &snapshotReaderFake{err: want}
	handler := NewListRunningHandler(reader)

	_, _, err := handler.Handle(context.Background(), nil, ListRunningInput{})
	if !errors.Is(err, want) {
		t.Fatalf("handler error = %v, want it to wrap %v", err, want)
	}
}

func callListRunning(t *testing.T, handler *ListRunningHandler, in ListRunningInput) ListRunningOutput {
	t.Helper()
	result, out, err := handler.Handle(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("beacon_list_running: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("beacon_list_running returned an error result: %#v", result)
	}
	return out
}
