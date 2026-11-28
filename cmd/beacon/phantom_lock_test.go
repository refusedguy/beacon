package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStaleDaemonLockIsReacquired(t *testing.T) {
	dir := t.TempDir()
	stale := daemonState{
		PID:       99999999,
		StartedAt: time.Now().UTC().Add(-time.Hour),
		Heartbeat: time.Now().UTC().Add(-time.Hour),
	}
	writeStateFile(t, filepath.Join(dir, daemonStateFile), stale)

	first, err := acquireLock(dir)
	if err != nil {
		t.Fatalf("first acquireLock: %v", err)
	}
	// The kernel releases the flock when its owner exits. A stale daemon
	// record does not hold the lock and must not block a new owner.
	first.Close()
	second, err := acquireLock(dir)
	if err != nil {
		t.Fatalf("second acquireLock after stale owner exits: %v", err)
	}
	defer second.Close()
	if _, err := os.Stat(filepath.Join(dir, "beacon.lock")); err != nil {
		t.Fatalf("lock file missing after takeover: %v", err)
	}
}
