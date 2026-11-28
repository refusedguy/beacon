package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
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
		t.Fatalf("server.Connect(context.Background(), serverTransport, nil): %v", err)
	}
	defer ss.Close()

	client := msdk.NewClient(&msdk.Implementation{Name: "test-client"}, nil)
	cs, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect(context.Background(), clientTransport, nil): %v", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("cs.ListTools(context.Background(), nil): %v", err)
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
	if orchestratorOnly != 6 || !byName["beacon_task_create"].OrchestratorOnly ||
		!byName["beacon_task_delete"].OrchestratorOnly || !byName["beacon_task_update"].OrchestratorOnly ||
		!byName["beacon_task_close"].OrchestratorOnly || !byName["beacon_task_start"].OrchestratorOnly ||
		!byName["beacon_reconcile"].OrchestratorOnly {
		t.Fatalf("only task creation, deletion, update, close, start and reconcile should be orchestrator-only: %+v", catalog)
	}
}

// workerToolNames is the golden list of tools workers may call. It is sorted
// by name. A worker tool added, removed or renamed changes the permission gate,
// so it must land with a catalog edit and a regenerated opencode.json.
var workerToolNames = []string{
	"beacon_control",
	"beacon_dispatch",
	"beacon_recent",
	"beacon_status",
	"beacon_wait",
	"beacon_watch_pr",
}

func TestWorkerToolNamesArePinned(t *testing.T) {
	var got []string
	for _, spec := range ToolCatalog() {
		if !spec.OrchestratorOnly {
			got = append(got, spec.Name)
		}
	}
	sort.Strings(got)
	if !slices.Equal(got, workerToolNames) {
		t.Fatalf("worker tools = %q, want golden list %q", got, workerToolNames)
	}
}

func TestToolRegistrationsCoverCatalog(t *testing.T) {
	catalog := ToolCatalog()
	if len(toolRegistrations) != len(catalog) {
		t.Fatalf("toolRegistrations has %d entries, ToolCatalog has %d", len(toolRegistrations), len(catalog))
	}
	for _, spec := range catalog {
		tool := catalogTool(spec.Name)
		if tool.Name != spec.Name || tool.Description != spec.Description {
			t.Fatalf("catalogTool(%q) = %+v, want name %q and description %q", spec.Name, tool, spec.Name, spec.Description)
		}
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
		t.Fatalf("server.Connect(context.Background(), serverTransport, nil): %v", err)
	}
	defer ss.Close()
	client := msdk.NewClient(&msdk.Implementation{Name: "test-client"}, nil)
	cs, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect(context.Background(), clientTransport, nil): %v", err)
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
		t.Fatalf("cs.CallTool(Name: \"beacon_task_create\"): %v", err)
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
		t.Fatalf("json.Unmarshal([]byte(text.Text), &created): %v", err)
	}
	if created.ID != "T-EPIC" || len(created.Children) != 2 || len(created.IDs) != 3 {
		t.Fatalf("created = %+v", created)
	}
	if store.calls != 1 || committer.calls != 1 {
		t.Fatalf("sync calls = %d, commit calls = %d", store.calls, committer.calls)
	}

	child, err := os.ReadFile(filepath.Join(dir, "T-EPIC-01.md"))
	if err != nil {
		t.Fatalf("os.ReadFile(filepath.Join(dir, \"T-EPIC-01.md\")): %v", err)
	}
	if !strings.Contains(string(child), "base_branch: beacon/T-EPIC") ||
		!strings.Contains(string(child), "- EXT-1") {
		t.Fatalf("child card = %s", child)
	}
	epic, err := os.ReadFile(filepath.Join(dir, "T-EPIC.md"))
	if err != nil {
		t.Fatalf("os.ReadFile(filepath.Join(dir, \"T-EPIC.md\")): %v", err)
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
		t.Fatalf("cs.CallTool(Name: \"beacon_task_create\"): %v", err)
	}
	text = res.Content[0].(*msdk.TextContent)
	var again app.CreatedTask
	if err := json.Unmarshal([]byte(text.Text), &again); err != nil {
		t.Fatalf("json.Unmarshal([]byte(text.Text), &again): %v", err)
	}
	if again.ID != created.ID || len(again.IDs) != len(created.IDs) {
		t.Fatalf("repeated create = %+v", again)
	}
	if store.calls != 1 || committer.calls != 1 {
		t.Fatalf("repeated create duplicated work: sync = %d, commit = %d", store.calls, committer.calls)
	}
}

func TestTaskCreateChildBodiesOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	source := tasksource.NewMarkdown(dir)
	creator := &app.TaskCreator{
		Source: source, Workspace: &stubCommitter{},
		Root: core.Workspace{Path: dir, Branch: "main"},
	}
	server := NewServer(Services{TaskCreator: creator})

	clientTransport, serverTransport := msdk.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect(context.Background(), serverTransport, nil): %v", err)
	}
	defer ss.Close()
	client := msdk.NewClient(&msdk.Implementation{Name: "test-client"}, nil)
	cs, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect(context.Background(), clientTransport, nil): %v", err)
	}
	defer cs.Close()

	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name: "beacon_task_create",
		Arguments: map[string]any{
			"id": "T-EPIC", "title": "Epic", "kind": "epic",
			"plan": []any{
				map[string]any{"title": "Child one", "body": "Write the first module."},
				map[string]any{"title": "Child two", "priority": 4},
			},
		},
	})
	if err != nil {
		t.Fatalf("cs.CallTool(Name: \"beacon_task_create\"): %v", err)
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
		t.Fatalf("json.Unmarshal([]byte(text.Text), &created): %v", err)
	}
	if created.ID != "T-EPIC" || len(created.Children) != 2 {
		t.Fatalf("created = %+v", created)
	}

	first, err := os.ReadFile(filepath.Join(dir, "T-EPIC-01.md"))
	if err != nil {
		t.Fatalf("os.ReadFile(filepath.Join(dir, \"T-EPIC-01.md\")): %v", err)
	}
	if !strings.Contains(string(first), "Write the first module.") {
		t.Fatalf("first child card missing body:\n%s", first)
	}
	second, err := os.ReadFile(filepath.Join(dir, "T-EPIC-02.md"))
	if err != nil {
		t.Fatalf("os.ReadFile(filepath.Join(dir, \"T-EPIC-02.md\")): %v", err)
	}
	if strings.Contains(string(second), "Write the first module.") {
		t.Fatalf("second child carried the first body:\n%s", second)
	}

	epic, err := os.ReadFile(filepath.Join(dir, "T-EPIC.md"))
	if err != nil {
		t.Fatalf("os.ReadFile(filepath.Join(dir, \"T-EPIC.md\")): %v", err)
	}
	if !strings.Contains(string(epic), "body: Write the first module.") {
		t.Fatalf("epic card plan missing child body:\n%s", epic)
	}
	if strings.HasPrefix(strings.TrimSpace(string(epic)), "body:") {
		t.Fatalf("child body leaked into epic frontmatter outside plan:\n%s", epic)
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
		t.Fatalf("server.Connect(context.Background(), serverTransport, nil): %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := msdk.NewClient(&msdk.Implementation{Name: "test-client"}, nil)
	cs, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect(context.Background(), clientTransport, nil): %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func writeDeleteCards(t *testing.T, dir string, ids ...string) {
	t.Helper()
	cards := filepath.Join(dir, "docs", "tasks")
	if err := os.MkdirAll(cards, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(cards, 0o755): %v", err)
	}
	for _, id := range ids {
		body := "---\nid: " + id + "\n---\n# " + id + ". Card\n"
		if err := os.WriteFile(filepath.Join(cards, id+".md"), []byte(body), 0o644); err != nil {
			t.Fatalf("os.WriteFile(filepath.Join(cards, id+\".md\"), []byte(body), 0o644): %v", err)
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
	t.Fatalf("os.Stat(filepath.Join(dir, \"docs\", \"tasks\", id+\".md\")): %v", err)
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
		t.Fatalf("cs.CallTool(Name: \"beacon_task_delete\"): %v", err)
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
		t.Fatalf("json.Unmarshal([]byte(text.Text), &deleted): %v", err)
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
		t.Fatalf("cs.CallTool(Name: \"beacon_task_delete\"): %v", err)
	}
	if !res.IsError {
		t.Fatal("expected a tool error when the commit fails")
	}
	if !cardExistsAt(t, dir, "T-A") || !cardExistsAt(t, dir, "T-B") {
		t.Fatal("failed commit did not restore cards")
	}
}

func writeUpdateTestCard(t *testing.T, dir, id, content string) string {
	t.Helper()
	cards := filepath.Join(dir, "docs", "tasks")
	if err := os.MkdirAll(cards, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(cards, 0o755): %v", err)
	}
	path := filepath.Join(cards, id+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(path, []byte(content), 0o644): %v", err)
	}
	return path
}

func updateService(dir string, store app.ActiveRunFinder, ws app.PathCommitter) Services {
	return Services{TaskUpdater: &app.TaskUpdater{
		Store: store, Workspace: ws,
		Root: core.Workspace{Path: dir, Branch: "main"},
	}}
}

func callUpdate(t *testing.T, cs *msdk.ClientSession, updates ...map[string]any) app.UpdateResult {
	t.Helper()
	args := make([]any, 0, len(updates))
	for _, update := range updates {
		args = append(args, update)
	}
	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name:      "beacon_task_update",
		Arguments: map[string]any{"updates": args},
	})
	if err != nil {
		t.Fatalf("cs.CallTool(Name: \"beacon_task_update\"): %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("empty tool result")
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatalf("unexpected result: %+v", res.Content)
	}
	var updated app.UpdateResult
	if err := json.Unmarshal([]byte(text.Text), &updated); err != nil {
		t.Fatalf("json.Unmarshal([]byte(text.Text), &updated): %v", err)
	}
	return updated
}

func TestTaskUpdateSingleOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	path := writeUpdateTestCard(t, dir, "T-A",
		"---\nid: T-A\ntitle: Old\nstatus: available\npriority: 5\n---\nbody\n")
	committer := &deleteCommitter{}
	cs := deleteClient(t, updateService(dir, &deleteRunStore{}, committer))

	got := callUpdate(t, cs, map[string]any{
		"id": "T-A", "title": "New title",
	})
	if !got.Pushed || len(got.Results) != 1 || got.Results[0].Status != "updated" {
		t.Fatalf("result = %+v", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(path): %v", err)
	}
	if !strings.Contains(string(content), "title: New title") ||
		!strings.Contains(string(content), "status: available") ||
		!strings.Contains(string(content), "priority: 5") {
		t.Fatalf("updated card = %s", content)
	}
	if committer.calls != 1 {
		t.Fatalf("commit calls = %d", committer.calls)
	}
}

func TestTaskUpdateBatchOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	a := writeUpdateTestCard(t, dir, "T-A", "---\nid: T-A\ntitle: Old A\n---\nbody\n")
	b := writeUpdateTestCard(t, dir, "T-B", "---\nid: T-B\ntitle: Old B\n---\nbody\n")
	committer := &deleteCommitter{}
	cs := deleteClient(t, updateService(dir, &deleteRunStore{}, committer))

	got := callUpdate(t, cs,
		map[string]any{"id": "T-A", "title": "New A"},
		map[string]any{"id": "T-B", "title": "New B"},
	)
	if !got.Pushed || len(got.Results) != 2 ||
		got.Results[0].Status != "updated" || got.Results[1].Status != "updated" {
		t.Fatalf("result = %+v", got)
	}
	contentA, err := os.ReadFile(a)
	if err != nil {
		t.Fatalf("os.ReadFile(a): %v", err)
	}
	contentB, err := os.ReadFile(b)
	if err != nil {
		t.Fatalf("os.ReadFile(b): %v", err)
	}
	if !strings.Contains(string(contentA), "title: New A") || !strings.Contains(string(contentB), "title: New B") {
		t.Fatal("batch did not update both cards")
	}
	if committer.calls != 1 {
		t.Fatalf("batch should commit once, got %d", committer.calls)
	}
}

func TestTaskUpdateNotFoundOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	committer := &deleteCommitter{}
	cs := deleteClient(t, updateService(dir, &deleteRunStore{}, committer))

	got := callUpdate(t, cs, map[string]any{"id": "T-MISSING", "title": "New"})
	if got.Pushed || len(got.Results) != 1 || got.Results[0].Status != "not_found" {
		t.Fatalf("result = %+v", got)
	}
	if committer.calls != 0 {
		t.Fatal("not_found still committed")
	}
}

func TestTaskUpdateUnchangedOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	writeUpdateTestCard(t, dir, "T-A", "---\nid: T-A\ntitle: Same\n---\nbody\n")
	committer := &deleteCommitter{}
	cs := deleteClient(t, updateService(dir, &deleteRunStore{}, committer))

	got := callUpdate(t, cs, map[string]any{"id": "T-A", "title": "Same"})
	if got.Pushed || len(got.Results) != 1 || got.Results[0].Status != "unchanged" {
		t.Fatalf("result = %+v", got)
	}
	if committer.calls != 0 {
		t.Fatal("unchanged still committed")
	}
}

func TestTaskUpdateRefusesActiveRunOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	a := writeUpdateTestCard(t, dir, "T-A", "---\nid: T-A\ntitle: Old A\n---\nbody\n")
	b := writeUpdateTestCard(t, dir, "T-B", "---\nid: T-B\ntitle: Old B\n---\nbody\n")
	committer := &deleteCommitter{}
	store := &deleteRunStore{active: map[string]bool{"T-B": true}}
	cs := deleteClient(t, updateService(dir, store, committer))

	got := callUpdate(t, cs,
		map[string]any{"id": "T-A", "title": "New A"},
		map[string]any{"id": "T-B", "title": "New B"},
	)
	if len(got.Refused) != 1 || got.Refused[0] != "T-B" || got.Pushed {
		t.Fatalf("result = %+v", got)
	}
	for path, want := range map[string]string{a: "Old A", b: "Old B"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile(path): %v", err)
		}
		if !strings.Contains(string(content), want) {
			t.Fatalf("refusal touched %s: %s", path, content)
		}
	}
	if committer.calls != 0 {
		t.Fatal("refusal still committed")
	}
}

func TestTaskUpdateRollsBackWhenCommitFails(t *testing.T) {
	dir := t.TempDir()
	path := writeUpdateTestCard(t, dir, "T-A", "---\nid: T-A\ntitle: Old\n---\nbody\n")
	committer := &deleteCommitter{err: errors.New("push failed")}
	cs := deleteClient(t, updateService(dir, &deleteRunStore{}, committer))

	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name: "beacon_task_update",
		Arguments: map[string]any{
			"updates": []any{map[string]any{"id": "T-A", "title": "New"}},
		},
	})
	if err != nil {
		t.Fatalf("cs.CallTool(Name: \"beacon_task_update\"): %v", err)
	}
	if !res.IsError {
		t.Fatal("expected a tool error when the commit fails")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(path): %v", err)
	}
	if string(content) != "---\nid: T-A\ntitle: Old\n---\nbody\n" {
		t.Fatalf("failed commit did not restore card: %s", content)
	}
}

type stubTickReconciler struct {
	calls    int
	pr       int
	decision core.MergeDecision
	err      error
}

func (s *stubTickReconciler) Reconcile(_ context.Context, pr int) (core.MergeDecision, error) {
	s.calls++
	s.pr = pr
	return s.decision, s.err
}

