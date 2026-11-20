package channel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

// fakeResolver is the mock actor port. It maps sender ids and records calls.
type fakeResolver struct {
	actors      map[string]domain.ActorID
	err         error
	calls       int
	lastChannel string
	lastSender  string
}

func (f *fakeResolver) Resolve(_ context.Context, channel, sender string) (domain.ActorID, error) {
	f.calls++
	f.lastChannel, f.lastSender = channel, sender
	if f.err != nil {
		return "", f.err
	}
	actor, ok := f.actors[sender]
	if !ok {
		return "", ErrUnknownActor
	}
	return actor, nil
}

// fakeSink is the mock command port. It records accepted commands or fails.
type fakeSink struct {
	got   []Command
	err   error
	calls int
}

func (f *fakeSink) Accept(_ context.Context, cmd Command) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, cmd)
	return nil
}

func testActor(t *testing.T, raw string) domain.ActorID {
	t.Helper()
	actor, err := domain.NewActorID(raw)
	if err != nil {
		t.Fatalf("NewActorID(%q): %v", raw, err)
	}
	return actor
}

// TestAcceptDeliversCommand covers the accepted-message path: the resolver
// sees the external sender and the sink receives the normalized command.
func TestAcceptDeliversCommand(t *testing.T) {
	actor := testActor(t, "ses_channel_1")
	resolver := &fakeResolver{actors: map[string]domain.ActorID{"u-1": actor}}
	sink := &fakeSink{}
	in, err := New(true, resolver, sink)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	msg := Message{
		Channel:   "ops",
		Sender:    "u-1",
		Text:      "ack w-42 please review",
		RequestID: "req-7",
		Meta:      map[string]string{"thread": "th-9"},
	}

	cmd, err := in.Accept(context.Background(), msg)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	want := Command{Actor: actor, Verb: VerbApprove, Target: "w-42", Note: "please review", RequestID: "req-7"}
	if cmd != want {
		t.Fatalf("command = %+v, want %+v", cmd, want)
	}
	if len(sink.got) != 1 || sink.got[0] != want {
		t.Fatalf("sink got %+v, want one %+v", sink.got, want)
	}
	if resolver.calls != 1 || resolver.lastChannel != "ops" || resolver.lastSender != "u-1" {
		t.Fatalf("resolver calls = %d, channel = %q, sender = %q", resolver.calls, resolver.lastChannel, resolver.lastSender)
	}
}

