// Package channel is the ingress boundary for messages that arrive from an
// external chat channel. The boundary converts one inbound message into a
// domain command and hands it to a port. It opens no socket, reads no file and
// knows no concrete channel, so the whole path is pure logic around two small
// ports. A transport adapter creates Message values and implements the ports;
// the ingress never names that adapter.
package channel

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"unicode"

	"github.com/refusedguy/beacon/internal/domain"
)

// MaxMessageBytes caps the text of one inbound message. A longer message is
// dropped before any port runs, so a hostile channel cannot grow the work
// done per message.
const MaxMessageBytes = 4096

// maxIdentifierBytes caps identifier-shaped values such as a meta key, meta
// value or command target. Every identifier Beacon issues is far shorter.
const maxIdentifierBytes = 256

// The decision verbs a channel message may produce. "ack" is accepted as an
// alias for "approve" so the channel vocabulary matches the dashboard.
const (
	VerbApprove = "approve"
	VerbReject  = "reject"
	VerbRework  = "rework"
	VerbPause   = "pause"
	VerbResume  = "resume"
	VerbUnblock = "unblock"
	VerbCancel  = "cancel"
)

var (
	// ErrDisabled reports that ingress is off. No command is delivered and no
	// port is called when the server has not opted in.
	ErrDisabled = errors.New("channel: ingress disabled")
	// ErrUnknownActor reports that the sender has no known Beacon actor.
	ErrUnknownActor = errors.New("channel: unknown actor")
	// ErrMessageTooLarge reports text beyond MaxMessageBytes.
	ErrMessageTooLarge = errors.New("channel: message too large")
	// ErrInvalidMessage reports text that cannot become a command.
	ErrInvalidMessage = errors.New("channel: invalid message")
	// ErrInvalidMeta reports metadata that is not identifier-only.
	ErrInvalidMeta = errors.New("channel: invalid metadata")
	// ErrMissingPort reports construction with a nil required port.
	ErrMissingPort = errors.New("channel: missing port")
)

// Message is one inbound message from an external channel. It carries only
// identifiers and text. There are no transport objects here, so an adapter can
// build a Message in memory and test the boundary without I/O.
type Message struct {
	// Channel names the source channel. The resolver receives it verbatim.
	Channel string
	// Sender is the external sender id. The resolver maps it to an actor.
	Sender string
	// Text is the raw message body. Normalize turns it into a command.
	Text string
	// RequestID is an optional idempotency key supplied by the channel.
	RequestID string
	// Meta carries identifier-only key/value pairs. Markup and control
	// characters are rejected so a value cannot close a <channel> element
	// when a downstream consumer renders it.
	Meta map[string]string
}

// Command is the normalized result. It is channel-agnostic: a consumer sees a
// verb, a target and the actor, never the external message format.
type Command struct {
	Actor     domain.ActorID
	Verb      string
	Target    string
	Note      string
	RequestID string
}

// ActorResolver maps an external sender to a known Beacon actor. An unknown
// sender is an error, so the ingress never guesses an identity.
type ActorResolver interface {
	Resolve(ctx context.Context, channel, sender string) (domain.ActorID, error)
}

// CommandSink accepts one normalized command. An adapter implements it with a
// queue, an HTTP call or an in-process handler. The ingress only holds the
// interface.
type CommandSink interface {
	Accept(ctx context.Context, cmd Command) error
}

// Ingress receives channel messages and delivers normalized commands. A
// disabled Ingress is silent: it calls no port and emits no command.
type Ingress struct {
	enabled  bool
	resolver ActorResolver
	sink     CommandSink
}

// New builds an Ingress. The enabled flag is the opt-in: a server that has not
// enabled channels passes false and gets no delivery. A nil resolver or sink is
// refused, because a boundary that cannot resolve an actor or deliver a
// command must not start.
func New(enabled bool, resolver ActorResolver, sink CommandSink) (*Ingress, error) {
	if resolver == nil {
		return nil, fmt.Errorf("%w: actor resolver", ErrMissingPort)
	}
	if sink == nil {
		return nil, fmt.Errorf("%w: command sink", ErrMissingPort)
	}
	return &Ingress{enabled: enabled, resolver: resolver, sink: sink}, nil
}

