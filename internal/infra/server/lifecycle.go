// The server lifecycle owns the machine-wide single-instance lease: the file
// lock, its pidfile and the liveness check that makes a stale pidfile harmless
// after a crash. All lease file I/O lives here; the command layer only calls
// the Acquirer port (BS.1).
package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	// LeaseLockName is the advisory lock file inside the Beacon state dir.
	// The kernel releases its flock when the owning process exits, so a crash
	// can never leave a lease held forever.
	LeaseLockName = "beacon-server.lock"

	// LeasePIDName records the pid of the process that currently owns the
	// lease. It names the holder in a refusal message and is never trusted as
	// the source of truth for whether the lease is held: the lock is.
	LeasePIDName = "beacon-server.pid"
)

// ErrAnotherInstance marks a start refused because another beacon-server
// already holds the lease. Callers can wrap it with a human-readable message.
var ErrAnotherInstance = errors.New("another instance is already running")

// LeaseHandle is the releasable returned by an Acquirer. It is an interface so
// callers can test their server lifecycle without touching the filesystem.
type LeaseHandle interface {
	Release() error
}

// Acquirer is the single-instance lease port consumed by the command layer.
type Acquirer interface {
	Acquire(stateDir string) (LeaseHandle, error)
}

// Manager is the production Acquirer. It uses a non-blocking advisory file
// lock in the state dir plus a pidfile; no other process may hold the lock.
type Manager struct{}

// NewManager returns the production lease manager.
func NewManager() Manager { return Manager{} }

// Lease is a held single-instance lease. The zero value is safe to Release:
// it represents "no lock held".
type Lease struct {
	lock    *os.File
	pidPath string
	pid     int
}

// Acquire takes the exclusive non-blocking lock in stateDir and records the
// current process id in the pidfile. A second concurrent start fails with
// ErrAnotherInstance and, when the pidfile names a living process, a message
// naming that process. A stale pidfile left by a dead process does not block:
// the kernel already released the dead owner's lock, so this call succeeds and
// overwrites the pidfile.
func (Manager) Acquire(stateDir string) (LeaseHandle, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("server lease: create state dir: %w", err)
	}

	lockPath := filepath.Join(stateDir, LeaseLockName)
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("server lease: open lock file %s: %w", lockPath, err)
	}

	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, anotherInstanceError(stateDir)
		}
		return nil, fmt.Errorf("server lease: lock %s: %w", lockPath, err)
	}

	l := &Lease{lock: lock, pidPath: filepath.Join(stateDir, LeasePIDName), pid: os.Getpid()}
	if err := l.writePID(); err != nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
		return nil, err
	}
	return l, nil
}

// writePID installs the pidfile atomically while the lease lock is held, so a
// refused starter never observes a half-written pid.
func (l *Lease) writePID() error {
	tmp := l.pidPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(l.pid)), 0o600); err != nil {
		return fmt.Errorf("server lease: write pidfile: %w", err)
	}
	if err := os.Rename(tmp, l.pidPath); err != nil {
		return fmt.Errorf("server lease: install pidfile: %w", err)
	}
	return nil
}

// Release removes the pidfile and drops the lock. It is idempotent. The
// pidfile is removed only when it still names this process, so a late release
// cannot clobber a lease another start has since acquired. On an abnormal exit
// this method never runs, but the kernel releases the flock with the process;
// the surviving pidfile is then stale and Acquire ignores it.
func (l *Lease) Release() error {
	if l == nil || l.lock == nil {
		return nil
	}

	if raw, err := os.ReadFile(l.pidPath); err == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil && pid == l.pid {
			_ = os.Remove(l.pidPath)
		}
	}

	unlockErr := syscall.Flock(int(l.lock.Fd()), syscall.LOCK_UN)
	closeErr := l.lock.Close()
	l.lock = nil
	if unlockErr != nil {
		return fmt.Errorf("server lease: unlock: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("server lease: close lock file: %w", closeErr)
	}
	return nil
}

// anotherInstanceError builds the refusal shown to a second starter. The
// lock is the authority: a held lock means a live owner even if the pidfile is
// missing or names a dead process. The pidfile only improves the message.
func anotherInstanceError(stateDir string) error {
	raw, err := os.ReadFile(filepath.Join(stateDir, LeasePIDName))
	if err == nil {
		pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if perr == nil && processAlive(pid) {
			return fmt.Errorf("%w (pid %d)", ErrAnotherInstance, pid)
		}
	}
	return ErrAnotherInstance
}

// processAlive reports whether pid names a process that currently exists.
// Signal 0 performs no signal delivery, only the existence check. EPERM means
// the process exists but belongs to another user, which still counts as alive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
