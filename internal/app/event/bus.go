// Package event implements a small in-process event bus with post-commit
// delivery. Events are staged inside a transaction and are only handed to
// subscribers after the transaction commits, so a rollback or a failing
// writer can never leak an uncommitted event to the rest of the process.
//
// The bus deliberately depends on a tiny Journal port instead of importing
// the store-backed event log (BS.11). That keeps this layer free of infra
// imports and lets the bus work with a nil journal using a local seq counter.
package event

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
)

// Envelope is one event carried by the bus. Its fields mirror the events
// table from BS.11 (seq, scope, type, schema_version, payload).
type Envelope struct {
	Seq           uint64 `json:"seq"`
	Scope         string `json:"scope"`
	Type          string `json:"type"`
	SchemaVersion int    `json:"schema_version"`
	Payload       []byte `json:"payload"`
}

// Hook lifecycle envelope constants. They mirror the SDK's HookEventMessage:
// a system message with subtype hook_started or hook_response and a
// hook_event_name identifying the hook.
const (
	// EventTypeHook is the Envelope.Type used for mirrored hook phases.
	EventTypeHook = "hook"
	// HookEventScope is the Envelope.Scope used for mirrored hook phases.
	HookEventScope = "hooks"
	// HookEventSchemaVersion is the payload schema version for hook events.
	HookEventSchemaVersion = 1

	// HookPhaseStarted is emitted before a hook callback runs.
	HookPhaseStarted = "hook_started"
	// HookPhaseResponse is emitted after a hook callback returns.
	HookPhaseResponse = "hook_response"
)

// HookPayload is the JSON payload of a hook Envelope. hook_event_name and
// Phase let an observer order the two phases without reading logs; Outcome
// and Error describe a completed call.
type HookPayload struct {
	HookEventName string `json:"hook_event_name"`
	Phase         string `json:"phase"`
	Outcome       string `json:"outcome,omitempty"`
	Error         string `json:"error,omitempty"`
}

// HookEventConfig controls mirroring of hook lifecycle phases into the event
// stream. IncludeHookEvents is false by default, so an existing stream is
// unchanged until a caller opts in.
type HookEventConfig struct {
	IncludeHookEvents bool `json:"include_hook_events"`
}

// Journal is the minimal persistence port the bus needs. BS.11 supplies the
// real store-backed implementation; the bus takes it as a parameter so it
// does not import store/events.go directly.
//
// Append must persist a batch atomically and return exactly one monotonic seq
// per envelope, in the same order as envs.
type Journal interface {
	Append(ctx context.Context, envs []Envelope) ([]uint64, error)
}

// Subscriber receives a committed envelope. A returned error marks the
// subscriber as failing but never blocks other subscribers or later events.
type Subscriber func(ctx context.Context, env Envelope) error

// PoisonError marks an envelope a subscriber cannot handle. The bus records
// it and moves on, so one bad event cannot stall the stream.
type PoisonError struct {
	Reason string
}

func (e *PoisonError) Error() string { return "event: poisoned: " + e.Reason }

// NewPoisonError builds an error that flags an envelope as poisoned.
func NewPoisonError(reason string) error { return &PoisonError{Reason: reason} }

// IsPoison reports whether err flags a poisoned envelope.
func IsPoison(err error) bool {
	var pe *PoisonError
	return errors.As(err, &pe)
}

// Poison is the record kept for the most recent undeliverable event.
type Poison struct {
	Seq        uint64 `json:"seq"`
	PayloadSHA string `json:"payload_sha"`
	Reason     string `json:"reason"`
}

var (
	// ErrTxDone is returned when a finished transaction is reused.
	ErrTxDone = errors.New("event: transaction already finished")
)

// Bus delivers committed envelopes to in-process subscribers.
type Bus struct {
	journal Journal

	hookEvents HookEventConfig

	commitMu sync.Mutex // serializes assignSeqs so seq values stay monotonic

	mu         sync.Mutex
	nextSeq    uint64 // local seq source when journal is nil
	nextSubID  uint64
	subs       []subscription
	watermark  uint64 // highest seq the bus has attempted to deliver
	lastPoison *Poison
}

type subscription struct {
	id uint64
	fn Subscriber
}

// NewBus builds a bus. journal may be nil, in which case the bus assigns seq
// values from a local counter.
func NewBus(journal Journal) *Bus {
	return &Bus{journal: journal}
}

// SetIncludeHookEvents enables or disables hook phase mirroring on the bus.
// The option is off by default; callers opt in before running hooks.
func (b *Bus) SetIncludeHookEvents(enabled bool) {
	b.mu.Lock()
	b.hookEvents.IncludeHookEvents = enabled
	b.mu.Unlock()
}

// includeHookEvents reports whether hook phases should be mirrored.
func (b *Bus) includeHookEvents() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hookEvents.IncludeHookEvents
}

// Subscribe registers a subscriber and returns a function that removes it.
// Nil subscribers are ignored.
func (b *Bus) Subscribe(sub Subscriber) func() {
	if sub == nil {
		return func() {}
	}
	b.mu.Lock()
	b.nextSubID++
	id := b.nextSubID
	b.subs = append(b.subs, subscription{id: id, fn: sub})
	b.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			for i, s := range b.subs {
				if s.id == id {
					b.subs = append(b.subs[:i], b.subs[i+1:]...)
					return
				}
			}
		})
	}
}

