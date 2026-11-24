package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// controlStore is a RunStore that can inject transient write-lock conflicts.
type controlStore struct {
	core.RunStore
	mu       sync.Mutex
	runs     map[core.RunID]*core.Run
	failures map[core.RunID]int
}

func newControlStore() *controlStore {
	return &controlStore{runs: map[core.RunID]*core.Run{}, failures: map[core.RunID]int{}}
}

func (s *controlStore) add(run *core.Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.ID] = run
}

func (s *controlStore) Get(_ context.Context, id core.RunID) (*core.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[id]
	if run == nil {
		return nil, nil
	}
	copy := *run
	return &copy, nil
}

func (s *controlStore) Save(_ context.Context, run *core.Run) error {
	s.mu.Lock()
	failures := s.failures[run.ID]
	if failures > 0 {
		s.failures[run.ID]--
	}
	s.mu.Unlock()
	if failures > 0 {
		return errors.New("database is locked (5) (SQLITE_BUSY)")
	}
	s.mu.Lock()
	copy := *run
	s.runs[run.ID] = &copy
	s.mu.Unlock()
	return nil
}

func (s *controlStore) SetApproval(context.Context, core.RunID, string) error { return nil }

func (s *controlStore) FindActiveByTask(context.Context, core.TaskID) (*core.Run, error) {
	return nil, nil
}

func controlClient(t *testing.T, sv Services) *msdk.ClientSession {
	t.Helper()
	server := NewServer(sv)
	clientTransport, serverTransport := msdk.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := msdk.NewClient(&msdk.Implementation{Name: "test-client"}, nil)
	cs, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callControl(t *testing.T, cs *msdk.ClientSession, args map[string]any) *msdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name: "beacon_control", Arguments: args,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func controlText(t *testing.T, res *msdk.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatalf("empty tool result: %+v", res)
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatalf("unexpected content: %+v", res.Content)
	}
	return text.Text
}

