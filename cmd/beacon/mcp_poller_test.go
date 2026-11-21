package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// withPollerAlive installs a liveness seam for one test. The real signal-0
// check cannot be driven from a test without spawning a process.
func withPollerAlive(t *testing.T, alive func(int) bool) {
	t.Helper()
	previous := pollerProcessAlive
	t.Cleanup(func() { pollerProcessAlive = previous })
	pollerProcessAlive = alive
}

func writePollerLockFile(t *testing.T, dir string, body string) string {
	t.Helper()
	path := filepath.Join(dir, pollerLockName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write poller lock: %v", err)
	}
	return path
}

func TestAcquirePollerLockStartsOnDeadPID(t *testing.T) {
	dir := t.TempDir()
	const stalePID = 999999
	path := writePollerLockFile(t, dir, strconv.Itoa(stalePID)+"\n")
	withPollerAlive(t, func(pid int) bool { return pid != stalePID && pid > 0 })

	release, lock, err := acquirePollerLock(dir)
	if err != nil {
		t.Fatalf("acquire poller lock: %v", err)
	}
	if !lock.owned {
		t.Fatal("stale lock: process did not take ownership of the poller")
	}
	if lock.activePID != os.Getpid() {
		t.Fatalf("activePID = %d, want current pid %d", lock.activePID, os.Getpid())
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read poller lock: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock body = %q, want current pid", got)
	}

	release()
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read released lock: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "0" {
		t.Fatalf("released lock body = %q, want 0", got)
	}
}

func TestAcquirePollerLockSkipsLivePID(t *testing.T) {
	dir := t.TempDir()
	const livePID = 4242
	path := writePollerLockFile(t, dir, strconv.Itoa(livePID)+"\n")
	withPollerAlive(t, func(pid int) bool { return pid == livePID })

	release, lock, err := acquirePollerLock(dir)
	if err != nil {
		t.Fatalf("acquire poller lock: %v", err)
	}
	if release != nil {
		release()
		t.Fatal("live lock: process must not receive a release function")
	}
	if lock.owned {
		t.Fatal("live lock: process must not start a second poller")
	}
	if lock.activePID != livePID {
		t.Fatalf("activePID = %d, want live pid %d", lock.activePID, livePID)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read poller lock: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != strconv.Itoa(livePID) {
		t.Fatalf("live lock body changed to %q", got)
	}
}

func TestAcquirePollerLockStartsOnMalformedPID(t *testing.T) {
	dir := t.TempDir()
	path := writePollerLockFile(t, dir, "not-a-pid\n")

	release, lock, err := acquirePollerLock(dir)
	if err != nil {
		t.Fatalf("acquire poller lock: %v", err)
	}
	if !lock.owned {
		t.Fatal("malformed lock: process did not take ownership of the poller")
	}
	release()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read poller lock: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "0" {
		t.Fatalf("released lock body = %q, want 0", got)
	}
}

func TestWithPollerStatsAddsInspectFields(t *testing.T) {
	previous := pollerMetrics.Poller()
	t.Cleanup(func() { pollerMetrics.SetPoller(previous.Active, previous.PID) })
	pollerMetrics.SetPoller(true, 4242)

	raw := []byte(`{"counter":"call_total","rssBytes":123}` + "\n")
	out := withPollerStats(raw)
	if !json.Valid(out) {
		t.Fatalf("poller stats are not valid JSON: %s", out)
	}
	var fields map[string]any
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatalf("decode poller stats: %v", err)
	}
	if fields["pollerActive"] != true {
		t.Fatalf("pollerActive = %v, want true; output %s", fields["pollerActive"], out)
	}
	if got, ok := fields["pollerPID"].(float64); !ok || int(got) != 4242 {
		t.Fatalf("pollerPID = %v, want 4242; output %s", fields["pollerPID"], out)
	}
	if fields["rssBytes"] != float64(123) {
		t.Fatalf("original metric lost: %s", out)
	}
}
