package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

var executorsTestStart = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func newExecutorsTestRegistry(t *testing.T) (*ExecutorRegistry, func(time.Time)) {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "executors.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	r, err := NewExecutorRegistry(d)
	if err != nil {
		t.Fatalf("NewExecutorRegistry: %v", err)
	}
	current := executorsTestStart
	r.now = func() time.Time { return current }
	return r, func(next time.Time) { current = next }
}

func executorsTestEntry(id string, caps ...domain.Capability) ExecutorEntry {
	return ExecutorEntry{
		ID:           id,
		Runtime:      domain.RuntimeSubagent,
		Capabilities: caps,
		Lease:        time.Hour,
	}
}

func assertExecutorUnavailable(t *testing.T, err error, wantID string) {
	t.Helper()
	if !errors.Is(err, ErrExecutorUnavailable) {
		t.Fatalf("want ErrExecutorUnavailable, got %v", err)
	}
	var typed *ExecutorUnavailableError
	if !errors.As(err, &typed) {
		t.Fatalf("want typed *ExecutorUnavailableError, got %T", err)
	}
	if typed.ID != wantID {
		t.Fatalf("refusal id = %q, want %q", typed.ID, wantID)
	}
}

func assertInvalidInput(t *testing.T, err error) {
	t.Helper()
	var appErr *domain.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("want *domain.AppError, got %T: %v", err, err)
	}
	if appErr.Code != domain.CodeInvalidInput {
		t.Fatalf("error code = %q, want %q", appErr.Code, domain.CodeInvalidInput)
	}
}

