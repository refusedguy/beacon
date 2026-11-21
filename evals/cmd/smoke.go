//go:build eval

// Package smoke holds the startup contract for the eval runner.
//
// Readiness is the MCP initialize handshake that client.Connect performs in
// evals/main.go. A startup check never sleeps: it waits on the handshake and
// on a context deadline. That keeps a slow start separate from a logic
// failure, so raising the timeout is never a logic fix.
package smoke

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

const (
	// DefaultStartTimeout is the budget for evalserver to become ready.
	DefaultStartTimeout = 60 * time.Second
	// DefaultTerminateTimeout is the shutdown budget for the child process.
	DefaultTerminateTimeout = 2 * time.Second

	// EnvStartTimeout overrides the startup budget with a time.ParseDuration value.
	EnvStartTimeout = "BEACON_EVAL_START_TIMEOUT"
	// EnvTerminateTimeout overrides the shutdown budget.
	EnvTerminateTimeout = "BEACON_EVAL_TERMINATE_TIMEOUT"
)

// Duration reads an env override. Empty, invalid and non-positive values keep
// the default, so a bad CI variable cannot shorten or break the startup check.
func Duration(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// Reason is the classified outcome of a startup attempt.
type Reason string

const (
	ReasonOK      Reason = "ok"
	ReasonTimeout Reason = "start_timeout"
	ReasonConnect Reason = "connect_error"
)

// Classify turns a startup error into a distinct reason. A deadline is a slow
// start, not a broken server. Any other error is a connect failure.
func Classify(err error) Reason {
	if err == nil {
		return ReasonOK
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonTimeout
	}
	return ReasonConnect
}

// Result is the outcome of one startup check.
type Result struct {
	Reason  Reason
	Err     error
	Timeout time.Duration
}

// Check runs connect under a deadline and classifies the result. The caller
// supplies a connect function that blocks until the MCP handshake succeeds or
// the context ends. No sleep is used as a readiness signal.
func Check(parent context.Context, timeout time.Duration, connect func(context.Context) error) Result {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	err := connect(ctx)
	return Result{Reason: Classify(err), Err: err, Timeout: timeout}
}

// SelfCheck tests the timeout-opposed classification. It proves a deadline maps
// to start_timeout, a plain error to connect_error and a nil error to ok. The
// eval runner records it so the failure reason is visible in the report.
func SelfCheck() (string, bool) {
	deadline := fmt.Errorf("connect: %w", context.DeadlineExceeded)
	gotTimeout := Classify(deadline)
	gotConnect := Classify(errors.New("dial failed"))
	gotOK := Classify(nil)
	actual := fmt.Sprintf("deadline=%s, error=%s, nil=%s", gotTimeout, gotConnect, gotOK)
	return actual, gotTimeout == ReasonTimeout && gotConnect == ReasonConnect && gotOK == ReasonOK
}
