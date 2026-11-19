// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// The server is transport-agnostic: Run serves it over stdio, while tests drive
// the same instance over an in-memory transport (ADR 0003, ADR 0007).
package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// ServerName and ServerVersion identify Beacon in the MCP handshake.
	ServerName    = "beacon"
	ServerVersion = "v1.0.0"
)

// LegacyProtocolVersions is the legacy initialize-handshake version set.
// Version 2026-07-28 is deliberately absent: it drops the handshake in favour
// of a stateless model and carries the experimental tasks extension that v1
// does not use. See WORKER-PROMPT §2 and ADR 0003.
var LegacyProtocolVersions = []string{
	"2025-11-25",
	"2025-06-18",
	"2025-03-26",
	"2024-11-05",
}

// CycleInstructions is delivered to clients in the MCP handshake. The agent
// cycle lives here, not in a separate tool (WORKER-PROMPT §4.1).
const CycleInstructions = `Beacon cycle protocol:

1. Register or renew a watch with beacon_watch_pr and keep the returned watchID.
2. Finish your turn with the report "waiting, watchID". Do not poll in a loop.
3. Wake only on keep_working, rebase, retry or ready. Re-run failed checks with your own rights; Beacon is read-only on CI.
4. Read state with beacon_wait_result and beacon_get_result. Mark content read with beacon_ack.
5. The orchestrator merges only after beacon_get_result returns verdict=ready, idempotently by (watchID, revision). An idle child report is not done.
6. Close a finished or superseded watch with beacon_cancel_watch. beacon_list_running and beacon_stats are read-only.

Verdicts: watching, ready, rebase, retry, keep_working, timeout, merged, closed. A watching-to-watching tick changes no revision and wakes nobody.`

// ToolNames is the exact set of seven tool names in the registry
// (WORKER-PROMPT §4.1). T2.1 registers stubs; behaviour arrives in later tasks.
var ToolNames = []string{
	"beacon_watch_pr",
	"beacon_wait_result",
	"beacon_get_result",
	"beacon_list_running",
	"beacon_ack",
	"beacon_cancel_watch",
	"beacon_stats",
}

func boolPtr(b bool) *bool { return &b }

// toolAnnotations records the honest annotation class of each contracted tool
// (WORKER-PROMPT §4.1). Tools that write readAt or claimedAt are additive, not
// read-only; cancel_watch is the only destructive tool. DestructiveHint is a
// pointer in the SDK, so it must be set explicitly even when false.
var toolAnnotations = map[string]mcpsdk.ToolAnnotations{
	"beacon_watch_pr":     {DestructiveHint: boolPtr(false), IdempotentHint: true},
	"beacon_wait_result":  {DestructiveHint: boolPtr(false), IdempotentHint: true},
	"beacon_get_result":   {DestructiveHint: boolPtr(false), IdempotentHint: true},
	"beacon_list_running": {ReadOnlyHint: true, DestructiveHint: boolPtr(false)},
	"beacon_ack":          {DestructiveHint: boolPtr(false), IdempotentHint: true},
	"beacon_cancel_watch": {DestructiveHint: boolPtr(true)},
	"beacon_stats":        {ReadOnlyHint: true, DestructiveHint: boolPtr(false)},
}

type stubInput struct{}

type stubOutput struct {
	OK bool `json:"ok" jsonschema:"true when the stub handled the call"`
}

func stubHandler(_ context.Context, _ *mcpsdk.CallToolRequest, _ stubInput) (*mcpsdk.CallToolResult, stubOutput, error) {
	return &mcpsdk.CallToolResult{}, stubOutput{OK: true}, nil
}

// NewServer builds the Beacon MCP server with all seven tools registered and
// the cycle protocol attached to the handshake.
func NewServer() *mcpsdk.Server {
	s := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: ServerName, Version: ServerVersion},
		&mcpsdk.ServerOptions{
			Instructions:              CycleInstructions,
			SupportedProtocolVersions: LegacyProtocolVersions,
		},
	)
	for _, name := range ToolNames {
		annotations := toolAnnotations[name]
		mcpsdk.AddTool(s, &mcpsdk.Tool{Name: name, Annotations: &annotations}, stubHandler)
	}
	return s
}

// Run serves MCP over stdin/stdout until the client disconnects.
func Run(ctx context.Context) error {
	return NewServer().Run(ctx, &mcpsdk.StdioTransport{})
}
