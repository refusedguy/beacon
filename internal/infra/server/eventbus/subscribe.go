package eventbus

import (
	"context"
	"sync"
)

// SubscriberRegistry is the subscriber-management port. It extends Port with
// explicit removal by id, a live subscriber count and broadcast of a batch the
// caller has already committed. Callers depend on this interface instead of the
// in-process Bus.
type SubscriberRegistry interface {
	Port
	SubscribeWithID(sub Subscriber) (uint64, func())
	Unsubscribe(id uint64) bool
	Count() int
	Broadcast(ctx context.Context, envs []Envelope)
}

var _ SubscriberRegistry = (*Bus)(nil)

// SubscribeWithID registers sub and returns its id together with a function
// that removes it. The id lets a caller remove the subscriber later through
// Unsubscribe. The returned function is idempotent. A nil subscriber gets id 0
// and a no-op remover.
func (b *Bus) SubscribeWithID(sub Subscriber) (uint64, func()) {
	if sub == nil {
		return 0, func() {}
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.subs[id] = sub
	b.mu.Unlock()

	var once sync.Once
	return id, func() {
		once.Do(func() { b.Unsubscribe(id) })
	}
}

// Unsubscribe removes the subscriber with id and reports whether one was
// removed. Removing an unknown id is harmless.
func (b *Bus) Unsubscribe(id uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[id]; !ok {
		return false
	}
	delete(b.subs, id)
	return true
}

// Count returns the number of registered subscribers.
func (b *Bus) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Broadcast delivers a batch of already-committed envelopes to every current
// subscriber in order. Commit uses the same rule: a subscriber sees an envelope
// only after its unit of work commits. This method serves callers that hold a
// committed batch, such as a replay. Delivery reuses deliver, so a failing or
// panicking subscriber cannot stop the others.
func (b *Bus) Broadcast(ctx context.Context, envs []Envelope) {
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.Lock()
	subs := make([]Subscriber, 0, len(b.subs))
	for _, sub := range b.subs {
		subs = append(subs, sub)
	}
	b.mu.Unlock()

	for _, env := range envs {
		for _, sub := range subs {
			deliver(ctx, sub, env)
		}
	}
}
