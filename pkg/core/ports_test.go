package core

import (
	"context"
	"testing"
)

type plainHarness struct{}

func (plainHarness) Spawn(context.Context, SpawnRequest) (Session, error) { return Session{}, nil }
func (plainHarness) Wake(context.Context, string, WakeContext) error      { return nil }
func (plainHarness) Stop(context.Context, string) error                   { return nil }
func (plainHarness) IsAlive(context.Context, string) (bool, error)        { return false, nil }
func (plainHarness) NotifyParent(context.Context, string, string) error   { return nil }
func (plainHarness) Events() <-chan HarnessEvent                          { return nil }

type idleOnlyHarness struct{ plainHarness }

func (*idleOnlyHarness) WatchIdle(context.Context) {}

type capableHarness struct {
	plainHarness
	watchCtx  context.Context
	watchCall int
	refresh   int
}

func (h *capableHarness) WatchIdle(ctx context.Context) {
	h.watchCtx = ctx
	h.watchCall++
}

func (h *capableHarness) RefreshWorktrees(context.Context) error {
	h.refresh++
	return nil
}

func TestCapabilityPortsAreOptionalAndIndependent(t *testing.T) {
	var plain AgentHarness = plainHarness{}
	if _, ok := plain.(IdleWatcher); ok {
		t.Error("plain harness must not claim IdleWatcher")
	}
	if _, ok := plain.(WorktreeRefresher); ok {
		t.Error("plain harness must not claim WorktreeRefresher")
	}

	var idleOnly AgentHarness = &idleOnlyHarness{}
	if _, ok := idleOnly.(IdleWatcher); !ok {
		t.Error("idle-only harness must claim IdleWatcher")
	}
	if _, ok := idleOnly.(WorktreeRefresher); ok {
		t.Error("idle-only harness must not claim WorktreeRefresher")
	}
}

func TestCapabilityPortsReachTheirImplementation(t *testing.T) {
	h := &capableHarness{}
	var harness AgentHarness = h

	watcher, ok := harness.(IdleWatcher)
	if !ok {
		t.Fatal("capable harness must claim IdleWatcher")
	}
	ctx := context.WithValue(context.Background(), struct{}{}, "idle")
	watcher.WatchIdle(ctx)
	if h.watchCall != 1 || h.watchCtx != ctx {
		t.Fatalf("WatchIdle called %d times with %v", h.watchCall, h.watchCtx)
	}

	refresher, ok := harness.(WorktreeRefresher)
	if !ok {
		t.Fatal("capable harness must claim WorktreeRefresher")
	}
	if err := refresher.RefreshWorktrees(context.Background()); err != nil {
		t.Fatalf("RefreshWorktrees: %v", err)
	}
	if h.refresh != 1 {
		t.Fatalf("RefreshWorktrees called %d times, want 1", h.refresh)
	}
}
