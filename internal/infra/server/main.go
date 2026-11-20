// This file holds the background server entry point. It mounts the health
// handler on a Lifecycle and drives it from a context. It stays a library
// function: no command wiring and no fork live here, so one process owns the
// listener (S1.001, ADR 0077).
package server

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Bootstrap is the minimal configuration for a background server.
type Bootstrap struct {
	// Addr is the TCP address to bind. An empty value uses 127.0.0.1:0.
	Addr string
	// Handler serves every path. A nil handler serves only /healthz.
	Handler http.Handler
	// DrainTimeout bounds graceful shutdown. Zero uses the default.
	DrainTimeout time.Duration
}

// RunBackground boots a background server, serves until ctx is cancelled, then
// drains in-flight requests. The caller owns the context, so a signal handler
// or a stop command decides when the server ends.
func RunBackground(ctx context.Context, boot Bootstrap) error {
	addr := boot.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	lc := NewLifecycle(addr, boot.Handler)
	if boot.DrainTimeout > 0 {
		lc.drain = boot.DrainTimeout
	}
	if err := lc.Run(ctx); err != nil {
		return fmt.Errorf("server bootstrap: %w", err)
	}
	return nil
}
