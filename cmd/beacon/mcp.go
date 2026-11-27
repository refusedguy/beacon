package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	beaconmcp "github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/pkg/core"
)

func newMCPCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve six worker and read-only MCP tools plus four orchestrator tools over stdio",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			parent := parentSessionFromEnv()
			w, err := wire(*configPath, parent)
			if err != nil {
				return err
			}
			defer w.close()
			go w.watcher.Run(ctx)

			services := mcpServices(w)
			dir := stateDir(w.cfg)
			if info, ok := liveDaemon(w.cfg); ok {
				client := &remoteClient{info: info, http: bearerClient(5 * time.Second)}
				services.Remote = client.call
			} else if lock, err := acquireLock(dir); err != nil {
				// A second daemon with no live owner cannot take the lock. Fail
				// rather than serve tools without a reconciler, which would
				// freeze active runs while looking healthy.
				writeDaemonError(dir, "lock", err)
				return fmt.Errorf("acquire daemon lock: %w", err)
			} else {
				defer lock.Close()
				slog.Info("daemon acquired reconciler lock", "pid", os.Getpid())
				defer slog.Info("daemon released reconciler lock", "pid", os.Getpid())
				tracker, stateErr := startDaemonState(dir)
				if stateErr != nil {
					writeDaemonError(dir, "state", stateErr)
					return fmt.Errorf("write daemon state: %w", stateErr)
				}
				defer tracker.Close()
				if watcher, ok := w.harness.(core.IdleWatcher); ok {
					go watcher.WatchIdle(ctx)
				}
				go func() {
					err := w.recon.Run(ctx, w.changes)
					if err != nil && !errors.Is(err, context.Canceled) {
						slog.Error("reconciler loop stopped", "error", err)
						writeDaemonError(dir, "reconciler", err)
					} else {
						slog.Info("reconciler loop exited", "error", err)
					}
				}()
			}
			return beaconmcp.ServeStdio(beaconmcp.NewServer(services))
		},
	}
}

func mcpServices(w *wiring) beaconmcp.Services {
	return beaconmcp.Services{
		Dispatcher: w.dispatcher, Control: w.control, Status: w.status,
		Wait: w.wait, TaskCreator: w.taskCreator,
		TaskDeleter: w.taskDeleter, TaskUpdater: w.taskUpdater,
		Reconcile: w.reconcile,
	}
}

type remoteClient struct {
	info *connectionInfo
	http *http.Client
}

func (c *remoteClient) call(ctx context.Context, action, runID string, pr int, sha string) error {
	payload, err := json.Marshal(controlRequest{
		Action: action, RunID: runID, SHA: sha, PR: pr,
		Message: beaconmcp.ControlMessage(ctx),
	})
	if err != nil {
		return err
	}
	return postControlJSON(ctx, c.info, payload, c.http)
}

func daemonAlive(info *connectionInfo) bool {
	if info == nil || info.Addr == "" {
		return false
	}
	addr := strings.TrimPrefix(info.Addr, "http://")
	if addr == info.Addr {
		return false
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, portOf(addr)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "7432"
	}
	return port
}
