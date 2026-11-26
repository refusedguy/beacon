package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/harnesses/opencode"
	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/planner"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/pkg/core"
)

type dispatchSource struct{ tasks []core.Task }

func (s *dispatchSource) ListAvailable(context.Context) ([]core.Task, error) { return s.tasks, nil }
func (s *dispatchSource) Claim(context.Context, core.TaskID) error           { return nil }
func (s *dispatchSource) Complete(context.Context, core.TaskID) error        { return nil }
func (s *dispatchSource) MarkInWorkspace(context.Context, core.TaskID, string, string) error {
	return nil
}
func (s *dispatchSource) Create(context.Context, core.Task) (core.Task, error) {
	return core.Task{}, nil
}

type dispatchWorkspace struct{}

func (dispatchWorkspace) Create(_ context.Context, t core.Task) (core.Workspace, error) {
	return core.Workspace{Path: filepath.Join("/ws", string(t.ID)), Branch: "beacon/" + string(t.ID)}, nil
}
func (dispatchWorkspace) Clean(context.Context, core.Workspace) (bool, error) { return false, nil }
func (dispatchWorkspace) Remove(context.Context, core.Workspace) error        { return nil }
func (dispatchWorkspace) ForceRemove(context.Context, core.Workspace) error   { return nil }
func (dispatchWorkspace) HasChanges(context.Context, core.Workspace, string) (bool, error) {
	return false, nil
}
func (dispatchWorkspace) CommitPath(context.Context, core.Workspace, string, string) error {
	return nil
}

type failingDispatchWorkspace struct{ dispatchWorkspace }

func (failingDispatchWorkspace) Create(_ context.Context, t core.Task) (core.Workspace, error) {
	if t.ID == "T-BAD" {
		return core.Workspace{}, errors.New("workspace: conflict: refusing to remove dirty worktree")
	}
	return dispatchWorkspace{}.Create(context.Background(), t)
}

type stubDispatchHarness struct{}

func (stubDispatchHarness) Spawn(_ context.Context, req core.SpawnRequest) (core.Session, error) {
	return core.Session{ID: "ses-" + string(req.RunID)}, nil
}
func (stubDispatchHarness) Wake(context.Context, string, core.WakeContext) error { return nil }
func (stubDispatchHarness) Stop(context.Context, string) error                   { return nil }
func (stubDispatchHarness) IsAlive(context.Context, string) (bool, error)        { return false, nil }
func (stubDispatchHarness) NotifyParent(context.Context, string, string) error   { return nil }
func (stubDispatchHarness) Events() <-chan core.HarnessEvent                     { return nil }

func decodeDispatchBody(r *http.Request) map[string]any {
	body := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	return body
}

