package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolsListByteBudget is the ceiling for the serialized tools/list result.
// The budget counts real bytes (method Tirith), not the presence of fields.
// 64 KiB leaves room for seven detailed input/output schemas while still
// catching runaway growth in descriptions and schemas. CI logs the measured
// size so the ceiling can be tightened once real numbers are available.
const toolsListByteBudget = 64 * 1024

// expectedToolNames is the complete tool contract from WORKER-PROMPT 4.1.
// The order is not part of the contract, so the test compares sorted slices.
var expectedToolNames = []string{
	"beacon_watch_pr",
	"beacon_wait_result",
	"beacon_get_result",
	"beacon_list_running",
	"beacon_ack",
	"beacon_cancel_watch",
	"beacon_stats",
}

// toolKind is the annotation class assigned to a tool in WORKER-PROMPT 4.1.
type toolKind int

const (
	kindAdditive toolKind = iota
	kindRead
	kindDestructive
)

// expectedToolKinds maps every contracted tool to its honest annotation class.
// A tool that writes readAt or claimedAt is additive, not read-only: the
// distinction is the lesson from Tirith ADR-0033/0034 recorded in the audit.
var expectedToolKinds = map[string]toolKind{
	"beacon_watch_pr":     kindAdditive,
	"beacon_wait_result":  kindAdditive,
	"beacon_get_result":   kindAdditive,
	"beacon_list_running": kindRead,
	"beacon_ack":          kindAdditive,
	"beacon_cancel_watch": kindDestructive,
	"beacon_stats":        kindRead,
}

// listAllTools walks every tools/list page and returns the tools together with
// the number of bytes produced by serializing each response. Counting every
// page keeps the "exactly seven" check honest if the server paginates.
func listAllTools(t *testing.T, session *sdkmcp.ClientSession) ([]*sdkmcp.Tool, int) {
	t.Helper()

	var tools []*sdkmcp.Tool
	totalBytes := 0
	cursor := ""
	seen := make(map[string]bool)

	for {
		params := &sdkmcp.ListToolsParams{}
		if cursor != "" {
			params.Cursor = cursor
		}

		response, err := session.ListTools(context.Background(), params)
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}

		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatalf("marshal tools/list response: %v", err)
		}
		totalBytes += len(raw)

		tools = append(tools, response.Tools...)
		if response.NextCursor == "" {
			return tools, totalBytes
		}
		if seen[response.NextCursor] {
			t.Fatalf("tools/list pagination loop: cursor %q repeated", response.NextCursor)
		}
		seen[response.NextCursor] = true
		cursor = response.NextCursor
	}
}

func TestToolsListContract(t *testing.T) {
	server := NewServer()
	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "beacon-contract-test", Version: "v0.0.0"},
		nil,
	)

	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})

	tools, responseBytes := listAllTools(t, clientSession)

	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}

	wantNames := slices.Clone(expectedToolNames)
	slices.Sort(wantNames)
	slices.Sort(names)
	if !slices.Equal(names, wantNames) {
		t.Fatalf("tools/list names = %v, want exactly %v", names, wantNames)
	}

	byName := make(map[string]*sdkmcp.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	for _, name := range expectedToolNames {
		tool := byName[name]
		if tool.OutputSchema == nil {
			t.Errorf("%s: missing output schema", name)
			continue
		}
		schemaJSON, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Errorf("%s: marshal output schema: %v", name, err)
			continue
		}
		if len(schemaJSON) == 0 || string(schemaJSON) == "null" || string(schemaJSON) == "{}" {
			t.Errorf("%s: output schema is empty: %s", name, schemaJSON)
		}

		annotations := tool.Annotations
		if annotations == nil {
			t.Errorf("%s: missing annotations", name)
			continue
		}
		switch expectedToolKinds[name] {
		case kindAdditive:
			if annotations.ReadOnlyHint {
				t.Errorf("%s: additive tool must not be read-only", name)
			}
			if annotations.DestructiveHint == nil || *annotations.DestructiveHint {
				t.Errorf("%s: additive tool must set destructiveHint=false", name)
			}
		case kindRead:
			if !annotations.ReadOnlyHint {
				t.Errorf("%s: read tool must set readOnlyHint=true", name)
			}
		case kindDestructive:
			if annotations.ReadOnlyHint {
				t.Errorf("%s: destructive tool must not be read-only", name)
			}
			if annotations.DestructiveHint == nil || !*annotations.DestructiveHint {
				t.Errorf("%s: destructive tool must set destructiveHint=true", name)
			}
		}
	}

	t.Logf("tools/list serialized to %d bytes (budget %d)", responseBytes, toolsListByteBudget)
	if responseBytes > toolsListByteBudget {
		t.Errorf("tools/list response is %d bytes, over the %d byte budget", responseBytes, toolsListByteBudget)
	}
}