func parseControlBatch(t *testing.T, res *msdk.CallToolResult) controlBatch {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", controlText(t, res))
	}
	var out controlBatch
	if err := json.Unmarshal([]byte(controlText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func newControlServices(store *controlStore) Services {
	return Services{Control: &app.Control{Store: store}}
}

func TestControlSingleCancelStillReturnsOK(t *testing.T) {
	store := newControlStore()
	store.add(core.NewRun("r1", "T-1", time.Now()))
	cs := controlClient(t, newControlServices(store))

	res := callControl(t, cs, map[string]any{"action": "cancel", "run_id": "r1"})
	if res.IsError || controlText(t, res) != "ok" {
		t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
	}
	if got := store.runs["r1"]; got.State != core.RunClosed {
		t.Fatalf("state = %s, want %s", got.State, core.RunClosed)
	}
}

func TestControlBatchCancelReportsPerID(t *testing.T) {
	store := newControlStore()
	store.add(core.NewRun("r1", "T-1", time.Now()))
	store.add(core.NewRun("r2", "T-1", time.Now()))
	cs := controlClient(t, newControlServices(store))

	res := callControl(t, cs, map[string]any{
		"action": "cancel", "run_ids": []any{"r1", "r2", "missing"},
	})
	got := parseControlBatch(t, res)
	if got.Action != "cancel" || len(got.Results) != 3 {
		t.Fatalf("result = %+v", got)
	}
	if got.Results[0].ID != "r1" || got.Results[0].Status != "ok" ||
		got.Results[1].ID != "r2" || got.Results[1].Status != "ok" {
		t.Fatalf("results = %+v", got.Results)
	}
	if got.Results[2].Status != "error" || !strings.Contains(got.Results[2].Error, "not_found") {
		t.Fatalf("missing run result = %+v", got.Results[2])
	}
	if store.runs["r1"].State != core.RunClosed || store.runs["r2"].State != core.RunClosed {
		t.Fatalf("runs were not cancelled: r1=%s r2=%s", store.runs["r1"].State, store.runs["r2"].State)
	}
}

func TestControlBatchRejectsEmptyRunIDs(t *testing.T) {
	cs := controlClient(t, newControlServices(newControlStore()))

	res := callControl(t, cs, map[string]any{"action": "cancel", "run_ids": []any{}})
	if !res.IsError || !strings.Contains(controlText(t, res), "invalid_input") {
		t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
	}
}

func TestControlBatchRetriesTransientBusy(t *testing.T) {
	store := newControlStore()
	store.add(core.NewRun("r1", "T-1", time.Now()))
	store.failures["r1"] = 1
	cs := controlClient(t, newControlServices(store))

	got := parseControlBatch(t, callControl(t, cs, map[string]any{
		"action": "cancel", "run_ids": []any{"r1"},
	}))
	if len(got.Results) != 1 || got.Results[0].Status != "ok" {
		t.Fatalf("result = %+v", got)
	}
	if store.runs["r1"].State != core.RunClosed {
		t.Fatalf("state = %s, want %s", store.runs["r1"].State, core.RunClosed)
	}
}

func TestControlBatchReportsPersistentBusyPerID(t *testing.T) {
	store := newControlStore()
	store.add(core.NewRun("r1", "T-1", time.Now()))
	store.add(core.NewRun("r2", "T-1", time.Now()))
	store.failures["r1"] = 100
	cs := controlClient(t, newControlServices(store))

	got := parseControlBatch(t, callControl(t, cs, map[string]any{
		"action": "cancel", "run_ids": []any{"r1", "r2"},
	}))
	if len(got.Results) != 2 {
		t.Fatalf("result = %+v", got)
	}
	if got.Results[0].ID != "r1" || got.Results[0].Status != "error" ||
		!strings.Contains(got.Results[0].Error, "SQLITE_BUSY") {
		t.Fatalf("busy result = %+v", got.Results[0])
	}
	if got.Results[1].ID != "r2" || got.Results[1].Status != "ok" {
		t.Fatalf("healthy result = %+v", got.Results[1])
	}
	if store.runs["r2"].State != core.RunClosed {
		t.Fatalf("r2 state = %s, want %s", store.runs["r2"].State, core.RunClosed)
	}
}

func TestControlRemoteBatchCollectsOutcomesSequentially(t *testing.T) {
	var calls []string
	sv := Services{Remote: func(_ context.Context, action, runID string, _ int, _ string) error {
		calls = append(calls, action+":"+runID)
		if runID == "bad" {
			return errors.New("beacon API status 409")
		}
		return nil
	}}
	cs := controlClient(t, sv)

	got := parseControlBatch(t, callControl(t, cs, map[string]any{
		"action": "cancel", "run_ids": []any{"good", "bad", "good2"},
	}))
	want := []string{"cancel:good", "cancel:bad", "cancel:good2"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	if len(got.Results) != 3 || got.Results[0].Status != "ok" ||
		got.Results[1].Status != "error" || got.Results[2].Status != "ok" {
		t.Fatalf("result = %+v", got)
	}
	if !strings.Contains(got.Results[1].Error, "409") {
		t.Fatalf("remote error = %q", got.Results[1].Error)
	}
}

func TestControlParallelCancelsAllSucceed(t *testing.T) {
	store := newControlStore()
	const n = 8
	for i := 0; i < n; i++ {
		store.add(core.NewRun(core.RunID(fmt.Sprintf("r-%d", i)), "T-1", time.Now()))
	}
	cs := controlClient(t, newControlServices(store))

	errs := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("r-%d", i)
			res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
				Name: "beacon_control", Arguments: map[string]any{"action": "cancel", "run_id": id},
			})
			if err != nil {
				errs <- id + ": " + err.Error()
				return
			}
			if res.IsError || len(res.Content) == 0 {
				errs <- id + ": tool error"
				return
			}
			text, ok := res.Content[0].(*msdk.TextContent)
			if !ok || text.Text != "ok" {
				errs <- id + ": unexpected result"
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Errorf("parallel cancel failed: %s", msg)
	}
	for i := 0; i < n; i++ {
		id := core.RunID(fmt.Sprintf("r-%d", i))
		if got := store.runs[id]; got.State != core.RunClosed {
			t.Fatalf("run %s = %s, want %s", id, got.State, core.RunClosed)
		}
	}
}
