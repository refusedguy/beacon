package bench

import (
	"container/list"
	"context"
	"fmt"
	"sync"

	"github.com/refusedguy/beacon/pkg/core"
)

// The algorithms below are unexported in their owning packages, so this
// benchmark harness cannot call them from outside. Each mirror copies the
// production data shape and the work done per operation. The only changes are
// names that would collide inside package bench, a step-at-a-time entry point
// so the harness can drain views between events and count deliveries, and a
// writable channel return on the legacy generic fanout. Do not treat a change
// here as a change to production behavior; update the mirror when the cited
// source changes, apart from returning writable subscriber channels so the
// harness can pre-fill the slow view.

// operationJournalLimit mirrors operationJournalLimit in
// internal/infra/mcp/server.go:104.
const operationJournalLimit = 128

type journalEntry struct{ result any }

// opJournal mirrors internal/infra/mcp/server.go:111-189. A single-flight
// channel covers concurrent calls with the same id. Recency is an LRU list and
// index maps an id to its list element, so touch and eviction stay O(1).
type opJournal struct {
	mu       sync.Mutex
	done     map[string]journalEntry
	order    *list.List
	index    map[string]*list.Element
	inflight map[string]chan struct{}
}

func newOpJournal() *opJournal {
	return &opJournal{
		done:     make(map[string]journalEntry),
		order:    list.New(),
		index:    make(map[string]*list.Element),
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

// touch moves id to the most-recent end of the LRU in constant time. Callers
// hold mu.
func (j *opJournal) touch(id string) {
	if elem, ok := j.index[id]; ok {
		j.order.MoveToBack(elem)
		return
	}
	j.index[id] = j.order.PushBack(id)
}

func (j *opJournal) finish(id string, result any, err error) {
	j.mu.Lock()
	wait := j.inflight[id]
	delete(j.inflight, id)
	if err == nil {
		j.done[id] = journalEntry{result: result}
		j.touch(id)
		for len(j.done) > operationJournalLimit {
			oldest := j.order.Front()
			if oldest == nil {
				break
			}
			key := oldest.Value.(string)
			j.order.Remove(oldest)
			delete(j.index, key)
			delete(j.done, key)
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

// subscriberBuffer mirrors subscriberBuffer in cmd/beacon/http.go:22. A full
// subscriber drops the value instead of blocking the other monitors.
const subscriberBuffer = 16

// eventFrames mirrors cmd/beacon/http.go:117-179. One json.Marshal per event
// produces the SSE frame bytes, and every subscriber gets the same bytes.
type eventFrames struct {
	mu      sync.Mutex
	subs    map[chan []byte]struct{}
	marshal func(any) ([]byte, error)
}

func newEventFrames(marshal func(any) ([]byte, error)) *eventFrames {
	return &eventFrames{
		subs:    make(map[chan []byte]struct{}),
		marshal: marshal,
	}
}

func (f *eventFrames) subscribe() (chan []byte, func()) {
	ch := make(chan []byte, subscriberBuffer)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	return ch, func() {
		f.mu.Lock()
		delete(f.subs, ch)
		f.mu.Unlock()
	}
}

// deliver runs the body of eventFrames.run for one event. The harness drives
// one event at a time so a full slow view always drops and a drained fast view
// always receives, which keeps the counts identical between runs. It returns
// the frame bytes, the number of successful deliveries and the number of drops.
func (f *eventFrames) deliver(ev core.HarnessEvent) ([]byte, int64, int64) {
	b, err := f.marshal(core.NewWireEvent(ev))
	if err != nil {
		return nil, 0, 0
	}
	frame := fmt.Appendf(nil, "event: harness\ndata: %s\n\n", b)
	var delivered, dropped int64
	f.mu.Lock()
	for ch := range f.subs {
		select {
		case ch <- frame:
			delivered++
		default:
			dropped++
		}
	}
	f.mu.Unlock()
	return frame, delivered, dropped
}

// runsCache mirrors the invalidation side of cmd/beacon/http.go:24-42. The
// harness does not issue status reads, so it only needs the generation break
// that changeSubscribers.run performs on every PR change.
type runsCache struct {
	mu   sync.Mutex
	body []byte
}

func (c *runsCache) invalidate() {
	c.mu.Lock()
	c.body = nil
	c.mu.Unlock()
}

// changeSubscribers mirrors cmd/beacon/http.go:65-115. Each SSE connection owns
// one channel, and a full channel is skipped so one slow monitor cannot stall
// the broadcast.
type changeSubscribers struct {
	mu       sync.Mutex
	subs     map[chan int]struct{}
	snapshot *runsCache
}

func newChangeSubscribers() *changeSubscribers {
	return &changeSubscribers{
		subs:     make(map[chan int]struct{}),
		snapshot: &runsCache{},
	}
}

func (s *changeSubscribers) subscribe() (chan int, func()) {
	ch := make(chan int, subscriberBuffer)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// deliver runs the body of changeSubscribers.run for one PR change. It returns
// the number of successful deliveries and drops.
func (s *changeSubscribers) deliver(pr int) (int64, int64) {
	s.snapshot.invalidate()
	var delivered, dropped int64
	s.mu.Lock()
	for ch := range s.subs {
		select {
		case ch <- pr:
			delivered++
		default:
			dropped++
		}
	}
	s.mu.Unlock()
	return delivered, dropped
}

// fanoutBuffer mirrors the view capacity of the generic fanout in
// cmd/beacon/serve.go:95-124. That helper still supplies the source views used
// by newFanoutBench; the measured sse-fanout hotspot uses the broadcasters
// above, not this generic channel copy.
const fanoutBuffer = 16

// fanout is the generic channel copy from cmd/beacon/serve.go:95-124. It copies
// every value into n buffered views and drops the value for a full view.
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
			case v, ok := <-src:
				if !ok {
					return
				}
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

// fanoutBench holds one long-lived generic fanout for the harness helper. The
// sse-fanout hotspot measures eventFrames and changeSubscribers instead.
type fanoutBench struct {
	src    chan int
	views  []chan int
	cancel context.CancelFunc
}

func (f *fanoutBench) close() {
	f.cancel()
}
