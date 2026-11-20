package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	appcommand "github.com/refusedguy/beacon/internal/app/command"
	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/channel"
	"github.com/refusedguy/beacon/internal/infra/httpapi"
)

// channelFakeOutbox is the mock Outbox port for the server wiring test.
type channelFakeOutbox struct {
	key     string
	kind    string
	payload []byte
	created bool
	calls   int
}

func (o *channelFakeOutbox) Commit(_ context.Context, key, kind string, payload []byte) (bool, error) {
	o.calls++
	o.key, o.kind, o.payload = key, kind, payload
	return o.created, nil
}

func TestChannelConfigFromEnv(t *testing.T) {
	values := map[string]string{
		channelEnabledEnv: "1",
		channelSendersEnv: "u-1=ses_alice, u-2 = ses_bob",
	}
	cfg, err := channelConfigFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("channelConfigFromEnv: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("channels disabled, want enabled")
	}
	if got, want := cfg.Senders["u-1"], domain.ActorID("ses_alice"); got != want {
		t.Errorf("sender u-1 = %q, want %q", got, want)
	}
	if got, want := cfg.Senders["u-2"], domain.ActorID("ses_bob"); got != want {
		t.Errorf("sender u-2 = %q, want %q", got, want)
	}
}

func TestChannelConfigFromEnvDisabledByDefault(t *testing.T) {
	cfg, err := channelConfigFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("channelConfigFromEnv: %v", err)
	}
	if cfg.Enabled || len(cfg.Senders) != 0 {
		t.Fatalf("config = %+v, want disabled with no senders", cfg)
	}
}

func TestChannelConfigFromEnvRejectsMalformedSender(t *testing.T) {
	values := map[string]string{channelSendersEnv: "u-1"}
	if _, err := channelConfigFromEnv(func(key string) string { return values[key] }); err == nil {
		t.Fatal("accepted a sender entry without an actor")
	}
}

func TestChannelSenderResolverUsesKnownSenders(t *testing.T) {
	resolver := channelSenderResolver{senders: map[string]domain.ActorID{"u-1": "ses_alice"}}
	actor, err := resolver.Resolve(context.Background(), channelTelegram, "u-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if actor != "ses_alice" {
		t.Fatalf("actor = %q, want ses_alice", actor)
	}
	_, err = resolver.Resolve(context.Background(), channelTelegram, "ghost")
	if !errors.Is(err, channel.ErrUnknownActor) {
		t.Fatalf("error = %v, want ErrUnknownActor", err)
	}
}

func TestBuildChannelIngressDeliversThroughCommandProcessor(t *testing.T) {
	cfg := channelConfig{
		Enabled: true,
		Senders: map[string]domain.ActorID{"u-1": "ses_alice"},
	}
	outbox := &channelFakeOutbox{created: true}
	var applied []channel.Command
	ingress, err := buildChannelIngress(cfg, outbox, func(_ context.Context, effect appcommand.Effect) error {
		var got channel.Command
		if err := json.Unmarshal(effect.Payload, &got); err != nil {
			return err
		}
		applied = append(applied, got)
		return nil
	})
	if err != nil {
		t.Fatalf("buildChannelIngress: %v", err)
	}

	cmd, err := ingress.Accept(context.Background(), channel.Message{
		Channel: channelTelegram, Sender: "u-1", Text: "approve w-42 please review",
	})
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	want := channel.Command{Actor: "ses_alice", Verb: channel.VerbApprove, Target: "w-42", Note: "please review"}
	if cmd != want {
		t.Fatalf("command = %+v, want %+v", cmd, want)
	}
	if outbox.calls != 1 || outbox.kind != appcommand.ChannelCommandKind {
		t.Fatalf("outbox calls = %d, kind = %q, want one %s effect", outbox.calls, outbox.kind, appcommand.ChannelCommandKind)
	}
	if len(applied) != 1 || applied[0] != want {
		t.Fatalf("applied = %+v, want one %+v", applied, want)
	}
}

func TestBuildChannelIngressDisabledIsSilent(t *testing.T) {
	cfg := channelConfig{Senders: map[string]domain.ActorID{"u-1": "ses_alice"}}
	outbox := &channelFakeOutbox{created: true}
	applied := 0
	ingress, err := buildChannelIngress(cfg, outbox, func(context.Context, appcommand.Effect) error {
		applied++
		return nil
	})
	if err != nil {
		t.Fatalf("buildChannelIngress: %v", err)
	}

	_, err = ingress.Accept(context.Background(), channel.Message{
		Channel: channelTelegram, Sender: "u-1", Text: "approve w-42",
	})
	if !errors.Is(err, channel.ErrDisabled) {
		t.Fatalf("error = %v, want ErrDisabled", err)
	}
	if outbox.calls != 0 || applied != 0 {
		t.Fatalf("outbox calls = %d, applied = %d, want 0", outbox.calls, applied)
	}
}

func TestBuildChannelIngressRefusesUnknownSender(t *testing.T) {
	cfg := channelConfig{Enabled: true, Senders: map[string]domain.ActorID{"u-1": "ses_alice"}}
	outbox := &channelFakeOutbox{created: true}
	ingress, err := buildChannelIngress(cfg, outbox, nil)
	if err != nil {
		t.Fatalf("buildChannelIngress: %v", err)
	}

	_, err = ingress.Accept(context.Background(), channel.Message{
		Channel: channelTelegram, Sender: "ghost", Text: "approve w-42",
	})
	if !errors.Is(err, channel.ErrUnknownActor) {
		t.Fatalf("error = %v, want ErrUnknownActor", err)
	}
	if outbox.calls != 0 {
		t.Fatalf("outbox calls = %d, want 0", outbox.calls)
	}
}

func TestChannelApplySinkPassesCommandToDecisionPort(t *testing.T) {
	decisions := &upFakeDecisions{receipt: httpapi.ClientDecisionReceipt{OK: true, Applied: true}}
	payload, err := json.Marshal(channel.Command{
		Actor: "ses_alice", Verb: channel.VerbReject, Target: "w-42", Note: "needs work",
	})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}

	apply := channelApplySink(decisions)
	if err := apply(context.Background(), appcommand.Effect{
		Key: "channel:req-1", Kind: appcommand.ChannelCommandKind, Payload: payload,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := httpapi.ClientDecision{Verb: channel.VerbReject, Target: "w-42", Note: "needs work"}
	if len(decisions.requests) != 1 || decisions.requests[0] != want {
		t.Fatalf("decisions = %+v, want one %+v", decisions.requests, want)
	}
}
