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
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/internal/infra/tasksource"
	"github.com/refusedguy/beacon/pkg/core"
)

func closeService(dir string, store app.ActiveRunFinder, committer app.PathCommitter) Services {
	return Services{TaskUpdater: &app.TaskUpdater{
		Store: store, Workspace: committer,
		Root: core.Workspace{Path: dir, Branch: "main"},
	}}
}

func writeCloseCard(t *testing.T, dir, id string) string {
	t.Helper()
	cards := filepath.Join(dir, "docs", "tasks")
	if err := os.MkdirAll(cards, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(cards): %v", err)
	}
	path := filepath.Join(cards, id+".md")
	body := "---\nid: " + id + "\ntitle: perf run\nstatus: available\n---\n# " + id + ". Card\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("os.WriteFile(path): %v", err)
	}
	return path
}

func callClose(t *testing.T, cs *msdk.ClientSession, args map[string]any) (app.CloseResult, *msdk.CallToolResult) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name: "beacon_task_close", Arguments: args,
	})
	if err != nil {
		t.Fatalf("cs.CallTool(beacon_task_close): %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("empty tool result")
	}
	if res.IsError {
		return app.CloseResult{}, res
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatalf("unexpected result: %+v", res.Content)
	}
	var out app.CloseResult
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("json.Unmarshal(%s): %v", text.Text, err)
	}
	return out, res
}

func closeArgs(id, status, reason string) map[string]any {
	return map[string]any{
		"updates": []any{map[string]any{"id": id, "status": status, "reason": reason}},
	}
}

func TestTaskCloseOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	path := writeCloseCard(t, dir, "T-A")
	committer := &deleteCommitter{}
	cs := deleteClient(t, closeService(dir, &deleteRunStore{}, committer))

	got, res := callClose(t, cs, closeArgs("T-A", "dropped", "every advisory row was flat"))
	if res.IsError {
		t.Fatalf("unexpected tool error: %+v", res)
	}
	if !got.Pushed || len(got.Results) != 1 || got.Results[0].Status != "closed" {
		t.Fatalf("result = %+v", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(path): %v", err)
	}
	if !strings.Contains(string(content), "status: dropped") ||
		!strings.Contains(string(content), "status_reason: every advisory row was flat") {
		t.Fatalf("card missing status or reason: %s", content)
	}
	if committer.calls != 1 {
		t.Fatalf("commit calls = %d, want 1", committer.calls)
	}
}

