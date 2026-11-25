package harness

import (
	"context"
	"testing"
)

// A CLI command has no session to inspect after it returns, so IsAlive cannot
// confirm that a worker exited. Orphan cleanup closes a run only on a confirmed
// false result, so reporting alive keeps a CLI worker from being killed.
func TestIsAliveNeverTriggersOrphanCleanup(t *testing.T) {
	h := NewCLIExec("true", "")

	for _, sessionID := range []string{"any-session", ""} {
		alive, err := h.IsAlive(context.Background(), sessionID)
		if err != nil {
			t.Fatalf("IsAlive(%q) error = %v", sessionID, err)
		}
		if !alive {
			t.Fatalf("IsAlive(%q) = false; CLI liveness cannot be verified, so the worker must be treated as alive", sessionID)
		}
	}
}
