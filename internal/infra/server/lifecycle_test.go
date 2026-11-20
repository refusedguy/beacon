package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// pidFile reads the pidfile written by the holder of the lease.
func pidFile(t *testing.T, stateDir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateDir, LeasePIDName))
	if err != nil {
		t.Fatalf("read pidfile: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

// deadPID returns a pid that does not name a living process. The scan keeps
// the test independent of subprocesses and of the host's pid space layout.
func deadPID(t *testing.T) int {
	t.Helper()
	for pid := 1_000_000; pid < 2_000_000_000; pid++ {
		if !processAlive(pid) {
			return pid
		}
	}
	t.Fatal("no dead pid found")
	return 0
}

func TestLeaseFirstStartSucceeds(t *testing.T) {
	stateDir := t.TempDir()
	m := NewManager()

	lease, err := m.Acquire(stateDir)
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	if lease == nil {
		t.Fatal("first start returned a nil lease")
	}

	if got, want := pidFile(t, stateDir), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("pidfile = %q, want %q", got, want)
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, LeasePIDName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pidfile still present after release: %v", err)
	}
	// A released lease can be taken again: the lock is truly gone.
	next, err := m.Acquire(stateDir)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	_ = next.Release()
}

func TestLeaseSecondStartRefusedClearly(t *testing.T) {
	stateDir := t.TempDir()
	m := NewManager()

	first, err := m.Acquire(stateDir)
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	defer func() { _ = first.Release() }()

	second, err := m.Acquire(stateDir)
	if second != nil {
		_ = second.Release()
		t.Fatal("second start must not receive a lease")
	}
	if !errors.Is(err, ErrAnotherInstance) {
		t.Fatalf("second start error = %v, want ErrAnotherInstance", err)
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second start error is not clear: %q", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Fatalf("second start error does not name the holder: %q", err)
	}
}

func TestLeaseStalePIDDoesNotBlock(t *testing.T) {
	stateDir := t.TempDir()
	stale := deadPID(t)

	// Model an abnormal exit: a pidfile naming a process that no longer
	// exists, with no live lock holder. The stale pid must not block a start.
	if err := os.WriteFile(filepath.Join(stateDir, LeasePIDName), []byte(strconv.Itoa(stale)), 0o600); err != nil {
		t.Fatalf("seed stale pidfile: %v", err)
	}

	lease, err := NewManager().Acquire(stateDir)
	if err != nil {
		t.Fatalf("stale pid blocked the start: %v", err)
	}
	defer func() { _ = lease.Release() }()

	if got, want := pidFile(t, stateDir), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("pidfile = %q after reacquire, want %q", got, want)
	}
}

func TestLeaseConcurrentStartsOnlyOneWins(t *testing.T) {
	stateDir := t.TempDir()
	m := NewManager()

	const starters = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := make([]LeaseHandle, 0, 1)
	errs := make([]error, 0, starters-1)

	for i := 0; i < starters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := m.Acquire(stateDir)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			winners = append(winners, lease)
		}()
	}
	wg.Wait()

	if len(winners) != 1 {
		t.Fatalf("concurrent starts produced %d leases, want 1", len(winners))
	}
	for _, lease := range winners {
		_ = lease.Release()
	}
	for _, err := range errs {
		if !errors.Is(err, ErrAnotherInstance) {
			t.Fatalf("refused starter returned %v, want ErrAnotherInstance", err)
		}
	}
}

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("current process must be alive")
	}
	if processAlive(deadPID(t)) {
		t.Fatal("dead pid reported as alive")
	}
	if processAlive(0) {
		t.Fatal("non-positive pid reported as alive")
	}
}

func TestLifecycleStartServesHealth(t *testing.T) {
	lc := NewLifecycle("127.0.0.1:0", nil)
	if got := lc.State(); got != StateNew {
		t.Fatalf("state before start = %s, want new", got)
	}
	if err := lc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = lc.Stop() }()
	if got := lc.State(); got != StateRunning {
		t.Fatalf("state after start = %s, want running", got)
	}

	addr := lc.Addr()
	if addr == "" || addr == "127.0.0.1:0" {
		t.Fatalf("addr not resolved after start: %q", addr)
	}
	resp, err := http.Get("http://" + addr + HealthPath)
	if err != nil {
		t.Fatalf("GET healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}
}

func TestLifecycleStopIsIdempotent(t *testing.T) {
	lc := NewLifecycle("127.0.0.1:0", nil)
	if err := lc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	addr := lc.Addr()
	if addr == "" || addr == "127.0.0.1:0" {
		t.Fatalf("addr not resolved after start: %q", addr)
	}

	if err := lc.Stop(); err != nil {
		t.Fatalf("first stop: %v", err)
	}
	if got := lc.State(); got != StateStopped {
		t.Fatalf("state after first stop = %s, want stopped", got)
	}
	if got := lc.Addr(); got != addr {
		t.Fatalf("addr changed after stop = %q, want %q", got, addr)
	}

	// A second Stop must return the same nil result and leave the state
	// untouched. The old dial check is gone: once the listener closes, the OS
	// can give the freed ephemeral port to another test, so a successful
	// connect would say nothing about this lifecycle.
	if err := lc.Stop(); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if got := lc.State(); got != StateStopped {
		t.Fatalf("state after second stop = %s, want stopped", got)
	}
}

func TestLifecycleStopBeforeStartIsNoop(t *testing.T) {
	lc := NewLifecycle("127.0.0.1:0", nil)
	if err := lc.Stop(); err != nil {
		t.Fatalf("stop before start: %v", err)
	}
	if got := lc.State(); got != StateNew {
		t.Fatalf("state after early stop = %s, want new", got)
	}
	if err := lc.Start(); err != nil {
		t.Fatalf("start after early stop: %v", err)
	}
	defer func() { _ = lc.Stop() }()
}

func TestLifecycleSecondStartRefused(t *testing.T) {
	lc := NewLifecycle("127.0.0.1:0", nil)
	if err := lc.Start(); err != nil {
		t.Fatalf("first start: %v", err)
	}
	defer func() { _ = lc.Stop() }()

	err := lc.Start()
	if !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second start error = %v, want ErrAlreadyStarted", err)
	}
}

func TestLifecycleOccupiedPortFails(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("blocking listener: %v", err)
	}
	addr := blocker.Addr().String()

	lc := NewLifecycle(addr, nil)
	if err := lc.Start(); !errors.Is(err, ErrListen) {
		t.Fatalf("start on occupied port error = %v, want ErrListen", err)
	}
	if got := lc.State(); got != StateNew {
		t.Fatalf("state after failed start = %s, want new", got)
	}

	// A failed bind leaves the lifecycle usable: retry on the freed port.
	_ = blocker.Close()
	if err := lc.Start(); err != nil {
		t.Fatalf("retry after port freed: %v", err)
	}
	defer func() { _ = lc.Stop() }()
}

func TestLifecycleRunStopsOnContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lc := NewLifecycle("127.0.0.1:0", nil)

	errCh := make(chan error, 1)
	go func() { errCh <- lc.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for lc.State() != StateRunning {
		if time.Now().After(deadline) {
			t.Fatal("server did not reach running state")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return after context cancellation")
	}
	if got := lc.State(); got != StateStopped {
		t.Fatalf("state after cancel = %s, want stopped", got)
	}
}