func callReconcile(t *testing.T, cs *msdk.ClientSession, pr int) app.ReconcileResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name:      "beacon_reconcile",
		Arguments: map[string]any{"pr": pr},
	})
	if err != nil {
		t.Fatalf("cs.CallTool(Name: \"beacon_reconcile\"): %v", err)
	}
	// A reconcile that reports an error sets IsError while still carrying the
	// structured decision body. Callers read the body either way.
	if len(res.Content) == 0 {
		t.Fatalf("unexpected reconcile result: %+v", res)
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatalf("unexpected result: %+v", res.Content)
	}
	var out app.ReconcileResult
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("json.Unmarshal([]byte(text.Text), &out): %v", err)
	}
	return out
}

func TestReconcileOverInMemoryTransport(t *testing.T) {
	ticker := &stubTickReconciler{decision: core.MergeDecision{
		Verdict: core.VerdictWait, Reason: "checks pending", Criterion: "checks",
	}}
	cs := deleteClient(t, Services{Reconcile: &app.ReconcileService{Reconciler: ticker}})

	got := callReconcile(t, cs, 42)
	if ticker.calls != 1 || ticker.pr != 42 {
		t.Fatalf("reconcile calls = %d, pr = %d", ticker.calls, ticker.pr)
	}
	if got.PR != 42 || got.Decision.Verdict != "wait" ||
		got.Decision.Reason != "checks pending" || got.Decision.Criterion != "checks" {
		t.Fatalf("result = %+v", got)
	}
	if got.Error != "" {
		t.Fatalf("unexpected error = %q", got.Error)
	}
}

func TestReconcileReturnsErrorAsStructuredJSON(t *testing.T) {
	ticker := &stubTickReconciler{err: errors.New("scm unavailable")}
	cs := deleteClient(t, Services{Reconcile: &app.ReconcileService{Reconciler: ticker}})

	got := callReconcile(t, cs, 7)
	if got.Error != "scm unavailable" {
		t.Fatalf("error = %q", got.Error)
	}
	if ticker.calls != 1 {
		t.Fatalf("reconcile calls = %d", ticker.calls)
	}
}

func createBatchArgs() map[string]any {
	return map[string]any{
		"tasks": []any{
			map[string]any{"id": "T-B1", "title": "First", "scope": []string{"a.go"}},
			map[string]any{"id": "T-B2", "title": "Second", "depends": []string{"@1"}},
		},
	}
}

func callCreateBatch(t *testing.T, cs *msdk.ClientSession, args map[string]any) (app.CreateBatchResult, *msdk.CallToolResult) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name: "beacon_task_create", Arguments: args,
	})
	if err != nil {
		t.Fatalf("cs.CallTool(Name: \"beacon_task_create\"): %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("empty tool result: %+v", res)
	}
	if res.IsError {
		return app.CreateBatchResult{}, res
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatalf("unexpected result: %+v", res.Content)
	}
	var out app.CreateBatchResult
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("unmarshal %q: %v", text.Text, err)
	}
	return out, res
}

func TestTaskCreateBatchOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	source := tasksource.NewMarkdown(dir)
	committer := &stubCommitter{}
	creator := &app.TaskCreator{
		Source: source, Workspace: committer,
		Root: core.Workspace{Path: dir, Branch: "main"},
	}
	cs := deleteClient(t, Services{TaskCreator: creator})

	got, res := callCreateBatch(t, cs, createBatchArgs())
	if res.IsError {
		t.Fatalf("unexpected tool error: %+v", res)
	}
	if !got.Pushed || len(got.Created) != 2 {
		t.Fatalf("result = %+v", got)
	}
	for i, id := range []string{"T-B1", "T-B2"} {
		if got.Created[i].ID != id || got.Created[i].Path == "" {
			t.Fatalf("created %d = %+v", i, got.Created[i])
		}
	}
	if committer.calls != 1 {
		t.Fatalf("commit calls = %d, want one for the whole batch", committer.calls)
	}
	second, err := os.ReadFile(filepath.Join(dir, "T-B2.md"))
	if err != nil {
		t.Fatalf("os.ReadFile(filepath.Join(dir, \"T-B2.md\")): %v", err)
	}
	if !strings.Contains(string(second), "- T-B1") {
		t.Fatalf("second card does not depend on the first:\n%s", second)
	}
}

func TestTaskCreateBatchIsIdempotentOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	committer := &stubCommitter{}
	creator := &app.TaskCreator{
		Source: tasksource.NewMarkdown(dir), Workspace: committer,
		Root: core.Workspace{Path: dir, Branch: "main"},
	}
	cs := deleteClient(t, Services{TaskCreator: creator})

	first, _ := callCreateBatch(t, cs, createBatchArgs())
	second, _ := callCreateBatch(t, cs, createBatchArgs())
	if len(second.Created) != len(first.Created) || !second.Pushed {
		t.Fatalf("second = %+v, first = %+v", second, first)
	}
	for i := range first.Created {
		if second.Created[i].ID != first.Created[i].ID ||
			second.Created[i].Path != first.Created[i].Path {
			t.Fatalf("card %d changed: %+v vs %+v", i, first.Created[i], second.Created[i])
		}
	}
	if committer.calls != 1 {
		t.Fatalf("repeated batch committed %d times, want 1", committer.calls)
	}
}

// removingSource adds the Remove capability the batch rollback needs and can
// fail one write to exercise it.
type removingSource struct {
	*tasksource.Markdown
	removed []core.TaskID
	failID  string
}

