package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// errMissing is the not-found error returned by the in-memory store for a
// missing Watch or Obligation. domain.Fanout relies on a non-nil error here.
var errMissing = errors.New("memstore: not found")

// memStore is a minimal domain.Store for these tool tests. It keeps the same
// value semantics as the SQLite store: SaveWatch replaces by ID and a reader
// never mutates the stored copy.
type memStore struct {
	mu          sync.Mutex
	watches     map[string]domain.Watch
	obligations map[string]domain.Obligation
}

func newMemStore() *memStore {
	return &memStore{
		watches:     map[string]domain.Watch{},
		obligations: map[string]domain.Obligation{},
	}
}

func (m *memStore) SaveWatch(w domain.Watch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.watches[w.ID] = w
	return nil
}

func (m *memStore) Watch(id string) (domain.Watch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.watches[id]
	if !ok {
		return domain.Watch{}, errMissing
	}
	return w, nil
}

func (m *memStore) ActiveWatches() ([]domain.Watch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Watch, 0, len(m.watches))
	for _, w := range m.watches {
		if w.Status != domain.StatusClosed {
			out = append(out, w)
		}
	}
	return out, nil
}

func (m *memStore) AllWatches() ([]domain.Watch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Watch, 0, len(m.watches))
	for _, w := range m.watches {
		out = append(out, w)
	}
	return out, nil
}

func (m *memStore) SaveObligation(o domain.Obligation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.obligations[o.ID] = o
	return nil
}

func (m *memStore) Obligation(id string) (domain.Obligation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.obligations[id]
	if !ok {
		return domain.Obligation{}, errMissing
	}
	return o, nil
}

func (m *memStore) PendingObligations() ([]domain.Obligation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Obligation, 0, len(m.obligations))
	for _, o := range m.obligations {
		out = append(out, o)
	}
	return out, nil
}

// stepClock lets a test advance time to observe a subscriber renewal.
type stepClock struct {
	t time.Time
}

func (c *stepClock) Now() time.Time { return c.t }

func (c *stepClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// fakeCI is the CIProvider used by the headSHA resolution test.
type fakeCI struct {
	head string
	base string
	err  error
}

func (f *fakeCI) PRState(_ context.Context, _ string, _ int) (domain.PRState, error) {
	if f.err != nil {
		return domain.PRState{}, f.err
	}
	return domain.PRState{HeadRefOid: f.head, BaseRefName: f.base}, nil
}

func sessionReq(id string) *mcpsdk.CallToolRequest {
	return &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{
		Meta: mcpsdk.Meta{watchPRSessionIDMetaKey: id},
	}}
}

func watchPRTestInput(head string, role domain.Role) WatchPRInput {
	return WatchPRInput{
		Repo:    "refusedguy/beacon",
		PR:      7,
		HeadSHA: head,
		Role:    role,
		Notify:  &WatchPRNotify{Adapter: "opencode", Target: "delivery-target"},
	}
}

// TestWatchPRIdempotentRenew registers the same subscriber on the same triple
// twice. It must produce one Watch, one subscriber and a newer LastActivity.
func TestWatchPRIdempotentRenew(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)}
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, clock)
	req := sessionReq("session-A")
	in := watchPRTestInput("aaa111", domain.RoleExecutor)

	first, err := h.Register(context.Background(), req, in)
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	clock.Advance(time.Minute)
	second, err := h.Register(context.Background(), req, in)
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}

	if first.WatchID == "" || second.WatchID == "" {
		t.Fatal("watchID must be populated in both responses")
	}
	if first.WatchID != second.WatchID {
		t.Errorf("watchID changed on renew: %q -> %q", first.WatchID, second.WatchID)
	}
	if first.Revision != second.Revision {
		t.Errorf("revision changed on renew: %d -> %d", first.Revision, second.Revision)
	}
	if first.Status != string(domain.StatusWatching) || first.Verdict != string(domain.VerdictWatching) {
		t.Errorf("unexpected initial status/verdict: %q/%q", first.Status, first.Verdict)
	}
	if first.PollAfterMs <= 0 || second.PollAfterMs <= 0 {
		t.Errorf("pollAfterMs must be a positive hint, got %d and %d", first.PollAfterMs, second.PollAfterMs)
	}

	all, err := store.AllWatches()
	if err != nil {
		t.Fatalf("AllWatches: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d watches, want 1", len(all))
	}
	if len(all[0].Subscribers) != 1 {
		t.Fatalf("got %d subscribers, want 1", len(all[0].Subscribers))
	}
	sub := all[0].Subscribers[0]
	if sub.ID != "session-A" || sub.Role != domain.RoleExecutor {
		t.Errorf("unexpected subscriber: %+v", sub)
	}
	if !sub.LastActivity.Equal(clock.t) {
		t.Errorf("LastActivity = %v, want renewed %v", sub.LastActivity, clock.t)
	}
	if sub.Adapter != "opencode" || sub.Target != "delivery-target" {
		t.Errorf("notify endpoint not stored: %+v", sub)
	}
}

