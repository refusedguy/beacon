package cibridge

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// baseGreenBatch records the Watches the refresh layer sampled and returns a
// green base observation for each of them.
type baseGreenBatch struct {
	seen []string
}

func (b *baseGreenBatch) FetchBatch(_ context.Context, watches []domain.Watch) (map[string]domain.PRSnapshot, error) {
	out := make(map[string]domain.PRSnapshot, len(watches))
	for _, w := range watches {
		b.seen = append(b.seen, w.ID)
		out[w.ID] = domain.PRSnapshot{
			PR:   domain.PRState{State: "OPEN", HeadRefOid: w.HeadSHA},
			Base: domain.BaseBranchState{BaseRef: "main", ChecksKnown: true},
		}
	}
	return out, nil
}

func (b *baseGreenBatch) PointStats() (int, int) { return len(b.seen), 1000 }

// TestFetchSnapshotsIncludesBlockedWatch pins the base-green fix. The resolver
// runs after the Watch loop over the snapshot map, so a blocked Watch missing
// from that map can never release its own incident.
func TestFetchSnapshotsIncludesBlockedWatch(t *testing.T) {
	batch := &baseGreenBatch{}
	b := New(nil, nil, fixedClock{at: time.Unix(1, 0)}, time.Second).WithBatchCI(batch)

	watches := []domain.Watch{
		{ID: "watching", Status: domain.StatusWatching, HeadSHA: "sha-watching"},
		{ID: "blocked", Status: domain.StatusBlocked, HeadSHA: "sha-blocked"},
		{ID: "needs-input", Status: domain.StatusNeedsInput, HeadSHA: "sha-input"},
		{ID: "closed", Status: domain.StatusClosed, HeadSHA: "sha-closed"},
	}
	snaps, err := b.fetchSnapshots(context.Background(), watches, "cycle-test")
	if err != nil {
		t.Fatalf("fetchSnapshots: %v", err)
	}
	for _, id := range []string{"watching", "blocked"} {
		if _, ok := snaps[id]; !ok {
			t.Errorf("snapshot for %q missing; got %v", id, snaps)
		}
	}
	for _, id := range []string{"needs-input", "closed"} {
		if _, ok := snaps[id]; ok {
			t.Errorf("snapshot for %q present; it must stay parked", id)
		}
	}
	found := false
	for _, id := range batch.seen {
		if id == "blocked" {
			found = true
		}
	}
	if !found {
		t.Errorf("blocked Watch was not polled; batch saw %v", batch.seen)
	}
}

// concurrencyCI blocks every PRState call until the test releases that PR, so
// the test controls completion order and can observe the in-flight cap.
type concurrencyCI struct {
	mu       sync.Mutex
	inFlight int
	maxSeen  int
	started  chan int
	proceed  map[int]chan struct{}
	failPR   int
}

func newConcurrencyCI(n int) *concurrencyCI {
	c := &concurrencyCI{
		started: make(chan int, n),
		proceed: make(map[int]chan struct{}, n),
	}
	for pr := 1; pr <= n; pr++ {
		c.proceed[pr] = make(chan struct{})
	}
	return c
}

func (c *concurrencyCI) PRState(_ context.Context, _ string, pr int) (domain.PRState, error) {
	c.mu.Lock()
	c.inFlight++
	if c.inFlight > c.maxSeen {
		c.maxSeen = c.inFlight
	}
	c.mu.Unlock()
	c.started <- pr
	<-c.proceed[pr]
	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()
	if pr == c.failPR {
		return domain.PRState{}, fmt.Errorf("PR %d unavailable", pr)
	}
	return domain.PRState{State: "OPEN", HeadRefOid: fmt.Sprintf("sha-%d", pr)}, nil
}

func (c *concurrencyCI) highWater() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxSeen
}

// TestFetchSnapshotsFallbackBoundedAndOrdered pins the fallback parallelism
// contract: at most four PRState calls at once, one snapshot per Watch at its
// own index even when responses arrive out of order, and one failing PR does
// not stop the other Watches.
func TestFetchSnapshotsFallbackBoundedAndOrdered(t *testing.T) {
	const n = 12
	ci := newConcurrencyCI(n)
	ci.failPR = 5
	b := New(nil, ci, fixedClock{at: time.Unix(1, 0)}, time.Second)

	watches := make([]domain.Watch, n)
	for i := range watches {
		pr := i + 1
		watches[i] = domain.Watch{
			ID:      fmt.Sprintf("w-%d", pr),
			Repo:    "o/r",
			PR:      pr,
			HeadSHA: fmt.Sprintf("sha-%d", pr),
			Status:  domain.StatusWatching,
		}
	}

	var logs bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(orig)

	done := make(chan struct{})
	var snaps map[string]ciObservation
	var fetchErr error
	go func() {
		snaps, fetchErr = b.fetchSnapshots(context.Background(), watches, "cycle-concurrency")
		close(done)
	}()

	first := make([]int, 0, 4)
	for len(first) < 4 {
		select {
		case pr := <-ci.started:
			first = append(first, pr)
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d PRState calls started", len(first))
		}
	}
	select {
	case pr := <-ci.started:
		t.Fatalf("more than four concurrent PRState calls: PR %d started", pr)
	default:
	}

	// Release the blocked calls from last to first so completion order differs
	// from the order of watches.
	for i := len(first) - 1; i >= 0; i-- {
		close(ci.proceed[first[i]])
	}
	released := len(first)
	for released < n {
		select {
		case pr := <-ci.started:
			close(ci.proceed[pr])
			released++
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d of %d PRState calls started", released, n)
		}
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("fetchSnapshots did not finish")
	}
	if fetchErr != nil {
		t.Fatalf("fetchSnapshots: %v", fetchErr)
	}
	if got := ci.highWater(); got != 4 {
		t.Errorf("max concurrent PRState calls = %d, want 4", got)
	}
	if len(snaps) != n-1 {
		t.Errorf("got %d snapshots, want %d", len(snaps), n-1)
	}
	for _, w := range watches {
		if w.PR == ci.failPR {
			if _, ok := snaps[w.ID]; ok {
				t.Errorf("snapshot for failed watch %s present", w.ID)
			}
			continue
		}
		obs, ok := snaps[w.ID]
		if !ok {
			t.Errorf("snapshot for %s missing", w.ID)
			continue
		}
		if obs.pr.HeadRefOid != w.HeadSHA {
			t.Errorf("watch %s got head %q, want %q", w.ID, obs.pr.HeadRefOid, w.HeadSHA)
		}
		if obs.base.BaseRef != "main" {
			t.Errorf("watch %s got base %q, want main", w.ID, obs.base.BaseRef)
		}
	}
	if !strings.Contains(logs.String(), "w-5") {
		t.Errorf("failing watch id missing from log: %q", logs.String())
	}
}
