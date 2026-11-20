package eventbus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// typeRecorder collects the types a subscriber receives.
type typeRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *typeRecorder) subscriber() Subscriber {
	return func(_ context.Context, env Envelope) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.seen = append(r.seen, env.Type)
		return nil
	}
}

func (r *typeRecorder) values() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.seen))
	copy(out, r.seen)
	return out
}

func TestCountTracksSubscribers(t *testing.T) {
	b := New()
	if got := b.Count(); got != 0 {
		t.Fatalf("empty bus count = %d, want 0", got)
	}

	id, stop := b.SubscribeWithID(func(context.Context, Envelope) error { return nil })
	if id == 0 {
		t.Fatal("SubscribeWithID returned id 0")
	}
	if got := b.Count(); got != 1 {
		t.Fatalf("count after SubscribeWithID = %d, want 1", got)
	}

	b.SubscribeWithID(func(context.Context, Envelope) error { return nil })
	if got := b.Count(); got != 2 {
		t.Fatalf("count after second SubscribeWithID = %d, want 2", got)
	}

	stop()
	if got := b.Count(); got != 1 {
		t.Fatalf("count after unsubscribe = %d, want 1", got)
	}
}

func TestNilSubscriberIsNoOp(t *testing.T) {
	b := New()
	id, stop := b.SubscribeWithID(nil)
	defer stop()
	if id != 0 {
		t.Fatalf("id for nil subscriber = %d, want 0", id)
	}
	if got := b.Count(); got != 0 {
		t.Fatalf("count with nil subscriber = %d, want 0", got)
	}
}

func TestUnsubscribeByID(t *testing.T) {
	b := New()
	rec := &typeRecorder{}
	id, _ := b.SubscribeWithID(rec.subscriber())

	if !b.Unsubscribe(id) {
		t.Fatal("Unsubscribe returned false for a live id")
	}
	if b.Unsubscribe(id) {
		t.Fatal("second Unsubscribe returned true")
	}
	if b.Unsubscribe(9999) {
		t.Fatal("Unsubscribe of unknown id returned true")
	}
	if got := b.Count(); got != 0 {
		t.Fatalf("count after Unsubscribe = %d, want 0", got)
	}

	b.Broadcast(context.Background(), []Envelope{{Type: "one"}})
	if got := rec.values(); len(got) != 0 {
		t.Fatalf("removed subscriber received %v, want none", got)
	}
}

func TestBroadcastDeliversCommittedBatch(t *testing.T) {
	b := New()
	first := &typeRecorder{}
	second := &typeRecorder{}
	b.SubscribeWithID(first.subscriber())
	b.SubscribeWithID(second.subscriber())

	b.Broadcast(context.Background(), []Envelope{{Type: "one"}, {Type: "two"}})

	want := []string{"one", "two"}
	if got := first.values(); !equalStrings(got, want) {
		t.Fatalf("first subscriber got %v, want %v", got, want)
	}
	if got := second.values(); !equalStrings(got, want) {
		t.Fatalf("second subscriber got %v, want %v", got, want)
	}
}

func TestDeliveryOnlyAfterCommit(t *testing.T) {
	b := New()
	rec := &typeRecorder{}
	b.SubscribeWithID(rec.subscriber())

	b.Publish(Envelope{Type: "created", Payload: []byte("one")})
	if got := rec.values(); len(got) != 0 {
		t.Fatalf("subscriber saw event before commit: %v", got)
	}

	b.Commit(context.Background())
	if got := rec.values(); !equalStrings(got, []string{"created"}) {
		t.Fatalf("after commit got %v, want [created]", got)
	}
	if got := b.Count(); got != 1 {
		t.Fatalf("count after commit = %d, want 1", got)
	}
}

func TestBroadcastIsolatesSubscriberErrors(t *testing.T) {
	b := New()
	healthy := &typeRecorder{}
	failingCalls := 0
	b.SubscribeWithID(func(context.Context, Envelope) error {
		failingCalls++
		return errors.New("subscriber down")
	})
	b.SubscribeWithID(func(context.Context, Envelope) error {
		panic("boom")
	})
	b.SubscribeWithID(healthy.subscriber())

	b.Broadcast(context.Background(), []Envelope{{Type: "one"}, {Type: "two"}})

	if failingCalls != 2 {
		t.Fatalf("failing subscriber calls = %d, want 2", failingCalls)
	}
	if got := healthy.values(); !equalStrings(got, []string{"one", "two"}) {
		t.Fatalf("healthy subscriber got %v, want [one two]", got)
	}
}

func TestCountUnderConcurrentSubscribe(t *testing.T) {
	b := New()
	const workers = 50

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, stop := b.SubscribeWithID(func(context.Context, Envelope) error { return nil })
			b.Unsubscribe(id)
			stop()
		}()
	}
	wg.Wait()

	if got := b.Count(); got != 0 {
		t.Fatalf("count after %d subscribe/unsubscribe pairs = %d, want 0", workers, got)
	}
}

func TestBroadcastUsesContext(t *testing.T) {
	type ctxKey struct{}
	b := New()
	want := "carried"
	var got string
	b.SubscribeWithID(func(ctx context.Context, _ Envelope) error {
		got, _ = ctx.Value(ctxKey{}).(string)
		return nil
	})

	ctx := context.WithValue(context.Background(), ctxKey{}, want)
	b.Broadcast(ctx, []Envelope{{Type: "one"}})
	if got != want {
		t.Fatalf("context value = %q, want %q", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func ExampleBus_Broadcast() {
	b := New()
	b.SubscribeWithID(func(_ context.Context, env Envelope) error {
		fmt.Println(env.Type)
		return nil
	})
	b.Broadcast(context.Background(), []Envelope{{Type: "watch.ready"}})
	// Output: watch.ready
}
