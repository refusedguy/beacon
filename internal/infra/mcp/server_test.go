package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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

func TestToolsListReturnsExactlyEightTools(t *testing.T) {
	session := connectTestSession(t)

	var got []string
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		got = append(got, tool.Name)
	}
	if len(got) != 8 {
		t.Fatalf("tools/list returned %d tools (%v), want 8", len(got), got)
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
			"notify":  map[string]any{"adapter": "opencode", "target": "ses_w44-session"},
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
	obligationID := domain.ObligationKey(out.WatchID, out.Revision, "ses_w44-session")
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
			"notify":  map[string]any{"adapter": "opencode", "target": "ses_t359-session"},
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

// TestNewServerWithDepsWiresSharedCIWaiter is the T3.83 acceptance check. It
// drives the real beacon_wait_result tool through NewServerWithDeps with a
// counting gate in Dependencies. The gate must be the one the tool uses at the
// long-poll entry; without the SetSharedCIWaiter call the direct default runs
// and the call count stays at zero.
func TestNewServerWithDepsWiresSharedCIWaiter(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	w := domain.Watch{
		ID:        "watch-gate",
		Repo:      "org/repo",
		PR:        7,
		HeadSHA:   "abc123",
		Status:    domain.StatusWatching,
		Verdict:   domain.VerdictWatching,
		Revision:  7,
		CreatedAt: time.Now().UTC(),
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("save watch: %v", err)
	}

	var calls atomic.Int32
	var gotKey string
	gate := func(ctx context.Context, key string, fn func(context.Context) error) error {
		calls.Add(1)
		gotKey = key
		return fn(ctx)
	}
	deps := Dependencies{
		Store:          db,
		Writer:         NewSQLWaitResultWriter(db),
		Source:         &waitTestSource{},
		SharedCIWaiter: gate,
	}
	session := connectTestSession(t, deps)

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "beacon_wait_result",
		Arguments: map[string]any{
			"watchID":   w.ID,
			"timeoutMs": 200,
		},
	})
	if err != nil {
		t.Fatalf("tools/call beacon_wait_result: %v", err)
	}
	if result.IsError {
		t.Fatalf("beacon_wait_result returned isError: %v", result.Content)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("shared CI gate called %d times, want 1", got)
	}
	if want := "ci:org/repo/7/abc123"; gotKey != want {
		t.Fatalf("shared CI gate key = %q, want %q", gotKey, want)
	}
}

// --- T3.91: production wiring of the setters ---
//
// Every test below drives a tool through tools/call on a server built by
// NewServerWithDeps. Calling a setter directly would only prove that the
// setter works, not that the production path reaches it.

// recordingPrecheckJournal stands in for the app service that persists
// precheck reasons. Its signature is the PrecheckJournal port.
type recordingPrecheckJournal struct {
	mu      sync.Mutex
	records []PrecheckFailureRecord
}

func (j *recordingPrecheckJournal) RecordPrecheckFailure(reason string, at time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.records = append(j.records, PrecheckFailureRecord{
		Outcome: PrecheckFailureOutcome, Reason: reason, At: at,
	})
	return nil
}

func (j *recordingPrecheckJournal) Records() []PrecheckFailureRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]PrecheckFailureRecord(nil), j.records...)
}

// countingIdempotency wraps the real in-memory index so a test can see that
// tools/call used the index injected through Dependencies.
type countingIdempotency struct {
	inner   WaitResultIdempotencyStore
	lookups atomic.Int32
	records atomic.Int32
}

func (c *countingIdempotency) Lookup(id string) (WaitResultOutput, bool) {
	c.lookups.Add(1)
	return c.inner.Lookup(id)
}

func (c *countingIdempotency) Record(id string, out WaitResultOutput) {
	c.records.Add(1)
	c.inner.Record(id, out)
}

// countingWriter wraps the SQL writer so a repeated tools/call can prove that
// only the first one marked the obligation read.
type countingWriter struct {
	inner WaitResultWriter
	calls atomic.Int32
}

func (w *countingWriter) MarkRead(ctx context.Context, id string, at time.Time) error {
	w.calls.Add(1)
	return w.inner.MarkRead(ctx, id, at)
}

