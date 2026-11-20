// Package eventbus provides an in-process event bus. Publishers stage envelopes
// and Commit delivers them to subscribers. A subscriber never sees an envelope
// before its unit of work commits.
package eventbus

import (
	"context"
	"sync"
)

// Envelope is one event carried by the bus.
type Envelope struct {
	Type    string
	Payload []byte
}

// Subscriber consumes a committed envelope. An error marks the subscriber as
// failing but never stops delivery to other subscribers.
type Subscriber func(ctx context.Context, env Envelope) error

// Port is the contract callers use to publish and subscribe. Depending on the
// interface keeps the server free of the in-process implementation details.
type Port interface {
	Subscribe(sub Subscriber) func()
	Publish(env Envelope)
	Commit(ctx context.Context)
}

// Bus is the in-process implementation of Port. It buffers published envelopes
// until Commit, then hands them to every current subscriber. A subscriber that
// returns an error or panics cannot stop the others.
type Bus struct {
	mu     sync.Mutex
	nextID uint64
	subs   map[uint64]Subscriber
	staged []Envelope
}

var _ Port = (*Bus)(nil)

// New builds an empty bus with no subscribers.
func New() *Bus {
	return &Bus{subs: make(map[uint64]Subscriber)}
}

// Subscribe registers sub and returns a function that removes it. The returned
// function is safe to call more than once. Nil subscribers are ignored.
func (b *Bus) Subscribe(sub Subscriber) func() {
	if sub == nil {
		return func() {}
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.subs[id] = sub
	b.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
		})
	}
}

// Publish stages env for delivery. Subscribers do not see env until Commit.
func (b *Bus) Publish(env Envelope) {
	b.mu.Lock()
	b.staged = append(b.staged, env)
	b.mu.Unlock()
}

// Commit delivers every staged envelope to the current subscribers and clears
// the buffer. Envelopes published after Commit go into the next batch.
func (b *Bus) Commit(ctx context.Context) {
	b.mu.Lock()
	envs := b.staged
	b.staged = nil
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

// deliver calls sub and drops its error or panic so one subscriber cannot stop
// the delivery loop.
func deliver(ctx context.Context, sub Subscriber, env Envelope) {
	defer func() {
		_ = recover()
	}()
	_ = sub(ctx, env)
}
