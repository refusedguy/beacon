package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	beaconmcp "github.com/refusedguy/beacon/internal/infra/mcp"
)

// errNoLiveDaemon records why beacon mcp refuses to serve. Only beacon serve
// owns the reconciler lock and writes the connection record, so a tool process
// without a live daemon has no reconciler to forward control calls to.
var errNoLiveDaemon = errors.New("no live beacon daemon")

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
			services, err := mcpServicesForDaemon(w)
			if err != nil {
				return err
			}
			go w.watcher.Run(ctx)
			return beaconmcp.ServeStdio(beaconmcp.NewServer(services))
		},
	}
}

// mcpServicesForDaemon points MCP control calls at the live daemon. It never
// takes the reconciler lock: beacon serve is the only lock owner. Without a
// daemon it returns no services at all, so no tool can fall back to a local
// write that bypasses the daemon's serialization.
func mcpServicesForDaemon(w *wiring) (beaconmcp.Services, error) {
	info, ok := liveDaemon(w.cfg)
	if !ok {
		writeDaemonError(stateDir(w.cfg), "daemon", errNoLiveDaemon)
		return beaconmcp.Services{}, fmt.Errorf("beacon mcp needs a live daemon: %w", errNoLiveDaemon)
	}
	services := mcpServices(w)
	client := &remoteClient{info: info, http: bearerClient(5 * time.Second)}
	services.Remote = client.call
	return services, nil
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
