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
	second, err := acquireLock(dir)
	if err != nil {
		t.Fatalf("second acquireLock after stale owner: %v", err)
	}
	defer second.Close()
	// Both descriptors name the same lock file path; the stale owner no
	// longer blocks a new daemon from taking over.
	if _, err := os.Stat(filepath.Join(dir, "beacon.lock")); err != nil {
		t.Fatalf("lock file missing after takeover: %v", err)
	}
	_ = first
}