func waitingWatchFixture(t *testing.T, id string) domain.Watch {
	t.Helper()
	w := domain.Watch{
		ID:        id,
		Repo:      "org/repo",
		PR:        7,
		HeadSHA:   "abc",
		Status:    domain.StatusWatching,
		Verdict:   domain.VerdictWatching,
		Revision:  7,
		CreatedAt: time.Now().UTC(),
	}
	return w
}

// TestToolsCallJournalsPrecheckFailure proves T3.82 in production: a server
// built by NewServerWithDeps returns the startup reason from tools/call and
// writes the same reason to the journal injected by the composition root.
func TestToolsCallJournalsPrecheckFailure(t *testing.T) {
	const reason = "bypass mode не настроен для init event"
	journal := &recordingPrecheckJournal{}
	deps := Dependencies{PrecheckJournal: journal, PrecheckReason: reason}
	session := connectTestSession(t, deps)

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "beacon_wait_result",
		Arguments: WaitResultInput{WatchID: "precheck-watch"},
	})
	if err != nil {
		t.Fatalf("tools/call beacon_wait_result: %v", err)
	}
	if result.IsError {
		t.Fatalf("beacon_wait_result returned isError: %v", result.Content)
	}
	out := decodeStructured[WaitResultOutput](t, result)
	if out.Status != PrecheckFailureOutcome || out.Verdict != PrecheckFailureOutcome {
		t.Fatalf("status/verdict = %q/%q, want %q", out.Status, out.Verdict, PrecheckFailureOutcome)
	}
	check, ok := out.Checks[precheckCheckName]
	if !ok || check.Conclusion != reason {
		t.Fatalf("precheck check = %+v, want reason %q", check, reason)
	}
	records := journal.Records()
	if len(records) != 1 || records[0].Reason != reason || records[0].Outcome != PrecheckFailureOutcome {
		t.Fatalf("journal = %+v, want one record with reason %q", records, reason)
	}
}

// TestToolsCallUsesCapabilityScopeAndDepartures proves T3.66 in production:
// the registry injected through Dependencies binds a tools/call to the caller
// from _meta. A departure closes the bound channel and tears down the tree;
// wait_result journals the reason through the bound capability context.
func TestToolsCallUsesCapabilityScopeAndDepartures(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	w := waitingWatchFixture(t, "watch-scope")
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	journal := NewMemoryCancelJournal()
	memoryJournal, ok := journal.(*memoryCancelJournal)
	if !ok {
		t.Fatalf("cancel journal type = %T, want *memoryCancelJournal", journal)
	}
	registry := NewCapabilityScopeRegistry(journal, waitTestClock{at: time.Now().UTC()})
	boundRequest := &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{
		Meta: mcpsdk.Meta{sessionIDMetaKey: "scope-session"},
	}}
	scope := registry.ScopeFor(boundRequest)
	if scope == nil {
		t.Fatal("capability scope was not created from the _meta sessionID")
	}
	if err := scope.AddTask(w.ID, ""); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if !registry.Depart(CapabilityID("scope-session"), CancelCallerCancelled) {
		t.Fatal("Depart did not find the bound scope")
	}

	deps := Dependencies{
		Store:      db,
		Writer:     NewSQLWaitResultWriter(db),
		Source:     &waitTestSource{},
		CallScopes: registry,
	}
	session := connectTestSession(t, deps)
	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "beacon_wait_result",
		Arguments: WaitResultInput{WatchID: w.ID, TimeoutMs: int64Ptr(200)},
		Meta:      mcpsdk.Meta{sessionIDMetaKey: "scope-session"},
	})
	if err != nil {
		t.Fatalf("tools/call beacon_wait_result: %v", err)
	}
	if !result.IsError {
		t.Fatalf("departed wait did not fail: %+v", result.StructuredContent)
	}
	body, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatalf("marshal error content: %v", err)
	}
	if !strings.Contains(string(body), "caller cancelled") {
		t.Fatalf("departure error = %s, want the caller-cancelled reason", body)
	}
	if !scope.IsCancelled() {
		t.Fatal("departure did not cancel the capability context")
	}
	tasks := scope.CancelledTasks()
	if len(tasks) != 1 || tasks[0].TaskID != w.ID || tasks[0].Reason != CancelCallerCancelled {
		t.Fatalf("cancelled tasks = %+v, want one task with reason %q", tasks, CancelCallerCancelled)
	}
	records := memoryJournal.Records()
	if len(records) != 1 || records[0].TaskID != w.ID || records[0].Reason != CancelCallerCancelled {
		t.Fatalf("cancel journal = %+v, want one record for %q", records, w.ID)
	}
}

