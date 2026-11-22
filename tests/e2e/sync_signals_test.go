//go:build e2e

package e2e_test

import (
	"os"
	"strings"
	"testing"
	"time"
)

// signalPollInterval paces polling only. Success comes from the observed
// predicate and the runner deadline, never from an elapsed delay (CCx-18).
const signalPollInterval = 50 * time.Millisecond

// waitForSignal polls ready until the observed state appears, the timeout
// expires, or the runner cancels the test. A fixed sleep cannot make this wait
// pass: only the predicate can.
func waitForSignal(t *testing.T, timeout time.Duration, ready func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	if runnerDeadline, ok := t.Deadline(); ok && runnerDeadline.Before(deadline) {
		deadline = runnerDeadline
	}
	ticker := time.NewTicker(signalPollInterval)
	defer ticker.Stop()
	for {
		if ready() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ticker.C:
		case <-t.Context().Done():
			return false
		}
	}
}

// syncSourceFiles are the e2e sources that must synchronize on observed state
// instead of a fixed delay (CCx-18).
var syncSourceFiles = []string{
	"e2e_test.go",
	"sync_signals_test.go",
}

// TestSyncSourcesHaveNoFixedSleeps keeps fixed delays out of the signal-based
// synchronization sources. A wait ends on an observed predicate or the runner
// deadline, never on an elapsed sleep. The guard first asserts that its own
// pattern matches a known delay, so an empty or broken pattern fails it.
func TestSyncSourcesHaveNoFixedSleeps(t *testing.T) {
	call := strings.Join([]string{"time", "Sleep("}, ".")
	empty := strings.Join([]string{"times", "Sleep("}, ".")
	sample := "x := " + call + " 200 * time.Millisecond)"
	if !strings.Contains(sample, call) || strings.Contains(sample, empty) {
		t.Fatal("guard pattern is empty or does not match a fixed delay")
	}
	for _, name := range syncSourceFiles {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, call) {
				t.Errorf("%s:%d: fixed delay in sync source: %s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
}
