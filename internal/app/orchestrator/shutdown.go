// Package orchestrator implements the automatic pass over open pull requests.
//
// This file owns the bounded shutdown drain. A collector that does not confirm
// its flush within the deadline is a failed shutdown, not a clean exit: the
// caller uses the returned error to pick a nonzero exit code and the reason is
// written to the process log before exit.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// DefaultShutdownTimeout bounds how long a process waits for the collector to
// confirm its drain. A collector that never confirms keeps the command alive
// for at most this long.
const DefaultShutdownTimeout = 5 * time.Second

// BoundedShutdown runs drain under a deadline and returns nil only when the
// drain is confirmed. drain flushes pending work before the process exits;
// both observe.Shutdown and the OTLP exporter supply it as a function. A drain
// error or an expired deadline returns a descriptive error so the caller can
// exit nonzero. The reason is logged once through logger when it is not nil.
func BoundedShutdown(logger *slog.Logger, timeout time.Duration, drain func(context.Context) error) error {
	if timeout <= 0 {
		timeout = DefaultShutdownTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- drain(ctx) }()

	select {
	case err := <-done:
		return drainResult(logger, err)
	case <-ctx.Done():
		// A drain that finished at the same instant as the deadline still
		// counts as confirmed. Only report a timeout when nothing completed.
		select {
		case err := <-done:
			return drainResult(logger, err)
		default:
		}
		reason := fmt.Errorf("shutdown drain timed out after %s: %w", timeout, ctx.Err())
		if logger != nil {
			logger.Error("shutdown drain timed out", "timeout", timeout.String(), "error", ctx.Err())
		}
		return reason
	}
}

func drainResult(logger *slog.Logger, err error) error {
	if err == nil {
		return nil
	}
	reason := fmt.Errorf("shutdown drain failed: %w", err)
	if logger != nil {
		logger.Error("shutdown drain failed", "error", err)
	}
	return reason
}
