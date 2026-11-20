package mcp

import (
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// verdictListerFake is the store seam the poller reads. set swaps the whole
// snapshot so a test can move one Watch to a new revision in one call.
type verdictListerFake struct {
	mu      sync.Mutex
	watches []domain.Watch
	err     error
}

func (f *verdictListerFake) LiveWatches() ([]domain.Watch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return append([]domain.Watch(nil), f.watches...), nil
}

func (f *verdictListerFake) set(watches ...domain.Watch) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watches = append([]domain.Watch(nil), watches...)
}

func sourceWatch(id string, revision uint64, verdict domain.Verdict) domain.Watch {
	return domain.Watch{
		ID:       id,
		Status:   domain.StatusWatching,
		Verdict:  verdict,
		Revision: revision,
		Checks:   map[string]domain.Check{},
	}
}

func recvVerdict(t *testing.T, ch <-chan WaitVerdictUpdate) WaitVerdictUpdate {
	t.Helper()
	select {
	case u, ok := <-ch:
		if !ok {
			t.Fatal("updates channel closed before the verdict arrived")
		}
		return u
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a verdict update")
		return WaitVerdictUpdate{}
	}
}

func assertNoVerdict(t *testing.T, ch <-chan WaitVerdictUpdate) {
	t.Helper()
	select {
	case u := <-ch:
		t.Fatalf("unexpected verdict update for another watch: %+v", u)
	case <-time.After(60 * time.Millisecond):
	}
}

func assertSameVerdict(t *testing.T, a, b WaitVerdictUpdate) {
	t.Helper()
	if a.WatchID != b.WatchID || a.Status != b.Status || a.Verdict != b.Verdict ||
		a.Revision != b.Revision || a.Kind != b.Kind || !checksEqual(a.Checks, b.Checks) {
		t.Fatalf("updates differ: A=%+v B=%+v", a, b)
	}
}

func TestDBVerdictSourceFansOutToTwoSubscribers(t *testing.T) {
	lister := &verdictListerFake{}
	src := NewDBVerdictSource(lister, 0)
	defer src.Stop()

	lister.set(sourceWatch("w1", 1, domain.VerdictWatching))
	subA := src.Subscribe("w1")
	subB := src.Subscribe("w1")

	// The first snapshot reaches both subscribers once.
	src.pollOnce()
	a := recvVerdict(t, subA.Updates())
	b := recvVerdict(t, subB.Updates())
	assertSameVerdict(t, a, b)
	if a.Kind != WaitEventProgress || a.Revision != 1 {
		t.Fatalf("initial update = %+v, want progress at revision 1", a)
	}

	// The same terminal change reaches both, with the same content.
	lister.set(sourceWatch("w1", 2, domain.VerdictReady))
	src.pollOnce()
	a = recvVerdict(t, subA.Updates())
	b = recvVerdict(t, subB.Updates())
	assertSameVerdict(t, a, b)
	if a.WatchID != "w1" || a.Revision != 2 || a.Verdict != domain.VerdictReady {
		t.Fatalf("terminal update = %+v, want w1/ready/rev2", a)
	}
	if a.Kind != WaitEventCompleted {
		t.Fatalf("terminal update kind = %q, want %q", a.Kind, WaitEventCompleted)
	}
}

func TestDBVerdictSourceIsolatesWatches(t *testing.T) {
	lister := &verdictListerFake{}
	src := NewDBVerdictSource(lister, 0)
	defer src.Stop()

	lister.set(sourceWatch("w1", 1, domain.VerdictWatching), sourceWatch("w2", 1, domain.VerdictWatching))
	sub1 := src.Subscribe("w1")
	sub2 := src.Subscribe("w2")
	src.pollOnce()
	recvVerdict(t, sub1.Updates())
	recvVerdict(t, sub2.Updates())

	// Only w1 changes. Its subscriber hears it; w2's subscriber hears nothing.
	lister.set(sourceWatch("w1", 2, domain.VerdictReady), sourceWatch("w2", 1, domain.VerdictWatching))
	src.pollOnce()
	got := recvVerdict(t, sub1.Updates())
	if got.WatchID != "w1" || got.Verdict != domain.VerdictReady {
		t.Fatalf("w1 update = %+v, want w1/ready", got)
	}
	assertNoVerdict(t, sub2.Updates())
}

