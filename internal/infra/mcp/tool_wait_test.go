package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// waitToolStore answers Get with one state per call, holding the last state
// once the sequence is exhausted. It lets a tool-level test observe a run that
// moves from running to waiting_ci while the call is in progress.
type waitToolStore struct {
	states []core.RunState
	calls  int
}

func (s *waitToolStore) Get(_ context.Context, id core.RunID) (*core.Run, error) {
	s.calls++
	index := s.calls - 1
	if index >= len(s.states) {
		index = len(s.states) - 1
	}
	if index < 0 {
		return nil, nil
	}
	run := core.NewRun(id, "T-wait", time.Now().UTC())
	run.State = s.states[index]
	return run, nil
}

func callWait(t *testing.T, cs *msdk.ClientSession, args map[string]any) *msdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name: "beacon_wait", Arguments: args,
	})
	if err != nil {
		t.Fatalf("cs.CallTool(Name: beacon_wait): %v", err)
	}
	return res
}

// TestBeaconWaitOverInMemoryTransport drives the registered tool through the
// SDK and checks that it returns the run observed at waiting_ci, not the first
// running snapshot.
func TestBeaconWaitOverInMemoryTransport(t *testing.T) {
	store := &waitToolStore{states: []core.RunState{
		core.RunRunning, core.RunRunning, core.RunWaitingCI,
	}}
	cs := deleteClient(t, Services{Wait: app.WaitService{Store: store, Interval: time.Millisecond}})

	res := callWait(t, cs, map[string]any{"run_id": "run-wait", "timeout_ms": 500})
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("unexpected tool result: %+v", res)
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *msdk.TextContent", res.Content[0])
	}
	var got struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", text.Text, err)
	}
	if got.ID != "run-wait" || got.State != string(core.RunWaitingCI) {
		t.Fatalf("result = %+v, want run-wait in waiting_ci", got)
	}
	if store.calls < 3 {
		t.Fatalf("Get calls = %d, want the run polled through the transition", store.calls)
	}
}

func TestBeaconWaitRejectsInvalidTimeoutAndMissingService(t *testing.T) {
	store := &waitToolStore{states: []core.RunState{core.RunWaitingCI}}
	cs := deleteClient(t, Services{Wait: app.WaitService{Store: store, Interval: time.Millisecond}})
	for _, timeout := range []int{0, -1, maxWaitMS + 1} {
		res := callWait(t, cs, map[string]any{"run_id": "run-wait", "timeout_ms": timeout})
		if !res.IsError {
			t.Fatalf("timeout_ms %d was accepted", timeout)
		}
	}
	if store.calls != 0 {
		t.Fatalf("Get calls = %d, want none for invalid input", store.calls)
	}

	withoutService := deleteClient(t, Services{})
	res := callWait(t, withoutService, map[string]any{"run_id": "run-wait", "timeout_ms": 100})
	if !res.IsError {
		t.Fatal("beacon_wait succeeded with no wait service configured")
	}
}

func TestBeaconWaitReportsDeadline(t *testing.T) {
	store := &waitToolStore{states: []core.RunState{core.RunRunning}}
	cs := deleteClient(t, Services{Wait: app.WaitService{Store: store, Interval: time.Millisecond}})

	res := callWait(t, cs, map[string]any{"run_id": "run-stuck", "timeout_ms": 20})
	if !res.IsError || len(res.Content) == 0 {
		t.Fatalf("unexpected tool result: %+v", res)
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *msdk.TextContent", res.Content[0])
	}
	if !strings.Contains(text.Text, "deadline") {
		t.Fatalf("result = %q, want a deadline error", text.Text)
	}
}
