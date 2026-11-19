package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
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

// TestReadToolsAdvertiseOutputSchema proves the two tools registered by the
// composition root carry an explicit output schema rather than relying on the
// one go-sdk infers by reflection from the handler's Go type.
func TestReadToolsAdvertiseOutputSchema(t *testing.T) {
	session := connectTestSession(t)
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	byName := make(map[string]*mcpsdk.Tool, len(tools.Tools))
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}
	for _, name := range []string{"beacon_list_running", "beacon_stats"} {
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("tools/list missing %s", name)
		}
		if tool.OutputSchema == nil {
			t.Errorf("%s: missing output schema", name)
		}
	}
}

// TestListRunningWithNilMapsValidates drives the real tool with a fresh Watch
// whose Checks and Flaky.Used maps are nil. Those maps marshal to JSON null, so
// a schema inferred by reflection (type "object") rejects the tool's own
// output and tools/call returns a validation error. The explicit schema from
// AllOutputSchemas accepts null, so the call must succeed without isError.
func TestListRunningWithNilMapsValidates(t *testing.T) {
	deps := Dependencies{
		Snapshot: SnapshotFunc(func() (StateSnapshot, error) {
			return StateSnapshot{
				ActiveWatches: []domain.Watch{{ID: "fresh-watch"}},
			}, nil
		}),
	}
	session := connectTestSession(t, deps)

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "beacon_list_running",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("tools/call beacon_list_running with nil maps: %v", err)
	}
	if result.IsError {
		t.Fatalf("beacon_list_running returned isError: %v", result.Content)
	}
	out := decodeStructured[ListRunningOutput](t, result)
	if len(out.Watches) != 1 || out.Watches[0].ID != "fresh-watch" {
		t.Fatalf("watches = %+v, want one watch with id fresh-watch", out.Watches)
	}
	if out.Watches[0].Checks != nil || out.Watches[0].Flaky.Used != nil {
		t.Fatalf("fresh watch maps should be nil, got checks=%v, flaky.used=%v",
			out.Watches[0].Checks, out.Watches[0].Flaky.Used)
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

// connectDialectSession wires a client advertising one task dialect to a
// server whose get_result handler uses the supplied --after threshold. The
// handshake itself is untouched: a 2026-07-28 client is signalled only by
// per-request capabilities, never by SupportedProtocolVersions.
func connectDialectSession(t *testing.T, dialect TaskDialect, deps Dependencies) *mcpsdk.ClientSession {
	t.Helper()
	ctx := context.Background()

	var clientOpts *mcpsdk.ClientOptions
	if dialect == DialectTasks2025 {
		clientOpts = &mcpsdk.ClientOptions{Capabilities: &mcpsdk.ClientCapabilities{
			Experimental: map[string]any{tasksExperimentalKey: map[string]any{}},
		}}
	}

	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := NewServerWithDeps(deps).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "beacon-dialect-test", Version: "v0.0.1"}, clientOpts)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})

	negotiated := clientSession.InitializeResult().ProtocolVersion
	if negotiated >= "2026-07-28" || !slices.Contains(LegacyProtocolVersions, negotiated) {
		t.Fatalf("handshake negotiated %q, want a legacy version", negotiated)
	}
	return clientSession
}

// dialectGetResultDeps builds the real store, stamper and clock used by the
// get_result handler in the dialect contract tests.
func dialectGetResultDeps(t *testing.T) (Dependencies, domain.Watch, domain.Obligation) {
	t.Helper()
	w := resultFixture(domain.VerdictReady)
	o := resultObligation(w, "dialect-session")
	mem := newMemStore()
	if err := mem.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	if err := mem.SaveObligation(o); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}
	stamper := newRecordingStamper()
	stamper.add(o)
	deps := Dependencies{
		Store:   mem,
		Stamper: stamper,
		Clock:   &dialectClock{t: time.Date(2026, 11, 19, 11, 0, 0, 0, time.UTC), step: time.Second},
		After:   time.Millisecond,
	}
	return deps, w, o
}

// dialectClock advances by step on every Now call, so a test can observe a
// call that crossed --after without sleeping.
type dialectClock struct {
	t    time.Time
	step time.Duration
}

func (c *dialectClock) Now() time.Time {
	now := c.t
	c.t = c.t.Add(c.step)
	return now
}

// callDialectGetResult calls beacon_get_result with the client's sessionID and,
// for the 2026 dialect, the per-request tasks extension capability.
func callDialectGetResult(t *testing.T, session *mcpsdk.ClientSession, dialect TaskDialect, watchID string) GetResultOutput {
	t.Helper()
	meta := mcpsdk.Meta{sessionIDMetaKey: "dialect-session"}
	if dialect == DialectTasks2026 {
		meta[clientCapabilitiesMetaKey] = map[string]any{
			"extensions": map[string]any{tasksExtensionKey: map[string]any{}},
		}
	}
	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "beacon_get_result",
		Arguments: map[string]any{"watchID": watchID},
		Meta:      meta,
	})
	if err != nil {
		t.Fatalf("tools/call beacon_get_result: %v", err)
	}
	if result.IsError {
		t.Fatalf("beacon_get_result returned isError: %v", result.Content)
	}
	return decodeStructured[GetResultOutput](t, result)
}

