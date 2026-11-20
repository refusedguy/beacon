package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/server"
)

// recordingLease stands in for a held lease and counts releases.
type recordingLease struct {
	releases int
}

func (r *recordingLease) Release() error {
	r.releases++
	return nil
}

// fakeLeaseAcquirer observes which state dir the command uses and can refuse
// the start the way a second server would.
type fakeLeaseAcquirer struct {
	handle *recordingLease
	err    error
	dirs   []string
}

func (f *fakeLeaseAcquirer) Acquire(dir string) (server.LeaseHandle, error) {
	f.dirs = append(f.dirs, dir)
	if f.err != nil {
		return nil, f.err
	}
	return f.handle, nil
}

func stubServerLease(t *testing.T, acquirer server.Acquirer) {
	t.Helper()
	prev := serverLease
	serverLease = acquirer
	t.Cleanup(func() { serverLease = prev })
}

func stubMCPRunner(t *testing.T, code int) {
	t.Helper()
	prev := mcpRunner
	mcpRunner = func(io.Writer) int { return code }
	t.Cleanup(func() { mcpRunner = prev })
}

func TestServerRunnerAcquiresLeaseAndRunsServer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(beaconStateDirEnv, dir)

	lease := &recordingLease{}
	acquirer := &fakeLeaseAcquirer{handle: lease}
	stubServerLease(t, acquirer)
	stubMCPRunner(t, 0)

	if code := serverRunner(io.Discard); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if len(acquirer.dirs) != 1 || acquirer.dirs[0] != dir {
		t.Fatalf("Acquire dirs = %v, want [%s]", acquirer.dirs, dir)
	}
	if lease.releases != 1 {
		t.Fatalf("Release called %d times, want 1", lease.releases)
	}
}

func TestServerRunnerRefusesSecondStartWithClearError(t *testing.T) {
	t.Setenv(beaconStateDirEnv, t.TempDir())

	acquirer := &fakeLeaseAcquirer{err: fmt.Errorf("%w (pid 42)", server.ErrAnotherInstance)}
	stubServerLease(t, acquirer)
	// The MCP server must never start when the lease is refused.
	stubMCPRunner(t, 0)

	var out bytes.Buffer
	if code := serverRunner(&out); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "already running") {
		t.Fatalf("stderr = %q, want a clear refusal", out.String())
	}
	if !strings.Contains(out.String(), "pid 42") {
		t.Fatalf("stderr = %q, want the holder pid", out.String())
	}
}
