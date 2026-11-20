package mcp

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
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

// TestWatchPRMergePolicyDefault proves that a caller who says nothing about
// merging still gets a stored Watch with auto-merge on and empty conditions.
func TestWatchPRMergePolicyDefault(t *testing.T) {
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})

	out, err := h.Register(context.Background(), sessionReq("merge-default"), watchPRTestInput("merge-default-sha", domain.RoleExecutor))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	w, err := store.Watch(out.WatchID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	want := domain.DefaultMergePolicy()
	if !reflect.DeepEqual(w.MergePolicy, want) {
		t.Fatalf("stored MergePolicy = %+v, want default %+v", w.MergePolicy, want)
	}
}

// TestWatchPRPolicyWithoutMergeKeepsDefault covers existing callers: a policy
// that sets only the old verification fields must still leave auto-merge on.
func TestWatchPRPolicyWithoutMergeKeepsDefault(t *testing.T) {
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
	in := watchPRTestInput("policy-no-merge-sha", domain.RoleExecutor)
	in.Policy = &WatchPRPolicy{RequireUpToDate: true, IgnoreReviews: true}

	out, err := h.Register(context.Background(), sessionReq("policy-no-merge"), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	w, err := store.Watch(out.WatchID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if !w.Policy.RequireUpToDate || !w.Policy.IgnoreReviews {
		t.Fatalf("old policy fields lost: %+v", w.Policy)
	}
	if want := domain.DefaultMergePolicy(); !reflect.DeepEqual(w.MergePolicy, want) {
		t.Fatalf("stored MergePolicy = %+v, want default %+v", w.MergePolicy, want)
	}
}

// TestWatchPRMergePolicyConditions checks every condition independently and
// together. An omitted autoMerge stays enabled; an explicit false disables it.
func TestWatchPRMergePolicyConditions(t *testing.T) {
	cases := []struct {
		name  string
		merge *WatchPRMergePolicy
		want  domain.MergePolicy
	}{
		{"auto merge disabled", &WatchPRMergePolicy{AutoMerge: boolPtr(false)}, domain.MergePolicy{}},
		{"require approval", &WatchPRMergePolicy{RequireApproval: true}, domain.MergePolicy{AutoMerge: true, RequireApproval: true}},
		{"wait for", &WatchPRMergePolicy{WaitFor: []string{"pr/42", "issue/7", "task/alpha"}}, domain.MergePolicy{AutoMerge: true, WaitFor: []string{"pr/42", "issue/7", "task/alpha"}}},
		{"wait for main", &WatchPRMergePolicy{WaitForMain: []string{"pr/99", "task/main"}}, domain.MergePolicy{AutoMerge: true, WaitForMain: []string{"pr/99", "task/main"}}},
		{"all conditions", &WatchPRMergePolicy{
			AutoMerge: boolPtr(false), RequireApproval: true,
			WaitFor: []string{"pr/1"}, WaitForMain: []string{"issue/2"},
		}, domain.MergePolicy{
			AutoMerge: false, RequireApproval: true,
			WaitFor: []string{"pr/1"}, WaitForMain: []string{"issue/2"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStore()
			h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
			in := watchPRTestInput("merge-conditions-sha", domain.RoleExecutor)
			in.Policy = &WatchPRPolicy{Merge: tc.merge}

			out, err := h.Register(context.Background(), sessionReq("merge-conditions"), in)
			if err != nil {
				t.Fatalf("Register: %v", err)
			}
			w, err := store.Watch(out.WatchID)
			if err != nil {
				t.Fatalf("Watch: %v", err)
			}
			if !reflect.DeepEqual(w.MergePolicy, tc.want) {
				t.Fatalf("stored MergePolicy = %+v, want %+v", w.MergePolicy, tc.want)
			}
		})
	}
}

// TestWatchPRMergePolicyCopiesInput proves the stored Watch owns its condition
// lists: mutating the caller's input after Register cannot change the policy.
func TestWatchPRMergePolicyCopiesInput(t *testing.T) {
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
	in := watchPRTestInput("copy-sha", domain.RoleExecutor)
	merge := &WatchPRMergePolicy{WaitFor: []string{"pr/1"}, WaitForMain: []string{"task/main"}}
	in.Policy = &WatchPRPolicy{Merge: merge}

	out, err := h.Register(context.Background(), sessionReq("merge-copy"), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	merge.WaitFor[0] = "pr/mutated"
	merge.WaitForMain[0] = "task/mutated"

	w, err := store.Watch(out.WatchID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if w.MergePolicy.WaitFor[0] != "pr/1" || w.MergePolicy.WaitForMain[0] != "task/main" {
		t.Fatalf("stored policy aliases input: %+v", w.MergePolicy)
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

// unambiguousAgentNames are concrete products or harnesses whose presence in
// a Go identifier or import path is a core binding to one agent (ADR 0024).
var unambiguousAgentNames = []string{
	"opencode", "claude", "goose", "copilot", "gemini", "codex",
	"windsurf", "aider", "replit", "tabnine", "sourcery",
}

// ambiguousAgentNames need context: ADR 0025 adds an event Cursor field to the
// core, so the bare word cursor is protocol data and not an agent binding.
var ambiguousAgentNames = []string{"cursor"}

// agentContextWords turn an ambiguous name into an agent reference, as in
// cursorAPI or cursorAgent.
var agentContextWords = []string{"api", "agent", "sdk", "cli", "cloud"}

// agentFindings scans Go identifiers and import paths for agent bindings. It
// ignores comments and string literals: the ADR 0025 event cursor and the
// "ai.opencode/sessionID" _meta key are protocol data, not agent bindings.
func agentFindings(src string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "core.go", src, 0)
	if err != nil {
		return []string{"parse: " + err.Error()}
	}

	var tokens []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Ident:
			tokens = append(tokens, v.Name)
		case *ast.ImportSpec:
			path := strings.Trim(v.Path.Value, `"`)
			tokens = append(tokens, path)
			tokens = append(tokens, strings.FieldsFunc(path, func(r rune) bool {
				return r == '/' || r == '.' || r == '-' || r == '_'
			})...)
		}
		return true
	})

	seen := map[string]bool{}
	var findings []string
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			findings = append(findings, name)
		}
	}
	for _, token := range tokens {
		lower := strings.ToLower(token)
		for _, agent := range unambiguousAgentNames {
			if strings.Contains(lower, agent) {
				add(agent)
			}
		}
		for _, agent := range ambiguousAgentNames {
			if !strings.Contains(lower, agent) {
				continue
			}
			for _, ctx := range agentContextWords {
				if strings.Contains(lower, ctx) {
					add(agent)
					break
				}
			}
		}
	}
	return findings
}

// TestCoreHasNoAgentNames checks that production Go code in the mcp package
// binds no concrete agent (ADR 0024). Test doubles live on the bridge side and
// are excluded.
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
		for _, agent := range agentFindings(string(data)) {
			t.Errorf("core file %s binds agent %q; ADR 0024 requires a bridge instead", name, agent)
		}
		scanned++
	}
	if scanned == 0 {
		t.Fatal("no production Go files scanned")
	}
}

// TestAgentFindingsPrecision proves the detector catches real agent bindings
// and ignores the ADR 0025 event cursor and the _meta protocol key.
func TestAgentFindingsPrecision(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "ADR 0025 event cursor is not an agent",
			src: "package mcp\n" +
				"// ADR 0025: Status and Cursor replace the event history.\n" +
				"type ListRunningOutput struct { Cursor string }\n" +
				"func decodeEventCursor(s string) uint64 { return 0 }",
			want: nil,
		},
		{
			name: "meta protocol key is not an agent",
			src:  "package mcp\nconst sessionIDMetaKey = \"ai.opencode/sessionID\"",
			want: nil,
		},
		{
			name: "import path binds an agent",
			src:  "package mcp\nimport \"github.com/anthropic/claude-sdk\"",
			want: []string{"claude"},
		},
		{
			name: "cursor with agent context binds an agent",
			src: "package mcp\n" +
				"type cursorAPI struct{}\n" +
				"var cursorAgent = cursorAPI{}",
			want: []string{"cursor"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := agentFindings(tc.src)
			if len(got) != len(tc.want) {
				t.Fatalf("findings = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("finding %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
