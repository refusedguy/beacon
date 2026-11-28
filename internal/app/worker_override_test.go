package app

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

type requestCapturingHarness struct {
	*fakeHarness
	mu       sync.Mutex
	requests []core.SpawnRequest
}

func (h *requestCapturingHarness) Spawn(ctx context.Context, req core.SpawnRequest) (core.Session, error) {
	h.mu.Lock()
	h.requests = append(h.requests, req)
	h.mu.Unlock()
	return h.fakeHarness.Spawn(ctx, req)
}

func TestDispatchAppliesWorkerOverride(t *testing.T) {
	store := newFakeStore()
	tasks := []core.Task{
		{ID: "T-OVERRIDE", Status: core.TaskAvailable,
			WorkerAgent: "table-driven", WorkerPrompt: "Use table-driven tests only.",
			Scope: []core.ScopePath{core.ScopeFile("override.go")}},
		{ID: "T-STOCK", Status: core.TaskAvailable,
			Scope: []core.ScopePath{core.ScopeFile("stock.go")}},
	}
	h := &requestCapturingHarness{fakeHarness: &fakeHarness{}}
	d := &Dispatcher{
		Source: &fakeSource{tasks: tasks}, Planner: NewPlanner(nil), Workspace: &fakeWorkspace{},
		Harness: h, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 2, MaxActiveAgents: 2},
	}
	if _, err := d.Dispatch(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if len(h.prompts) != 2 || len(h.requests) != 2 {
		t.Fatalf("prompts = %d, requests = %d", len(h.prompts), len(h.requests))
	}

	promptFor := func(id core.TaskID) string {
		for _, p := range h.prompts {
			if strings.Contains(p, "Task: "+string(id)+" ") {
				return p
			}
		}
		return ""
	}
	requestFor := func(id core.TaskID) core.SpawnRequest {
		for _, req := range h.requests {
			if req.Task.ID == id {
				return req
			}
		}
		return core.SpawnRequest{}
	}

	overridePrompt := promptFor("T-OVERRIDE")
	if !strings.Contains(overridePrompt, "Worker prompt from task card:") ||
		!strings.Contains(overridePrompt, "Use table-driven tests only.") {
		t.Fatalf("prompt override missing: %s", overridePrompt)
	}
	overrideRequest := requestFor("T-OVERRIDE")
	if overrideRequest.Task.WorkerAgent != "table-driven" {
		t.Fatalf("agent override missing from spawn request: %+v", overrideRequest.Task)
	}

	stockPrompt := promptFor("T-STOCK")
	if strings.Contains(stockPrompt, "Worker prompt from task card:") {
		t.Fatalf("stock task gained override: %s", stockPrompt)
	}
	if requestFor("T-STOCK").Task.WorkerAgent != "" {
		t.Fatal("stock task gained an agent override")
	}
}
