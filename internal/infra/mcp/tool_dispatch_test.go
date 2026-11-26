package mcp

import (
	"context"
	"encoding/json"
	"errors"
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
	if len(payload.Runs) != 1 {
		t.Fatalf("runs = %+v, want only the started run", payload.Runs)
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
	if byTask["T-OK"].state != "running" || byTask["T-BAD"].state != "failed" {
		t.Fatalf("outcomes = %+v, want running and failed", byTask)
	}
	if !strings.Contains(byTask["T-BAD"].reason, "dirty worktree") {
		t.Fatalf("failure reason = %q", byTask["T-BAD"].reason)
	}
	if byTask["T-BAD"].id != "T-BAD" || !strings.HasPrefix(byTask["T-OK"].id, "run-") {
		t.Fatalf("outcome ids = %q and %q", byTask["T-OK"].id, byTask["T-BAD"].id)
	}
}

// countingDispatchStore counts ListRecent calls so a test can prove a failed
// spawn does not trigger a recent-run query. The other DispatchStore methods
// keep the interface satisfied and return the ready task for dispatch.
type countingDispatchStore struct {
	ready    []core.Task
	calls    int
	gotLimit int
}

func (*countingDispatchStore) SyncTasks(context.Context, []core.Task) error { return nil }
func (s *countingDispatchStore) ListReadyTasks(context.Context) ([]core.Task, error) {
	return s.ready, nil
}
func (*countingDispatchStore) ListActive(context.Context) ([]*core.Run, error) { return nil, nil }
func (s *countingDispatchStore) ListRecent(_ context.Context, limit int) ([]*core.Run, error) {
	s.calls++
	s.gotLimit = limit
	return nil, nil
}
func (*countingDispatchStore) FindActiveByTask(context.Context, core.TaskID) (*core.Run, error) {
	return nil, nil
}
func (*countingDispatchStore) Save(context.Context, *core.Run) error                    { return nil }
func (*countingDispatchStore) SetTaskStatus(context.Context, core.TaskID, string) error { return nil }

func TestDispatchFailureSkipsRecentLookup(t *testing.T) {
	task := core.Task{ID: "T-BAD", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("bad.go")}}
	store := &countingDispatchStore{ready: []core.Task{task}}
	d := &app.Dispatcher{
		Source:  &dispatchSource{tasks: []core.Task{task}},
		Planner: planner.New(planner.Options{}), Workspace: failingDispatchWorkspace{},
		Harness: stubDispatchHarness{}, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
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
		Name: "beacon_dispatch", Arguments: DispatchArgs{N: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) == 0 {
		t.Fatal("empty dispatch result")
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatal("unexpected tool content")
	}
	var payload struct {
		Summary []struct {
			ID     string `json:"id"`
			TaskID string `json:"task_id"`
			State  string `json:"state"`
			Reason string `json:"reason"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(text.Text), &payload); err != nil {
		t.Fatal(err)
	}
	if store.calls != 0 {
		t.Fatalf("ListRecent calls = %d, want 0 after a failed spawn", store.calls)
	}
	if len(payload.Summary) != 1 || payload.Summary[0].ID != "T-BAD" ||
		payload.Summary[0].State != "failed" || !strings.Contains(payload.Summary[0].Reason, "dirty worktree") {
		t.Fatalf("summary = %+v, want one failed outcome for T-BAD", payload.Summary)
	}
}

type parentCapturingHarness struct {
	stubDispatchHarness
	parents []string
}

func (h *parentCapturingHarness) Spawn(_ context.Context, req core.SpawnRequest) (core.Session, error) {
	h.parents = append(h.parents, req.ParentSessionID)
	return core.Session{ID: "ses-" + string(req.RunID)}, nil
}

// dispatchWithMeta calls beacon_dispatch over the in-memory transport and
// returns the harness that captured each spawn's parent session together with
// the store that persisted the run.
func dispatchWithMeta(t *testing.T, meta msdk.Meta) (*parentCapturingHarness, *sqlite.Store) {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	h := &parentCapturingHarness{}
	d := &app.Dispatcher{
		Source: &dispatchSource{tasks: []core.Task{{ID: "T-PARENT", Status: core.TaskAvailable,
			Scope: []core.ScopePath{core.ScopeFile("parent.go")}}}},
		Planner: planner.New(planner.Options{}), Workspace: dispatchWorkspace{},
		Harness: h, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
	}
	server := NewServer(Services{Dispatcher: d})
	clientTransport, serverTransport := msdk.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	client := msdk.NewClient(&msdk.Implementation{Name: "test-client"}, nil)
	cs, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name: "beacon_dispatch", Arguments: DispatchArgs{N: 1}, Meta: meta,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) == 0 {
		t.Fatal("empty dispatch result")
	}
	return h, store
}

func TestDispatchToolFallsBackToEnvParentWithoutMeta(t *testing.T) {
	t.Setenv("BEACON_PARENT_SESSION", "ses_operator")
	h, store := dispatchWithMeta(t, nil)
	if !reflect.DeepEqual(h.parents, []string{"ses_operator"}) {
		t.Fatalf("spawn parents = %v, want [ses_operator]", h.parents)
	}
	active, err := store.ListActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ParentSessionID != "ses_operator" {
		t.Fatalf("persisted runs = %+v, want one with parent ses_operator", active)
	}
}

func TestDispatchToolMetaOverridesEnvParent(t *testing.T) {
	t.Setenv("BEACON_PARENT_SESSION", "ses_env")
	h, store := dispatchWithMeta(t, msdk.Meta{"ai.opencode/sessionID": "ses_meta"})
	if !reflect.DeepEqual(h.parents, []string{"ses_meta"}) {
		t.Fatalf("spawn parents = %v, want [ses_meta]", h.parents)
	}
	active, err := store.ListActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ParentSessionID != "ses_meta" {
		t.Fatalf("persisted runs = %+v, want one with parent ses_meta", active)
	}
}

func TestParentSessionAcceptsAdditionalMetaKeys(t *testing.T) {
	keys := []string{
		"ai.opencode/sessionId", "beacon/sessionId", "opencode/sessionID",
		"sessionID", "sessionId",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			t.Setenv("BEACON_PARENT_SESSION", "ses_env")
			req := &msdk.CallToolRequest{}
			req.Params = &msdk.CallToolParamsRaw{Meta: msdk.Meta{key: "ses_meta"}}
			if got := parentSession(req); got != "ses_meta" {
				t.Fatalf("parentSession(%q) = %q, want ses_meta", key, got)
			}
		})
	}
}

func TestParentSessionUsesEnvWithoutMeta(t *testing.T) {
	t.Setenv("BEACON_PARENT_SESSION", "ses_operator")
	if got := parentSession(nil); got != "ses_operator" {
		t.Fatalf("parentSession(nil) = %q, want ses_operator", got)
	}
	t.Setenv("BEACON_PARENT_SESSION", "")
	if got := parentSession(nil); got != "" {
		t.Fatalf("parentSession(nil) = %q, want empty", got)
	}
}
