package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// connectTestSession wires a client and server over in-memory transports.
// Optional dependencies are passed to the server under test so a test can give
// the real handlers a persistent store or a distinctive snapshot.
func connectTestSession(t *testing.T, deps ...Dependencies) *mcpsdk.ClientSession {
	t.Helper()
	ctx := context.Background()

	var seed Dependencies
	if len(deps) > 0 {
		seed = deps[0]
	}
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := NewServerWithDeps(seed).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "beacon-test", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})
	return clientSession
}

func TestToolsListReturnsExactlySevenTools(t *testing.T) {
	session := connectTestSession(t)

	var got []string
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		got = append(got, tool.Name)
	}
	if len(got) != 7 {
		t.Fatalf("tools/list returned %d tools (%v), want 7", len(got), got)
	}

	want := slices.Clone(ToolNames)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("tools/list names = %v, want %v", got, want)
	}
}

func TestHandshakeInstructionsCarryCycleProtocol(t *testing.T) {
	session := connectTestSession(t)

	instructions := session.InitializeResult().Instructions
	if instructions == "" {
		t.Fatal("handshake returned empty instructions")
	}

	for _, want := range []string{
		"cycle protocol",
		"beacon_watch_pr",
		"beacon_wait_result",
		"beacon_get_result",
		"beacon_list_running",
		"beacon_ack",
		"beacon_cancel_watch",
		"beacon_stats",
		"waiting, watchID",
		"keep_working",
		"rebase",
		"retry",
		"ready",
		"verdict",
	} {
		if !strings.Contains(instructions, want) {
			t.Errorf("handshake instructions missing cycle-protocol token %q", want)
		}
	}
}

func TestLegacyProtocolWithoutExperimentalRevision(t *testing.T) {
	for _, version := range LegacyProtocolVersions {
		if version >= "2026-07-28" {
			t.Errorf("supported version %q is not a legacy handshake version", version)
		}
	}

	session := connectTestSession(t)
	negotiated := session.InitializeResult().ProtocolVersion
	if negotiated == "" || negotiated >= "2026-07-28" {
		t.Fatalf("negotiated protocol version = %q, want a legacy version", negotiated)
	}
	if !slices.Contains(LegacyProtocolVersions, negotiated) {
		t.Fatalf("negotiated version %q is outside the legacy set %v", negotiated, LegacyProtocolVersions)
	}
}

// decodeStructured decodes a tool's structured content into T regardless of
// whether the in-memory transport hands back json.RawMessage or a decoded map.
func decodeStructured[T any](t *testing.T, result *mcpsdk.CallToolResult) T {
	t.Helper()
	var out T
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode structured content into %T: %v", out, err)
	}
	return out
}

// TestDefaultServerToolsCallReachesRealHandlers proves the startup stubs are
// gone: a tools/call on the zero-dependency server runs the real projection
// handlers and returns their output shape, never the stub's {"ok":true}.
func TestDefaultServerToolsCallReachesRealHandlers(t *testing.T) {
	session := connectTestSession(t)
	ctx := context.Background()

	stats, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "beacon_stats"})
	if err != nil {
		t.Fatalf("tools/call beacon_stats: %v", err)
	}
	if stats.IsError {
		t.Fatalf("beacon_stats returned isError: %v", stats.Content)
	}
	statsOut := decodeStructured[StatsOutput](t, stats)
	if statsOut.ActiveWatches != 0 || statsOut.LoadErrors == nil {
		t.Fatalf("beacon_stats did not run the real stats projection: %+v", statsOut)
	}

	listing, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "beacon_list_running"})
	if err != nil {
		t.Fatalf("tools/call beacon_list_running: %v", err)
	}
	if listing.IsError {
		t.Fatalf("beacon_list_running returned isError: %v", listing.Content)
	}
	listOut := decodeStructured[ListRunningOutput](t, listing)
	if listOut.Watches == nil || listOut.Counts.ByVerdict == nil {
		t.Fatalf("beacon_list_running did not run the real projection: %+v", listOut)
	}
}

// TestToolsCallWithRealStore wires the real SQLite store and the real SQL
// stamper/writer/event-log from the merged tool files into NewServerWithDeps,
// then calls beacon_watch_pr and checks that the real handler persisted a Watch
// and an obligation. This fails if any stub is still registered.
func TestToolsCallWithRealStore(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	deps := Dependencies{
		Store:   db,
		Stamper: NewSQLObligationStamper(db),
		Writer:  NewSQLWaitResultWriter(db),
		Events:  NewSQLCancelEventLog(db),
		Snapshot: SnapshotFunc(func() (StateSnapshot, error) {
			watches, err := db.ActiveWatches()
			if err != nil {
				return StateSnapshot{}, err
			}
			return StateSnapshot{ActiveWatches: watches, Version: ServerVersion}, nil
		}),
	}
	session := connectTestSession(t, deps)

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "beacon_watch_pr",
		Arguments: map[string]any{
			"repo":    "refusedguy/beacon",
			"pr":      42,
			"headSHA": "w44-head",
			"role":    string(domain.RoleExecutor),
			"notify":  map[string]any{"adapter": "opencode", "target": "w44-session"},
		},
	})
	if err != nil {
		t.Fatalf("tools/call beacon_watch_pr: %v", err)
	}
	if result.IsError {
		t.Fatalf("beacon_watch_pr returned isError: %v", result.Content)
	}

	out := decodeStructured[WatchPROutput](t, result)
	if out.WatchID == "" || out.Status != string(domain.StatusWatching) || out.PollAfterMs <= 0 {
		t.Fatalf("beacon_watch_pr did not run the real handler: %+v", out)
	}

	watches, err := db.ActiveWatches()
	if err != nil {
		t.Fatalf("ActiveWatches: %v", err)
	}
	if len(watches) != 1 || watches[0].ID != out.WatchID {
		t.Fatalf("store holds %d watches (%v), want one with id %q", len(watches), watches, out.WatchID)
	}
	obligationID := domain.ObligationKey(out.WatchID, out.Revision, "w44-session")
	if _, err := db.Obligation(obligationID); err != nil {
		t.Fatalf("stored obligation %q: %v", obligationID, err)
	}
}