func TestExecutorTableCreatedOnFirstUse(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "executors.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	ok, err := tableExists(context.Background(), d.readDB, "executor_registry")
	if err != nil {
		t.Fatalf("tableExists before use: %v", err)
	}
	if ok {
		t.Fatal("executor_registry must not exist before first use")
	}

	r, err := NewExecutorRegistry(d)
	if err != nil {
		t.Fatalf("NewExecutorRegistry: %v", err)
	}
	if err := r.Register(executorsTestEntry("exec-1", domain.CapabilityWatch)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ok, err = tableExists(context.Background(), d.readDB, "executor_registry")
	if err != nil {
		t.Fatalf("tableExists after use: %v", err)
	}
	if !ok {
		t.Fatal("executor_registry must be created inside executors.go on first use")
	}
}

func TestAvailableExecutorIsSelected(t *testing.T) {
	r, _ := newExecutorsTestRegistry(t)
	if err := r.Register(executorsTestEntry("exec-1",
		domain.CapabilityWatch, domain.CapabilityReadVerdict)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := r.SelectAvailable(SelectRequest{
		Capabilities: []domain.Capability{domain.CapabilityWatch},
		Runtime:      domain.RuntimeSubagent,
	})
	if err != nil {
		t.Fatalf("SelectAvailable: %v", err)
	}
	if got.ID != "exec-1" {
		t.Fatalf("selected %q, want exec-1", got.ID)
	}
	if !got.Available {
		t.Fatal("selected executor must report Available=true")
	}
}

func TestUnavailableExecutorIsListedButNotSelected(t *testing.T) {
	r, _ := newExecutorsTestRegistry(t)
	if err := r.Register(executorsTestEntry("exec-1", domain.CapabilityWatch)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.SetAvailable("exec-1", false); err != nil {
		t.Fatalf("SetAvailable: %v", err)
	}

	listed, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("List length = %d, want 1", len(listed))
	}
	if listed[0].ID != "exec-1" || listed[0].Available {
		t.Fatalf("disabled executor must stay visible with Available=false, got %+v", listed[0])
	}

	_, err = r.SelectAvailable(SelectRequest{})
	assertExecutorUnavailable(t, err, "")
	_, err = r.Acquire("exec-1")
	assertExecutorUnavailable(t, err, "exec-1")
}

func TestHeartbeatRefreshesLeaseAndTimestamp(t *testing.T) {
	r, advance := newExecutorsTestRegistry(t)
	if err := r.Register(executorsTestEntry("exec-1", domain.CapabilityWatch)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	before, err := r.List()
	if err != nil {
		t.Fatalf("List before heartbeat: %v", err)
	}
	if !before[0].LastHeartbeat.Equal(executorsTestStart) {
		t.Fatalf("initial heartbeat = %v, want %v", before[0].LastHeartbeat, executorsTestStart)
	}
	if !before[0].LeaseUntil.Equal(executorsTestStart.Add(time.Hour)) {
		t.Fatalf("initial lease = %v, want %v", before[0].LeaseUntil, executorsTestStart.Add(time.Hour))
	}

	refreshedAt := executorsTestStart.Add(50 * time.Minute)
	advance(refreshedAt)
	if err := r.Heartbeat("exec-1"); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	// The original lease would have ended at t+60m. The heartbeat extends it
	// to t+110m, so the executor is still selectable at t+90m.
	advance(executorsTestStart.Add(90 * time.Minute))
	if _, err := r.Acquire("exec-1"); err != nil {
		t.Fatalf("Acquire after heartbeat: %v", err)
	}
	after, err := r.List()
	if err != nil {
		t.Fatalf("List after heartbeat: %v", err)
	}
	if !after[0].LastHeartbeat.Equal(refreshedAt) {
		t.Fatalf("heartbeat moved to %v, want %v", after[0].LastHeartbeat, refreshedAt)
	}
	wantLease := refreshedAt.Add(time.Hour)
	if !after[0].LeaseUntil.Equal(wantLease) {
		t.Fatalf("lease moved to %v, want %v", after[0].LeaseUntil, wantLease)
	}
}

func TestExpiredLeaseBecomesUnavailable(t *testing.T) {
	r, advance := newExecutorsTestRegistry(t)
	if err := r.Register(executorsTestEntry("exec-1", domain.CapabilityWatch)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	advance(executorsTestStart.Add(2 * time.Hour))
	listed, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("List length = %d, want 1", len(listed))
	}
	if listed[0].Available {
		t.Fatal("expired lease must report Available=false")
	}
	_, err = r.Acquire("exec-1")
	assertExecutorUnavailable(t, err, "exec-1")

	// A heartbeat from a live executor revives the lease.
	if err := r.Heartbeat("exec-1"); err != nil {
		t.Fatalf("Heartbeat after expiry: %v", err)
	}
	if _, err := r.Acquire("exec-1"); err != nil {
		t.Fatalf("Acquire after revived lease: %v", err)
	}
}

func TestRevokedExecutorIsVisibleButNotSelectable(t *testing.T) {
	r, _ := newExecutorsTestRegistry(t)
	e := executorsTestEntry("exec-1", domain.CapabilityWatch)
	e.AuthState = AuthStateRevoked
	if err := r.Register(e); err != nil {
		t.Fatalf("Register: %v", err)
	}

	listed, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].Available {
		t.Fatalf("revoked executor must stay visible as unavailable, got %+v", listed)
	}
	_, err = r.Acquire("exec-1")
	assertExecutorUnavailable(t, err, "exec-1")
}

func TestRegisterTypechecksCapabilities(t *testing.T) {
	r, _ := newExecutorsTestRegistry(t)

	if err := r.Register(executorsTestEntry("bad-cap", domain.Capability("admin"))); err == nil {
		t.Fatal("unknown capability must be rejected")
	} else {
		assertInvalidInput(t, err)
	}

	e := executorsTestEntry("bad-runtime", domain.CapabilityWatch)
	e.Runtime = domain.Runtime("daemon")
	if err := r.Register(e); err == nil {
		t.Fatal("unknown runtime must be rejected")
	} else {
		assertInvalidInput(t, err)
	}

	e = executorsTestEntry("bad-auth", domain.CapabilityWatch)
	e.AuthState = AuthState("magic")
	if err := r.Register(e); err == nil {
		t.Fatal("unknown auth state must be rejected")
	} else {
		assertInvalidInput(t, err)
	}

	if err := r.Register(executorsTestEntry("exec-1", domain.CapabilityWatch)); err != nil {
		t.Fatalf("valid Register: %v", err)
	}
	listed, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("rejected registrations must not write rows, got %d", len(listed))
	}
}

func TestChildCannotWidenParentCapabilities(t *testing.T) {
	r, _ := newExecutorsTestRegistry(t)
	if err := r.Register(executorsTestEntry("parent", domain.CapabilityWatch)); err != nil {
		t.Fatalf("Register parent: %v", err)
	}

	child := executorsTestEntry("child", domain.CapabilityWatch, domain.CapabilityMerge)
	child.ParentID = "parent"
	if err := r.Register(child); err == nil {
		t.Fatal("child must not gain a capability the parent does not hold")
	} else if !errors.Is(err, ErrCapabilityEscalation) {
		t.Fatalf("want ErrCapabilityEscalation, got %v", err)
	} else {
		var typed *CapabilityEscalationError
		if !errors.As(err, &typed) {
			t.Fatalf("want typed *CapabilityEscalationError, got %T", err)
		}
		if typed.ParentID != "parent" || typed.Capability != domain.CapabilityMerge {
			t.Fatalf("wrong escalation details: %+v", typed)
		}
	}

	if err := r.CheckChildCapabilities("parent",
		[]domain.Capability{domain.CapabilityMerge}); err == nil {
		t.Fatal("separate capability check must refuse the escalation")
	} else if !errors.Is(err, ErrCapabilityEscalation) {
		t.Fatalf("want ErrCapabilityEscalation, got %v", err)
	}

	allowed := executorsTestEntry("child-ok", domain.CapabilityWatch)
	allowed.ParentID = "parent"
	if err := r.Register(allowed); err != nil {
		t.Fatalf("Register child with subset capabilities: %v", err)
	}
	if _, err := r.Acquire("child-ok"); err != nil {
		t.Fatalf("Acquire allowed child: %v", err)
	}

	if err := r.CheckChildCapabilities("missing",
		[]domain.Capability{domain.CapabilityWatch}); err == nil {
		t.Fatal("missing parent must be refused")
	} else if !errors.Is(err, ErrExecutorNotFound) {
		t.Fatalf("want ErrExecutorNotFound, got %v", err)
	}
}

func TestSetAvailableAndHeartbeatUnknownExecutorAreTyped(t *testing.T) {
	r, _ := newExecutorsTestRegistry(t)
	if err := r.SetAvailable("missing", false); !errors.Is(err, ErrExecutorNotFound) {
		t.Fatalf("SetAvailable missing: want ErrExecutorNotFound, got %v", err)
	}
	if err := r.Heartbeat("missing"); !errors.Is(err, ErrExecutorNotFound) {
		t.Fatalf("Heartbeat missing: want ErrExecutorNotFound, got %v", err)
	}
}