// Accept validates one message, resolves its actor, normalizes its text and
// delivers the command. It returns the delivered command. Every rejection
// happens before a command reaches the sink, so a bad message has no effect.
func (in *Ingress) Accept(ctx context.Context, msg Message) (Command, error) {
	if !in.enabled {
		return Command{}, ErrDisabled
	}
	if len(msg.Text) > MaxMessageBytes {
		return Command{}, ErrMessageTooLarge
	}
	if err := validateMeta(msg.Meta); err != nil {
		return Command{}, err
	}

	actor, err := in.resolver.Resolve(ctx, msg.Channel, msg.Sender)
	if err != nil {
		return Command{}, fmt.Errorf("%w: %v", ErrUnknownActor, err)
	}

	cmd, err := Normalize(actor, msg)
	if err != nil {
		return Command{}, err
	}
	if err := in.sink.Accept(ctx, cmd); err != nil {
		return Command{}, fmt.Errorf("channel: deliver command: %w", err)
	}
	return cmd, nil
}

// Normalize converts message text into a canonical command without I/O. The
// first field is the verb and the second is the target. Remaining fields form
// the note, with markup escaped so the text cannot break a <channel> element
// when a consumer renders it. Exported so an adapter can preview the mapping.
func Normalize(actor domain.ActorID, msg Message) (Command, error) {
	fields := strings.Fields(strings.TrimSpace(msg.Text))
	if len(fields) == 0 {
		return Command{}, fmt.Errorf("%w: empty text", ErrInvalidMessage)
	}
	verb, ok := canonicalVerb(fields[0])
	if !ok {
		return Command{}, fmt.Errorf("%w: unknown command %q", ErrInvalidMessage, fields[0])
	}
	if len(fields) < 2 {
		return Command{}, fmt.Errorf("%w: command %q needs a target", ErrInvalidMessage, verb)
	}
	target := fields[1]
	if !safeIdentifier(target) {
		return Command{}, fmt.Errorf("%w: invalid target %q", ErrInvalidMessage, target)
	}
	return Command{
		Actor:     actor,
		Verb:      verb,
		Target:    target,
		Note:      html.EscapeString(strings.Join(fields[2:], " ")),
		RequestID: msg.RequestID,
	}, nil
}

// canonicalVerb lowercases raw and maps the "ack" alias. It reports false for
// any verb outside the closed decision set.
func canonicalVerb(raw string) (string, bool) {
	verb := strings.ToLower(strings.TrimSpace(raw))
	if verb == "ack" {
		verb = VerbApprove
	}
	switch verb {
	case VerbApprove, VerbReject, VerbRework, VerbPause, VerbResume, VerbUnblock, VerbCancel:
		return verb, true
	default:
		return "", false
	}
}

// validateMeta requires every key and value to be an identifier. Markup and
// control characters are rejected, so a value cannot inject a closing tag or
// a stray control sequence into a downstream <channel> element.
func validateMeta(meta map[string]string) error {
	for key, value := range meta {
		if !safeIdentifier(key) {
			return fmt.Errorf("%w: bad key %q", ErrInvalidMeta, key)
		}
		if !safeIdentifier(value) {
			return fmt.Errorf("%w: bad value for %q", ErrInvalidMeta, key)
		}
	}
	return nil
}

// safeIdentifier reports whether s is a short token with no whitespace, no
// control characters and no markup. Separators such as ':' and '.' stay valid
// because Beacon identifiers use them.
func safeIdentifier(s string) bool {
	if s == "" || len(s) > maxIdentifierBytes {
		return false
	}
	for _, r := range s {
		if r == '<' || r == '>' || r == '&' {
			return false
		}
		if unicode.IsSpace(r) || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
