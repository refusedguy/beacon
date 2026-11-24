package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
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

func decodeDispatchBody(r *http.Request) map[string]any {
	body := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	return body
}

// parentModel mirrors the composition root's session lookup. The model comes
// from the parent session over HTTP, not from BEACON_MODEL or config.
func parentModel(endpoint, token string) app.ModelResolver {
	return func(parent string) map[string]string {
		if parent == "" {
			return nil
		}
		req, err := http.NewRequest(http.MethodGet, endpoint+"/api/session/"+url.PathEscape(parent), nil)
		if err != nil {
			return nil
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return nil
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
			return nil
		}
		ref := map[string]string{"id": info.Data.Model.ID, "providerID": info.Data.Model.ProviderID}
		if info.Data.Model.Variant != "" {
			ref["variant"] = info.Data.Model.Variant
		}
		return ref
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

	h := opencode.New(srv.URL, "", "", "test-token")
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
