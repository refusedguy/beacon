package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/channel"
)

// channelFakeIngress is the mock channel boundary. It records the message the
// loop hands over and returns either a command or a refusal.
type channelFakeIngress struct {
	msgs  []channel.Message
	cmd   channel.Command
	err   error
	calls int
}

func (f *channelFakeIngress) Accept(_ context.Context, msg channel.Message) (channel.Command, error) {
	f.calls++
	f.msgs = append(f.msgs, msg)
	if f.err != nil {
		return channel.Command{}, f.err
	}
	return f.cmd, nil
}

func installUpChannelIngress(t *testing.T, port channelMessagePort) {
	t.Helper()
	previous := upChannelIngress
	upChannelIngress = port
	t.Cleanup(func() { upChannelIngress = previous })
}

func TestHandleUpCommandRoutesPlainChannelCommand(t *testing.T) {
	ingress := &channelFakeIngress{cmd: channel.Command{
		Actor: "ses_alice", Verb: channel.VerbApprove, Target: "w-7",
	}}
	installUpChannelIngress(t, ingress)
	messenger := &upFakeMessenger{}
	decisions := &upFakeDecisions{}

	handleUpCommand(context.Background(), messenger, "42", "approve w-7 ok", upFakeStatus{}, decisions, nil)

	if ingress.calls != 1 || len(ingress.msgs) != 1 {
		t.Fatalf("ingress calls = %d, messages = %+v, want one", ingress.calls, ingress.msgs)
	}
	msg := ingress.msgs[0]
	if msg.Channel != channelTelegram || msg.Sender != "42" || msg.Text != "approve w-7 ok" {
		t.Fatalf("message = %+v, want a telegram message from 42", msg)
	}
	if len(decisions.requests) != 0 {
		t.Fatalf("legacy decisions = %+v, want 0", decisions.requests)
	}
	if len(messenger.texts) != 1 || !strings.Contains(messenger.texts[0], "approve w-7") {
		t.Fatalf("reply = %v, want a channel receipt", messenger.texts)
	}
}

func TestHandleUpCommandChannelRefusalFallsThrough(t *testing.T) {
	ingress := &channelFakeIngress{err: errors.New("unknown sender")}
	installUpChannelIngress(t, ingress)
	messenger := &upFakeMessenger{}
	decisions := &upFakeDecisions{}

	handleUpCommand(context.Background(), messenger, "42", "approve w-7", upFakeStatus{}, decisions, nil)

	if ingress.calls != 1 {
		t.Fatalf("ingress calls = %d, want 1", ingress.calls)
	}
	if len(decisions.requests) != 0 || len(messenger.texts) != 0 {
		t.Fatalf("fallback dispatched decisions=%+v replies=%v, want 0", decisions.requests, messenger.texts)
	}
}

func TestHandleUpCommandSlashCommandSkipsChannelIngress(t *testing.T) {
	ingress := &channelFakeIngress{cmd: channel.Command{
		Actor: "ses_alice", Verb: channel.VerbApprove, Target: "w-7",
	}}
	installUpChannelIngress(t, ingress)
	messenger := &upFakeMessenger{}

	handleUpCommand(context.Background(), messenger, "42", "/status", upFakeStatus{}, &upFakeDecisions{}, nil)

	if ingress.calls != 0 {
		t.Fatalf("ingress calls = %d, want 0 for a slash command", ingress.calls)
	}
	if len(messenger.texts) != 1 || !strings.Contains(messenger.texts[0], "watches=0") {
		t.Fatalf("reply = %v, want the status projection", messenger.texts)
	}
}

func TestUpChannelStateUsesInstalledIngress(t *testing.T) {
	installUpChannelIngress(t, &channelFakeIngress{})
	if got := upChannelState(); got != "on" {
		t.Fatalf("channel state = %q, want on", got)
	}
}