func (s *removingSource) Create(ctx context.Context, task core.Task) (core.Task, error) {
	if string(task.ID) == s.failID {
		return core.Task{}, errors.New("write failed")
	}
	return s.Markdown.Create(ctx, task)
}

func (s *removingSource) Remove(ctx context.Context, id core.TaskID) error {
	s.removed = append(s.removed, id)
	return os.Remove(filepath.Join(s.Dir, string(id)+".md"))
}

func TestTaskCreateBatchRollsBackOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	source := &removingSource{Markdown: tasksource.NewMarkdown(dir), failID: "T-B2"}
	committer := &stubCommitter{}
	creator := &app.TaskCreator{
		Source: source, Workspace: committer,
		Root: core.Workspace{Path: dir, Branch: "main"},
	}
	cs := deleteClient(t, Services{TaskCreator: creator})

	_, res := callCreateBatch(t, cs, map[string]any{
		"tasks": []any{
			map[string]any{"id": "T-B1", "title": "First"},
			map[string]any{"id": "T-B2", "title": "Second"},
			map[string]any{"id": "T-B3", "title": "Third"},
		},
	})
	if !res.IsError {
		t.Fatal("expected a tool error when a batch card fails")
	}
	if len(source.removed) != 1 || source.removed[0] != "T-B1" {
		t.Fatalf("removed = %v, want the card written before the failure", source.removed)
	}
	for _, id := range []string{"T-B1", "T-B2", "T-B3"} {
		if _, err := os.Stat(filepath.Join(dir, id+".md")); !os.IsNotExist(err) {
			t.Fatalf("failed batch left card %s on disk: %v", id, err)
		}
	}
	// Nothing publishes until all files are written, so a failed write leaves
	// no card in the remote either.
	if committer.calls != 0 {
		t.Fatalf("commit calls = %d, want none after a failed write", committer.calls)
	}
}

func decodeToolResult[T any](t *testing.T, res *msdk.CallToolResult) T {
	t.Helper()
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("unexpected tool result: %+v", res)
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatalf("unexpected result: %+v", res.Content)
	}
	var out T
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("unmarshal %q: %v", text.Text, err)
	}
	return out
}

func callUpdateArgs(t *testing.T, cs *msdk.ClientSession, args map[string]any) app.UpdateResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name: "beacon_task_update", Arguments: args,
	})
	if err != nil {
		t.Fatalf("cs.CallTool(Name: \"beacon_task_update\"): %v", err)
	}
	return decodeToolResult[app.UpdateResult](t, res)
}

func callDeleteArgs(t *testing.T, cs *msdk.ClientSession, args map[string]any) app.DeleteResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name: "beacon_task_delete", Arguments: args,
	})
	if err != nil {
		t.Fatalf("cs.CallTool(Name: \"beacon_task_delete\"): %v", err)
	}
	return decodeToolResult[app.DeleteResult](t, res)
}

func TestTaskCreateOperationIDIsIdempotentOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	committer := &stubCommitter{}
	creator := &app.TaskCreator{
		Source: tasksource.NewMarkdown(dir), Workspace: committer,
		Root: core.Workspace{Path: dir, Branch: "main"},
	}
	cs := deleteClient(t, Services{TaskCreator: creator})
	args := createBatchArgs()
	args["operation_id"] = "op-create-1"

	first, res := callCreateBatch(t, cs, args)
	if res.IsError {
		t.Fatalf("unexpected tool error: %+v", res)
	}
	if first.Status != "applied" || first.OperationID != "op-create-1" || len(first.Created) != 2 {
		t.Fatalf("first = %+v", first)
	}
	commits := committer.calls

	second, res := callCreateBatch(t, cs, args)
	if res.IsError {
		t.Fatalf("unexpected tool error: %+v", res)
	}
	if second.Status != "duplicate" || second.OperationID != "op-create-1" {
		t.Fatalf("second = %+v", second)
	}
	if len(second.Created) != len(first.Created) {
		t.Fatalf("duplicate changed the card count: %d vs %d", len(second.Created), len(first.Created))
	}
	for i := range first.Created {
		if second.Created[i].ID != first.Created[i].ID {
			t.Fatalf("card %d changed: %+v vs %+v", i, second.Created[i], first.Created[i])
		}
	}
	if committer.calls != commits {
		t.Fatalf("duplicate committed %d more times", committer.calls-commits)
	}
}

func TestTaskUpdateOperationIDIsIdempotentOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	path := writeUpdateTestCard(t, dir, "T-A", "---\nid: T-A\ntitle: Old\n---\nbody\n")
	committer := &deleteCommitter{}
	cs := deleteClient(t, updateService(dir, &deleteRunStore{}, committer))
	args := map[string]any{
		"operation_id": "op-update-1",
		"updates":      []any{map[string]any{"id": "T-A", "title": "New"}},
	}

	first := callUpdateArgs(t, cs, args)
	if first.Status != "applied" || first.OperationID != "op-update-1" || !first.Pushed {
		t.Fatalf("first = %+v", first)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(path): %v", err)
	}
	if !strings.Contains(string(content), "title: New") {
		t.Fatalf("card not updated: %s", content)
	}
	commits := committer.calls

	second := callUpdateArgs(t, cs, args)
	if second.Status != "duplicate" || second.OperationID != "op-update-1" || !second.Pushed {
		t.Fatalf("second = %+v", second)
	}
	if committer.calls != commits {
		t.Fatalf("duplicate committed %d more times", committer.calls-commits)
	}
}

