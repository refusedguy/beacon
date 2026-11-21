package wake

import (
	"context"
	"errors"
	"fmt"
)

// CommandRunner runs one command and returns its stdout. The wake layer speaks
// tmux subcommands and arguments; the runner owns process execution. Tests pass
// a fake runner, so no tmux binary is needed (ADR 0026).
type CommandRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

var (
	// ErrNoPaneRunner means a capture or paste was attempted without a runner.
	ErrNoPaneRunner = errors.New("wake: no pane command runner")
	// ErrNoPaneTarget means the tmux pane target was empty.
	ErrNoPaneTarget = errors.New("wake: no pane target")
)

// CapturePane reads the visible contents of a tmux pane with `capture-pane -p`.
// The result is a snapshot of the pane, not a full pipe-pane stream.
func CapturePane(ctx context.Context, runner CommandRunner, target string) (string, error) {
	if runner == nil {
		return "", ErrNoPaneRunner
	}
	if target == "" {
		return "", ErrNoPaneTarget
	}
	out, err := runner.Run(ctx, "capture-pane", "-p", "-t", target)
	if err != nil {
		return "", fmt.Errorf("wake: capture-pane %q: %w", target, err)
	}
	return string(out), nil
}

// PasteTwoStep inserts text into a pane in two separate steps. The first step
// types the text literally without submitting it. The second step sends Enter
// on its own. A multiline message stays in one argument and never reaches a
// shell, and a pane change between the two steps cannot turn the text into a
// command.
func PasteTwoStep(ctx context.Context, runner CommandRunner, target, text string) error {
	if runner == nil {
		return ErrNoPaneRunner
	}
	if target == "" {
		return ErrNoPaneTarget
	}
	if _, err := runner.Run(ctx, "send-keys", "-t", target, "-l", "--", text); err != nil {
		return fmt.Errorf("wake: send-keys text to %q: %w", target, err)
	}
	return SubmitOnly(ctx, runner, target)
}

// SubmitOnly sends Enter as a standalone step. It never carries prompt text.
func SubmitOnly(ctx context.Context, runner CommandRunner, target string) error {
	if runner == nil {
		return ErrNoPaneRunner
	}
	if target == "" {
		return ErrNoPaneTarget
	}
	if _, err := runner.Run(ctx, "send-keys", "-t", target, "Enter"); err != nil {
		return fmt.Errorf("wake: send-keys Enter to %q: %w", target, err)
	}
	return nil
}
