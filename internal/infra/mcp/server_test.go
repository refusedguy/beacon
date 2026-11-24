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
	if orchestratorOnly != 4 || !byName["beacon_task_create"].OrchestratorOnly ||
		!byName["beacon_task_delete"].OrchestratorOnly || !byName["beacon_task_update"].OrchestratorOnly ||
		!byName["beacon_reconcile"].OrchestratorOnly {
		t.Fatalf("only task creation, deletion, update and reconcile should be orchestrator-only: %+v", catalog)
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

func writeUpdateTestCard(t *testing.T, dir, id, content string) string {
	t.Helper()
	cards := filepath.Join(dir, "docs", "tasks")
	if err := os.MkdirAll(cards, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cards, id+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
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
		t.Fatal(err)
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
		"id": "T-A", "title": "New title", "status": "done",
	})
	if !got.Pushed || len(got.Results) != 1 || got.Results[0].Status != "updated" {
		t.Fatalf("result = %+v", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	contentB, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
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
			t.Fatal(err)
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
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected a tool error when the commit fails")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("unexpected reconcile result: %+v", res)
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatalf("unexpected result: %+v", res.Content)
	}
	var out app.ReconcileResult
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
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
	if committer.calls != 2 {
		t.Fatalf("commit calls = %d, want one per card", committer.calls)
	}
	second, err := os.ReadFile(filepath.Join(dir, "T-B2.md"))
	if err != nil {
		t.Fatal(err)
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
	if committer.calls != 2 {
		t.Fatalf("repeated batch committed %d times, want 2", committer.calls)
	}
}

// removingSource adds the Remove capability the batch rollback needs.
type removingSource struct {
	*tasksource.Markdown
	removed []core.TaskID
}

func (s *removingSource) Remove(_ context.Context, id core.TaskID) error {
	s.removed = append(s.removed, id)
	return os.Remove(filepath.Join(s.Dir, string(id)+".md"))
}

func TestTaskCreateBatchRollsBackOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	source := &removingSource{Markdown: tasksource.NewMarkdown(dir)}
	committer := &stubCommitter{}
	creator := &app.TaskCreator{
		Source: source, Workspace: committer,
		Root: core.Workspace{Path: dir, Branch: "main"},
	}
	cs := deleteClient(t, Services{TaskCreator: creator})

	_, res := callCreateBatch(t, cs, map[string]any{
		"tasks": []any{
			map[string]any{"id": "T-B1", "title": "First"},
			map[string]any{"id": "T-B2", "title": "  "},
		},
	})
	if !res.IsError {
		t.Fatal("expected a tool error when a batch card fails")
	}
	if len(source.removed) != 1 || source.removed[0] != "T-B1" {
		t.Fatalf("removed = %v, want the card written before the failure", source.removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "T-B1.md")); !os.IsNotExist(err) {
		t.Fatalf("failed batch left the first card on disk: %v", err)
	}
	// The first card publishes before the later card fails; rollback removes
	// its file but cannot undo that commit.
	if committer.calls != 1 {
		t.Fatalf("commit calls = %d, want the first card's publish only", committer.calls)
	}
}
