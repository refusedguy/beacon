package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if orchestratorOnly != 2 || !byName["beacon_task_create"].OrchestratorOnly ||
		!byName["beacon_task_delete"].OrchestratorOnly {
		t.Fatalf("only task creation and deletion should be orchestrator-only: %+v", catalog)
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

type deleteRunStore struct {
	active map[string]bool
	err    error
}

func (s *deleteRunStore) FindActiveByTask(_ context.Context, id core.TaskID) (*core.Run, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.active[string(id)] {
		return core.NewRun(core.RunID("run-"+string(id)), id, time.Now().UTC()), nil
	}
	return nil, nil
}

type deleteCommitter struct {
	calls int
	err   error
}

func (s *deleteCommitter) CommitPath(context.Context, core.Workspace, string, string) error {
	s.calls++
	return s.err
}

func deleteClient(t *testing.T, sv Services) *msdk.ClientSession {
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

func writeDeleteCards(t *testing.T, dir string, ids ...string) {
	t.Helper()
	cards := filepath.Join(dir, "docs", "tasks")
	if err := os.MkdirAll(cards, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		body := "---\nid: " + id + "\n---\n# " + id + ". Card\n"
		if err := os.WriteFile(filepath.Join(cards, id+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func cardExistsAt(t *testing.T, dir, id string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, "docs", "tasks", id+".md"))
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	t.Fatal(err)
	return false
}

func callDelete(t *testing.T, cs *msdk.ClientSession, ids ...string) app.DeleteResult {
	t.Helper()
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name:      "beacon_task_delete",
		Arguments: map[string]any{"ids": args},
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
	var deleted app.DeleteResult
	if err := json.Unmarshal([]byte(text.Text), &deleted); err != nil {
		t.Fatal(err)
	}
	return deleted
}

func TestTaskDeleteOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	writeDeleteCards(t, dir, "T-A", "T-KEEP")
	committer := &deleteCommitter{}
	deleter := &app.TaskDeleter{
		Store: &deleteRunStore{}, Workspace: committer,
		Root: core.Workspace{Path: dir, Branch: "main"},
	}
	cs := deleteClient(t, Services{TaskDeleter: deleter})

	got := callDelete(t, cs, "T-A")
	if !got.Pushed || len(got.Results) != 1 || got.Results[0].Status != "deleted" {
		t.Fatalf("result = %+v", got)
	}
	if cardExistsAt(t, dir, "T-A") || !cardExistsAt(t, dir, "T-KEEP") {
		t.Fatal("wrong card removed")
	}
	if committer.calls != 1 {
		t.Fatalf("commit calls = %d", committer.calls)
	}
}

func TestTaskDeleteBatchOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	writeDeleteCards(t, dir, "T-A", "T-B", "T-C")
	committer := &deleteCommitter{}
	deleter := &app.TaskDeleter{
		Store: &deleteRunStore{}, Workspace: committer,
		Root: core.Workspace{Path: dir, Branch: "main"},
	}
	cs := deleteClient(t, Services{TaskDeleter: deleter})

	got := callDelete(t, cs, "T-A", "T-B")
	if !got.Pushed || len(got.Results) != 2 {
		t.Fatalf("result = %+v", got)
	}
	if cardExistsAt(t, dir, "T-A") || cardExistsAt(t, dir, "T-B") || !cardExistsAt(t, dir, "T-C") {
		t.Fatal("wrong cards removed")
	}
	if committer.calls != 1 {
		t.Fatalf("batch should commit once, got %d", committer.calls)
	}
}

func TestTaskDeleteNotFoundOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	writeDeleteCards(t, dir, "T-A")
	committer := &deleteCommitter{}
	deleter := &app.TaskDeleter{
		Store: &deleteRunStore{}, Workspace: committer,
		Root: core.Workspace{Path: dir, Branch: "main"},
	}
	cs := deleteClient(t, Services{TaskDeleter: deleter})

	got := callDelete(t, cs, "T-MISSING")
	if got.Pushed || len(got.Results) != 1 || got.Results[0].Status != "not_found" {
		t.Fatalf("result = %+v", got)
	}
	if committer.calls != 0 {
		t.Fatal("not_found delete still committed")
	}
	got = callDelete(t, cs, "T-MISSING")
	if got.Results[0].Status != "not_found" {
		t.Fatalf("repeated delete = %+v", got)
	}
}

func TestTaskDeleteRefusesActiveRunOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	writeDeleteCards(t, dir, "T-A", "T-B")
	committer := &deleteCommitter{}
	deleter := &app.TaskDeleter{
		Store:     &deleteRunStore{active: map[string]bool{"T-B": true}},
		Workspace: committer,
		Root:      core.Workspace{Path: dir, Branch: "main"},
	}
	cs := deleteClient(t, Services{TaskDeleter: deleter})

	got := callDelete(t, cs, "T-A", "T-B")
	if len(got.Refused) != 1 || got.Refused[0] != "T-B" || got.Pushed {
		t.Fatalf("result = %+v", got)
	}
	if !cardExistsAt(t, dir, "T-A") || !cardExistsAt(t, dir, "T-B") {
		t.Fatal("refusal touched the tree")
	}
	if committer.calls != 0 {
		t.Fatal("refusal still committed")
	}
}

func TestTaskDeleteRollsBackWhenCommitFails(t *testing.T) {
	dir := t.TempDir()
	writeDeleteCards(t, dir, "T-A", "T-B")
	committer := &deleteCommitter{err: errors.New("push failed")}
	deleter := &app.TaskDeleter{
		Store: &deleteRunStore{}, Workspace: committer,
		Root: core.Workspace{Path: dir, Branch: "main"},
	}
	cs := deleteClient(t, Services{TaskDeleter: deleter})

	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name:      "beacon_task_delete",
		Arguments: map[string]any{"ids": []any{"T-A", "T-B"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected a tool error when the commit fails")
	}
	if !cardExistsAt(t, dir, "T-A") || !cardExistsAt(t, dir, "T-B") {
		t.Fatal("failed commit did not restore cards")
	}
}