func TestDBVerdictSourceCloseIsIdempotentAndUnblocksPoller(t *testing.T) {
	lister := &verdictListerFake{}
	src := NewDBVerdictSource(lister, 0)
	defer src.Stop()

	lister.set(sourceWatch("w1", 1, domain.VerdictWatching))
	sub := src.Subscribe("w1").(*dbVerdictSubscription)
	other := src.Subscribe("w1").(*dbVerdictSubscription)
	src.pollOnce()
	recvVerdict(t, sub.Updates())
	recvVerdict(t, other.Updates())

	sub.Close()
	sub.Close()
	if _, ok := src.subs["w1"][sub]; ok {
		t.Fatal("closed subscriber is still registered")
	}

	// Fill the closed subscriber's channel. A send to it must not stall the
	// shared poller, while the other subscriber still gets the update.
	for i := 0; i < verdictSubBuffer; i++ {
		sub.updates <- WaitVerdictUpdate{}
	}
	lister.set(sourceWatch("w1", 2, domain.VerdictRetry))
	polled := make(chan struct{})
	go func() {
		src.pollOnce()
		close(polled)
	}()
	select {
	case <-polled:
	case <-time.After(2 * time.Second):
		t.Fatal("pollOnce blocked on a closed subscriber")
	}
	got := recvVerdict(t, other.Updates())
	if got.Revision != 2 || got.Verdict != domain.VerdictRetry || got.Kind != WaitEventCompleted {
		t.Fatalf("other subscriber update = %+v, want retry/rev2/completed", got)
	}
}

func TestDBVerdictSourceTerminalVerdictsAreCompleted(t *testing.T) {
	terminal := []domain.Verdict{
		domain.VerdictReady,
		domain.VerdictMerged,
		domain.VerdictClosed,
		domain.VerdictRebase,
		domain.VerdictRetry,
		domain.VerdictKeepWorking,
		domain.VerdictTimeout,
	}
	for _, verdict := range terminal {
		t.Run(string(verdict), func(t *testing.T) {
			lister := &verdictListerFake{}
			src := NewDBVerdictSource(lister, 0)
			defer src.Stop()

			lister.set(sourceWatch("w1", 1, domain.VerdictWatching))
			sub := src.Subscribe("w1")
			src.pollOnce()
			recvVerdict(t, sub.Updates())

			lister.set(sourceWatch("w1", 2, verdict))
			src.pollOnce()
			got := recvVerdict(t, sub.Updates())
			if got.Kind != WaitEventCompleted {
				t.Fatalf("kind for %s = %q, want %q", verdict, got.Kind, WaitEventCompleted)
			}
		})
	}
}

func TestDBVerdictSourceUnchangedChecksAreNotRepublished(t *testing.T) {
	lister := &verdictListerFake{}
	src := NewDBVerdictSource(lister, 0)
	defer src.Stop()

	lister.set(sourceWatch("w1", 1, domain.VerdictWatching))
	sub := src.Subscribe("w1")
	src.pollOnce()
	recvVerdict(t, sub.Updates())

	// An identical snapshot, including equivalent checks, is not a change.
	src.pollOnce()
	assertNoVerdict(t, sub.Updates())

	lister.set(domain.Watch{
		ID:       "w1",
		Status:   domain.StatusWatching,
		Verdict:  domain.VerdictWatching,
		Revision: 2,
		Checks:   map[string]domain.Check{"build": {State: "complete", Conclusion: "success"}},
	})
	src.pollOnce()
	got := recvVerdict(t, sub.Updates())
	if got.Revision != 2 || got.Kind != WaitEventProgress {
		t.Fatalf("checks/revision update = %+v, want progress at rev2", got)
	}
	if got.Checks["build"].Conclusion != "success" {
		t.Fatalf("checks not delivered: %+v", got.Checks)
	}
}

func TestDBVerdictSourceStopEndsGoroutine(t *testing.T) {
	lister := &verdictListerFake{}
	src := NewDBVerdictSource(lister, time.Millisecond)

	sub := src.Subscribe("w1")
	sub.Close()

	// Stop waits for the loop. Both calls must return promptly and the second
	// one must stay idempotent.
	for i := 0; i < 2; i++ {
		finished := make(chan struct{})
		go func() {
			src.Stop()
			close(finished)
		}()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Fatalf("Stop call %d did not return", i+1)
		}
	}
}
