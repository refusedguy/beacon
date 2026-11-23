package mcp

import (
	"context"
	"testing"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"
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