// TestWatchPRFanout registers an executor and an orchestrator on the same PR
// head. They must share one Watch but own separate obligations.
func TestWatchPRFanout(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)}
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, clock)

	execOut, err := h.Register(context.Background(), sessionReq("exec-1"), watchPRTestInput("bbb222", domain.RoleExecutor))
	if err != nil {
		t.Fatalf("executor Register: %v", err)
	}
	orchOut, err := h.Register(context.Background(), sessionReq("orch-1"), watchPRTestInput("bbb222", domain.RoleOrchestrator))
	if err != nil {
		t.Fatalf("orchestrator Register: %v", err)
	}

	if execOut.WatchID != orchOut.WatchID {
		t.Fatalf("fan-out split the Watch: %q vs %q", execOut.WatchID, orchOut.WatchID)
	}

	all, err := store.AllWatches()
	if err != nil {
		t.Fatalf("AllWatches: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d watches, want 1", len(all))
	}
	if len(all[0].Subscribers) != 2 {
		t.Fatalf("got %d subscribers, want 2", len(all[0].Subscribers))
	}
	roles := map[domain.Role]string{}
	for _, s := range all[0].Subscribers {
		roles[s.Role] = s.ID
	}
	if roles[domain.RoleExecutor] != "exec-1" || roles[domain.RoleOrchestrator] != "orch-1" {
		t.Errorf("unexpected role mapping: %+v", roles)
	}

	obligations, err := store.PendingObligations()
	if err != nil {
		t.Fatalf("PendingObligations: %v", err)
	}
	ids := map[string]bool{}
	for _, o := range obligations {
		if o.WatchID != execOut.WatchID {
			continue
		}
		if ids[o.ID] {
			t.Errorf("duplicate obligation ID %q", o.ID)
		}
		ids[o.ID] = true
	}
	if len(ids) != 2 {
		t.Errorf("got %d obligations for the Watch, want 2", len(ids))
	}
	wantExec := domain.ObligationKey(execOut.WatchID, execOut.Revision, "exec-1")
	wantOrch := domain.ObligationKey(execOut.WatchID, orchOut.Revision, "orch-1")
	if !ids[wantExec] || !ids[wantOrch] {
		t.Errorf("missing independent obligations: have %v, want %q and %q", ids, wantExec, wantOrch)
	}
}

// TestWatchPRHeadChanged closes the old Watch and carries its subscribers to
// a successor with the new head (ADR 0019).
func TestWatchPRHeadChanged(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)}
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, clock)
	req := sessionReq("session-H")

	old, err := h.Register(context.Background(), req, watchPRTestInput("old-head", domain.RoleExecutor))
	if err != nil {
		t.Fatalf("register old head: %v", err)
	}
	clock.Advance(time.Second)
	in := watchPRTestInput("new-head", domain.RoleExecutor)
	next, err := h.Register(context.Background(), req, in)
	if err != nil {
		t.Fatalf("register new head: %v", err)
	}

	if old.WatchID == next.WatchID {
		t.Fatalf("head change reused watchID %q", old.WatchID)
	}

	oldWatch, err := store.Watch(old.WatchID)
	if err != nil {
		t.Fatalf("old Watch missing: %v", err)
	}
	if oldWatch.Status != domain.StatusClosed {
		t.Errorf("old status = %q, want closed", oldWatch.Status)
	}
	if oldWatch.CloseReason != string(domain.CloseReasonHeadChanged) {
		t.Errorf("old closeReason = %q, want %q", oldWatch.CloseReason, domain.CloseReasonHeadChanged)
	}
	if !oldWatch.ClosedAt.Equal(clock.t) {
		t.Errorf("old ClosedAt = %v, want %v", oldWatch.ClosedAt, clock.t)
	}

	newWatch, err := store.Watch(next.WatchID)
	if err != nil {
		t.Fatalf("successor Watch missing: %v", err)
	}
	if newWatch.HeadSHA != "new-head" {
		t.Errorf("successor headSHA = %q, want new-head", newWatch.HeadSHA)
	}
	if len(newWatch.Subscribers) != 1 || newWatch.Subscribers[0].ID != "session-H" {
		t.Errorf("subscribers not carried over: %+v", newWatch.Subscribers)
	}

	active, err := store.ActiveWatches()
	if err != nil {
		t.Fatalf("ActiveWatches: %v", err)
	}
	if len(active) != 1 || active[0].ID != next.WatchID {
		t.Errorf("active watches after head change = %+v", active)
	}
}

