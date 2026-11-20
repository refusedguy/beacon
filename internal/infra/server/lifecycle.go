// This file owns the server's two lifecycle pieces. The single-instance lease
// keeps one process alive: the file lock, its pidfile and the liveness check
// that makes a stale pidfile harmless after a crash (BS.1). The Lifecycle type
// binds one listener and drains it on command or context cancellation (S1.001).
// All lease file I/O lives here; the command layer only calls the Acquirer
// port.
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
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

const (
	// HealthPath is the liveness endpoint served by a background server.
	HealthPath = "/healthz"

	// defaultDrainTimeout bounds a graceful stop. In-flight requests get this
	// long to finish before the listener is closed.
	defaultDrainTimeout = 5 * time.Second
)

var (
	// ErrAlreadyStarted reports a second Start on a Lifecycle that is already
	// running or stopping.
	ErrAlreadyStarted = errors.New("server: lifecycle already started")

	// ErrListen reports a failed bind, for example a port already in use.
	ErrListen = errors.New("server: listen failed")
)

// State is the observable state of a Lifecycle.
type State int

const (
	// StateNew means Start has not succeeded yet.
	StateNew State = iota
	// StateRunning means the listener is bound and serving.
	StateRunning
	// StateDraining means Stop is waiting for in-flight requests.
	StateDraining
	// StateStopped means the server has stopped. The state is terminal.
	StateStopped
)

func (s State) String() string {
	switch s {
	case StateNew:
		return "new"
	case StateRunning:
		return "running"
	case StateDraining:
		return "draining"
	case StateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// HealthHandler answers GET /healthz with 200 while the process is alive. It
// has no dependency on the lease, the token or the journal, so a caller can
// mount it alone in tests and in a minimal bootstrap.
func HealthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != HealthPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
}

// Lifecycle owns one background HTTP listener and its shutdown. Start binds
// the address, Stop drains in-flight work, and Run ties both to a context.
// All methods are safe for concurrent use and Stop is idempotent.
type Lifecycle struct {
	addr    string
	handler http.Handler
	drain   time.Duration

	mu       sync.Mutex
	state    State
	listener net.Listener
	httpSrv  *http.Server
	done     chan struct{}
	serveErr error
	stopErr  error
	stopOnce sync.Once
}

// NewLifecycle builds a Lifecycle that binds addr on Start. A nil handler
// serves only the health endpoint. Addr may use port 0 to let the OS choose.
func NewLifecycle(addr string, handler http.Handler) *Lifecycle {
	if handler == nil {
		handler = HealthHandler()
	}
	return &Lifecycle{addr: addr, handler: handler, drain: defaultDrainTimeout}
}

// Addr returns the bound address. Before Start it returns the configured
// address; after Start it returns the resolved address, which matters when the
// configured port is 0.
func (l *Lifecycle) Addr() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.listener == nil {
		return l.addr
	}
	return l.listener.Addr().String()
}

// State returns the current lifecycle state.
func (l *Lifecycle) State() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

// Start binds the listener and begins serving. A second Start fails with
// ErrAlreadyStarted. A bind failure, such as a busy port, fails with ErrListen
// and leaves the state at StateNew so the caller can retry on another port.
func (l *Lifecycle) Start() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != StateNew {
		return fmt.Errorf("%w: state %s", ErrAlreadyStarted, l.state)
	}

	listener, err := net.Listen("tcp", l.addr)
	if err != nil {
		return fmt.Errorf("%w on %s: %w", ErrListen, l.addr, err)
	}
	srv := &http.Server{
		Handler:           l.handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	l.listener = listener
	l.httpSrv = srv
	l.done = make(chan struct{})
	l.state = StateRunning

	go func() {
		err := srv.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			l.mu.Lock()
			l.serveErr = err
			l.mu.Unlock()
		}
		close(l.done)
	}()
	return nil
}

// Stop drains in-flight requests and closes the listener. Repeated calls
// return the same result without doing the work twice. Stop before Start is a
// no-op and leaves the lifecycle startable.
func (l *Lifecycle) Stop() error {
	l.mu.Lock()
	state := l.state
	l.mu.Unlock()
	if state == StateNew {
		return nil
	}

	l.stopOnce.Do(func() {
		l.mu.Lock()
		if l.state == StateStopped {
			l.mu.Unlock()
			return
		}
		srv := l.httpSrv
		done := l.done
		l.state = StateDraining
		l.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), l.drain)
		defer cancel()
		err := srv.Shutdown(ctx)
		if errors.Is(err, context.DeadlineExceeded) {
			err = srv.Close()
		}

		// Shutdown closes the listener, which unblocks Serve. Wait for that
		// goroutine to exit before publishing StateStopped, so the state means
		// the accept loop is gone and not just that shutdown was requested.
		<-done

		l.mu.Lock()
		defer l.mu.Unlock()
		l.state = StateStopped
		if err == nil {
			err = l.serveErr
		}
		l.stopErr = err
	})

	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stopErr
}

// Run starts the lifecycle, serves until ctx is cancelled, then drains.
// A serve error also ends Run. The return value is the stop result.
func (l *Lifecycle) Run(ctx context.Context) error {
	if err := l.Start(); err != nil {
		return err
	}
	l.mu.Lock()
	done := l.done
	l.mu.Unlock()

	select {
	case <-ctx.Done():
		return l.Stop()
	case <-done:
		return l.Stop()
	}
}
