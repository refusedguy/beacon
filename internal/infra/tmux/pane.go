// Package tmux runs tmux commands for Beacon. It owns process execution only.
// The wake package decides which commands to send and how to combine them.
package tmux

import (
	"context"
	"os/exec"
)

// Binary is the executable the Runner invokes.
const Binary = "tmux"

// Runner executes `tmux <args>` and returns stdout. It satisfies the
// wake.CommandRunner port by shape, so this package does not import wake and
// the two infra packages stay independent.
type Runner struct {
	// Command replaces exec.CommandContext in tests inside this package.
	Command func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// Run invokes tmux with args and returns its stdout. A nil Command uses
// exec.CommandContext with the caller's context.
func (r Runner) Run(ctx context.Context, args ...string) ([]byte, error) {
	newCmd := r.Command
	if newCmd == nil {
		newCmd = exec.CommandContext
	}
	return newCmd(ctx, Binary, args...).Output()
}
