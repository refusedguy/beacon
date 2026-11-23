package domain

import (
	"errors"
	"syscall"
)

// ProcessAlive reports whether pid names a process that currently exists.
// Signal 0 checks existence without delivering a signal, so this neither
// enumerates processes nor starts ps (ADR 0036 §4). EPERM still means the
// process exists but belongs to another user.
//
// The check answers existence only, not identity. It cannot tell a live process
// from a new one that reused the PID after the original exited (ATX-14).
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
