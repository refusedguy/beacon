package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/infra/observe"
	"github.com/refusedguy/beacon/internal/infra/opencode"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

// daemonWakeAdapter is the built-in deliverer the daemon registers. It lives
// in cmd because it names the wake infra package (ARDH-15 DIP). Deliver posts
// through the shared openCodePort and uses the CLI only when no live port is
// installed (AUD-02).
type daemonWakeAdapter struct {
	api *wake.T202API
}

func (a *daemonWakeAdapter) Bind(p wake.T210WakePointer, _ *wake.T211SessionRegistry) (wake.T210Target, bool) {
	if err := wake.ValidateT210Target(p); err != nil {
		log.Printf("daemon: skip wake bind for %s: %v", p.ObligationID, err)
		return "", false
	}
	if err := orchestrator.ValidateWakeTarget(p.Target); err != nil {
		log.Printf("daemon: skip wake bind for %s: %v", p.ObligationID, err)
		return "", false
	}
	return wake.T210Target(p.Target), true
}

func (a *daemonWakeAdapter) Deliver(p wake.T210WakePointer, target wake.T210Target) (wake.T210Receipt, error) {
	receipt := wake.T210Receipt{
		ObligationID: p.ObligationID,
		Attempt:      p.Attempt,
		Delivery:     string(wake.T202DeliverySteer),
	}
	if string(target) != p.Target {
		err := fmt.Errorf("daemon: target %q is not subscriber %q", target, p.Target)
		receipt.Outcome = wake.T210Errored
		return receipt, err
	}
	if a == nil {
		receipt.Outcome = wake.T210Errored
		return receipt, errors.New("daemon: wake adapter is nil")
	}
	if port := openCodePort(); port != nil {
		opts := opencode.StartPromptOptions(p.ObligationID)
		if err := port.PromptWithOptions(context.Background(), string(target), p.VerdictRef, opts); err != nil {
			receipt.Outcome = wake.T210Errored
			return receipt, fmt.Errorf("daemon: HTTP prompt: %w", err)
		}
		receipt.Outcome = wake.T210Delivered
		return receipt, nil
	}
	// No live port: the CLI path is the fallback, not a second HTTP attempt.
	observe.Event(observe.LevelWarn, "daemon_wake_cli_fallback", observe.Fields{
		"reason": "no live opencode port installed",
	})
	if a.api == nil {
		receipt.Outcome = wake.T210Errored
		return receipt, errors.New("daemon: wake API is nil")
	}
	if _, err := a.api.Prompt(context.Background(), string(target), p.VerdictRef, wake.T202DeliverySteer); err != nil {
		receipt.Outcome = wake.T210Errored
		return receipt, fmt.Errorf("daemon: CLI fallback: %w", err)
	}
	receipt.Outcome = wake.T210Delivered
	return receipt, nil
}

// daemonWakeBackend implements orchestrator.WakeBackend over the wake bridge.
type daemonWakeBackend struct {
	adapters *wake.T211AdapterRegistry
}

var _ orchestrator.WakeBackend = (*daemonWakeBackend)(nil)

func newDaemonWakeBackend() *daemonWakeBackend {
	adapters := wake.T211NewAdapterRegistry()
	adapters.Register(wake.AdapterOpenCode, &daemonWakeAdapter{api: &wake.T202API{}})
	return &daemonWakeBackend{adapters: adapters}
}

func (b *daemonWakeBackend) Deliver(ctx context.Context, p orchestrator.WakePointer) (orchestrator.WakeReceipt, bool) {
	r, ok := wake.T210Deliver(ctx, wake.T210WakePointer{
		Adapter:      p.Adapter,
		Target:       p.Target,
		WatchID:      p.WatchID,
		ObligationID: p.ObligationID,
		Revision:     p.Revision,
		VerdictRef:   p.VerdictRef,
		Attempt:      p.Attempt,
		Delivery:     p.Delivery,
	}, nil, nil, b.adapters, nil)
	return orchestrator.WakeReceipt{
		ObligationID: r.ObligationID,
		Attempt:      r.Attempt,
		Delivery:     r.Delivery,
		Outcome:      r.Outcome,
	}, ok
}
