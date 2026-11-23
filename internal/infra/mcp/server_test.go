package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/internal/infra/tasksource"
	"github.com/refusedguy/beacon/pkg/core"
)

func TestStatusOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	run := core.NewRun("run-1", "T-1", time.Now().UTC())
	run.State = core.RunWaitingCI
	run.PRNumber = 7
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	server := NewServer(Services{
		Dispatcher: &app.Dispatcher{},
		Control:    &app.Control{},
		Status:     app.StatusService{Store: store},
	})
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

	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{Name: "beacon_status"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) == 0 {
		t.Fatal("empty tool result")
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok || !strings.Contains(text.Text, "run-1") {
		t.Fatalf("unexpected result: %+v", res.Content)
	}
}

type stubCommitter struct{ calls int }

func (s *stubCommitter) CommitPath(context.Context, core.Workspace, string, string) error {
	s.calls++
	return nil
}

func TestTaskCreateOverInMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	source := tasksource.NewMarkdown(dir)
	committer := &stubCommitter{}
	creator := &app.TaskCreator{
		Source: source, Workspace: committer,
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
			"title":    "Add task creation tool",
			"body":     "Create a card in one call.",
			"scope":    []string{"internal/app/taskcreate.go"},
			"depends":  []string{"E2E-03"},
			"priority": 11,
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
	if !strings.HasPrefix(created.ID, "T-") || !created.Pushed {
		t.Fatalf("created = %+v", created)
	}
	if committer.calls != 1 {
		t.Fatalf("commit calls = %d", committer.calls)
	}
	tasks, err := source.ListAvailable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != core.TaskID(created.ID) || tasks[0].Title != "Add task creation tool" {
		t.Fatalf("tasks = %+v", tasks)
	}
}
