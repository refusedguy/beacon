// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// The server is transport-agnostic: Run serves it over stdio, while tests drive
// the same instance over an in-memory transport (ADR 0003, ADR 0007).
package mcp

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
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

// Bridge is the six-operation port between Beacon's MCP core and one external
// agent (ADR 0024). The core speaks only MCP; a bridge translates these six
// operations into the agent's CLI, HTTP API or ACP. The core never names an
// agent: bridges are injected, so adding a second agent needs no core edit.
//
// The six operations are the uniform meaning shared by live agent environments:
// start work, read status, read events, wake a waiting turn, cancel work and
// collect artifacts. Differences in where those operations live belong to the
// bridge, not to this interface.
type Bridge interface {
	Start(ctx context.Context, req BridgeRequest) (BridgeReply, error)
	Status(ctx context.Context, req BridgeRequest) (BridgeReply, error)
	Events(ctx context.Context, req BridgeRequest) (BridgeReply, error)
	Wake(ctx context.Context, req BridgeRequest) (BridgeReply, error)
	Cancel(ctx context.Context, req BridgeRequest) (BridgeReply, error)
	Artifacts(ctx context.Context, req BridgeRequest) (BridgeReply, error)
}

// BridgeRequest is the agent-neutral payload of one bridge operation. It
// carries MCP-level identifiers only; interpretation belongs to the bridge.
type BridgeRequest struct {
	WatchID      string `json:"watchID"`
	Repo         string `json:"repo"`
	PR           int    `json:"pr"`
	HeadSHA      string `json:"headSHA"`
	SubscriberID string `json:"subscriberID"`
}

// BridgeReply is the agent-neutral result of one bridge operation. Handle is
// an opaque token the bridge uses to correlate follow-up operations.
type BridgeReply struct {
	Handle string `json:"handle,omitempty"`
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

// Dependencies groups the ports the seven real handlers need. NewServer's
// zero-dependency form still registers every real handler; handlers that lack a
// backing port report a domain error instead of pretending to succeed. A
// composition root supplies the real ports through NewServerWithDeps.
//
// The ports are all declared inside internal/domain or this package, so wiring
// stays inside the layer matrix (ADR 0005): a caller in cmd or internal/app
// builds them from store/github and passes them in; this package never imports
// a sibling infra package.
type Dependencies struct {
	Store    domain.Store
	CI       domain.CIProvider
	Stamper  ObligationStamper
	Writer   WaitResultWriter
	Events   CancelEventLog
	Source   VerdictSource
	Snapshot SnapshotReader
	Clock    domain.Clock
}

// NewServer builds the Beacon MCP server with all seven real tools registered
// and the cycle protocol attached to the handshake. With no dependencies it
// still exercises the real handlers; inject Dependencies with NewServerWithDeps
// to give them a persistent store and the other production ports.
func NewServer() *mcpsdk.Server {
	return NewServerWithDeps(Dependencies{})
}

// NewServerWithDeps builds the server and replaces every startup stub with the
// real handler from its Attach/Register helper. The PR #21 annotations are kept
// on the initial registration; each helper re-registers its tool with the same
// honest annotation class.
func NewServerWithDeps(deps Dependencies) *mcpsdk.Server {
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

	// The real read tools carry an explicit output schema. go-sdk infers one by
	// reflection from the handler's Go type, but that inferred schema rejects
	// the null a fresh Watch emits for its nil maps (ADR 0003, ADR 0009).
	// AllOutputSchemas relaxes exactly those fields. A derivation failure is a
	// broken contract type: log it and leave the tool unregistered instead of
	// advertising a schema that rejects the tool's own output.
	schemas, err := AllOutputSchemas()
	if err != nil {
		log.Printf("mcp: output schemas: %v; real read tools not registered", err)
		return s
	}

	AttachWatchPR(s, NewWatchPRHandler(deps.Store, deps.CI, deps.Clock))
	AttachWaitResult(s, NewWaitResultService(deps.Store, deps.Writer, deps.Source, deps.Clock))
	AttachGetResult(s, NewGetResultHandler(deps.Store, deps.Stamper, deps.Clock))
	if err := attachListRunning(s, deps.Snapshot, schemas["beacon_list_running"]); err != nil {
		log.Printf("mcp: beacon_list_running: %v; tool not registered", err)
	}
	AttachAck(s, NewAckHandler(deps.Store, deps.Stamper, deps.Clock))
	RegisterCancelWatch(s, NewCancelWatcher(deps.Store, deps.Events, deps.Clock))
	if err := attachStats(s, deps.Snapshot, schemas["beacon_stats"]); err != nil {
		log.Printf("mcp: beacon_stats: %v; tool not registered", err)
	}
	return s
}

// attachListRunning installs the real read-only list_running tool. The handler
// is defined in tool_list_running.go; there is no separate Attach helper there,
// so the registration lives in this composition file. A nil reader falls back
// to an empty snapshot so the tool never panics before a caller wires a store.
// The caller supplies the schema from AllOutputSchemas so go-sdk never infers
// one from the handler's Go type.
func attachListRunning(s *mcpsdk.Server, reader SnapshotReader, outputSchema *jsonschema.Schema) error {
	if s == nil {
		return nil
	}
	if outputSchema == nil {
		return fmt.Errorf("no output schema for beacon_list_running")
	}
	if reader == nil {
		reader = emptySnapshotReader()
	}
	annotations := toolAnnotations["beacon_list_running"]
	s.RemoveTools("beacon_list_running")
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:         "beacon_list_running",
		Description:  "List running Watches from a read-only state snapshot.",
		OutputSchema: outputSchema,
		Annotations:  &annotations,
	}, NewListRunningHandler(reader).Handle)
	return nil
}

