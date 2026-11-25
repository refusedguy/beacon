package bench

import (
	"context"
	"sync"
)

// The two algorithms below are unexported in their owning packages, so this
// benchmark harness cannot call them from outside. Each mirror is a line-for-
// line copy of the production code at the cited location. The only changes are
// names that would collide inside package bench and, for fanout, returning the
// writable channels so the harness can pre-fill one view. Do not treat a change
// here as a change to production behavior; update the mirror when the cited
// source changes.

// journalLimit mirrors operationJournalLimit in internal/infra/mcp/server.go:102.
const journalLimit = 128

type journalEntry struct{ result any }

// opJournal mirrors internal/infra/mcp/server.go:112-178. A single-flight
// channel covers concurrent calls with the same id, and order bounds the
// completed entries so a long-lived daemon does not grow the map without limit.
type opJournal struct {
	mu       sync.Mutex
	done     map[string]journalEntry
	order    []string
	inflight map[string]chan struct{}
}

func newOpJournal() *opJournal {
	return &opJournal{
		done:     make(map[string]journalEntry),
		inflight: make(map[string]chan struct{}),
	}
}

func (j *opJournal) claim(id string) (<-chan struct{}, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.done[id]; ok {
		j.touch(id)
		return nil, false
	}
	if wait, ok := j.inflight[id]; ok {
		return wait, false
	}
	wait := make(chan struct{})
	j.inflight[id] = wait
	return wait, true
}

func (j *opJournal) touch(id string) {
	for i, key := range j.order {
		if key == id {
			j.order = append(j.order[:i], j.order[i+1:]...)
			break
		}
	}
	j.order = append(j.order, id)
}

func (j *opJournal) finish(id string, result any, err error) {
	j.mu.Lock()
	wait := j.inflight[id]
	delete(j.inflight, id)
	if err == nil {
		j.done[id] = journalEntry{result: result}
		j.touch(id)
		if len(j.order) > journalLimit {
			oldest := j.order[0]
			j.order = j.order[1:]
			delete(j.done, oldest)
		}
	}
	j.mu.Unlock()
	close(wait)
}

func (j *opJournal) cached(id string) (any, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	entry, ok := j.done[id]
	if ok {
		j.touch(id)
	}
	return entry.result, ok
}

// fanoutBuffer mirrors the 16-slot view in cmd/beacon/serve.go:114.
const fanoutBuffer = 16

// fanout mirrors cmd/beacon/serve.go:110-133. A slow view loses the value
// instead of blocking the other views or the source. The harness uses the
// writable return type to push events and to keep the third view full.
func fanout[T any](ctx context.Context, src <-chan T, n int) []chan T {
	outs := make([]chan T, n)
	for i := range outs {
		outs[i] = make(chan T, fanoutBuffer)
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case v := <-src:
				for _, out := range outs {
					select {
					case out <- v:
					default:
					}
				}
			}
		}
	}()
	return outs
}

// fanoutBench holds one long-lived fanout so a pass measures delivery, not
// goroutine startup. views[2] is pre-filled to model a monitor that has stopped
// reading; its drops are counted in every pass.
type fanoutBench struct {
	src    chan int
	views  []chan int
	cancel context.CancelFunc
}

func newFanoutBench(n int) *fanoutBench {
	ctx, cancel := context.WithCancel(context.Background())
	src := make(chan int, 64)
	views := fanout(ctx, src, n)
	for i := 0; i < fanoutBuffer; i++ {
		views[2] <- -1
	}
	return &fanoutBench{src: src, views: views, cancel: cancel}
}

func (f *fanoutBench) close() {
	f.cancel()
}