func TestTaskDeleteOperationIDIsIdempotentOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	writeDeleteCards(t, dir, "T-A", "T-B")
	committer := &deleteCommitter{}
	deleter := &app.TaskDeleter{
		Store: &deleteRunStore{}, Workspace: committer,
		Root: core.Workspace{Path: dir, Branch: "main"},
	}
	cs := deleteClient(t, Services{TaskDeleter: deleter})
	args := map[string]any{"operation_id": "op-delete-1", "ids": []any{"T-A", "T-B"}}

	first := callDeleteArgs(t, cs, args)
	if first.Status != "applied" || first.OperationID != "op-delete-1" || !first.Pushed {
		t.Fatalf("first = %+v", first)
	}
	commits := committer.calls

	second := callDeleteArgs(t, cs, args)
	if second.Status != "duplicate" || second.OperationID != "op-delete-1" {
		t.Fatalf("second = %+v", second)
	}
	if len(second.Results) != len(first.Results) {
		t.Fatalf("duplicate changed the result count: %d vs %d", len(second.Results), len(first.Results))
	}
	if committer.calls != commits {
		t.Fatalf("duplicate committed %d more times", committer.calls-commits)
	}
}

func TestOpJournalRunIsIdempotent(t *testing.T) {
	j := newOpJournal()
	calls := 0
	fn := func(context.Context) (any, error) {
		calls++
		return app.DeleteResult{Pushed: true}, nil
	}
	first, err := j.run(context.Background(), "op-1", fn)
	if err != nil {
		t.Fatalf("j.run(context.Background(), \"op-1\", fn): %v", err)
	}
	if got, ok := first.(app.DeleteResult); !ok || got.Status != "applied" {
		t.Fatalf("first = %+v", first)
	}
	second, err := j.run(context.Background(), "op-1", fn)
	if err != nil {
		t.Fatalf("j.run(context.Background(), \"op-1\", fn): %v", err)
	}
	if calls != 1 {
		t.Fatalf("mutation ran %d times, want 1", calls)
	}
	if got, ok := second.(app.DeleteResult); !ok || got.Status != "duplicate" {
		t.Fatalf("second = %+v", second)
	}
}

// extraResult is a result type that was not in the old withMeta switch. A new
// mutation response only needs the shared metadata and a WithMeta method.
type extraResult struct {
	app.OperationMeta
	Value string `json:"value"`
}

func (r extraResult) WithMeta(id, status string) any {
	r.Status, r.OperationID = status, id
	return r
}

func TestOpJournalRunAppliesMetaToUnknownResultType(t *testing.T) {
	j := newOpJournal()
	calls := 0
	fn := func(context.Context) (any, error) {
		calls++
		return extraResult{Value: "kept"}, nil
	}
	first, err := j.run(context.Background(), "op-extra", fn)
	if err != nil {
		t.Fatalf("j.run(context.Background(), \"op-extra\", fn): %v", err)
	}
	got, ok := first.(extraResult)
	if !ok || got.Value != "kept" || got.Status != "applied" || got.OperationID != "op-extra" {
		t.Fatalf("first = %+v", first)
	}

	second, err := j.run(context.Background(), "op-extra", fn)
	if err != nil {
		t.Fatalf("j.run(context.Background(), \"op-extra\", fn): %v", err)
	}
	again, ok := second.(extraResult)
	if !ok || again.Value != "kept" || again.Status != "duplicate" || again.OperationID != "op-extra" {
		t.Fatalf("second = %+v", second)
	}
	if calls != 1 {
		t.Fatalf("mutation ran %d times, want 1", calls)
	}

	wire, err := json.Marshal(again)
	if err != nil {
		t.Fatalf("json.Marshal(again): %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatalf("json.Unmarshal(wire, &fields): %v", err)
	}
	if fields["value"] != "kept" || fields["status"] != "duplicate" || fields["operation_id"] != "op-extra" {
		t.Fatalf("wire shape = %s", wire)
	}
}

func TestMutationResultsKeepFlatWireShape(t *testing.T) {
	cases := []struct {
		name   string
		result app.OperationResult
		fields []string
	}{
		{"created", app.CreatedTask{ID: "T-A"}, []string{"id", "status", "operation_id"}},
		{"batch", app.CreateBatchResult{}, []string{"created", "status", "operation_id"}},
		{"updated", app.UpdateResult{}, []string{"results", "status", "operation_id"}},
		{"deleted", app.DeleteResult{}, []string{"results", "status", "operation_id"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := json.Marshal(tc.result.WithMeta("op-wire", "applied"))
			if err != nil {
				t.Fatalf("json.Marshal(tc.result.WithMeta(\"op-wire\", \"applied\")): %v", err)
			}
			var fields map[string]any
			if err := json.Unmarshal(wire, &fields); err != nil {
				t.Fatalf("json.Unmarshal(wire, &fields): %v", err)
			}
			for _, key := range tc.fields {
				if _, ok := fields[key]; !ok {
					t.Fatalf("missing %q in %s", key, wire)
				}
			}
			if fields["status"] != "applied" || fields["operation_id"] != "op-wire" {
				t.Fatalf("metadata not at top level: %s", wire)
			}
		})
	}
}

func TestOpJournalRunSerializesConcurrentCalls(t *testing.T) {
	j := newOpJournal()
	calls := 0
	release := make(chan struct{})
	fn := func(context.Context) (any, error) {
		calls++
		<-release
		return app.DeleteResult{Pushed: true}, nil
	}
	results := make([]app.DeleteResult, 32)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := j.run(context.Background(), "op-concurrent", fn)
			if err != nil {
				return
			}
			results[i], _ = v.(app.DeleteResult)
		}(i)
	}
	close(release)
	wg.Wait()
	if calls != 1 {
		t.Fatalf("mutation ran %d times under concurrency, want 1", calls)
	}
	for _, got := range results {
		if got.Status != "duplicate" && got.Status != "applied" {
			t.Fatalf("unexpected status %q", got.Status)
		}
	}
}

