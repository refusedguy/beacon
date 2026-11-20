package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/refusedguy/beacon/internal/infra/webhook"
)

// webhookSecretEnv is the CLI boundary for the shared secret. The value is
// passed to the receiver as a config field, never logged or printed.
const webhookSecretEnv = "BEACON_WEBHOOK_SECRET"

var webhookRunner = func(c *CLI, args []string) int { return runWebhook(c, args) }

// RegisterWebhook replaces the command implementation in tests.
func RegisterWebhook(fn func(*CLI, []string) int) { webhookRunner = fn }

// runWebhook wires the state store and the secret into the infra receiver.
// Socket handling stays in infra; this function only parses flags and config.
func runWebhook(c *CLI, args []string) int {
	fs := flag.NewFlagSet("webhook", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "адрес локального HTTP-сервера")
	dryRun := fs.Bool("dry-run", false, "проверить подпись и событие без записи в store")
	if !parseFlags("webhook", args, c.errOut, fs) {
		return 2
	}

	secret := os.Getenv(webhookSecretEnv)
	if secret == "" {
		fmt.Fprintf(c.errOut, "beacon webhook: задайте переменную %s\n", webhookSecretEnv)
		return 2
	}

	deps, closeStore, err := openMCPDependencies()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon webhook: %v\n", err)
		return 2
	}
	defer func() { _ = closeStore() }()

	receiver, err := webhook.New(webhook.Config{
		Secret: secret, Store: deps.Store, Clock: deps.Clock, DryRun: *dryRun,
	})
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon webhook: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Fprintf(c.out, "beacon webhook: слушаю http://%s%s (dry-run=%t)\n", *addr, webhook.Path, *dryRun)
	if err := webhook.Serve(ctx, *addr, receiver.Handler()); err != nil {
		fmt.Fprintf(c.errOut, "beacon webhook: %v\n", err)
		return 2
	}
	return 0
}
