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

// connectionInfo is the daemon's loopback address, auth token and process id.
type connectionInfo struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

// acquireLock takes an exclusive file lock in dir, failing when another daemon holds it.
func acquireLock(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "beacon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
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
	data, err := json.Marshal(info)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "connection.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return token, nil
}

// readConnection loads the daemon's loopback connection record from dir.
func readConnection(dir string) (*connectionInfo, error) {
	b, err := os.ReadFile(filepath.Join(dir, "connection.json"))
	if err != nil {
		return nil, err
	}
	var info connectionInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// removeConnection deletes the connection record on shutdown.
func removeConnection(dir string) {
	path := filepath.Join(dir, "connection.json")
	// Shutdown cleanup is best effort: the lock is already released and the
	// next start overwrites a stale record.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Error("remove connection record", "path", path, "error", err)
	}
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
	if err := os.Remove(filepath.Join(dir, daemonErrorFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Error("clear daemon error record", "dir", dir, "error", err)
	}
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
	return writeJSONAtomic(filepath.Join(t.dir, daemonStateFile), state)
}

// write persists the current daemon state atomically.
func (t *daemonTracker) write() error {
	t.mu.Lock()
	state := t.state
	t.mu.Unlock()
	return writeJSONAtomic(filepath.Join(t.dir, daemonStateFile), state)
}

// Close stops the heartbeat loop and removes the state file.
func (t *daemonTracker) Close() {
	close(t.stop)
	<-t.done
	path := filepath.Join(t.dir, daemonStateFile)
	// Best effort shutdown cleanup: a leftover state file is replaced on the
	// next start and the heartbeat loop has already stopped.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Error("remove daemon state", "path", path, "error", err)
	}
}

// writeDaemonError persists a fatal error record for the doctor command to read.
func writeDaemonError(dir, kind string, err error) {
	record := daemonError{At: time.Now().UTC(), Kind: kind, Message: err.Error()}
	if writeErr := writeJSONAtomic(filepath.Join(dir, daemonErrorFile), record); writeErr != nil {
		slog.Error("write daemon error record", "error", writeErr)
	}
}

// readDaemonStatus reconstructs daemon liveness from the state and error files.
func readDaemonStatus(dir string) daemonStatus {
	status := daemonStatus{State: "absent"}
	data, err := os.ReadFile(filepath.Join(dir, daemonStateFile))
	if err == nil {
		var state daemonState
		if json.Unmarshal(data, &state) != nil || state.PID <= 0 {
			status.State = "invalid"
		} else {
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
		}
	} else if !os.IsNotExist(err) {
		status.State = "unreadable"
	}
	if data, err := os.ReadFile(filepath.Join(dir, daemonErrorFile)); err == nil {
		var record daemonError
		if json.Unmarshal(data, &record) == nil {
			status.Error = &record
		}
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

// writeJSONAtomic encodes v to a temp file and renames it into place.
func writeJSONAtomic(path string, v any) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".beacon-state-*")
	if err != nil {
		return err
	}
	defer func() {
		// The rename consumes the temp file. A leftover on an earlier failure is
		// never read and uses a fresh name next time, so removal stays best effort.
		if err := os.Remove(f.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Error("remove temp state", "path", f.Name(), "error", err)
		}
	}()
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
