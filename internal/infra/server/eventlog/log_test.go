package eventlog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"pgregory.net/rapid"
)

// errBoom is the sentinel the fake port returns to exercise error paths.
var errBoom = errors.New("port failure")

// memPort is an in-memory Port used to drive Log through its public API. It is
// mutex-protected because Replay and MaxSeq can run while appends are in flight.
type memPort struct {
	mu     sync.Mutex
	events []Event

	appendErr error
	loadErr   error
	maxErr    error
}

var _ Port = (*memPort)(nil)

func newMemPort() *memPort { return &memPort{} }

func (p *memPort) Append(ctx context.Context, events []Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.appendErr != nil {
		return p.appendErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, ev := range events {
		p.events = append(p.events, cloneEvent(ev))
	}
	return nil
}

func (p *memPort) Load(ctx context.Context, q Query) ([]Event, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loadErr != nil {
		return nil, p.loadErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(p.events))
	for _, ev := range p.events {
		if q.Scope != "" && ev.Scope != q.Scope {
			continue
		}
		if q.Type != "" && ev.Type != q.Type {
			continue
		}
		if ev.Seq <= q.After {
			continue
		}
		out = append(out, cloneEvent(ev))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (p *memPort) MaxSeq(ctx context.Context) (uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.maxErr != nil {
		return 0, p.maxErr
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var max uint64
	for _, ev := range p.events {
		if ev.Seq > max {
			max = ev.Seq
		}
	}
	return max, nil
}

func cloneEvent(ev Event) Event {
	ev.Payload = append([]byte(nil), ev.Payload...)
	return ev
}

func mustLog(t *testing.T, p Port) *Log {
	t.Helper()
	log, err := New(context.Background(), p)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return log
}

func checkSeqs(t *testing.T, events []Event, want ...uint64) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(want), events)
	}
	for i, seq := range want {
		if events[i].Seq != seq {
			t.Fatalf("event %d seq = %d, want %d", i, events[i].Seq, seq)
		}
	}
}

func TestNewMissingPort(t *testing.T) {
	if _, err := New(context.Background(), nil); !errors.Is(err, ErrMissingPort) {
		t.Fatalf("New(nil) error = %v, want ErrMissingPort", err)
	}
}

func TestAppend(t *testing.T) {
	ctx := context.Background()
	log := mustLog(t, newMemPort())

	first, err := log.Append(ctx, "watch", "created", []byte(`{"id":1}`))
	if err != nil {
		t.Fatalf("first Append: %v", err)
	}
	if first.Seq != 1 || first.Scope != "watch" || first.Type != "created" {
		t.Fatalf("first event = %+v, want seq 1 in watch/created", first)
	}
	if first.SchemaVersion != SchemaVersion {
		t.Fatalf("first schema version = %d, want %d", first.SchemaVersion, SchemaVersion)
	}
	if string(first.Payload) != `{"id":1}` {
		t.Fatalf("first payload = %q", first.Payload)
	}

	second, err := log.Append(ctx, "watch", "verdict", []byte(`{"verdict":"ready"}`))
	if err != nil {
		t.Fatalf("second Append: %v", err)
	}
	if second.Seq != 2 {
		t.Fatalf("second seq = %d, want 2", second.Seq)
	}
	if got := log.MaxSeq(); got != 2 {
		t.Fatalf("MaxSeq = %d, want 2", got)
	}

	// The committed event keeps its own copy of the payload.
	payload := []byte("original")
	third, err := log.Append(ctx, "watch", "note", payload)
	if err != nil {
		t.Fatalf("third Append: %v", err)
	}
	payload[0] = 'X'
	if string(third.Payload) != "original" {
		t.Fatalf("returned payload changed to %q", third.Payload)
	}
	stored, err := log.ReadAfter(ctx, "watch", 2)
	if err != nil {
		t.Fatalf("ReadAfter: %v", err)
	}
	if len(stored) != 1 || string(stored[0].Payload) != "original" {
		t.Fatalf("stored payload = %+v, want original", stored)
	}
}

func TestAppendInvalidInput(t *testing.T) {
	ctx := context.Background()
	log := mustLog(t, newMemPort())
	if _, err := log.Append(ctx, "", "created", nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty scope error = %v, want ErrInvalidInput", err)
	}
	if _, err := log.Append(ctx, "watch", "", nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty type error = %v, want ErrInvalidInput", err)
	}
	if got := log.MaxSeq(); got != 0 {
		t.Fatalf("rejected appends moved MaxSeq to %d", got)
	}
}

func TestReadAfterCursor(t *testing.T) {
	ctx := context.Background()
	log := mustLog(t, newMemPort())
	for i := 0; i < 4; i++ {
		if _, err := log.Append(ctx, "watch", "evt", []byte(fmt.Sprint(i))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if _, err := log.Append(ctx, "other", "evt", []byte("other")); err != nil {
		t.Fatalf("Append other: %v", err)
	}

	all, err := log.ReadAfter(ctx, "watch", 0)
	if err != nil {
		t.Fatalf("ReadAfter 0: %v", err)
	}
	checkSeqs(t, all, 1, 2, 3, 4)

	tail, err := log.ReadAfter(ctx, "watch", 2)
	if err != nil {
		t.Fatalf("ReadAfter 2: %v", err)
	}
	checkSeqs(t, tail, 3, 4)

	past, err := log.ReadAfter(ctx, "watch", 99)
	if err != nil {
		t.Fatalf("ReadAfter 99: %v", err)
	}
	if len(past) != 0 {
		t.Fatalf("cursor past last seq returned %d events", len(past))
	}

	other, err := log.ReadAfter(ctx, "other", 0)
	if err != nil {
		t.Fatalf("ReadAfter other: %v", err)
	}
	checkSeqs(t, other, 5)
}

func TestReplayFiltersAndLimit(t *testing.T) {
	ctx := context.Background()
	log := mustLog(t, newMemPort())
	appends := []struct {
		scope string
		typ   string
	}{
		{"watch", "created"},
		{"watch", "verdict"},
		{"watch", "verdict"},
		{"watch", "closed"},
		{"other", "verdict"},
	}
	for _, a := range appends {
		if _, err := log.Append(ctx, a.scope, a.typ, nil); err != nil {
			t.Fatalf("Append %+v: %v", a, err)
		}
	}

	got, err := log.Replay(ctx, Query{Scope: "watch", Type: "verdict", After: 1, Limit: 1})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	checkSeqs(t, got, 2)
	if got[0].Type != "verdict" || got[0].Scope != "watch" {
		t.Fatalf("filtered event = %+v", got[0])
	}

	allTypes, err := log.Replay(ctx, Query{Scope: "watch"})
	if err != nil {
		t.Fatalf("Replay all types: %v", err)
	}
	checkSeqs(t, allTypes, 1, 2, 3, 4)

	if _, err := log.Replay(ctx, Query{Limit: -1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative limit error = %v, want ErrInvalidInput", err)
	}
}

func TestEmptyLog(t *testing.T) {
	ctx := context.Background()
	log := mustLog(t, newMemPort())
	if got := log.MaxSeq(); got != 0 {
		t.Fatalf("MaxSeq of empty log = %d, want 0", got)
	}
	got, err := log.ReadAfter(ctx, "watch", 0)
	if err != nil {
		t.Fatalf("ReadAfter: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ReadAfter of empty log = %+v, want empty", got)
	}
	all, err := log.Replay(ctx, Query{})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("Replay of empty log = %+v, want empty", all)
	}
}

func TestPortErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("new_max_seq", func(t *testing.T) {
		p := newMemPort()
		p.maxErr = errBoom
		if _, err := New(ctx, p); !errors.Is(err, errBoom) {
			t.Fatalf("New error = %v, want errBoom", err)
		}
	})

	t.Run("append_keeps_seq_on_failure", func(t *testing.T) {
		p := newMemPort()
		log := mustLog(t, p)
		p.appendErr = errBoom
		if _, err := log.Append(ctx, "watch", "evt", nil); !errors.Is(err, errBoom) {
			t.Fatalf("Append error = %v, want errBoom", err)
		}
		if got := log.MaxSeq(); got != 0 {
			t.Fatalf("failed append moved MaxSeq to %d", got)
		}
		p.appendErr = nil
		ev, err := log.Append(ctx, "watch", "evt", nil)
		if err != nil {
			t.Fatalf("retry Append: %v", err)
		}
		if ev.Seq != 1 {
			t.Fatalf("retry seq = %d, want 1", ev.Seq)
		}
	})

	t.Run("replay", func(t *testing.T) {
		p := newMemPort()
		log := mustLog(t, p)
		p.loadErr = errBoom
		if _, err := log.Replay(ctx, Query{Scope: "watch"}); !errors.Is(err, errBoom) {
			t.Fatalf("Replay error = %v, want errBoom", err)
		}
	})
}

func TestMaxSeqAfterRestart(t *testing.T) {
	ctx := context.Background()
	p := newMemPort()
	first := mustLog(t, p)
	for i := 0; i < 3; i++ {
		if _, err := first.Append(ctx, "watch", "evt", nil); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	second := mustLog(t, p)
	ev, err := second.Append(ctx, "watch", "evt", nil)
	if err != nil {
		t.Fatalf("Append after restart: %v", err)
	}
	if ev.Seq != 4 {
		t.Fatalf("seq after restart = %d, want 4", ev.Seq)
	}
}

func TestMonotonicUnderParallelWriters(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		writers := rapid.IntRange(2, 12).Draw(rt, "writers")
		perWriter := rapid.IntRange(1, 24).Draw(rt, "perWriter")
		ctx := context.Background()
		p := newMemPort()
		log, err := New(ctx, p)
		if err != nil {
			rt.Fatalf("New: %v", err)
		}

		want := writers * perWriter
		seqs := make(chan uint64, want)
		errs := make(chan error, want)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				<-start
				for i := 0; i < perWriter; i++ {
					ev, err := log.Append(ctx, fmt.Sprintf("scope-%d", id%3), "evt",
						[]byte(fmt.Sprintf("%d:%d", id, i)))
					if err != nil {
						errs <- err
						continue
					}
					seqs <- ev.Seq
				}
			}(w)
		}
		close(start)
		wg.Wait()
		close(seqs)
		close(errs)
		for err := range errs {
			rt.Fatalf("concurrent Append: %v", err)
		}

		seen := make(map[uint64]bool, want)
		for seq := range seqs {
			if seq == 0 {
				rt.Fatal("seq zero was assigned")
			}
			if seen[seq] {
				rt.Fatalf("seq %d was assigned twice", seq)
			}
			seen[seq] = true
		}
		if len(seen) != want {
			rt.Fatalf("got %d distinct seqs, want %d", len(seen), want)
		}
		if got := log.MaxSeq(); got != uint64(want) {
			rt.Fatalf("MaxSeq = %d, want %d", got, want)
		}

		all, err := log.Replay(ctx, Query{})
		if err != nil {
			rt.Fatalf("Replay: %v", err)
		}
		if len(all) != want {
			rt.Fatalf("stored %d events, want %d", len(all), want)
		}
		for i := 1; i < len(all); i++ {
			if all[i].Seq <= all[i-1].Seq {
				rt.Fatalf("stored seq not strictly increasing at %d: %d then %d",
					i, all[i-1].Seq, all[i].Seq)
			}
		}
	})
}