// TestOpJournalConcurrentTouches stresses the indexed LRU from 32 goroutines
// sharing one journal and checks that the map, list and done store stay in sync.
func TestOpJournalConcurrentTouches(t *testing.T) {
	j := newOpJournal()
	const workers = 32
	const perWorker = 256
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				// Stay within the journal limit: eviction between a waiter's
				// wake-up and its cache read is an existing error path, not a
				// property of the LRU.
				id := fmt.Sprintf("op-%d", (w+i)%workers)
				result, err := j.run(context.Background(), id, func(context.Context) (any, error) {
					return app.DeleteResult{}, nil
				})
				if err != nil {
					errs <- err
					return
				}
				if result == nil {
					errs <- fmt.Errorf("operation %q returned a nil result", id)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.done) != j.order.Len() || len(j.index) != j.order.Len() {
		t.Fatalf("journal out of sync: done=%d order=%d index=%d", len(j.done), j.order.Len(), len(j.index))
	}
	for e := j.order.Front(); e != nil; e = e.Next() {
		id, ok := e.Value.(string)
		if !ok {
			t.Fatalf("order holds a %T, want string", e.Value)
		}
		if j.index[id] != e {
			t.Fatalf("order id %q does not match its indexed element", id)
		}
		if _, ok := j.done[id]; !ok {
			t.Fatalf("order id %q missing from done", id)
		}
	}
}