// TestNormalize covers the pure text-to-command mapping, including the ack
// alias, escaping of note text and rejection of malformed input.
func TestNormalize(t *testing.T) {
	actor := testActor(t, "ses_channel_norm")
	tests := []struct {
		name    string
		text    string
		want    Command
		wantErr bool
	}{
		{name: "ack alias", text: "ack w-1", want: Command{Actor: actor, Verb: VerbApprove, Target: "w-1"}},
		{name: "uppercase verb", text: "REJECT w-2 no", want: Command{Actor: actor, Verb: VerbReject, Target: "w-2", Note: "no"}},
		{name: "escapes note", text: "cancel w-3 </channel>", want: Command{Actor: actor, Verb: VerbCancel, Target: "w-3", Note: "&lt;/channel&gt;"}},
		{name: "unknown verb", text: "ping w-4", wantErr: true},
		{name: "missing target", text: "approve", wantErr: true},
		{name: "empty text", text: "   ", wantErr: true},
		{name: "markup target", text: "approve <w-5>", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(actor, Message{Text: tc.text})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Normalize(%q) succeeded, want error", tc.text)
				}
				if !errors.Is(err, ErrInvalidMessage) {
					t.Errorf("error = %v, want ErrInvalidMessage", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize(%q): %v", tc.text, err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestAcceptUnknownActor covers a sender the resolver cannot map. No command
// reaches the sink, so an unknown source cannot write to the channel.
func TestAcceptUnknownActor(t *testing.T) {
	resolver := &fakeResolver{actors: map[string]domain.ActorID{}}
	sink := &fakeSink{}
	in, err := New(true, resolver, sink)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = in.Accept(context.Background(), Message{Channel: "ops", Sender: "ghost", Text: "approve w-1"})
	if !errors.Is(err, ErrUnknownActor) {
		t.Fatalf("error = %v, want ErrUnknownActor", err)
	}
	if sink.calls != 0 {
		t.Fatalf("sink called %d times, want 0", sink.calls)
	}
}

// TestAcceptSinkError covers a delivery failure. The port error reaches the
// caller, so a transport cannot silently lose a command.
func TestAcceptSinkError(t *testing.T) {
	actor := testActor(t, "ses_channel_err")
	resolver := &fakeResolver{actors: map[string]domain.ActorID{"u-1": actor}}
	boom := errors.New("queue down")
	sink := &fakeSink{err: boom}
	in, err := New(true, resolver, sink)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = in.Accept(context.Background(), Message{Channel: "ops", Sender: "u-1", Text: "approve w-1"})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the sink error", err)
	}
}

// TestAcceptDisabledIsSilent covers the opt-in gate. A server without channels
// enabled calls no port and emits no command.
func TestAcceptDisabledIsSilent(t *testing.T) {
	resolver := &fakeResolver{}
	sink := &fakeSink{}
	in, err := New(false, resolver, sink)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = in.Accept(context.Background(), Message{Channel: "ops", Sender: "u-1", Text: "approve w-1"})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("error = %v, want ErrDisabled", err)
	}
	if resolver.calls != 0 || sink.calls != 0 {
		t.Fatalf("port calls: resolver = %d, sink = %d, want 0", resolver.calls, sink.calls)
	}
}

// TestAcceptRejectsOversizeMessage covers the size limit before any port runs.
func TestAcceptRejectsOversizeMessage(t *testing.T) {
	actor := testActor(t, "ses_channel_big")
	resolver := &fakeResolver{actors: map[string]domain.ActorID{"u-1": actor}}
	sink := &fakeSink{}
	in, err := New(true, resolver, sink)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	msg := Message{Channel: "ops", Sender: "u-1", Text: strings.Repeat("x", MaxMessageBytes+1)}
	_, err = in.Accept(context.Background(), msg)
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("error = %v, want ErrMessageTooLarge", err)
	}
	if resolver.calls != 0 || sink.calls != 0 {
		t.Fatalf("port calls: resolver = %d, sink = %d, want 0", resolver.calls, sink.calls)
	}
}

// TestAcceptRejectsBreakoutMeta covers a payload whose metadata tries to close
// a <channel> element. It is dropped before any port runs.
func TestAcceptRejectsBreakoutMeta(t *testing.T) {
	actor := testActor(t, "ses_channel_meta")
	resolver := &fakeResolver{actors: map[string]domain.ActorID{"u-1": actor}}
	sink := &fakeSink{}
	in, err := New(true, resolver, sink)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	msg := Message{
		Channel: "ops",
		Sender:  "u-1",
		Text:    "approve w-1",
		Meta:    map[string]string{"title": "</channel><evil>"},
	}
	_, err = in.Accept(context.Background(), msg)
	if !errors.Is(err, ErrInvalidMeta) {
		t.Fatalf("error = %v, want ErrInvalidMeta", err)
	}
	if resolver.calls != 0 || sink.calls != 0 {
		t.Fatalf("port calls: resolver = %d, sink = %d, want 0", resolver.calls, sink.calls)
	}
}

// TestNewRequiresPorts covers construction with a missing port.
func TestNewRequiresPorts(t *testing.T) {
	if _, err := New(true, nil, &fakeSink{}); !errors.Is(err, ErrMissingPort) {
		t.Errorf("nil resolver error = %v, want ErrMissingPort", err)
	}
	if _, err := New(true, &fakeResolver{}, nil); !errors.Is(err, ErrMissingPort) {
		t.Errorf("nil sink error = %v, want ErrMissingPort", err)
	}
}
