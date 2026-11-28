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
	"github.com/refusedguy/beacon/internal/infra/tasksource"
	"github.com/refusedguy/beacon/pkg/core"
)

// controlStore is a RunStore that can inject transient write-lock conflicts.
type controlStore struct {
	core.RunStore
	mu        sync.Mutex
	runs      map[core.RunID]*core.Run
	failures  map[core.RunID]int
	approvals map[core.RunID]string
}

func newControlStore() *controlStore {
	return &controlStore{
		runs:      map[core.RunID]*core.Run{},
		failures:  map[core.RunID]int{},
		approvals: map[core.RunID]string{},
	}
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

func (s *controlStore) FindByPR(_ context.Context, pr int) (*core.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, run := range s.runs {
		if run.PRNumber == pr {
			copy := *run
			return &copy, nil
		}
	}
	return nil, nil
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

func (s *controlStore) SetApproval(_ context.Context, id core.RunID, sha string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approvals[id] = sha
	return nil
}

func (s *controlStore) approval(id core.RunID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.approvals[id]
}

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
	return callTool(t, cs, "beacon_control", args)
}

func callTool(t *testing.T, cs *msdk.ClientSession, name string, args map[string]any) *msdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name: name, Arguments: args,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func controlRunInState(t *testing.T, id core.RunID, events ...core.Event) *core.Run {
	t.Helper()
	run := core.NewRun(id, "T-1", time.Now())
	for _, event := range events {
		if err := run.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	return run
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
	var out controlBatch
	if err := json.Unmarshal([]byte(controlText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func requireBatchError(t *testing.T, res *msdk.CallToolResult) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("batch with failed items reported success: %s", controlText(t, res))
	}
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
	requireBatchError(t, res)
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

	res := callControl(t, cs, map[string]any{
		"action": "cancel", "run_ids": []any{"r1", "r2"},
	})
	requireBatchError(t, res)
	got := parseControlBatch(t, res)
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

	res := callControl(t, cs, map[string]any{
		"action": "cancel", "run_ids": []any{"good", "bad", "good2"},
	})
	requireBatchError(t, res)
	got := parseControlBatch(t, res)
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

func TestControlApproveBindsSHA(t *testing.T) {
	for _, tc := range []struct {
		name, sha, want string
	}{
		{"explicit sha", "deadbeef", "deadbeef"},
		{"run head sha", "", "abc123"}, // omitted sha falls back to run.HeadSHA
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newControlStore()
			run := core.NewRun("r1", "T-1", time.Now())
			run.HeadSHA = "abc123"
			store.add(run)
			cs := controlClient(t, newControlServices(store))

			args := map[string]any{"action": "approve", "run_id": "r1"}
			if tc.sha != "" {
				args["sha"] = tc.sha
			}
			res := callControl(t, cs, args)
			if res.IsError || controlText(t, res) != "ok" {
				t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
			}
			if got := store.approval("r1"); got != tc.want {
				t.Fatalf("approval = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestControlUnblockMovesBlockedRunToWaitingCI(t *testing.T) {
	store := newControlStore()
	store.add(controlRunInState(t, "r1",
		core.Event{Kind: core.EvStart}, core.Event{Kind: core.EvBlock}))
	cs := controlClient(t, newControlServices(store))

	res := callControl(t, cs, map[string]any{"action": "unblock", "run_id": "r1"})
	if res.IsError || controlText(t, res) != "ok" {
		t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
	}
	if got := store.runs["r1"].State; got != core.RunWaitingCI {
		t.Fatalf("state = %s, want %s", got, core.RunWaitingCI)
	}
}

func TestControlRetryRearmsRun(t *testing.T) {
	for _, tc := range []struct {
		name      string
		build     func(t *testing.T) *core.Run
		wantState core.RunState
		wantPR    int
		wantSess  string
	}{
		{
			name: "blocked run resets attempts",
			build: func(t *testing.T) *core.Run {
				r := controlRunInState(t, "r1",
					core.Event{Kind: core.EvStart}, core.Event{Kind: core.EvBlock})
				r.RebaseAttempts, r.FixAttempts = 2, 2
				return r
			},
			wantState: core.RunWaitingCI,
		},
		{
			name: "closed run without PR returns to pending",
			build: func(t *testing.T) *core.Run {
				r := core.NewRun("r1", "T-1", time.Now())
				r.State = core.RunClosed
				r.SessionID = "session-1"
				r.WorkspacePath = "/tmp/work"
				return r
			},
			wantState: core.RunPending,
		},
		{
			name: "closed run with PR returns to waiting_ci",
			build: func(t *testing.T) *core.Run {
				r := core.NewRun("r1", "T-1", time.Now())
				r.State = core.RunClosed
				r.PRNumber = 42
				return r
			},
			wantState: core.RunWaitingCI,
			wantPR:    42,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newControlStore()
			store.add(tc.build(t))
			cs := controlClient(t, newControlServices(store))

			res := callControl(t, cs, map[string]any{"action": "retry", "run_id": "r1"})
			if res.IsError || controlText(t, res) != "ok" {
				t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
			}
			got := store.runs["r1"]
			if got.State != tc.wantState || got.PRNumber != tc.wantPR || got.SessionID != tc.wantSess {
				t.Fatalf("run = %+v, want state %s pr %d session %q", got, tc.wantState, tc.wantPR, tc.wantSess)
			}
			if got.RebaseAttempts != 0 || got.FixAttempts != 0 {
				t.Fatalf("attempts = %d/%d, want 0/0", got.RebaseAttempts, got.FixAttempts)
			}
		})
	}
}

func TestControlForwardsAllAdvertisedActionsToRemote(t *testing.T) {
	type forwarded struct {
		action, runID, sha string
		pr                 int
	}
	var got []forwarded
	sv := Services{Remote: func(_ context.Context, action, runID string, pr int, sha string) error {
		got = append(got, forwarded{action: action, runID: runID, sha: sha, pr: pr})
		return nil
	}}
	cs := controlClient(t, sv)

	cases := []forwarded{
		{action: "approve", runID: "r1", sha: "abc123"},
		{action: "unblock", runID: "r2"},
		{action: "cancel", runID: "r3"},
		{action: "retry", runID: "r4"},
		{action: "watch", runID: "r5", pr: 42}, // bind a PR: action and pr reach the daemon
	}
	for _, tc := range cases {
		args := map[string]any{"action": tc.action, "run_id": tc.runID}
		if tc.sha != "" {
			args["sha"] = tc.sha
		}
		if tc.pr != 0 {
			args["pr"] = tc.pr
		}
		res := callControl(t, cs, args)
		if res.IsError || controlText(t, res) != "ok" {
			t.Fatalf("%s result = %+v, text = %q", tc.action, res, controlText(t, res))
		}
	}
	if len(got) != len(cases) {
		t.Fatalf("got %d calls, want %d", len(got), len(cases))
	}
	for i, tc := range cases {
		if got[i] != tc {
			t.Errorf("call %d = %+v, want %+v", i, got[i], tc)
		}
	}
}

func TestControlBindPRMovesRunToWaitingCI(t *testing.T) {
	store := newControlStore()
	store.add(controlRunInState(t, "r1", core.Event{Kind: core.EvStart}))
	cs := controlClient(t, newControlServices(store))

	res := callTool(t, cs, "beacon_watch_pr", map[string]any{"run_id": "r1", "pr": 42})
	if res.IsError {
		t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
	}
	got := store.runs["r1"]
	if got.State != core.RunWaitingCI || got.PRNumber != 42 {
		t.Fatalf("state = %s pr = %d, want %s pr 42", got.State, got.PRNumber, core.RunWaitingCI)
	}
}

func TestControlInvalidTransitionReportsStructuredErrorWithoutPartialSuccess(t *testing.T) {
	store := newControlStore()
	store.add(core.NewRun("r1", "T-1", time.Now())) // pending: unblock is invalid
	store.add(controlRunInState(t, "r2",
		core.Event{Kind: core.EvStart}, core.Event{Kind: core.EvBlock}))
	cs := controlClient(t, newControlServices(store))

	res := callControl(t, cs, map[string]any{
		"action": "unblock", "run_ids": []any{"r1", "r2"},
	})
	requireBatchError(t, res)
	got := parseControlBatch(t, res)
	if len(got.Results) != 2 {
		t.Fatalf("result = %+v", got)
	}
	if got.Results[0].ID != "r1" || got.Results[0].Status != "error" ||
		!strings.Contains(got.Results[0].Error, "invalid_transition") {
		t.Fatalf("invalid result = %+v", got.Results[0])
	}
	if got.Results[1].ID != "r2" || got.Results[1].Status != "ok" {
		t.Fatalf("valid result = %+v", got.Results[1])
	}
	if s := store.runs["r1"].State; s != core.RunPending {
		t.Fatalf("r1 state = %s, want %s with no partial success", s, core.RunPending)
	}
	if s := store.runs["r2"].State; s != core.RunWaitingCI {
		t.Fatalf("r2 state = %s, want %s", s, core.RunWaitingCI)
	}
}

func TestControlSingleInvalidTransitionReturnsStructuredError(t *testing.T) {
	store := newControlStore()
	store.add(core.NewRun("r1", "T-1", time.Now()))
	cs := controlClient(t, newControlServices(store))

	res := callControl(t, cs, map[string]any{"action": "unblock", "run_id": "r1"})
	if !res.IsError || !strings.Contains(controlText(t, res), "invalid_transition") {
		t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
	}
	if s := store.runs["r1"].State; s != core.RunPending {
		t.Fatalf("state = %s, want %s with no partial success", s, core.RunPending)
	}
}

// TestControlBatchExampleReturnsJSONResult is the complete request and result
// pair quoted in docs/research/integration.md. A blocked run unblocks and a
// pending run keeps its state, so one call shows both outcome codes.
func TestControlBatchExampleReturnsJSONResult(t *testing.T) {
	store := newControlStore()
	store.add(controlRunInState(t, "r1",
		core.Event{Kind: core.EvStart}, core.Event{Kind: core.EvBlock}))
	store.add(core.NewRun("r2", "T-2", time.Now()))
	cs := controlClient(t, newControlServices(store))

	const request = `{"action":"unblock","run_ids":["r1","r2"]}`
	var arguments map[string]any
	if err := json.Unmarshal([]byte(request), &arguments); err != nil {
		t.Fatal(err)
	}
	res := callControl(t, cs, arguments)
	requireBatchError(t, res)
	got := parseControlBatch(t, res)
	if got.Action != "unblock" || len(got.Results) != 2 {
		t.Fatalf("result = %+v", got)
	}
	if got.Results[0].ID != "r1" || got.Results[0].Status != "ok" {
		t.Fatalf("r1 result = %+v, want ok", got.Results[0])
	}
	if got.Results[1].ID != "r2" || got.Results[1].Status != "error" ||
		!strings.Contains(got.Results[1].Error, "invalid_transition") {
		t.Fatalf("r2 result = %+v, want invalid_transition", got.Results[1])
	}
	if store.runs["r1"].State != core.RunWaitingCI || store.runs["r2"].State != core.RunPending {
		t.Fatalf("states = r1:%s r2:%s, want r1:%s r2:%s",
			store.runs["r1"].State, store.runs["r2"].State, core.RunWaitingCI, core.RunPending)
	}
	t.Logf("request: %s", request)
	t.Logf("result: %s", controlText(t, res))
	t.Logf("r1 state=%s r2 state=%s", store.runs["r1"].State, store.runs["r2"].State)
}

func TestControlRemoteNudgeCarriesMessage(t *testing.T) {
	var action, runID, message string
	sv := Services{Remote: func(ctx context.Context, a, id string, _ int, _ string) error {
		action, runID, message = a, id, ControlMessage(ctx)
		return nil
	}}
	cs := controlClient(t, sv)

	res := callControl(t, cs, map[string]any{
		"action": "nudge", "run_id": "r1", "message": "follow the checklist",
	})
	if res.IsError || controlText(t, res) != "ok" {
		t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
	}
	if action != "nudge" || runID != "r1" || message != "follow the checklist" {
		t.Fatalf("forwarded action=%q run=%q message=%q", action, runID, message)
	}
}

func TestControlNudgeRequiresMessage(t *testing.T) {
	cs := controlClient(t, Services{})
	for _, args := range []map[string]any{
		{"action": "nudge", "run_id": "r1"},
		{"action": "nudge", "run_id": "r1", "message": "  "},
		{"action": "nudge", "run_ids": []any{"r1"}},
	} {
		res := callControl(t, cs, args)
		if !res.IsError || !strings.Contains(controlText(t, res), "message is required") {
			t.Fatalf("args %v: result = %+v, text = %q", args, res, controlText(t, res))
		}
	}
}

func TestControlRejectsMixedSingleAndBatchShapes(t *testing.T) {
	cs := controlClient(t, Services{})
	res := callControl(t, cs, map[string]any{
		"action": "cancel", "run_id": "r1", "run_ids": []any{"r2"},
	})
	if !res.IsError || !strings.Contains(controlText(t, res), "cannot be combined") {
		t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
	}
}

func TestControlApproveRejectsActiveAndOwnRun(t *testing.T) {
	t.Run("running run", func(t *testing.T) {
		store := newControlStore()
		store.add(controlRunInState(t, "r1", core.Event{Kind: core.EvStart}))
		cs := controlClient(t, newControlServices(store))

		res := callControl(t, cs, map[string]any{"action": "approve", "run_id": "r1"})
		if !res.IsError || !strings.Contains(controlText(t, res), "idle run") {
			t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
		}
		if store.approval("r1") != "" {
			t.Fatal("approval was recorded on a running run")
		}
	})
	t.Run("worker approving its own run", func(t *testing.T) {
		store := newControlStore()
		run := core.NewRun("r1", "T-1", time.Now())
		run.AgentIdentity = "worker"
		store.add(run)
		cs := controlClient(t, newControlServices(store))

		res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
			Name:      "beacon_control",
			Arguments: map[string]any{"action": "approve", "run_id": "r1"},
			Meta:      msdk.Meta{"beacon/agent": "worker"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError || !strings.Contains(controlText(t, res), "own run") {
			t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
		}
		if store.approval("r1") != "" {
			t.Fatal("worker self-approval was recorded")
		}
	})
}

func TestWaitRejectsOutOfRangeTimeout(t *testing.T) {
	cs := controlClient(t, Services{})
	for _, timeout := range []any{0, -1, maxWaitMS + 1} {
		res := callTool(t, cs, "beacon_wait", map[string]any{
			"run_id": "r1", "timeout_ms": timeout,
		})
		if !res.IsError || !strings.Contains(controlText(t, res), "invalid_input") {
			t.Fatalf("timeout %v: result = %+v, text = %q", timeout, res, controlText(t, res))
		}
	}
}

func TestDispatchRejectsNonPositiveN(t *testing.T) {
	cs := controlClient(t, Services{})
	for _, n := range []any{0, -1} {
		res := callTool(t, cs, "beacon_dispatch", map[string]any{"n": n})
		if !res.IsError || !strings.Contains(controlText(t, res), "invalid_input") {
			t.Fatalf("n %v: result = %+v, text = %q", n, res, controlText(t, res))
		}
	}
}

func TestTaskCreateRejectsMixedShapes(t *testing.T) {
	dir := t.TempDir()
	creator := &app.TaskCreator{
		Source: tasksource.NewMarkdown(dir), Workspace: &stubCommitter{},
		Root: core.Workspace{Path: dir, Branch: "main"},
	}
	cs := controlClient(t, Services{TaskCreator: creator})

	res := callTool(t, cs, "beacon_task_create", map[string]any{
		"title": "single", "tasks": []any{map[string]any{"title": "batch"}},
	})
	if !res.IsError || !strings.Contains(controlText(t, res), "cannot be combined") {
		t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
	}
}

func TestTaskUpdateRejectsStatus(t *testing.T) {
	cs := controlClient(t, Services{TaskUpdater: &app.TaskUpdater{}})
	res := callTool(t, cs, "beacon_task_update", map[string]any{
		"updates": []any{map[string]any{"id": "T-1", "status": "done"}},
	})
	if !res.IsError || !strings.Contains(controlText(t, res), "status cannot be set") {
		t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
	}
}

func TestTaskUpdateKeepsChildBody(t *testing.T) {
	in := updateInput(updateOneArgs{
		ID:   "T-1",
		Plan: &[]epicChildArg{{Title: "child", Body: "child body", Scope: []string{"a.go"}}},
	})
	if in.Plan == nil || len(*in.Plan) != 1 || (*in.Plan)[0].Body != "child body" {
		t.Fatalf("child plan = %+v", in.Plan)
	}
}

func TestReconcileErrorSetsToolError(t *testing.T) {
	ticker := &stubTickReconciler{err: errors.New("scm down")}
	cs := controlClient(t, Services{Reconcile: &app.ReconcileService{Reconciler: ticker}})

	res := callTool(t, cs, "beacon_reconcile", map[string]any{"pr": 7})
	if !res.IsError {
		t.Fatalf("reconcile error reported success: %s", controlText(t, res))
	}
	var out app.ReconcileResult
	if err := json.Unmarshal([]byte(controlText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Error != "scm down" {
		t.Fatalf("error = %q, want %q", out.Error, "scm down")
	}
}

func TestOrchestratorToolsRejectWorkerCaller(t *testing.T) {
	cs := controlClient(t, Services{})
	cases := []struct {
		name string
		args map[string]any
	}{
		{"beacon_task_create", map[string]any{"title": "x"}},
		{"beacon_task_update", map[string]any{"updates": []any{}}},
		{"beacon_task_delete", map[string]any{"ids": []any{"T-1"}}},
		{"beacon_reconcile", map[string]any{"pr": 7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
				Name: tc.name, Arguments: tc.args,
				Meta: msdk.Meta{"beacon/agent": "worker"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError || !strings.Contains(controlText(t, res), "orchestrator only") {
				t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
			}
		})
	}
}

func TestControlBatchReplayAfterRestart(t *testing.T) {
	store := journalTestStore(t)
	ctx := context.Background()
	for _, id := range []core.RunID{"r1", "r2"} {
		if err := store.Save(ctx, core.NewRun(id, "T-1", time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
	args := map[string]any{
		"action":       "cancel",
		"run_ids":      []any{"r1", "r2"},
		"operation_id": "op-cancel-restart",
	}
	firstServer := controlClient(t, Services{Control: &app.Control{Store: store}})
	first := parseControlBatch(t, callControl(t, firstServer, args))
	if first.Status != "applied" || len(first.Results) != 2 {
		t.Fatalf("first batch = %+v", first)
	}

	// A fresh server has a fresh in-memory journal but reads the same database.
	secondServer := controlClient(t, Services{Control: &app.Control{Store: store}})
	res := callControl(t, secondServer, args)
	second := parseControlBatch(t, res)
	if second.Status != "duplicate" || second.OperationID != "op-cancel-restart" {
		t.Fatalf("replayed metadata = %+v", second)
	}
	if res.IsError {
		t.Fatal("replayed batch reported an error")
	}
	if len(second.Results) != len(first.Results) {
		t.Fatalf("replayed results = %+v, want %+v", second.Results, first.Results)
	}
	for i := range first.Results {
		if second.Results[i] != first.Results[i] {
			t.Fatalf("result %d changed: %+v vs %+v", i, second.Results[i], first.Results[i])
		}
		if second.Results[i].Reason != "" {
			t.Fatalf("replayed result %d was reapplied: %+v", i, second.Results[i])
		}
	}
}

func TestControlBatchReplayKeepsPartialFailure(t *testing.T) {
	store := journalTestStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, core.NewRun("r1", "T-1", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{
		"action":       "cancel",
		"run_ids":      []any{"r1", "missing"},
		"operation_id": "op-partial-restart",
	}
	firstServer := controlClient(t, Services{Control: &app.Control{Store: store}})
	firstRes := callControl(t, firstServer, args)
	first := parseControlBatch(t, firstRes)
	if !firstRes.IsError || len(first.Results) != 2 {
		t.Fatalf("first batch = %+v, result = %+v", first, firstRes)
	}

	secondServer := controlClient(t, Services{Control: &app.Control{Store: store}})
	res := callControl(t, secondServer, args)
	second := parseControlBatch(t, res)
	if !res.IsError {
		t.Fatal("replayed partial batch lost IsError")
	}
	if len(second.Results) != len(first.Results) {
		t.Fatalf("replayed results = %+v, want %+v", second.Results, first.Results)
	}
	for i := range first.Results {
		if second.Results[i] != first.Results[i] {
			t.Fatalf("result %d changed: %+v vs %+v", i, second.Results[i], first.Results[i])
		}
	}
}

func TestControlBatchRetryKeepsSucceededIDs(t *testing.T) {
	store := newControlStore()
	store.add(core.NewRun("r1", "T-1", time.Now()))
	cs := controlClient(t, newControlServices(store))

	args := map[string]any{"action": "cancel", "run_ids": []any{"r1", "missing"}}
	first := parseControlBatch(t, callControl(t, cs, args))
	if first.Results[0].Status != "ok" || first.Results[1].Status != "error" {
		t.Fatalf("first results = %+v", first.Results)
	}

	// A new operation id reissues the batch. The already cancelled run is a
	// no-op success, so it does not flip to an error on the retry.
	args["operation_id"] = "op-retry-partial"
	res := callControl(t, cs, args)
	second := parseControlBatch(t, res)
	if !res.IsError || len(second.Results) != 2 {
		t.Fatalf("retry = %+v", res)
	}
	if second.Results[0].ID != "r1" || second.Results[0].Status != "ok" ||
		second.Results[0].Reason != "already_closed" {
		t.Fatalf("cancelled id changed: %+v", second.Results[0])
	}
	if second.Results[1].ID != "missing" || second.Results[1].Status != "error" {
		t.Fatalf("missing id result = %+v", second.Results[1])
	}
}

func TestControlSingleRepeatCancelStillReturnsOK(t *testing.T) {
	store := newControlStore()
	run := core.NewRun("r1", "T-1", time.Now())
	run.State = core.RunClosed
	run.Reason = "cancelled by operator"
	store.add(run)
	cs := controlClient(t, newControlServices(store))

	res := callControl(t, cs, map[string]any{"action": "cancel", "run_id": "r1"})
	if res.IsError || controlText(t, res) != "ok" {
		t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
	}
}
