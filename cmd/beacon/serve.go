package main

import (
	"context"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/internal/infra/scm"
	"github.com/refusedguy/beacon/pkg/core"
)

// refuseStaleBinary stops a daemon or MCP process whose binary no longer
// matches the sources before it can take the lock, bind a port or serve tools.
// A binary built by go run has no stamped hash and passes the check.
func refuseStaleBinary() error {
	root, err := projectRoot()
	if err != nil {
		return err
	}
	return buildHashMismatch(root)
}

func newServeCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the reconciler, dispatcher queue, webhook and loopback API",
		Long: `serve is the long-lived daemon. It writes the loopback address, a fresh token and its PID to .beacon/connection.json.
Set BEACON_API_ADDR to change the listen address; it defaults to 127.0.0.1:7432. BEACON_API_ADDR is the only way to set it.
Clients read the current token from that file after each restart.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := refuseStaleBinary(); err != nil {
				return err
			}
			parent := parentSessionFromEnv()
			w, err := wire(*configPath, parent)
			if err != nil {
				return err
			}
			defer w.close()
			if h, ok := w.harness.(*reloadableHarness); ok {
				defer h.stop()
			}
			if err := config.ValidateWebhook(w.cfg.Webhook); err != nil {
				return err
			}
			dir := stateDir(w.cfg)
			lock, err := acquireLock(dir)
			if err != nil {
				// A losing opener leaves its own process-keyed diagnostic. The
				// winner clears only its key, so this record survives startup.
				writeDaemonError(dir, "lock", err)
				return err
			}
			defer lock.Close()
			tracker, err := startDaemonState(dir)
			if err != nil {
				return err
			}
			defer tracker.Close()
			addr := envString("BEACON_API_ADDR", "127.0.0.1:7432")
			token, err := writeConnection(dir, "http://"+addr)
			if err != nil {
				return err
			}
			defer removeConnection(dir)

			ctx, stop := signalContext(cmd.Context())
			defer stop()
			go w.watcher.Run(ctx)
			if watcher, ok := w.harness.(core.IdleWatcher); ok {
				go watcher.WatchIdle(ctx)
			}
			events := fanout(ctx, w.harness.Events(), 2)
			w.recon.Events = events[0]

			// The reconciler and the API previously read w.changes directly,
			// so a single PR change went to whichever one was selected first.
			// Give each its own view, then let the API fan changes out to every
			// connected monitor.
			changeViews := fanout(ctx, w.changes, 2)
			subs := newChangeSubscribers()
			go subs.run(ctx, changeViews[1])

			errCh := make(chan error, 3)
			go func() { errCh <- w.recon.Run(ctx, changeViews[0]) }()

			startAPIServer(ctx, apiServices{status: w.status, control: w.control}, token, events[1], subs, addr, errCh)

			if w.cfg.Webhook.Enabled {
				hookAddr := w.cfg.Webhook.Addr
				if hookAddr == "" {
					hookAddr = "127.0.0.1:8766"
				}
				mux := http.NewServeMux()
				mux.Handle("/webhook", (&scm.Webhook{Secret: w.cfg.Webhook.Secret, Changes: w.changes}).Handler())
				runHTTP(ctx, errCh, hookAddr, mux)
			}

			select {
			case <-ctx.Done():
				return nil
			case err := <-errCh:
				if err == nil || err == http.ErrServerClosed || err == context.Canceled {
					return nil
				}
				writeDaemonError(dir, "serve", err)
				return err
			}
		},
	}
}

// fanout copies every value from src into n buffered views. A slow view drops
// the value instead of blocking the others. It works for harness events and
// for PR change numbers alike.
func fanout[T any](ctx context.Context, src <-chan T, n int) []<-chan T {
	outs := make([]chan T, n)
	views := make([]<-chan T, n)
	for i := range outs {
		outs[i] = make(chan T, 16)
		views[i] = outs[i]
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case v, ok := <-src:
				if !ok {
					return
				}
				for _, out := range outs {
					select {
					case out <- v:
					default:
					}
				}
			}
		}
	}()
	return views
}
