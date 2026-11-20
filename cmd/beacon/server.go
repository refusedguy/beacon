package main

import (
	"fmt"
	"io"

	"github.com/refusedguy/beacon/internal/infra/server"
)

// serverLease is the single-instance lease port. The file locking, pidfile and
// liveness logic live in internal/infra/server; this layer only calls the
// port. Tests replace it to exercise the command without a real state dir.
var serverLease server.Acquirer = server.NewManager()

// serverRunner is the single-instance entry point for beacon-server. It takes
// the machine-wide lease before the MCP server starts and releases it on
// return. A second concurrent start is refused with a clear error (BS.1).
func serverRunner(errOut io.Writer) int {
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

	return mcpRunner(errOut)
}
