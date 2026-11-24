package core

import (
	"context"
	"reflect"
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
	repo      string
	ref       map[string]string
	watchCtx  context.Context
	watchCall int
	refresh   int
}

func (h *capableHarness) SetRepo(root string) { h.repo = root }

func (h *capableHarness) ResolveModel(parent string) map[string]string {
	return map[string]string{"parent": parent, "id": h.ref["id"]}
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
	if _, ok := plain.(RepoSetter); ok {
		t.Error("plain harness must not claim RepoSetter")
	}
	if _, ok := plain.(ModelResolver); ok {
		t.Error("plain harness must not claim ModelResolver")
	}
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
	if _, ok := idleOnly.(RepoSetter); ok {
		t.Error("idle-only harness must not claim RepoSetter")
	}
	if _, ok := idleOnly.(ModelResolver); ok {
		t.Error("idle-only harness must not claim ModelResolver")
	}
}

func TestCapabilityPortsReachTheirImplementation(t *testing.T) {
	h := &capableHarness{ref: map[string]string{"id": "free"}}
	var harness AgentHarness = h

	setter, ok := harness.(RepoSetter)
	if !ok {
		t.Fatal("capable harness must claim RepoSetter")
	}
	setter.SetRepo("/workspace/repo")
	if h.repo != "/workspace/repo" {
		t.Fatalf("SetRepo stored %q, want %q", h.repo, "/workspace/repo")
	}

	resolver, ok := harness.(ModelResolver)
	if !ok {
		t.Fatal("capable harness must claim ModelResolver")
	}
	want := map[string]string{"parent": "parent-1", "id": "free"}
	if got := resolver.ResolveModel("parent-1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveModel = %v, want %v", got, want)
	}
	// The method value must match the signature dispatch uses at spawn time.
	var lookup func(string) map[string]string = resolver.ResolveModel
	if got := lookup("parent-2"); got["parent"] != "parent-2" {
		t.Fatalf("method value resolved parent %q", got["parent"])
	}

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
