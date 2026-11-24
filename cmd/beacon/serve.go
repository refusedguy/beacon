package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/refusedguy/beacon/internal/infra/scm"
	"github.com/refusedguy/beacon/pkg/core"
)

// idleWatcher is an alias kept for config_watch.go. New code uses the port.
type idleWatcher = core.IdleWatcher

func newServeCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the reconciler, dispatcher queue, webhook and loopback API",
		RunE: func(cmd *cobra.Command, _ []string) error {
			parent := os.Getenv("BEACON_PARENT_SESSION")
			w, err := wire(*configPath, parent)
			if err != nil {
				return err
			}
			defer w.close()
			dir := filepath.Dir(w.cfg.StateDB)
			lock, err := acquireLock(dir)
			if err != nil {
				return err
			}
			defer lock.Close()
			tracker, err := startDaemonState(dir)
			if err != nil {
				return err
			}
			defer tracker.Close()
			addr := os.Getenv("BEACON_API_ADDR")
			if addr == "" {
				addr = "127.0.0.1:7432"
			}
			token, err := writeConnection(dir, "http://"+addr)
			if err != nil {
				return err
			}
			defer removeConnection(dir)

			ctx, stop := signalContext(cmd.Context())
			defer stop()
			if watcher, ok := w.harness.(core.IdleWatcher); ok {
				go watcher.WatchIdle(ctx)
			}
			events := fanout(ctx, w.harness.Events(), 2)
			w.recon.Events = events[0]

			errCh := make(chan error, 3)
			go func() { errCh <- w.recon.Run(ctx, w.changes) }()

			api := &apiServer{wiring: w, token: token, eventStream: events[1]}
			srv := &http.Server{Addr: addr, Handler: api.handler(), ReadHeaderTimeout: 5 * time.Second}
			go func() {
				<-ctx.Done()
				shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdown)
			}()
			go func() { errCh <- srv.ListenAndServe() }()

			if w.cfg.Webhook.Enabled {
				hookAddr := w.cfg.Webhook.Addr
				if hookAddr == "" {
					hookAddr = "127.0.0.1:8766"
				}
				mux := http.NewServeMux()
				mux.Handle("/webhook", (&scm.Webhook{Secret: w.cfg.Webhook.Secret, Changes: w.changes}).Handler())
				hook := &http.Server{Addr: hookAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
				go func() { errCh <- hook.ListenAndServe() }()
			}

			select {
			case <-ctx.Done():
				return nil
			case err := <-errCh:
				if err == http.ErrServerClosed || err == context.Canceled {
					return nil
				}
				writeDaemonError(dir, "serve", err)
				return err
			}
		},
	}
}

func fanout(ctx context.Context, src <-chan core.HarnessEvent, n int) []<-chan core.HarnessEvent {
	outs := make([]chan core.HarnessEvent, n)
	views := make([]<-chan core.HarnessEvent, n)
	for i := range outs {
		outs[i] = make(chan core.HarnessEvent, 16)
		views[i] = outs[i]
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-src:
				for _, out := range outs {
					select {
					case out <- ev:
					default:
					}
				}
			}
		}
	}()
	return views
}
