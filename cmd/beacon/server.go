package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	appcommand "github.com/refusedguy/beacon/internal/app/command"
	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/channel"
	"github.com/refusedguy/beacon/internal/infra/httpapi"
	"github.com/refusedguy/beacon/internal/infra/server"
)

// serverLease is the single-instance lease port. The file locking, pidfile and
// liveness logic live in internal/infra/server; this layer only calls the
// port. Tests replace it to exercise the command without a real state dir.
var serverLease server.Acquirer = server.NewManager()

const (
	// channelEnabledEnv is the opt-in for inbound channel commands. An unset
	// or false value leaves the ingress silent, so a stock server accepts no
	// external message.
	channelEnabledEnv = "BEACON_CHANNELS"
	// channelSendersEnv is a comma-separated sender=actor registry. Only a
	// sender named here can resolve to a Beacon actor; every other sender is
	// refused before a command reaches the processor.
	channelSendersEnv = "BEACON_CHANNEL_SENDERS"
	// channelTelegram names the source of messages the up loop polls.
	channelTelegram = "telegram"
)

// channelConfig is the process-side opt-in for the channel boundary. The
// channel package itself stays free of configuration and transport (BS.60).
type channelConfig struct {
	Enabled bool
	Senders map[string]domain.ActorID
}

// channelConfigFromEnv parses the opt-in and the known-sender registry at the
// CLI boundary. A malformed registry entry is a start-up error: an operator
// must not believe a sender is allowed when its actor id was rejected.
func channelConfigFromEnv(lookup func(string) string) (channelConfig, error) {
	if lookup == nil {
		lookup = os.Getenv
	}
	cfg := channelConfig{
		Enabled: channelEnabledValue(lookup(channelEnabledEnv)),
		Senders: make(map[string]domain.ActorID),
	}
	for _, entry := range strings.Split(lookup(channelSendersEnv), ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		sender, rawActor, ok := strings.Cut(entry, "=")
		sender, rawActor = strings.TrimSpace(sender), strings.TrimSpace(rawActor)
		if !ok || sender == "" || rawActor == "" {
			return cfg, fmt.Errorf("beacon channel: invalid sender entry %q", entry)
		}
		actor, err := domain.NewActorID(rawActor)
		if err != nil {
			return cfg, fmt.Errorf("beacon channel: sender %q: %w", sender, err)
		}
		cfg.Senders[sender] = actor
	}
	return cfg, nil
}

func channelEnabledValue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// channelSenderResolver is the concrete ActorResolver. It maps an external
// sender through the operator-owned registry and never guesses an identity.
type channelSenderResolver struct {
	senders map[string]domain.ActorID
}

func (r channelSenderResolver) Resolve(_ context.Context, _ string, sender string) (domain.ActorID, error) {
	actor, ok := r.senders[sender]
	if !ok {
		return "", channel.ErrUnknownActor
	}
	return actor, nil
}

// buildChannelIngress assembles the channel boundary from its concrete ports:
// the known-sender resolver and a durable command processor. apply runs only
// after the processor commits, so a failed delivery cannot leave an applied
// decision without its intent row.
func buildChannelIngress(cfg channelConfig, outbox appcommand.Outbox, apply appcommand.Sink) (*channel.Ingress, error) {
	resolver := channelSenderResolver{senders: cfg.Senders}
	processor := appcommand.New(outbox, apply)
	return channel.New(cfg.Enabled, resolver, appcommand.NewChannelSink(processor))
}

// channelApplySink is the effect the processor runs after it commits. It
// decodes the stored channel command and sends the decision through the same
// port `beacon human` uses, so channel and human decisions share the journal
// and idempotency rules.
func channelApplySink(decisions upDecisionPort) appcommand.Sink {
	return func(ctx context.Context, effect appcommand.Effect) error {
		var cmd channel.Command
		if err := json.Unmarshal(effect.Payload, &cmd); err != nil {
			return fmt.Errorf("beacon channel: decode %s: %w", effect.Key, err)
		}
		_, err := decisions.Decide(ctx, httpapi.ClientDecision{
			Verb:   cmd.Verb,
			Target: cmd.Target,
			Note:   cmd.Note,
		})
		return err
	}
}

// serverRunner is the single-instance entry point for beacon-server. It takes
// the machine-wide lease before the MCP server starts and releases it on
// return. A second concurrent start is refused with a clear error (BS.1).
func serverRunner(errOut io.Writer) int {
	cfg, err := channelConfigFromEnv(nil)
	if err != nil {
		fmt.Fprintf(errOut, "beacon server: channel config: %v\n", err)
		return 2
	}

	dir, err := stateDir()
	if err != nil {
		fmt.Fprintf(errOut, "beacon server: state dir: %v\n", err)
		return 2
	}

	lease, err := serverLease.Acquire(dir)
	if err != nil {
		fmt.Fprintf(errOut, "beacon server: %v\n", err)
		return 1
	}
	defer func() { _ = lease.Release() }()

	if cfg.Enabled {
		fmt.Fprintf(errOut, "beacon server: channel ingress enabled senders=%d\n", len(cfg.Senders))
	}
	return mcpRunner(errOut)
}