// TestGetResultDialectForms is the contract test for ADR 0031 §2: a plain
// client keeps the classical tools/call result, the 2025 dialect gets a real
// task handle right away, and the 2026 dialect gets resultType "task" after
// --after. None of the three changes the handshake version.
func TestGetResultDialectForms(t *testing.T) {
	tests := []struct {
		name       string
		dialect    TaskDialect
		wantJob    bool
		wantTaskID bool
		wantType   string
	}{
		{name: "no tasks", dialect: DialectPlain, wantJob: true},
		{name: "tasks 2025-11-25", dialect: DialectTasks2025, wantTaskID: true},
		{name: "tasks 2026-07-28", dialect: DialectTasks2026, wantTaskID: true, wantType: TaskResultType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, w, o := dialectGetResultDeps(t)
			session := connectDialectSession(t, tc.dialect, deps)
			out := callDialectGetResult(t, session, tc.dialect, w.ID)

			if out.Status != string(domain.StatusNotifying) || out.Verdict != string(domain.VerdictReady) {
				t.Fatalf("core projection changed by dialect: %+v", out)
			}
			if tc.wantJob {
				if out.JobID != o.ID || out.Instructions == "" {
					t.Errorf("plain client job form = %+v, want jobID and instructions", out)
				}
				if out.ResultType != "" || out.TaskID != "" {
					t.Errorf("plain client must not see a task form: %+v", out)
				}
			}
			if tc.wantTaskID && (out.TaskID != o.ID || out.TaskStatus != TaskStatusWorking) {
				t.Errorf("%s task form = %+v, want taskId %q working", tc.dialect, out, o.ID)
			}
			if out.ResultType != tc.wantType {
				t.Errorf("resultType = %q, want %q", out.ResultType, tc.wantType)
			}
			if out.JobID != "" && tc.dialect != DialectPlain {
				t.Errorf("task dialect must not carry a plain jobID: %+v", out)
			}
		})
	}
}

// TestGetResultAfterThresholdSwitchesForm proves the --after switch: a call
// below the threshold keeps the classic result even for a Tasks client, while
// a call at or above it takes the dialect form.
func TestGetResultAfterThresholdSwitchesForm(t *testing.T) {
	for _, dialect := range []TaskDialect{DialectPlain, DialectTasks2026} {
		t.Run(string(dialect), func(t *testing.T) {
			deps, w, _ := dialectGetResultDeps(t)
			deps.After = time.Hour
			session := connectDialectSession(t, dialect, deps)
			below := callDialectGetResult(t, session, dialect, w.ID)
			if below.ResultType != "" || below.TaskID != "" || below.JobID != "" {
				t.Errorf("below --after must stay classic for %s: %+v", dialect, below)
			}
		})
	}
}

// TestDialectFromRequestPrefersExtension proves capability detection is
// per-request and that the extension wins over the initialize capability.
func TestDialectFromRequestPrefersExtension(t *testing.T) {
	req := &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{
		Meta: mcpsdk.Meta{clientCapabilitiesMetaKey: map[string]any{
			"extensions": map[string]any{tasksExtensionKey: map[string]any{}},
		}},
	}}
	if got := DialectFromRequest(req); got != DialectTasks2026 {
		t.Errorf("DialectFromRequest = %q, want %q", got, DialectTasks2026)
	}
	if got := DialectFromRequest(nil); got != DialectPlain {
		t.Errorf("DialectFromRequest(nil) = %q, want %q", got, DialectPlain)
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

// TestSchemaErrorKeepsRealHandlers proves T3.59: a broken output schema must
// never leave the server silently running on the startup stubs. One schema
// factory is forced to fail, so AllOutputSchemas returns an error. Every tool
// must still run its real domain handler and return a domain-shaped response,
// not the stub's {"ok":true}.
func TestSchemaErrorKeepsRealHandlers(t *testing.T) {
	const broken = "beacon_stats"
	original := outputSchemaFactories[broken]
	outputSchemaFactories[broken] = func() (*jsonschema.Schema, error) {
		return nil, fmt.Errorf("injected schema failure")
	}
	t.Cleanup(func() { outputSchemaFactories[broken] = original })

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
	ctx := context.Background()

	// stats: the real projection emits loadErrors; a stub only emits {"ok":true}.
	stats, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "beacon_stats"})
	if err != nil {
		t.Fatalf("tools/call beacon_stats with broken schema: %v", err)
	}
	if stats.IsError {
		t.Fatalf("beacon_stats returned isError: %v", stats.Content)
	}
	statsOut := decodeStructured[StatsOutput](t, stats)
	if statsOut.LoadErrors == nil {
		t.Fatalf("beacon_stats ran the stub, not the real handler: %+v", statsOut)
	}

	// list_running: the real projection emits counts and watches; a stub does not.
	listing, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "beacon_list_running"})
	if err != nil {
		t.Fatalf("tools/call beacon_list_running with broken schema: %v", err)
	}
	if listing.IsError {
		t.Fatalf("beacon_list_running returned isError: %v", listing.Content)
	}
	listOut := decodeStructured[ListRunningOutput](t, listing)
	if listOut.Watches == nil || listOut.Counts.ByVerdict == nil {
		t.Fatalf("beacon_list_running ran the stub, not the real handler: %+v", listOut)
	}

	// watch_pr: a write handler must also be registered when schema derivation fails.
	watch, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "beacon_watch_pr",
		Arguments: map[string]any{
			"repo":    "refusedguy/beacon",
			"pr":      42,
			"headSHA": "t359-head",
			"role":    string(domain.RoleExecutor),
			"notify":  map[string]any{"adapter": "opencode", "target": "t359-session"},
		},
	})
	if err != nil {
		t.Fatalf("tools/call beacon_watch_pr with broken schema: %v", err)
	}
	if watch.IsError {
		t.Fatalf("beacon_watch_pr returned isError: %v", watch.Content)
	}
	watchOut := decodeStructured[WatchPROutput](t, watch)
	if watchOut.WatchID == "" || watchOut.Status != string(domain.StatusWatching) {
		t.Fatalf("beacon_watch_pr ran the stub, not the real handler: %+v", watchOut)
	}
}
