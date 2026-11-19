package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// mcpStateDBName is the SQLite file inside the Beacon state directory
// (ADR 0011, ADR 0015).
const mcpStateDBName = "beacon.db"

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

// runMCP composes the dependencies and serves them until the client leaves.
func runMCP(ctx context.Context) error {
	deps, closeStore, err := openMCPDependencies()
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