// TestToolsCallUsesInjectedCIWaitAndIdempotency drives both ports through
// tools/call: the expensive operation must run inside the shared gate, and a
// repeat with the same clientRequestId must reuse the injected result index
// without a second operation or obligation write.
func TestToolsCallUsesInjectedCIWaitAndIdempotency(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	w := waitingWatchFixture(t, "watch-ci")
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	if err := db.SaveObligation(resultObligation(w, "ci-session")); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}
	source := &waitTestSource{}
	source.publish(WaitVerdictUpdate{
		WatchID: w.ID, Status: domain.StatusNotifying, Verdict: domain.VerdictReady,
		Revision: w.Revision, Checks: w.Checks, At: time.Now().UTC(),
	})

	var gateCalls atomic.Int32
	var gateKey string
	gate := func(ctx context.Context, key string, fn func(context.Context) error) error {
		gateCalls.Add(1)
		gateKey = key
		return fn(ctx)
	}
	var ciCalls atomic.Int32
	writer := &countingWriter{inner: NewSQLWaitResultWriter(db)}
	index := &countingIdempotency{inner: NewMemoryWaitResultIdempotency()}
	deps := Dependencies{
		Store:                 db,
		Writer:                writer,
		Source:                source,
		SharedCIWaiter:        gate,
		CIWait:                func(context.Context) error { ciCalls.Add(1); return nil },
		WaitResultIdempotency: index,
	}
	session := connectTestSession(t, deps)

	call := func() WaitResultOutput {
		t.Helper()
		result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
			Name:      "beacon_wait_result",
			Arguments: WaitResultInput{WatchID: w.ID, ClientRequestID: "req-ci"},
			Meta:      mcpsdk.Meta{sessionIDMetaKey: "ci-session"},
		})
		if err != nil {
			t.Fatalf("tools/call beacon_wait_result: %v", err)
		}
		if result.IsError {
			t.Fatalf("beacon_wait_result returned isError: %v", result.Content)
		}
		return decodeStructured[WaitResultOutput](t, result)
	}

	first, second := call(), call()
	if first.Verdict != string(domain.VerdictReady) || second.Verdict != string(domain.VerdictReady) {
		t.Fatalf("verdicts = %q/%q, want ready", first.Verdict, second.Verdict)
	}
	if got := gateCalls.Load(); got != 1 {
		t.Fatalf("shared CI gate called %d times, want 1", got)
	}
	if want := "ci:org/repo/7/abc"; gateKey != want {
		t.Fatalf("gate key = %q, want %q", gateKey, want)
	}
	if got := ciCalls.Load(); got != 1 {
		t.Fatalf("CI wait operation called %d times, want 1", got)
	}
	if got := writer.calls.Load(); got != 1 {
		t.Fatalf("obligation written %d times, want 1", got)
	}
	if got := index.records.Load(); got != 1 {
		t.Fatalf("idempotency index recorded %d results, want 1", got)
	}
	if got := index.lookups.Load(); got != 2 {
		t.Fatalf("idempotency index looked up %d times, want 2", got)
	}
}

