// Package command records a command's intent and its queued effect as one
// durable unit. The processor owns no I/O: persistence goes through the Outbox
// port and the effect runs through an injected sink only after that port has
// committed. A repeated key is a replay that writes nothing and runs nothing.
package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/refusedguy/beacon/internal/infra/channel"
)

// Effect is both the intent the processor commits and the effect handed to
// the sink afterwards. Key is the command's idempotency key.
type Effect struct {
	Key     string
	Kind    string
	Payload []byte
}

// Outbox is the transactional port the processor consumes. Commit stores the
// intent and one outbox entry in a single transaction: either both land or
// neither does. created is true only on the first call for a key; a repeat
// writes nothing and reports false. Commit returns only after the transaction
// has committed or rolled back, so an effect the processor starts afterwards
// cannot precede the commit.
type Outbox interface {
	Commit(ctx context.Context, key, kind string, payload []byte) (created bool, err error)
}

// Sink executes a committed effect. The processor calls it after Outbox.Commit
// returns successfully; a sink error does not roll the commit back. A nil sink
// means the caller only wants the durable intent and outbox entry.
type Sink func(ctx context.Context, effect Effect) error

// Result reports what Process did. Created marks the first command for a key;
// Replayed marks a repeat that wrote no row and ran no effect.
type Result struct {
	Created  bool
	Replayed bool
}

var (
	ErrEmptyKey     = errors.New("command: empty idempotency key")
	ErrEmptyKind    = errors.New("command: empty effect kind")
	ErrNilOutbox    = errors.New("command: nil outbox")
	ErrNilProcessor = errors.New("command: nil processor")
)

// Processor commits command intent and outbox through Outbox, then runs the
// effect through the sink. It performs no external I/O of its own.
type Processor struct {
	outbox Outbox
	sink   Sink
}

// New builds a Processor. outbox is required; sink may be nil.
func New(outbox Outbox, sink Sink) *Processor {
	return &Processor{outbox: outbox, sink: sink}
}

// Process records in and executes its effect only after the recording
// commits. A repeat with the same key is a no-op that returns Replayed.
func (p *Processor) Process(ctx context.Context, in Effect) (Result, error) {
	if p == nil || p.outbox == nil {
		return Result{}, ErrNilOutbox
	}
	if err := validate(in); err != nil {
		return Result{}, err
	}

	created, err := p.outbox.Commit(ctx, in.Key, in.Kind, in.Payload)
	if err != nil {
		return Result{}, err
	}
	if !created {
		return Result{Replayed: true}, nil
	}
	if p.sink != nil {
		if err := p.sink(ctx, in); err != nil {
			return Result{Created: true}, err
		}
	}
	return Result{Created: true}, nil
}

func validate(in Effect) error {
	if strings.TrimSpace(in.Key) == "" {
		return ErrEmptyKey
	}
	if strings.TrimSpace(in.Kind) == "" {
		return ErrEmptyKind
	}
	return nil
}

// ChannelCommandKind labels the durable effect recorded for one channel
// command. The processor's sink receives the same label, so a drainer can
// route the effect without decoding the payload twice.
const ChannelCommandKind = "channel_command"

// ChannelSink adapts a normalized channel command to the durable processor.
// It implements channel.CommandSink: the ingress hands it a Command, and the
// processor records the intent and outbox entry before the caller's sink runs
// the effect (BS.60, WIRING-486).
type ChannelSink struct {
	processor *Processor
}

// NewChannelSink builds a sink over processor.
func NewChannelSink(processor *Processor) *ChannelSink {
	return &ChannelSink{processor: processor}
}

// Accept records cmd as an Effect and runs the processor's effect sink. A
// repeated request id replays the original effect instead of writing another
// row, so a channel that resends a delivery cannot apply a decision twice.
func (s *ChannelSink) Accept(ctx context.Context, cmd channel.Command) error {
	if s == nil || s.processor == nil {
		return ErrNilProcessor
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("command: encode channel command: %w", err)
	}
	effect := Effect{
		Key:     channelEffectKey(cmd),
		Kind:    ChannelCommandKind,
		Payload: payload,
	}
	_, err = s.processor.Process(ctx, effect)
	return err
}

// channelEffectKey builds the idempotency key for one channel command. A
// channel request id wins when present. Without one the key uses the actor,
// verb and target, which are the fields that decide what the command does;
// the note is deliberately excluded so a reworded note does not make a second
// effect. The escape makes the composite unambiguous when a value contains a
// ':' or a backslash.
func channelEffectKey(cmd channel.Command) string {
	if cmd.RequestID != "" {
		return ChannelCommandKind + ":" + escapeChannelKey(cmd.RequestID)
	}
	parts := []string{string(cmd.Actor), cmd.Verb, cmd.Target}
	for i, part := range parts {
		parts[i] = escapeChannelKey(part)
	}
	return ChannelCommandKind + ":" + strings.Join(parts, ":")
}

func escapeChannelKey(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, ":", `\:`)
}
