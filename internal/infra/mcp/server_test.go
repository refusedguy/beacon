package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/tasksource"
	"github.com/refusedguy/beacon/pkg/core"
)

func TestToolCatalogMatchesRegisteredTools(t *testing.T) {
	server := NewServer(Services{})
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

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	catalog := ToolCatalog()
	byName := make(map[string]ToolSpec, len(catalog))
	for _, spec := range catalog {
		byName[spec.Name] = spec
	}
	if len(byName) != len(catalog) {
		t.Fatalf("ToolCatalog contains duplicate names: %+v", catalog)
	}
	if len(res.Tools) != len(catalog) {
		t.Fatalf("server registered %d tools, catalog has %d", len(res.Tools), len(catalog))
	}

	orchestratorOnly := 0
	for _, tool := range res.Tools {
		spec, ok := byName[tool.Name]
		if !ok {
			t.Fatalf("registered tool %q is missing from ToolCatalog", tool.Name)
		}
		if tool.Description != spec.Description {
			t.Fatalf("tool %q description = %q, catalog = %q", tool.Name, tool.Description, spec.Description)
		}
		if spec.OrchestratorOnly {
			orchestratorOnly++
		}
	}
	if orchestratorOnly != 1 || !byName["beacon_task_create"].OrchestratorOnly {
		t.Fatalf("only task creation should be orchestrator-only: %+v", catalog)
	}
}

type epicGraphStore struct{ calls int }

func (s *epicGraphStore) SyncTasks(context.Context, []core.Task) error {
	s.calls++
	return nil
}

func TestTaskCreateExpandsEpicOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	source := tasksource.NewMarkdown(dir)
	committer := &stubCommitter{}
	store := &epicGraphStore{}
	creator := &app.TaskCreator{
		Source: source, Workspace: committer, Store: store,
		Root: core.Workspace{Path: dir, Branch: "main"},
	}
	server := NewServer(Services{TaskCreator: creator})

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
		Name: "beacon_task_create",
		Arguments: map[string]any{
			"id":    "T-EPIC",
			"title": "Epic",
			"kind":  "epic",
			"plan": []any{
				map[string]any{"title": "Child one", "scope": []string{"a.go"}, "depends": []string{"EXT-1"}, "priority": 7},
				map[string]any{"title": "Child two", "scope": []string{"b.go"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) == 0 {
		t.Fatal("empty tool result")
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatalf("unexpected result: %+v", res.Content)
	}
	var created app.CreatedTask
	if err := json.Unmarshal([]byte(text.Text), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID != "T-EPIC" || len(created.Children) != 2 || len(created.IDs) != 3 {
		t.Fatalf("created = %+v", created)
	}
	if store.calls != 1 || committer.calls != 1 {
		t.Fatalf("sync calls = %d, commit calls = %d", store.calls, committer.calls)
	}

	child, err := os.ReadFile(filepath.Join(dir, "T-EPIC-01.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(child), "base_branch: beacon/T-EPIC") ||
		!strings.Contains(string(child), "- EXT-1") {
		t.Fatalf("child card = %s", child)
	}
	epic, err := os.ReadFile(filepath.Join(dir, "T-EPIC.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"T-EPIC-01", "T-EPIC-02"} {
		if !strings.Contains(string(epic), "- "+id) {
			t.Fatalf("epic card does not depend on %s:\n%s", id, epic)
		}
	}

	res, err = cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name: "beacon_task_create",
		Arguments: map[string]any{
			"id": "T-EPIC", "title": "Epic", "kind": "epic",
			"plan": []any{
				map[string]any{"title": "Child one", "scope": []string{"a.go"}, "depends": []string{"EXT-1"}, "priority": 7},
				map[string]any{"title": "Child two", "scope": []string{"b.go"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	text = res.Content[0].(*msdk.TextContent)
	var again app.CreatedTask
	if err := json.Unmarshal([]byte(text.Text), &again); err != nil {
		t.Fatal(err)
	}
	if again.ID != created.ID || len(again.IDs) != len(created.IDs) {
		t.Fatalf("repeated create = %+v", again)
	}
	if store.calls != 1 || committer.calls != 1 {
		t.Fatalf("repeated create duplicated work: sync = %d, commit = %d", store.calls, committer.calls)
	}
}
