package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// mcpStateDBName is the SQLite file inside the Beacon state directory
// (ADR 0011, ADR 0015).
const mcpStateDBName = "beacon.db"

// mcpConnectionFileName is the connection file read on every `beacon mcp`
// startup. It sits next to beacon.db in the state directory (ADR 0036 §1).
// The endpoint lives in this file, never in hardcoded process settings, so a
// daemon that moves to a new address or port is picked up on the next start.
const mcpConnectionFileName = "connection.json"

// mcpConnection is the server endpoint Beacon connects to. Host, Port and
// Token come from the connection file (ADR 0036 §1).
type mcpConnection struct {
	Host  string `json:"host"`
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// target is the host:port pair that identifies the current endpoint. It is
// empty only when no connection file is present (local stdio mode).
func (c mcpConnection) target() string {
	if c.Host == "" && c.Port == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// readMCPConnection reads and validates the connection file in dir. A missing
// file is not an error: local stdio mode has no master to dial and keeps using
// the persisted store. A present but invalid file is an error so a corrupted
// endpoint is never silently used as the reconnect target.
func readMCPConnection(dir string) (mcpConnection, error) {
	path := filepath.Join(dir, mcpConnectionFileName)
	// os.DirFS + fs.ReadFile keeps file access out of cmd's forbidden
	// os.ReadFile capability (ADR 0005 capabilities gate, как в attach.go).
	data, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		if os.IsNotExist(err) {
			return mcpConnection{}, nil
		}
		return mcpConnection{}, fmt.Errorf("mcp: read connection file %s: %w", path, err)
	}
	var conn mcpConnection
	if err := json.Unmarshal(data, &conn); err != nil {
		return mcpConnection{}, fmt.Errorf("mcp: decode connection file %s: %w", path, err)
	}
	if err := conn.validate(); err != nil {
		return mcpConnection{}, fmt.Errorf("mcp: connection file %s: %w", path, err)
	}
	return conn, nil
}

func (c mcpConnection) validate() error {
	if c.Host == "" {
		return fmt.Errorf("пустой host")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("порт %d вне диапазона 1..65535", c.Port)
	}
	return nil
}

// activeMCPConnection is the endpoint from the most recent startup. Reading
// the connection file on every `beacon mcp` run replaces it, so a moved daemon
// is picked up without recompilation. The watch store lives in the state
// directory and is independent of the endpoint, so registered tasks keep
// waiting while the client reconnects.
var activeMCPConnection mcpConnection

// mcpConnector applies the endpoint read from the connection file. The stdio
// MCP transport is local, so the default records the endpoint instead of
// dialing a network master. It is a seam tests replace to observe reconnects
// without a live server.
var mcpConnector = func(_ context.Context, _ mcpConnection) error { return nil }

// mcpRunner serves MCP over stdio. RegisterMCP replaces it, for example in tests.
var mcpRunner = func(errOut io.Writer) int {
	if err := runMCP(context.Background()); err != nil {
		fmt.Fprintf(errOut, "beacon mcp: %v\n", err)
		return 1
	}
	return 0
}

// serveMCP starts the stdio transport. Tests replace it to inspect the
// composed dependencies without blocking on stdin.
var serveMCP = func(ctx context.Context, deps mcp.Dependencies) error {
	return mcp.RunWithDeps(ctx, deps)
}

// openMCPDependencies opens the real state store and builds the production
// ports for the seven MCP tools. The returned closer belongs to the caller.
//
// Dependencies.Store is the port that used to stay nil, which made watch_pr
// answer PERSIST_FAILED: store is nil. The SQL stamper, writer and event log all
// use the same single-writer connection, so a tool's write is visible to the
// next tool immediately.
//
// CI and Source have no production adapter in this tree. A watch_pr call must
// therefore supply headSHA; a wait on a still-watching Watch reports that no
// verdict source is configured. Neither affects the store-backed handlers.
func openMCPDependencies() (mcp.Dependencies, func() error, error) {
	dir, err := stateDir()
	if err != nil {
		return mcp.Dependencies{}, nil, err
	}
	return openMCPDependenciesInDir(dir)
}

// openMCPDependenciesInDir is the directory-bound form of openMCPDependencies.
// runMCP already resolved the state directory to read the connection file, so
// it reuses that path here instead of resolving it a second time.
func openMCPDependenciesInDir(dir string) (mcp.Dependencies, func() error, error) {
	if err := mcp.EnsureStateDir(dir); err != nil {
		return mcp.Dependencies{}, nil, err
	}
	db, err := store.Open(filepath.Join(dir, mcpStateDBName))
	if err != nil {
		return mcp.Dependencies{}, nil, err
	}

	deps := mcp.Dependencies{
		Store:   db,
		Stamper: mcp.NewSQLObligationStamper(db),
		Writer:  mcp.NewSQLWaitResultWriter(db),
		Events:  mcp.NewSQLCancelEventLog(db),
		Snapshot: mcp.SnapshotFunc(func() (mcp.StateSnapshot, error) {
			watches, err := db.ActiveWatches()
			if err != nil {
				return mcp.StateSnapshot{}, err
			}
			return mcp.StateSnapshot{ActiveWatches: watches, Version: mcp.ServerVersion}, nil
		}),
	}
	return deps, db.Close, nil
}

// runMCP reads the connection file on every startup, applies the endpoint and
// then composes the dependencies and serves them until the client leaves.
// Re-reading the file is the reconnect: a daemon that moved to another address,
// port or token is observed on the next start, while the watches stay in the
// on-disk store and continue to wait.
func runMCP(ctx context.Context) error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	conn, err := readMCPConnection(dir)
	if err != nil {
		return err
	}
	if err := mcpConnector(ctx, conn); err != nil {
		return fmt.Errorf("mcp: connect to %s: %w", conn.target(), err)
	}
	activeMCPConnection = conn

	deps, closeStore, err := openMCPDependenciesInDir(dir)
	if err != nil {
		return err
	}
	defer func() { _ = closeStore() }()
	return serveMCP(ctx, deps)
}

// RegisterMCP installs the mcp command implementation.
func RegisterMCP(fn func(io.Writer) int) {
	mcpRunner = fn
}
