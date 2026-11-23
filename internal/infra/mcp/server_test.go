package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
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