// Begin starts a transaction. Envelopes emitted into it stay private until
// Commit, and are discarded by Rollback (the discardOnThrow behavior).
func (b *Bus) Begin(ctx context.Context) *Tx {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Tx{bus: b, ctx: ctx}
}

// WithTx runs fn and commits on success. Any error or panic rolls the
// transaction back, so no staged envelope escapes a failed unit of work.
func (b *Bus) WithTx(ctx context.Context, fn func(*Tx) error) (err error) {
	tx := b.Begin(ctx)
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()
	if err = fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Watermark returns the highest seq the bus has attempted to deliver.
func (b *Bus) Watermark() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.watermark
}

// LastPoison returns a copy of the most recent poison record, or nil.
func (b *Bus) LastPoison() *Poison {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lastPoison == nil {
		return nil
	}
	p := *b.lastPoison
	return &p
}

// Tx is a unit of work that stages envelopes before delivery.
type Tx struct {
	bus    *Bus
	ctx    context.Context
	staged []Envelope
	done   bool
}

// Emit stages an envelope. It is not visible to subscribers until Commit.
func (tx *Tx) Emit(env Envelope) error {
	if tx.done {
		return ErrTxDone
	}
	tx.staged = append(tx.staged, env)
	return nil
}

// Savepoint returns a marker for the current end of the staged batch. Pass it
// to RollbackTo to discard envelopes staged after this point.
func (tx *Tx) Savepoint() int {
	return len(tx.staged)
}

// RollbackTo discards every envelope staged after the savepoint.
func (tx *Tx) RollbackTo(sp int) error {
	if tx.done {
		return ErrTxDone
	}
	if sp < 0 || sp > len(tx.staged) {
		return fmt.Errorf("event: bad savepoint %d", sp)
	}
	tx.staged = tx.staged[:sp]
	return nil
}

// Rollback discards all staged envelopes (discardOnThrow).
func (tx *Tx) Rollback() {
	if tx.done {
		return
	}
	tx.done = true
	tx.staged = nil
}

// Commit persists staged envelopes, then drains them to subscribers. It is
// safe to call only once; later calls return ErrTxDone. Seq assignment is
// serialized to keep order, but delivery runs with the lock released so a
// subscriber can commit another transaction without deadlocking.
func (tx *Tx) Commit() error {
	if tx.done {
		return ErrTxDone
	}
	tx.done = true
	if len(tx.staged) == 0 {
		return nil
	}

	b := tx.bus
	envs := make([]Envelope, len(tx.staged))
	copy(envs, tx.staged)

	b.commitMu.Lock()
	err := b.assignSeqs(tx.ctx, envs)
	b.commitMu.Unlock()
	if err != nil {
		return err
	}

	b.drain(tx.ctx, envs)
	return nil
}

// assignSeqs gives each envelope a monotonic seq, from the journal when one
// is configured and from a local counter otherwise.
func (b *Bus) assignSeqs(ctx context.Context, envs []Envelope) error {
	if b.journal != nil {
		seqs, err := b.journal.Append(ctx, envs)
		if err != nil {
			return err
		}
		if len(seqs) != len(envs) {
			return fmt.Errorf("event: journal returned %d seqs for %d envelopes", len(seqs), len(envs))
		}
		for i, s := range seqs {
			envs[i].Seq = s
		}
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range envs {
		b.nextSeq++
		envs[i].Seq = b.nextSeq
	}
	return nil
}

// drain hands each envelope to every subscriber. A failing or panicking
// subscriber is isolated and never stops the remaining work.
func (b *Bus) drain(ctx context.Context, envs []Envelope) {
	b.mu.Lock()
	subs := make([]Subscriber, 0, len(b.subs))
	for _, s := range b.subs {
		subs = append(subs, s.fn)
	}
	b.mu.Unlock()

	for _, env := range envs {
		for _, sub := range subs {
			if err := deliverOne(ctx, sub, env); err != nil && IsPoison(err) {
				b.recordPoison(env, err.Error())
			}
		}
		b.advanceWatermark(env)
	}
}

// deliverOne calls a subscriber, converting a panic into a plain error so it
// cannot tear down the delivery loop.
func deliverOne(ctx context.Context, sub Subscriber, env Envelope) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if panicErr, ok := r.(error); ok {
				err = fmt.Errorf("subscriber panic: %w", panicErr)
				return
			}
			err = fmt.Errorf("subscriber panic: %v", r)
		}
	}()
	return sub(ctx, env)
}

func (b *Bus) advanceWatermark(env Envelope) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if env.Seq > b.watermark {
		b.watermark = env.Seq
	}
}

func (b *Bus) recordPoison(env Envelope, reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastPoison = &Poison{
		Seq:        env.Seq,
		PayloadSHA: fmt.Sprintf("%x", sha256.Sum256(env.Payload)),
		Reason:     reason,
	}
}
