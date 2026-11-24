package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	beaconmcp "github.com/refusedguy/beacon/internal/infra/mcp"
)

func newMCPCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve six worker and read-only MCP tools plus three orchestrator tools over stdio",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			parent := os.Getenv("BEACON_PARENT_SESSION")
			w, err := wire(*configPath, parent)
			if err != nil {
				return err
			}
			defer w.close()
			go w.watcher.Run(ctx)

			services := mcpServices(w)
			dir := filepath.Dir(w.cfg.StateDB)
			info, connErr := readConnection(dir)
			if connErr == nil && daemonAlive(w.cfg.StateDB) {
				client := &remoteClient{info: info, http: &http.Client{Timeout: 5 * time.Second}}
				services.Remote = client.call
			} else if lock, err := acquireLock(dir); err != nil {
				slog.Error("acquire daemon lock", "error", err)
				writeDaemonError(dir, "lock", err)
			} else {
				defer lock.Close()
				tracker, stateErr := startDaemonState(dir)
				if stateErr != nil {
					slog.Error("write daemon state", "error", stateErr)
					writeDaemonError(dir, "state", stateErr)
				} else {
					defer tracker.Close()
				}
				if watcher, ok := w.harness.(idleWatcher); ok {
					go watcher.WatchIdle(ctx)
				}
				go func() {
					if err := w.recon.Run(ctx, w.changes); err != nil && !errors.Is(err, context.Canceled) {
						slog.Error("reconciler loop stopped", "error", err)
						writeDaemonError(dir, "reconciler", err)
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
	payload, err := json.Marshal(map[string]any{
		"action": action, "run_id": runID, "pr": pr, "sha": sha,
	})
	if err != nil {
		return err
	}
	addr := strings.TrimSuffix(c.info.Addr, "/") + "/api/v1/control"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, addr, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.info.Token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("beacon API status %d", resp.StatusCode)
	}
	return nil
}

func daemonAlive(stateDB string) bool {
	info, err := readConnection(filepath.Dir(stateDB))
	if err != nil || info.Addr == "" {
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