// TestWatchPRResolvesHeadSHA covers the omitted headSHA path through CIProvider.
func TestWatchPRResolvesHeadSHA(t *testing.T) {
	store := newMemStore()
	ci := &fakeCI{head: "resolved-sha", base: "main"}
	h := NewWatchPRHandler(store, ci, &stepClock{t: time.Now()})
	in := watchPRTestInput("", domain.RoleExecutor)

	out, err := h.Register(context.Background(), sessionReq("session-R"), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	w, err := store.Watch(out.WatchID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if w.HeadSHA != "resolved-sha" || w.BaseRef != "main" {
		t.Errorf("resolved Watch = %+v", w)
	}
}

// TestWatchPRSubscriberIdentity checks the fallback rule: without a sessionID
// the notify.target is required and becomes the subscriber ID.
func TestWatchPRSubscriberIdentity(t *testing.T) {
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})

	_, err := h.Register(context.Background(), nil, WatchPRInput{
		Repo: "o/r", PR: 1, Role: domain.RoleExecutor,
	})
	assertCode(t, err, domain.CodeInvalidInput)

	in := WatchPRInput{
		Repo: "o/r", PR: 1, Role: domain.RoleExecutor, HeadSHA: "sha",
		Notify: &WatchPRNotify{Adapter: "opencode", Target: "fallback-target"},
	}
	out, err := h.Register(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("Register with target fallback: %v", err)
	}
	w, err := store.Watch(out.WatchID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if len(w.Subscribers) != 1 || w.Subscribers[0].ID != "fallback-target" {
		t.Errorf("subscriber ID = %+v, want fallback-target", w.Subscribers)
	}
}

// TestWatchPRRejectsInvalidRole covers input validation before any mutation.
func TestWatchPRRejectsInvalidRole(t *testing.T) {
	h := NewWatchPRHandler(newMemStore(), nil, &stepClock{t: time.Now()})
	_, err := h.Register(context.Background(), sessionReq("s"), WatchPRInput{
		Repo: "o/r", PR: 1, HeadSHA: "sha", Role: domain.Role("untrusted"),
	})
	assertCode(t, err, domain.CodeInvalidInput)
}

func assertCode(t *testing.T, err error, want domain.ErrorCode) {
	t.Helper()
	var appErr *domain.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("error %v does not carry a domain.AppError", err)
	}
	if appErr.Code != want {
		t.Errorf("error code = %q, want %q", appErr.Code, want)
	}
}

// bridgeCall is one recorded invocation of a bridge operation.
type bridgeCall struct {
	op  string
	req BridgeRequest
}

// recordingBridge is a test double for the Bridge port (ADR 0024). It records
// every operation and never names a concrete agent. Two instances stand for
// two different agents driven by the same watch_pr call.
type recordingBridge struct {
	name  string
	mu    sync.Mutex
	calls []bridgeCall
}

func (b *recordingBridge) record(op string, req BridgeRequest) (BridgeReply, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, bridgeCall{op: op, req: req})
	return BridgeReply{Handle: b.name + "-" + op}, nil
}

func (b *recordingBridge) Start(_ context.Context, req BridgeRequest) (BridgeReply, error) {
	return b.record("start", req)
}

func (b *recordingBridge) Status(_ context.Context, req BridgeRequest) (BridgeReply, error) {
	return b.record("status", req)
}

func (b *recordingBridge) Events(_ context.Context, req BridgeRequest) (BridgeReply, error) {
	return b.record("events", req)
}

func (b *recordingBridge) Wake(_ context.Context, req BridgeRequest) (BridgeReply, error) {
	return b.record("wake", req)
}

