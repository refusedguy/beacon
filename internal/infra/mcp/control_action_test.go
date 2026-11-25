package mcp

import (
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestControlRoutesWatchLocally(t *testing.T) {
	store := newControlStore()
	store.add(controlRunInState(t, "r1", core.Event{Kind: core.EvStart}))
	cs := controlClient(t, newControlServices(store))

	res := callControl(t, cs, map[string]any{"action": "watch", "run_id": "r1", "pr": 42})
	if res.IsError || controlText(t, res) != "ok" {
		t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
	}
	got := store.runs["r1"]
	if got.State != core.RunWaitingCI || got.PRNumber != 42 {
		t.Fatalf("state = %s pr = %d, want %s pr 42", got.State, got.PRNumber, core.RunWaitingCI)
	}
}

func TestControlRejectsUnknownVerbs(t *testing.T) {
	cs := controlClient(t, newControlServices(newControlStore()))

	for _, args := range []map[string]any{
		{"action": "bind", "run_id": "r1", "pr": 42},
		{"action": "APPROVE", "run_id": "r1"},
		{"action": "nudge-all", "run_ids": []any{"r1", "r2"}},
	} {
		res := callControl(t, cs, args)
		if !res.IsError {
			t.Errorf("action %v was accepted", args["action"])
		}
	}
}