func TestOpJournalRunMarksRecoveredCaller(t *testing.T) {
	j := newOpJournal()
	ctx, cancel := context.WithCancel(context.Background())
	result, err := j.run(ctx, "op-recovered", func(context.Context) (any, error) {
		cancel()
		return app.DeleteResult{Pushed: true, Results: []app.DeletedTask{{ID: "T-A", Status: "deleted"}}}, nil
	})
	if err != nil {
		t.Fatalf("j.run(ctx, \"op-recovered\", func(context.Context) (any, error): %v", err)
	}
	got, ok := result.(app.DeleteResult)
	if !ok || got.Status != "recovered" || got.OperationID != "op-recovered" || !got.Pushed {
		t.Fatalf("result = %+v", result)
	}
	repeat, err := j.run(context.Background(), "op-recovered", func(context.Context) (any, error) {
		t.Fatal("duplicate ran the mutation again")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("j.run(context.Background(), \"op-recovered\", func(context.Context) (any, error): %v", err)
	}
	if again, ok := repeat.(app.DeleteResult); !ok || again.Status != "duplicate" {
		t.Fatalf("repeat = %+v", repeat)
	}
}

func TestOpJournalIsBounded(t *testing.T) {
	j := newOpJournal()
	for i := 0; i < operationJournalLimit+5; i++ {
		if _, err := j.run(context.Background(), fmt.Sprintf("op-%d", i), func(context.Context) (any, error) {
			return app.DeleteResult{}, nil
		}); err != nil {
			t.Fatalf("j.run(op-i): %v", err)
		}
	}
	j.mu.Lock()
	size, order, indexed := len(j.done), j.order.Len(), len(j.index)
	j.mu.Unlock()
	if size > operationJournalLimit || order > operationJournalLimit || indexed > operationJournalLimit {
		t.Fatalf("journal grew to %d entries, %d order keys and %d indexed ids", size, order, indexed)
	}
	if _, ok := j.cached("op-0"); ok {
		t.Fatal("oldest entry was not evicted")
	}
}

func TestOpJournalRunDuplicateReturnsContextError(t *testing.T) {
	j := newOpJournal()
	started := make(chan struct{})
	release := make(chan struct{})
	ownerDone := make(chan struct{})
	go func() {
		defer close(ownerDone)
		_, _ = j.run(context.Background(), "op-cancel", func(context.Context) (any, error) {
			close(started)
			<-release
			return app.DeleteResult{Pushed: true}, nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := j.run(ctx, "op-cancel", func(context.Context) (any, error) {
			return nil, errors.New("duplicate must not run the mutation")
		})
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled duplicate blocked on the in-flight operation")
	}
	close(release)
	<-ownerDone
}

func TestOpJournalRunPanicDoesNotBlockLaterCalls(t *testing.T) {
	j := newOpJournal()
	_, err := j.run(context.Background(), "op-panic", func(context.Context) (any, error) {
		panic("boom")
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("panic error = %v, want an error describing the panic", err)
	}

	done := make(chan app.DeleteResult, 1)
	errs := make(chan error, 1)
	go func() {
		got, err := j.run(context.Background(), "op-panic", func(context.Context) (any, error) {
			return app.DeleteResult{Pushed: true}, nil
		})
		if err != nil {
			errs <- err
			return
		}
		result, ok := got.(app.DeleteResult)
		if !ok {
			errs <- fmt.Errorf("unexpected result %T", got)
			return
		}
		done <- result
	}()

	select {
	case err := <-errs:
		t.Fatalf("later call failed: %v", err)
	case got := <-done:
		if got.Status != "applied" || got.OperationID != "op-panic" {
			t.Fatalf("later call = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("later call blocked after the operation panicked")
	}
}

func TestMutationAndControlToolsRejectNilServices(t *testing.T) {
	cs := deleteClient(t, Services{})
	cases := []struct {
		label string
		name  string
		args  map[string]any
	}{
		{"control", "beacon_control", map[string]any{"action": "approve", "run_id": "run-1"}},
		{"controlMany", "beacon_control", map[string]any{"action": "approve", "run_ids": []any{"run-1"}}},
		{"dispatch", "beacon_dispatch", map[string]any{"n": 1}},
		{"watchPR", "beacon_watch_pr", map[string]any{"run_id": "run-1", "pr": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
				Name: tc.name, Arguments: tc.args,
			})
			if err != nil {
				t.Fatalf("cs.CallTool(Name: tc.name): %v", err)
			}
			if !res.IsError {
				t.Fatalf("%s returned success with no service configured: %+v", tc.name, res)
			}
		})
	}
}

// benchmarkWireRuns builds the fixed 200-value payload used by the JSON result
// benchmark, so the encoded bytes are reproducible across runs.
func benchmarkWireRuns(n int) []core.WireRun {
	runs := make([]core.WireRun, n)
	for i := range runs {
		runs[i] = core.WireRun{
			ID:        fmt.Sprintf("run-%04d", i),
			TaskID:    fmt.Sprintf("T-%04d", i),
			State:     "open",
			PR:        i + 1,
			HeadSHA:   fmt.Sprintf("%040x", i),
			Branch:    fmt.Sprintf("beacon/T-%04d", i),
			Reason:    "waiting for checks",
			UpdatedAt: "2026-01-02T03:04:05Z",
		}
	}
	return runs
}

func resultWireBytes(t testing.TB, res *msdk.CallToolResult) []byte {
	t.Helper()
	if res.IsError || len(res.Content) != 1 {
		t.Fatalf("unexpected tool result: %+v", res)
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *msdk.TextContent", res.Content[0])
	}
	return []byte(text.Text)
}

func TestJSONResultKeepsWireBytes(t *testing.T) {
	runs := benchmarkWireRuns(200)
	want, err := json.Marshal(runs)
	if err != nil {
		t.Fatalf("json.Marshal(runs): %v", err)
	}
	got := resultWireBytes(t, jsonResult(runs))
	if !bytes.Equal(got, want) {
		t.Fatalf("jsonResult changed the wire bytes\ngot  %s\nwant %s", got, want)
	}
}

// jsonResultWithCopy is the pre-optimization writer. It stays in the test so
// the benchmark can report the allocations the string view removes.
func jsonResultWithCopy(v any) *msdk.CallToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		return textResult(fmt.Sprintf("{\"error\":%q}", err.Error()))
	}
	return textResult(string(b))
}

var benchResult *msdk.CallToolResult

func BenchmarkJSONResult(b *testing.B) {
	runs := benchmarkWireRuns(200)
	want, err := json.Marshal(runs)
	if err != nil {
		b.Fatalf("json.Marshal(runs): %v", err)
	}
	if got := resultWireBytes(b, jsonResult(runs)); !bytes.Equal(got, want) {
		b.Fatalf("jsonResult changed the wire bytes\ngot  %s\nwant %s", got, want)
	}
	if got := resultWireBytes(b, jsonResultWithCopy(runs)); !bytes.Equal(got, want) {
		b.Fatalf("baseline changed the wire bytes\ngot  %s\nwant %s", got, want)
	}
	b.Run("stringCopy", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchResult = jsonResultWithCopy(runs)
		}
	})
	b.Run("stringView", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchResult = jsonResult(runs)
		}
	})
}

// BenchmarkOpJournalDuplicate measures the hot path for repeated operation ids:
// a full journal and a batch of duplicate claims that only touch the LRU.
func BenchmarkOpJournalDuplicate(b *testing.B) {
	j := newOpJournal()
	for i := 0; i < operationJournalLimit; i++ {
		if _, err := j.run(context.Background(), fmt.Sprintf("op-%d", i), func(context.Context) (any, error) {
			return app.DeleteResult{}, nil
		}); err != nil {
			b.Fatalf("j.run(op-%d): %v", i, err)
		}
	}
	ids := make([]string, 4096)
	for i := range ids {
		ids[i] = fmt.Sprintf("op-%d", i%operationJournalLimit)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, id := range ids {
			result, err := j.run(context.Background(), id, func(context.Context) (any, error) {
				b.Fatal("duplicate ran the mutation again")
				return nil, nil
			})
			if err != nil {
				b.Fatalf("duplicate call: %v", err)
			}
			if result == nil {
				b.Fatal("duplicate returned a nil result")
			}
		}
	}
}