// attachStats installs the real read-only stats tool. Its handler is defined in
// tool_stats.go and has no separate Attach helper. The schema comes from
// AllOutputSchemas for the same reason as list_running.
func attachStats(s *mcpsdk.Server, reader SnapshotReader, outputSchema *jsonschema.Schema) error {
	if s == nil {
		return nil
	}
	if outputSchema == nil {
		return fmt.Errorf("no output schema for beacon_stats")
	}
	if reader == nil {
		reader = emptySnapshotReader()
	}
	annotations := toolAnnotations["beacon_stats"]
	s.RemoveTools("beacon_stats")
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:         "beacon_stats",
		Description:  "Report persisted state counters from a read-only snapshot.",
		OutputSchema: outputSchema,
		Annotations:  &annotations,
	}, NewStatsHandler(reader).Handle)
	return nil
}

// emptySnapshotReader is the minimal dependency used when no SnapshotReader is
// injected. It is a real SnapshotFunc, not a stub handler: the observability
// tools still run their own projection over the empty state.
func emptySnapshotReader() SnapshotReader {
	return SnapshotFunc(func() (StateSnapshot, error) { return StateSnapshot{}, nil })
}

// EnsureStateDir creates the Beacon state directory before the store opens its
// SQLite file. The filesystem call lives in infra: ADR 0005 keeps file
// capabilities out of cmd, so the composition root only passes a path.
func EnsureStateDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("mcp: empty state directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mcp: state directory %s: %w", dir, err)
	}
	return nil
}

// RunWithDeps serves MCP over stdin/stdout with the supplied production ports.
// The composition root builds them from the real store and calls this instead
// of Run, so every tool receives a non-nil Store.
func RunWithDeps(ctx context.Context, deps Dependencies) error {
	return NewServerWithDeps(deps).Run(ctx, &mcpsdk.StdioTransport{})
}

// Run serves MCP over stdin/stdout with no injected ports. Kept for callers
// that only need the zero-dependency server; `beacon mcp` uses RunWithDeps.
func Run(ctx context.Context) error {
	return RunWithDeps(ctx, Dependencies{})
}