// parentModel mirrors the composition root's session lookup. The model comes
// from the parent session over HTTP, not from BEACON_MODEL or config.
func parentModel(endpoint, token string) app.ModelResolver {
	return func(parent string) core.ModelRef {
		if parent == "" {
			return core.ModelRef{}
		}
		req, err := http.NewRequest(http.MethodGet, endpoint+"/api/session/"+url.PathEscape(parent), nil)
		if err != nil {
			return core.ModelRef{}
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return core.ModelRef{}
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return core.ModelRef{}
		}
		var info struct {
			Data struct {
				Model struct {
					ID         string `json:"id"`
					ProviderID string `json:"providerID"`
					Variant    string `json:"variant"`
				} `json:"model"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.Data.Model.ID == "" {
			return core.ModelRef{}
		}
		return core.ModelRef{
			ID:         info.Data.Model.ID,
			ProviderID: info.Data.Model.ProviderID,
			Variant:    info.Data.Model.Variant,
		}
	}
}

func TestDispatchToolResolvesParentModel(t *testing.T) {
	var mu sync.Mutex
	var sessions, models []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_parent":
			_, _ = io.WriteString(w, `{"data":{"model":{"id":"hy4-flash-free","providerID":"opencode","variant":"default"}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			mu.Lock()
			sessions = append(sessions, decodeDispatchBody(r))
			mu.Unlock()
			_, _ = io.WriteString(w, `{"id":"ses_worker"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_worker/model":
			mu.Lock()
			models = append(models, decodeDispatchBody(r))
			mu.Unlock()
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_worker/prompt":
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	h := opencode.New(opencode.Config{Endpoint: srv.URL, Token: "test-token"})
	d := &app.Dispatcher{
		Source: &dispatchSource{tasks: []core.Task{{ID: "T-MCP", Status: core.TaskAvailable,
			Scope: []core.ScopePath{core.ScopeFile("mcp.go")}}}},
		Planner: planner.New(planner.Options{}), Workspace: dispatchWorkspace{},
		Harness: h, Store: store,
		Concurrency:  core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
		ResolveModel: parentModel(srv.URL, "test-token"),
	}

	server := NewServer(Services{Dispatcher: d})
	clientTransport, serverTransport := msdk.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := msdk.NewClient(&msdk.Implementation{Name: "test-client"}, nil)
	cs, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name:      "beacon_dispatch",
		Arguments: DispatchArgs{N: 1},
		Meta:      msdk.Meta{"ai.opencode/sessionID": "ses_parent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) == 0 {
		t.Fatal("empty tool result")
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatal("unexpected tool content")
	}
	var payload struct {
		Summary []struct {
			Launch   string `json:"launch_source"`
			Identity string `json:"agent_identity"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(text.Text), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Summary) != 1 || payload.Summary[0].Launch != "mcp" || payload.Summary[0].Identity != "" {
		t.Fatalf("summary = %+v", payload.Summary)
	}

	want := map[string]any{"id": "hy4-flash-free", "providerID": "opencode", "variant": "default"}
	mu.Lock()
	defer mu.Unlock()
	if len(sessions) != 1 {
		t.Fatalf("session creates = %d, want 1: %v", len(sessions), sessions)
	}
	if !reflect.DeepEqual(sessions[0]["model"], want) {
		t.Errorf("session model = %v, want %v", sessions[0]["model"], want)
	}
	if sessions[0]["parentID"] != "ses_parent" {
		t.Errorf("parentID = %v, want ses_parent", sessions[0]["parentID"])
	}
	if len(models) != 1 || !reflect.DeepEqual(models[0]["model"], want) {
		t.Fatalf("model switches = %v, want one with %v", models, want)
	}
}

func TestDispatchToolReportsStartedAndFailedRunsInSummary(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	d := &app.Dispatcher{
		Source: &dispatchSource{tasks: []core.Task{
			{ID: "T-OK", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("ok.go")}},
			{ID: "T-BAD", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("bad.go")}},
		}},
		Planner: planner.New(planner.Options{}), Workspace: failingDispatchWorkspace{},
		Harness: stubDispatchHarness{}, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 2, MaxActiveAgents: 2},
	}
	server := NewServer(Services{Dispatcher: d})
	clientTransport, serverTransport := msdk.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := msdk.NewClient(&msdk.Implementation{Name: "test-client"}, nil)
	cs, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name: "beacon_dispatch", Arguments: DispatchArgs{N: 2},
		Meta: msdk.Meta{"beacon/launchSource": "cli", "beacon/agent": "operator"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatal("unexpected tool content")
	}
	var payload struct {
		Runs []struct {
			ID            string `json:"ID"`
			LaunchSource  string `json:"LaunchSource"`
			AgentIdentity string `json:"AgentIdentity"`
		} `json:"runs"`
		Failures []struct {
			TaskID string `json:"TaskID"`
			Err    string `json:"Err"`
		} `json:"failures"`
		Summary []struct {
			ID       string `json:"id"`
			TaskID   string `json:"task_id"`
			State    string `json:"state"`
			Reason   string `json:"reason"`
			Launch   string `json:"launch_source"`
			Identity string `json:"agent_identity"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(text.Text), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Failures) != 1 || payload.Failures[0].TaskID != "T-BAD" {
		t.Fatalf("failures = %+v", payload.Failures)
	}
	if len(payload.Runs) != 2 {
		t.Fatalf("runs = %+v, want the started and the failed run", payload.Runs)
	}
	if len(payload.Summary) != 2 {
		t.Fatalf("summary = %+v, want two outcomes", payload.Summary)
	}
	byTask := map[string]struct{ id, state, reason string }{}
	for _, outcome := range payload.Summary {
		if outcome.ID == "" || outcome.State == "" || outcome.Reason == "" {
			t.Fatalf("incomplete outcome = %+v", outcome)
		}
		if outcome.Launch != "cli" || outcome.Identity != "operator" {
			t.Fatalf("outcome launch = %q, agent = %q", outcome.Launch, outcome.Identity)
		}
		byTask[outcome.TaskID] = struct{ id, state, reason string }{outcome.ID, outcome.State, outcome.Reason}
	}
	if byTask["T-OK"].state != "running" || byTask["T-BAD"].state != "closed" {
		t.Fatalf("outcomes = %+v, want running and closed", byTask)
	}
	if !strings.Contains(byTask["T-BAD"].reason, "dirty worktree") {
		t.Fatalf("failure reason = %q", byTask["T-BAD"].reason)
	}
	if byTask["T-OK"].id == byTask["T-BAD"].id || !strings.HasPrefix(byTask["T-BAD"].id, "run-") {
		t.Fatalf("run ids = %q and %q", byTask["T-OK"].id, byTask["T-BAD"].id)
	}
}

// countingDispatchStore serves recent runs and records how often and with
// which limit they were requested. Only ListRecent matters to failedRuns; the
// other DispatchStore methods keep the interface satisfied.
type countingDispatchStore struct {
	runs     []*core.Run
	calls    int
	gotLimit int
}

func (*countingDispatchStore) SyncTasks(context.Context, []core.Task) error        { return nil }
func (*countingDispatchStore) ListReadyTasks(context.Context) ([]core.Task, error) { return nil, nil }
func (*countingDispatchStore) ListActive(context.Context) ([]*core.Run, error)     { return nil, nil }
func (s *countingDispatchStore) ListRecent(_ context.Context, limit int) ([]*core.Run, error) {
	s.calls++
	s.gotLimit = limit
	return s.runs, nil
}
func (*countingDispatchStore) FindActiveByTask(context.Context, core.TaskID) (*core.Run, error) {
	return nil, nil
}
func (*countingDispatchStore) Save(context.Context, *core.Run) error                    { return nil }
func (*countingDispatchStore) SetTaskStatus(context.Context, core.TaskID, string) error { return nil }

func TestFailedRunsIndexesRecentOnce(t *testing.T) {
	const total, failed = 200, 50
	store := &countingDispatchStore{}
	failures := make([]app.DispatchFailure, 0, failed)

	// ListRecent returns newest first. The newest 50 tasks failed to start.
	for i := total - 1; i >= 0; i-- {
		taskID := core.TaskID(fmt.Sprintf("T-%03d", i))
		state := core.RunMerged
		if i >= total-failed {
			state = core.RunClosed
		}
		store.runs = append(store.runs, &core.Run{
			ID:            core.RunID(fmt.Sprintf("run-%03d", i)),
			TaskID:        taskID,
			State:         state,
			Reason:        string(state),
			LaunchSource:  core.LaunchMCP,
			AgentIdentity: "worker",
		})
		if i >= total-failed {
			failures = append(failures, app.DispatchFailure{TaskID: taskID, Err: "workspace: conflict"})
		}
	}

	got := failedRuns(context.Background(), &app.Dispatcher{Store: store}, failures)
	if store.calls != 1 {
		t.Fatalf("ListRecent calls = %d, want 1 with %d failures", store.calls, failed)
	}
	if store.gotLimit != failed+app.DefaultRecentLimit {
		t.Fatalf("ListRecent limit = %d, want %d", store.gotLimit, failed+app.DefaultRecentLimit)
	}
	if len(got) != failed {
		t.Fatalf("resolved failures = %d, want %d", len(got), failed)
	}

	// The previous nested scan is the reference for unchanged output.
	reference := map[core.TaskID]*core.Run{}
	for _, failure := range failures {
		for _, run := range store.runs {
			if run.TaskID == failure.TaskID {
				reference[failure.TaskID] = run
				break
			}
		}
	}
	if !reflect.DeepEqual(got, reference) {
		t.Fatalf("resolved runs differ from the previous scan")
	}

	// Build the caller-facing summary the same way registerDispatch does and
	// compare the wire JSON. Order follows the failure list, not the index.
	summaryFor := func(byTask map[core.TaskID]*core.Run) []dispatchOutcome {
		out := make([]dispatchOutcome, 0, len(failures))
		for _, failure := range failures {
			if run := byTask[failure.TaskID]; run != nil {
				out = append(out, outcomeForRun(run))
				continue
			}
			out = append(out, dispatchOutcome{
				ID: string(failure.TaskID), TaskID: string(failure.TaskID),
				State: "failed", Reason: failure.Err,
				Launch: string(core.LaunchMCP), Identity: "worker",
			})
		}
		return out
	}
	gotJSON, err := json.Marshal(summaryFor(got))
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(summaryFor(reference))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("summary JSON changed:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func TestFailedRunsKeepsNewestRunForTask(t *testing.T) {
	older := &core.Run{ID: "run-old", TaskID: "T-DUP", State: core.RunClosed, Reason: "old"}
	newer := &core.Run{ID: "run-new", TaskID: "T-DUP", State: core.RunClosed, Reason: "new"}
	store := &countingDispatchStore{runs: []*core.Run{newer, older}}
	got := failedRuns(context.Background(), &app.Dispatcher{Store: store}, []app.DispatchFailure{{TaskID: "T-DUP"}})
	if got["T-DUP"] != newer {
		t.Fatalf("resolved run = %v, want the newest %v", got["T-DUP"], newer)
	}
}