func TestTaskCloseAcceptsDoneStatus(t *testing.T) {
	dir := t.TempDir()
	path := writeCloseCard(t, dir, "T-A")
	cs := deleteClient(t, closeService(dir, &deleteRunStore{}, &deleteCommitter{}))

	got, res := callClose(t, cs, closeArgs("T-A", "done", "merged in PR 2861"))
	if res.IsError {
		t.Fatalf("unexpected tool error: %+v", res)
	}
	if len(got.Results) != 1 || got.Results[0].Status != "closed" {
		t.Fatalf("result = %+v", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(path): %v", err)
	}
	if !strings.Contains(string(content), "status: done") {
		t.Fatalf("card not closed as done: %s", content)
	}
}

func TestTaskCloseRefusesActiveRun(t *testing.T) {
	dir := t.TempDir()
	path := writeCloseCard(t, dir, "T-A")
	committer := &deleteCommitter{}
	store := &deleteRunStore{active: map[string]bool{"T-A": true}}
	cs := deleteClient(t, closeService(dir, store, committer))

	got, res := callClose(t, cs, closeArgs("T-A", "dropped", "cancelled"))
	if res.IsError {
		t.Fatalf("unexpected tool error: %+v", res)
	}
	if len(got.Refused) != 1 || got.Refused[0] != "T-A" || got.Pushed {
		t.Fatalf("result = %+v", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(path): %v", err)
	}
	if !strings.Contains(string(content), "status: available") || committer.calls != 0 {
		t.Fatal("active-run refusal changed the card or committed")
	}
}

func TestTaskCloseRejectsInvalidStatus(t *testing.T) {
	dir := t.TempDir()
	path := writeCloseCard(t, dir, "T-A")
	committer := &deleteCommitter{}
	cs := deleteClient(t, closeService(dir, &deleteRunStore{}, committer))

	_, res := callClose(t, cs, closeArgs("T-A", "in_progress", "still running"))
	if !res.IsError || !strings.Contains(controlText(t, res), "must be done or dropped") {
		t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
	}
	if committer.calls != 0 {
		t.Fatal("invalid status still committed")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(path): %v", err)
	}
	if !strings.Contains(string(content), "status: available") {
		t.Fatal("invalid status changed the card")
	}
}

func TestTaskCloseRequiresReason(t *testing.T) {
	dir := t.TempDir()
	committer := &deleteCommitter{}
	cs := deleteClient(t, closeService(dir, &deleteRunStore{}, committer))

	_, res := callClose(t, cs, closeArgs("T-A", "dropped", ""))
	if !res.IsError || !strings.Contains(controlText(t, res), "reason is required") {
		t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
	}
	if committer.calls != 0 {
		t.Fatal("missing reason still committed")
	}
}

func TestTaskCloseRejectsWorker(t *testing.T) {
	dir := t.TempDir()
	cs := deleteClient(t, closeService(dir, &deleteRunStore{}, &deleteCommitter{}))

	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name: "beacon_task_close", Arguments: closeArgs("T-A", "dropped", "cancelled"),
		Meta: msdk.Meta{"beacon/agent": "worker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(controlText(t, res), "orchestrator only") {
		t.Fatalf("result = %+v, text = %q", res, controlText(t, res))
	}
}

func TestTaskCloseOperationIDIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeCloseCard(t, dir, "T-A")
	committer := &deleteCommitter{}
	cs := deleteClient(t, closeService(dir, &deleteRunStore{}, committer))
	args := closeArgs("T-A", "dropped", "cancelled")
	args["operation_id"] = "op-close-1"

	first, res := callClose(t, cs, args)
	if res.IsError || first.Status != "applied" || first.OperationID != "op-close-1" || !first.Pushed {
		t.Fatalf("first = %+v, res = %+v", first, res)
	}
	second, res := callClose(t, cs, args)
	if res.IsError || second.Status != "duplicate" || second.OperationID != "op-close-1" || !second.Pushed {
		t.Fatalf("second = %+v, res = %+v", second, res)
	}
	if committer.calls != 1 {
		t.Fatalf("duplicate committed %d times", committer.calls)
	}
}

func TestTaskCloseReplaysAfterRestart(t *testing.T) {
	dir := t.TempDir()
	writeCloseCard(t, dir, "T-A")
	store := journalTestStore(t)
	committer := &deleteCommitter{}
	args := closeArgs("T-A", "dropped", "cancelled")
	args["operation_id"] = "op-close-restart"

	firstJournal := deleteClient(t, closeService(dir, store, committer))
	first, res := callClose(t, firstJournal, args)
	if res.IsError || first.Status != "applied" || !first.Pushed {
		t.Fatalf("first = %+v, res = %+v", first, res)
	}

	secondCommitter := &deleteCommitter{}
	secondJournal := deleteClient(t, closeService(dir, store, secondCommitter))
	second, res := callClose(t, secondJournal, args)
	if res.IsError || second.Status != "duplicate" || second.OperationID != "op-close-restart" {
		t.Fatalf("replayed = %+v, res = %+v", second, res)
	}
	if committer.calls != 1 || secondCommitter.calls != 0 {
		t.Fatalf("replay committed again: first=%d second=%d", committer.calls, secondCommitter.calls)
	}
}

func TestTaskCloseSourceSyncsGraph(t *testing.T) {
	dir := t.TempDir()
	writeCloseCard(t, dir, "T-A")
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	source := tasksource.NewMarkdown(dir)
	if tasks, err := source.ListAvailable(context.Background()); err != nil || len(tasks) != 1 {
		t.Fatalf("source.ListAvailable: %d tasks, err %v", len(tasks), err)
	}
	if err := store.SyncTasks(context.Background(), []core.Task{
		{ID: "T-A", Status: core.TaskAvailable},
	}); err != nil {
		t.Fatalf("store.SyncTasks: %v", err)
	}
	sv := Services{TaskUpdater: &app.TaskUpdater{
		Store: store, Source: source, Workspace: &deleteCommitter{},
		Root: core.Workspace{Path: dir, Branch: "main"},
	}}
	cs := deleteClient(t, sv)

	got, res := callClose(t, cs, closeArgs("T-A", "dropped", "cancelled"))
	if res.IsError || !got.Pushed {
		t.Fatalf("result = %+v, res = %+v", got, res)
	}
	available, err := source.ListAvailable(context.Background())
	if err != nil {
		t.Fatalf("source.ListAvailable after close: %v", err)
	}
	if len(available) != 0 {
		t.Fatalf("closed card is still available: %+v", available)
	}
	ready, err := store.ListReadyTasks(context.Background())
	if err != nil {
		t.Fatalf("store.ListReadyTasks after close: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("dispatcher can still claim the closed card: %+v", ready)
	}
}