// TestToolsCallAckUsesInjectedContinuationRegistry proves the round counter
// injected into the composition root is the one beacon_ack consumes: a
// pre-filled registry refuses the answer through tools/call.
func TestToolsCallAckUsesInjectedContinuationRegistry(t *testing.T) {
	w := parkedAckWatch()
	o := resultObligation(w, "session-answer")
	mem := newMemStore()
	if err := mem.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	if err := mem.SaveObligation(o); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}
	stamper := newRecordingStamper()
	stamper.add(o)
	registry := NewContinuationRegistry()
	for i := uint64(0); i < domain.MaxContinueRounds; i++ {
		if _, err := registry.Next(w.ID); err != nil {
			t.Fatalf("seed round %d: %v", i, err)
		}
	}
	deps := Dependencies{Store: mem, Stamper: stamper, Continuations: registry}
	session := connectTestSession(t, deps)

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "beacon_ack",
		Arguments: AckInput{WatchID: w.ID, ObligationID: o.ID, ContinueOf: w.ID},
	})
	if err != nil {
		t.Fatalf("tools/call beacon_ack: %v", err)
	}
	if !result.IsError {
		t.Fatalf("answer past the round cap was accepted: %+v", result.StructuredContent)
	}
	if got := registry.Rounds(w.ID); got != domain.MaxContinueRounds {
		t.Fatalf("rounds = %d, want %d", got, domain.MaxContinueRounds)
	}
	parked, err := mem.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch after refused answer: %v", err)
	}
	if parked.Status != domain.StatusNeedsInput {
		t.Fatalf("refused answer changed the Watch status to %q", parked.Status)
	}
}

// TestToolsCallOptionalPortsKeepRealDefaults is the invariant behind the
// setters that production deliberately does not call. With no injected idempotency
// store or continuation registry, tools/call still uses the real in-memory
// index and the handler's own round counter. CIWait stays a no-op until a CI
// adapter is bound; the shared gate is wired independently (T3.83).
func TestToolsCallOptionalPortsKeepRealDefaults(t *testing.T) {
	t.Run("idempotency", func(t *testing.T) {
		db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })

		w := waitingWatchFixture(t, "watch-default")
		if err := db.SaveWatch(w); err != nil {
			t.Fatalf("SaveWatch: %v", err)
		}
		if err := db.SaveObligation(resultObligation(w, "default-session")); err != nil {
			t.Fatalf("SaveObligation: %v", err)
		}
		source := &waitTestSource{}
		source.publish(WaitVerdictUpdate{
			WatchID: w.ID, Status: domain.StatusNotifying, Verdict: domain.VerdictReady,
			Revision: w.Revision, Checks: w.Checks, At: time.Now().UTC(),
		})
		writer := &countingWriter{inner: NewSQLWaitResultWriter(db)}
		session := connectTestSession(t, Dependencies{Store: db, Writer: writer, Source: source})
		for i := 0; i < 2; i++ {
			result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
				Name:      "beacon_wait_result",
				Arguments: WaitResultInput{WatchID: w.ID, ClientRequestID: "req-default"},
				Meta:      mcpsdk.Meta{sessionIDMetaKey: "default-session"},
			})
			if err != nil {
				t.Fatalf("tools/call beacon_wait_result: %v", err)
			}
			if result.IsError {
				t.Fatalf("beacon_wait_result returned isError: %v", result.Content)
			}
		}
		if got := writer.calls.Load(); got != 1 {
			t.Fatalf("default idempotency wrote the obligation %d times, want 1", got)
		}
	})

	t.Run("continuations", func(t *testing.T) {
		w := parkedAckWatch()
		o := resultObligation(w, "session-default")
		mem := newMemStore()
		if err := mem.SaveWatch(w); err != nil {
			t.Fatalf("SaveWatch: %v", err)
		}
		if err := mem.SaveObligation(o); err != nil {
			t.Fatalf("SaveObligation: %v", err)
		}
		stamper := newRecordingStamper()
		stamper.add(o)
		session := connectTestSession(t, Dependencies{Store: mem, Stamper: stamper})
		result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
			Name:      "beacon_ack",
			Arguments: AckInput{WatchID: w.ID, ObligationID: o.ID, ContinueOf: w.ID},
		})
		if err != nil {
			t.Fatalf("tools/call beacon_ack: %v", err)
		}
		if result.IsError {
			t.Fatalf("default continuation registry refused the first answer: %v", result.Content)
		}
		out := decodeStructured[OKOutput](t, result)
		if !out.OK {
			t.Fatal("default continuation registry did not acknowledge the answer")
		}
	})
}
