package eventbus

import (
	"context"
	"errors"
	"testing"
)

func TestSubscribeAndUnsubscribe(t *testing.T) {
	b := New()
	var calls int
	stop := b.Subscribe(func(_ context.Context, _ Envelope) error {
		calls++
		return nil
	})

	b.Publish(Envelope{Type: "one"})
	b.Commit(context.Background())
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}

	stop()
	stop()
	b.Publish(Envelope{Type: "two"})
	b.Commit(context.Background())
	if calls != 1 {
		t.Fatalf("calls after unsubscribe = %d, want 1", calls)
	}
}

func TestPublishDeliversOnlyAfterCommit(t *testing.T) {
	b := New()
	var got []string
	b.Subscribe(func(_ context.Context, env Envelope) error {
		got = append(got, env.Type)
		return nil
	})

	b.Publish(Envelope{Type: "created", Payload: []byte("one")})
	if len(got) != 0 {
		t.Fatalf("subscriber saw event before commit: %v", got)
	}
	b.Commit(context.Background())
	if len(got) != 1 || got[0] != "created" {
		t.Fatalf("after commit got = %v, want [created]", got)
	}
}

func TestMultipleSubscribers(t *testing.T) {
	b := New()
	var first, second []string
	b.Subscribe(func(_ context.Context, env Envelope) error {
		first = append(first, env.Type)
		return nil
	})
	b.Subscribe(func(_ context.Context, env Envelope) error {
		second = append(second, env.Type)
		return nil
	})

	b.Publish(Envelope{Type: "one"})
	b.Publish(Envelope{Type: "two"})
	b.Commit(context.Background())

	if len(first) != 2 || first[0] != "one" || first[1] != "two" {
		t.Fatalf("first subscriber got %v, want [one two]", first)
	}
	if len(second) != 2 || second[0] != "one" || second[1] != "two" {
		t.Fatalf("second subscriber got %v, want [one two]", second)
	}
}

func TestSubscriberErrorDoesNotBlock(t *testing.T) {
	b := New()
	failing := 0
	b.Subscribe(func(_ context.Context, _ Envelope) error {
		failing++
		return errors.New("subscriber failed")
	})
	panicking := 0
	b.Subscribe(func(_ context.Context, _ Envelope) error {
		panicking++
		panic("subscriber panic")
	})
	var healthy []string
	b.Subscribe(func(_ context.Context, env Envelope) error {
		healthy = append(healthy, env.Type)
		return nil
	})

	b.Publish(Envelope{Type: "one"})
	b.Publish(Envelope{Type: "two"})
	b.Commit(context.Background())

	if len(healthy) != 2 || healthy[0] != "one" || healthy[1] != "two" {
		t.Fatalf("healthy subscriber got %v, want [one two]", healthy)
	}
	if failing != 2 || panicking != 2 {
		t.Fatalf("failing = %d, panicking = %d, want 2 each", failing, panicking)
	}
}

func TestEmptyBus(t *testing.T) {
	b := New()

	b.Publish(Envelope{Type: "orphan"})
	b.Commit(context.Background())

	var got []string
	stop := b.Subscribe(func(_ context.Context, env Envelope) error {
		got = append(got, env.Type)
		return nil
	})
	defer stop()
	b.Commit(context.Background())
	if len(got) != 0 {
		t.Fatalf("empty bus delivered %v, want none", got)
	}
}