func (b *recordingBridge) Cancel(_ context.Context, req BridgeRequest) (BridgeReply, error) {
	return b.record("cancel", req)
}

func (b *recordingBridge) Artifacts(_ context.Context, req BridgeRequest) (BridgeReply, error) {
	return b.record("artifacts", req)
}

func (b *recordingBridge) snapshot() []bridgeCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]bridgeCall(nil), b.calls...)
}

// TestWatchPRDrivesTwoBridges runs a single watch_pr through two independent
// bridges. Both receive the same neutral Start request with no core edit
// between them (ADR 0024).
func TestWatchPRDrivesTwoBridges(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	store := newMemStore()
	alpha := &recordingBridge{name: "alpha"}
	beta := &recordingBridge{name: "beta"}
	h := NewWatchPRHandler(store, nil, clock).WithBridges(alpha, beta)

	out, err := h.Register(context.Background(), sessionReq("session-2bridges"), watchPRTestInput("bridge-sha", domain.RoleExecutor))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if out.WatchID == "" {
		t.Fatal("watchID must be populated")
	}

	for _, b := range []*recordingBridge{alpha, beta} {
		calls := b.snapshot()
		if len(calls) != 1 {
			t.Fatalf("bridge %s: got %d operations, want 1", b.name, len(calls))
		}
		if calls[0].op != "start" {
			t.Errorf("bridge %s: operation = %q, want start", b.name, calls[0].op)
		}
		req := calls[0].req
		if req.WatchID != out.WatchID {
			t.Errorf("bridge %s: watchID = %q, want %q", b.name, req.WatchID, out.WatchID)
		}
		if req.Repo != "refusedguy/beacon" || req.PR != 7 || req.HeadSHA != "bridge-sha" {
			t.Errorf("bridge %s: unexpected request %+v", b.name, req)
		}
		if req.SubscriberID != "session-2bridges" {
			t.Errorf("bridge %s: subscriberID = %q, want session-2bridges", b.name, req.SubscriberID)
		}
	}
}

// TestBridgePortSixOperations exercises all six operations on both bridges.
// It proves the port is a six-operation contract that two agents can satisfy.
func TestBridgePortSixOperations(t *testing.T) {
	ops := []string{"start", "status", "events", "wake", "cancel", "artifacts"}
	req := BridgeRequest{WatchID: "w1", Repo: "o/r", PR: 1, HeadSHA: "sha", SubscriberID: "s1"}
	bridges := []*recordingBridge{{name: "alpha"}, {name: "beta"}}

	for _, b := range bridges {
		for _, op := range ops {
			var err error
			switch op {
			case "start":
				_, err = b.Start(context.Background(), req)
			case "status":
				_, err = b.Status(context.Background(), req)
			case "events":
				_, err = b.Events(context.Background(), req)
			case "wake":
				_, err = b.Wake(context.Background(), req)
			case "cancel":
				_, err = b.Cancel(context.Background(), req)
			case "artifacts":
				_, err = b.Artifacts(context.Background(), req)
			}
			if err != nil {
				t.Fatalf("bridge %s %s: %v", b.name, op, err)
			}
		}
		calls := b.snapshot()
		if len(calls) != len(ops) {
			t.Fatalf("bridge %s: got %d calls, want %d", b.name, len(calls), len(ops))
		}
		for i, want := range ops {
			if calls[i].op != want {
				t.Errorf("bridge %s call %d = %q, want %q", b.name, i, calls[i].op, want)
			}
		}
	}
}

// externalAgentNames lists concrete agent products that belong in bridges,
// not in the Go core (ADR 0024). OpenCode is excluded on purpose: its
// "ai.opencode/sessionID" key is Beacon's own MCP _meta protocol field from
// ADR 0006, not an external product that a bridge translates to.
var externalAgentNames = []string{
	"cursor", "claude", "copilot", "codex", "gemini",
	"windsurf", "aider", "replit", "tabnine", "sourcery",
}

// TestCoreHasNoAgentNames greps every production Go file in the mcp package
// for concrete agent product names. Test doubles are bridge side and excluded.
func TestCoreHasNoAgentNames(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		lower := strings.ToLower(string(data))
		for _, agent := range externalAgentNames {
			if strings.Contains(lower, agent) {
				t.Errorf("core file %s names agent product %q; ADR 0024 requires a bridge instead", name, agent)
			}
		}
		scanned++
	}
	if scanned == 0 {
		t.Fatal("no production Go files scanned")
	}
}
