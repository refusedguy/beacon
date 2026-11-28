package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// errInvalidState marks a state file that exists but does not decode, so a
// caller can tell a malformed record from a read failure.
var errInvalidState = errors.New("invalid state file")

// stateAccessor is a typed JSON state file bound to a directory and file name.
type stateAccessor[T any] struct {
	dir  string
	name string
}

// stateFile binds a typed state record to dir and name, leaving both visible at
// the call site.
func stateFile[T any](dir, name string) stateAccessor[T] {
	return stateAccessor[T]{dir: dir, name: name}
}

func (s stateAccessor[T]) path() string {
	return filepath.Join(s.dir, s.name)
}

// load reads and decodes the state file.
func (s stateAccessor[T]) load() (*T, error) {
	data, err := os.ReadFile(s.path())
	if err != nil {
		return nil, err
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, errors.Join(errInvalidState, err)
	}
	return &v, nil
}

// save encodes v to a temp file and renames it into place with mode 0600.
func (s stateAccessor[T]) save(v T) error {
	path := s.path()
	f, err := os.CreateTemp(filepath.Dir(path), ".beacon-state-*")
	if err != nil {
		return err
	}
	defer removeIfExists(f.Name(), true)
	if err := json.NewEncoder(f).Encode(v); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Chmod(0o600); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// clear removes the state file, treating a missing file as success.
func (s stateAccessor[T]) clear() {
	removeIfExists(s.path(), true)
}

// removeIfExists removes path and logs advisory errors when log is true. A
// missing file is always a successful no-op.
func removeIfExists(path string, log bool) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && log {
		slog.Error("remove state file", "path", path, "error", err)
	}
}

// connectionInfo is the daemon's loopback address, auth token and process id.
type connectionInfo struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

const connectionFile = "connection.json"

// acquireLock takes the exclusive file lock in dir. Contention means another
// process owns the reconciler, so the caller must fail. The kernel releases
// the flock when its owner exits; the lock file is never unlinked or replaced.
func acquireLock(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "beacon.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Keep the lock conflict primary and surface a close failure too.
		return nil, fmt.Errorf("beacon is already running: %w", errors.Join(err, f.Close()))
	}
	return f, nil
}

// writeConnection records the loopback address and a fresh token, returning the token.
func writeConnection(dir, addr string) (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b[:])
	info := connectionInfo{Addr: addr, Token: token, PID: os.Getpid()}
	if err := stateFile[connectionInfo](dir, connectionFile).save(info); err != nil {
		return "", err
	}
	return token, nil
}

// readConnection loads the daemon's loopback connection record from dir.
func readConnection(dir string) (*connectionInfo, error) {
	return stateFile[connectionInfo](dir, connectionFile).load()
}

// removeConnection deletes the connection record on shutdown.
func removeConnection(dir string) {
	// Shutdown cleanup is best effort: the lock is already released and the
	// next start overwrites a stale record.
	stateFile[connectionInfo](dir, connectionFile).clear()
}

// daemonStateFile and daemonErrorFile name the daemon status records; heartbeatEvery and staleAfter set heartbeat cadence and freshness.
const (
	daemonStateFile = "daemon.json"
	daemonErrorFile = "daemon-error.json"
	heartbeatEvery  = 5 * time.Second
	staleAfter      = 30 * time.Second
)

// daemonState is the on-disk heartbeat record written by a running daemon.
type daemonState struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Root      string    `json:"root"`
	Heartbeat time.Time `json:"heartbeat"`
}

// daemonError is the on-disk record of the daemon's most recent fatal error.
type daemonError struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}

// daemonStatus is the liveness report built from the state and error records.
type daemonStatus struct {
	State              string       `json:"state"`
	PID                int          `json:"pid,omitempty"`
	StartedAt          *time.Time   `json:"started_at,omitempty"`
	Root               string       `json:"root,omitempty"`
	Heartbeat          *time.Time   `json:"heartbeat,omitempty"`
	HeartbeatAgeSecond float64      `json:"heartbeat_age_seconds"`
	Error              *daemonError `json:"error,omitempty"`
}

// daemonTracker writes heartbeats until closed and owns the state file lifecycle.
type daemonTracker struct {
	mu    sync.Mutex
	dir   string
	state daemonState
	stop  chan struct{}
	done  chan struct{}
}

// startDaemonState writes the initial state and starts the heartbeat loop.
func startDaemonState(dir string) (*daemonTracker, error) {
	root, err := filepath.Abs(filepath.Dir(dir))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	t := &daemonTracker{
		dir:   dir,
		state: daemonState{PID: os.Getpid(), StartedAt: now, Root: root, Heartbeat: now},
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	if err := t.write(); err != nil {
		return nil, err
	}
	// A stale error record is cleared best effort; the first new error replaces it.
	stateFile[daemonError](dir, daemonErrorFile).clear()
	go t.loop()
	return t, nil
}

// loop refreshes the heartbeat on a ticker until Close stops it.
func (t *daemonTracker) loop() {
	defer close(t.done)
	tick := time.NewTicker(heartbeatEvery)
	defer tick.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-tick.C:
			if err := t.refresh(); err != nil {
				slog.Error("refresh daemon heartbeat", "error", err)
			}
		}
	}
}

// refresh updates the heartbeat timestamp and writes the state atomically.
func (t *daemonTracker) refresh() error {
	t.mu.Lock()
	t.state.Heartbeat = time.Now().UTC()
	state := t.state
	t.mu.Unlock()
	return stateFile[daemonState](t.dir, daemonStateFile).save(state)
}

// write persists the current daemon state atomically.
func (t *daemonTracker) write() error {
	t.mu.Lock()
	state := t.state
	t.mu.Unlock()
	return stateFile[daemonState](t.dir, daemonStateFile).save(state)
}

// Close stops the heartbeat loop and removes the state file.
func (t *daemonTracker) Close() {
	close(t.stop)
	<-t.done
	// Best effort shutdown cleanup: a leftover state file is replaced on the
	// next start and the heartbeat loop has already stopped.
	stateFile[daemonState](t.dir, daemonStateFile).clear()
}

// writeDaemonError persists a fatal error record for the doctor command to read.
func writeDaemonError(dir, kind string, err error) {
	record := daemonError{At: time.Now().UTC(), Kind: kind, Message: err.Error()}
	if writeErr := stateFile[daemonError](dir, daemonErrorFile).save(record); writeErr != nil {
		slog.Error("write daemon error record", "error", writeErr)
	}
}

// readDaemonStatus reconstructs daemon liveness from the state and error files.
func readDaemonStatus(dir string) daemonStatus {
	status := daemonStatus{State: "absent"}
	state, err := stateFile[daemonState](dir, daemonStateFile).load()
	switch {
	case err == nil && state.PID > 0:
		status.PID = state.PID
		status.StartedAt = &state.StartedAt
		status.Root = state.Root
		status.Heartbeat = &state.Heartbeat
		age := time.Since(state.Heartbeat)
		if age < 0 {
			age = 0
		}
		status.HeartbeatAgeSecond = age.Round(time.Millisecond).Seconds()
		switch {
		case !pidAlive(state.PID):
			status.State = "dead"
		case age > staleAfter:
			status.State = "stale"
		default:
			status.State = "alive"
		}
	case err == nil || errors.Is(err, errInvalidState):
		status.State = "invalid"
	case !os.IsNotExist(err):
		status.State = "unreadable"
	}
	if record, err := stateFile[daemonError](dir, daemonErrorFile).load(); err == nil {
		status.Error = record
	}
	return status
}

// pidAlive reports whether a process with pid exists or can be signaled.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
