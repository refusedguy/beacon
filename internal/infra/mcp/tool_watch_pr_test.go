package mcp

import (
	"context"
	"encoding/json"
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

	"github.com/google/jsonschema-go/jsonschema"
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
		Notify:  &WatchPRNotify{Adapter: "opencode", Target: "ses_delivery-target"},
	}
}

// TestWatchPRInputSchemaHasExecutorDescriptor keeps the optional executor
// descriptor in the derived MCP input schema, so a client can send the
// executor data before it reports the waiting turn (T5.31, ADR 0088).
func TestWatchPRInputSchemaHasExecutorDescriptor(t *testing.T) {
	schema, err := jsonschema.For[WatchPRInput](nil)
	if err != nil {
		t.Fatalf("input schema: %v", err)
	}
	executor, ok := schema.Properties["executor"]
	if !ok {
		t.Fatal("input schema has no executor property")
	}
	props := executor.Properties
	if executor.Ref != "" {
		def, ok := schema.Defs[strings.TrimPrefix(executor.Ref, "#/$defs/")]
		if !ok {
			t.Fatalf("executor $ref %q has no definition", executor.Ref)
		}
		props = def.Properties
	}
	for _, field := range []string{"id", "role", "runtime", "agent", "capabilities", "configId"} {
		if _, ok := props[field]; !ok {
			t.Errorf("executor descriptor has no %q property", field)
		}
	}
}

// TestWatchPRExecutorJSONFields proves the tool accepts the six descriptor
// fields at the MCP boundary under their wire names.
func TestWatchPRExecutorJSONFields(t *testing.T) {
	var in WatchPRInput
	raw := `{"executor":{"id":"ses_exec-1","role":"executor","runtime":"subagent",` +
		`"agent":"build","capabilities":["watch","ci-retry"],"configId":"cfg-1"}}`
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatalf("decode watch_pr input: %v", err)
	}
	if in.Executor == nil {
		t.Fatal("executor descriptor was not decoded")
	}
	want := &WatchPRExecutor{
		ID: "ses_exec-1", Role: "executor", Runtime: "subagent", Agent: "build",
		Capabilities: []string{"watch", "ci-retry"}, ConfigID: "cfg-1",
	}
	if !reflect.DeepEqual(in.Executor, want) {
		t.Fatalf("decoded executor = %+v, want %+v", in.Executor, want)
	}
}

// TestWatchPRExecutorIsOptional proves a call made as before the descriptor
// existed still registers, and that supplying a descriptor changes neither
// validation nor the response shape (T5.31).
func TestWatchPRExecutorIsOptional(t *testing.T) {
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
	req := sessionReq("ses_session-optional")

	without, err := h.Register(context.Background(), req, watchPRTestInput("optional-sha", domain.RoleExecutor))
	if err != nil {
		t.Fatalf("Register without executor: %v", err)
	}
	in := watchPRTestInput("optional-sha-2", domain.RoleExecutor)
	in.Executor = &WatchPRExecutor{
		ID: "ses_exec-1", Role: "executor", Runtime: "subagent", Agent: "build",
		Capabilities: []string{"watch", "read-verdict"}, ConfigID: "cfg-1",
	}
	with, err := h.Register(context.Background(), req, in)
	if err != nil {
		t.Fatalf("Register with executor: %v", err)
	}
	for name, out := range map[string]WatchPROutput{"without": without, "with": with} {
		if out.WatchID == "" {
			t.Errorf("%s: watchID is empty in %+v", name, out)
		}
		if out.Status != string(domain.StatusWatching) || out.Verdict != string(domain.VerdictWatching) {
			t.Errorf("%s: unexpected status/verdict in %+v", name, out)
		}
	}

	bad := in
	bad.Repo = ""
	_, err = h.Register(context.Background(), req, bad)
	assertCode(t, err, domain.CodeInvalidInput)
}

// recordingRegistrar captures the registry row written by a registration.
type recordingRegistrar struct {
	calls        int
	projectKey   string
	watchID      string
	subscriberID string
	role         string
	runtime      string
	adapter      string
	target       string
	configID     string
	revision     uint64
}

func (r *recordingRegistrar) RegisterWatchExecutor(projectKey, watchID, subscriberID, role, runtime, adapter, target, configID string, revision uint64) error {
	r.calls++
	r.projectKey, r.watchID, r.subscriberID = projectKey, watchID, subscriberID
	r.role, r.runtime, r.adapter, r.target = role, runtime, adapter, target
	r.configID, r.revision = configID, revision
	return nil
}

// TestWatchPRCarriesExecutorConfigID proves the descriptor's configId reaches
// the stored Watch on creation and on a renewal with a new configuration.
func TestWatchPRCarriesExecutorConfigID(t *testing.T) {
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
	req := sessionReq("ses_config-1")

	created := watchPRTestInput("config-create-sha", domain.RoleExecutor)
	created.Executor = &WatchPRExecutor{ConfigID: "cfg-created"}
	first, err := h.Register(context.Background(), req, created)
	if err != nil {
		t.Fatalf("Register with configId: %v", err)
	}
	w, err := store.Watch(first.WatchID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if w.ConfigID != "cfg-created" {
		t.Errorf("stored configId = %q, want cfg-created", w.ConfigID)
	}

	// A renewal on the same head keeps the Watch and applies the new configId.
	renewed := watchPRTestInput("config-create-sha", domain.RoleExecutor)
	renewed.Executor = &WatchPRExecutor{ConfigID: "cfg-renewed"}
	second, err := h.Register(context.Background(), req, renewed)
	if err != nil {
		t.Fatalf("renew with configId: %v", err)
	}
	if second.WatchID != first.WatchID {
		t.Fatalf("renew changed watchID: %q -> %q", first.WatchID, second.WatchID)
	}
	w, err = store.Watch(second.WatchID)
	if err != nil {
		t.Fatalf("Watch after renew: %v", err)
	}
	if w.ConfigID != "cfg-renewed" {
		t.Errorf("renewed configId = %q, want cfg-renewed", w.ConfigID)
	}
}

// TestWatchPRWritesRegistryBinding proves a registration with an executor
// descriptor records the executor, Watch revision and configId. A call made
// as before the descriptor existed writes no registry row.
func TestWatchPRWritesRegistryBinding(t *testing.T) {
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
	reg := &recordingRegistrar{}
	h.SetRegistry(reg)

	in := watchPRTestInput("binding-sha", domain.RoleExecutor)
	in.Executor = &WatchPRExecutor{
		ID: "ses_exec-1", Role: "executor", Runtime: "subagent", ConfigID: "cfg-61",
	}
	out, err := h.Register(context.Background(), sessionReq("ses_binding-1"), in)
	if err != nil {
		t.Fatalf("Register with executor: %v", err)
	}
	if reg.calls != 1 {
		t.Fatalf("registrar calls = %d, want 1", reg.calls)
	}
	if reg.watchID != out.WatchID || reg.revision != out.Revision {
		t.Errorf("binding targets %q rev %d, want %q rev %d",
			reg.watchID, reg.revision, out.WatchID, out.Revision)
	}
	if reg.subscriberID != "ses_binding-1" || reg.configID != "cfg-61" {
		t.Errorf("binding identity = %q/%q, want ses_binding-1/cfg-61",
			reg.subscriberID, reg.configID)
	}
	if reg.projectKey != in.Repo || reg.role != string(domain.RoleExecutor) || reg.runtime != "subagent" {
		t.Errorf("binding descriptor = %q/%q/%q", reg.projectKey, reg.role, reg.runtime)
	}
	if reg.adapter != "opencode" || reg.target != "ses_delivery-target" {
		t.Errorf("binding notify = %q/%q", reg.adapter, reg.target)
	}

	legacy := watchPRTestInput("binding-sha-2", domain.RoleExecutor)
	if _, err := h.Register(context.Background(), sessionReq("ses_binding-1"), legacy); err != nil {
		t.Fatalf("Register without executor: %v", err)
	}
	if reg.calls != 1 {
		t.Errorf("legacy call wrote %d registry rows, want 0 more", reg.calls-1)
	}
}

// TestWatchPRExecutorAgentNameIsOpaque proves the core does not interpret the
// descriptor's agent name: changing only that field leaves the Watch and its
// subscriber identity untouched (T5.31 acceptance, ADR 0088).
func TestWatchPRExecutorAgentNameIsOpaque(t *testing.T) {
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
	in := watchPRTestInput("opaque-sha", domain.RoleExecutor)
	in.Executor = &WatchPRExecutor{ID: "ses_exec-1", Role: "executor", Agent: "build"}
	req := sessionReq("ses_session-opaque")

	first, err := h.Register(context.Background(), req, in)
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	in.Executor.Agent = "general"
	second, err := h.Register(context.Background(), req, in)
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}

	if first.WatchID != second.WatchID {
		t.Errorf("agent name changed the Watch: %q -> %q", first.WatchID, second.WatchID)
	}
	all, err := store.AllWatches()
	if err != nil {
		t.Fatalf("AllWatches: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d watches, want 1", len(all))
	}
	if len(all[0].Subscribers) != 1 || all[0].Subscribers[0].ID != "ses_session-opaque" {
		t.Fatalf("agent name changed subscriber identity: %+v", all[0].Subscribers)
	}
}

// TestWatchPRIdempotentRenew registers the same subscriber on the same triple
// twice. It must produce one Watch, one subscriber and a newer LastActivity.
func TestWatchPRIdempotentRenew(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)}
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, clock)
	req := sessionReq("ses_session-A")
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
	if sub.ID != "ses_session-A" || sub.Role != domain.RoleExecutor {
		t.Errorf("unexpected subscriber: %+v", sub)
	}
	if !sub.LastActivity.Equal(clock.t) {
		t.Errorf("LastActivity = %v, want renewed %v", sub.LastActivity, clock.t)
	}
	if sub.Adapter != "opencode" || sub.Target != "ses_delivery-target" {
		t.Errorf("notify endpoint not stored: %+v", sub)
	}
}

// TestWatchPRFanout registers an executor and an orchestrator on the same PR
// head. They must share one Watch but own separate obligations.
func TestWatchPRFanout(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)}
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, clock)

	execOut, err := h.Register(context.Background(), sessionReq("ses_exec-1"), watchPRTestInput("bbb222", domain.RoleExecutor))
	if err != nil {
		t.Fatalf("executor Register: %v", err)
	}
	orchOut, err := h.Register(context.Background(), sessionReq("ses_orch-1"), watchPRTestInput("bbb222", domain.RoleOrchestrator))
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
	if roles[domain.RoleExecutor] != "ses_exec-1" || roles[domain.RoleOrchestrator] != "ses_orch-1" {
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
	wantExec := domain.ObligationKey(execOut.WatchID, execOut.Revision, "ses_exec-1")
	wantOrch := domain.ObligationKey(execOut.WatchID, orchOut.Revision, "ses_orch-1")
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
	req := sessionReq("ses_session-H")

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
	if len(newWatch.Subscribers) != 1 || newWatch.Subscribers[0].ID != "ses_session-H" {
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

	out, err := h.Register(context.Background(), sessionReq("ses_session-R"), in)
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

	out, err := h.Register(context.Background(), sessionReq("ses_merge-default"), watchPRTestInput("merge-default-sha", domain.RoleExecutor))
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

	out, err := h.Register(context.Background(), sessionReq("ses_policy-no-merge"), in)
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

			out, err := h.Register(context.Background(), sessionReq("ses_merge-conditions"), in)
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

	out, err := h.Register(context.Background(), sessionReq("ses_merge-copy"), in)
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
		Notify: &WatchPRNotify{Adapter: "opencode", Target: "ses_fallback-target"},
	}
	out, err := h.Register(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("Register with target fallback: %v", err)
	}
	w, err := store.Watch(out.WatchID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if len(w.Subscribers) != 1 || w.Subscribers[0].ID != "ses_fallback-target" {
		t.Errorf("subscriber ID = %+v, want fallback-target", w.Subscribers)
	}
}

// TestWatchPRRejectsNonSessionSubscriber proves an executor cannot register
// under a card id or an empty address: neither can receive a wake pointer.
func TestWatchPRRejectsNonSessionSubscriber(t *testing.T) {
	h := NewWatchPRHandler(newMemStore(), nil, &stepClock{t: time.Now()})
	for _, target := range []string{"", "<BS.42>", "card-123"} {
		in := WatchPRInput{
			Repo: "o/r", PR: 1, HeadSHA: "non-session-sha", Role: domain.RoleExecutor,
			Notify: &WatchPRNotify{Adapter: "opencode", Target: target},
		}
		_, err := h.Register(context.Background(), nil, in)
		assertCode(t, err, domain.CodeInvalidInput)
	}
	// The final identity comes from _meta first, so a bad meta value is refused
	// even when notify.target happens to be a real session.
	in := watchPRTestInput("bad-meta-sha", domain.RoleExecutor)
	_, err := h.Register(context.Background(), sessionReq("card-9"), in)
	assertCode(t, err, domain.CodeInvalidInput)
}

// TestWatchPRRejectsInvalidRole covers input validation before any mutation.
func TestWatchPRRejectsInvalidRole(t *testing.T) {
	h := NewWatchPRHandler(newMemStore(), nil, &stepClock{t: time.Now()})
	_, err := h.Register(context.Background(), sessionReq("ses_s"), WatchPRInput{
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

	out, err := h.Register(context.Background(), sessionReq("ses_session-2bridges"), watchPRTestInput("bridge-sha", domain.RoleExecutor))
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
		if req.SubscriberID != "ses_session-2bridges" {
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

// ---- T5.33: the session ID from _meta keys the registry entry ----

func TestRegistrySessionIDUsesMetaThenTarget(t *testing.T) {
	cases := []struct {
		name     string
		req      *mcpsdk.CallToolRequest
		fallback string
		want     string
	}{
		{"meta wins over target", sessionReq("ses_session-A"), "target-A", "ses_session-A"},
		{"no meta falls back to target", nil, "target-B", "target-B"},
		{"empty meta value falls back", sessionReq(""), "target-C", "target-C"},
		{"no meta and no target", nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RegistrySessionID(tc.req, tc.fallback); got != tc.want {
				t.Errorf("RegistrySessionID = %q, want %q", got, tc.want)
			}
		})
	}
}

// Two calls from different sessions must produce two entries, even when the
// notify pair and the Watch revision are identical. The adapter name is never
// a key: only the opaque session ID is (ADR 0090, T5.33).
func TestWatchSessionRegistryDifferentSessions(t *testing.T) {
	reg := NewWatchSessionRegistry()
	in := watchPRTestInput("bind-sha", domain.RoleExecutor)
	out := WatchPROutput{WatchID: "watch-1", Revision: 1}

	first, created, err := reg.BindWatchPR(sessionReq("ses_session-A"), in, out)
	if err != nil || !created {
		t.Fatalf("first BindWatchPR: created=%v err=%v", created, err)
	}
	second, created, err := reg.BindWatchPR(sessionReq("ses_session-B"), in, out)
	if err != nil || !created {
		t.Fatalf("second BindWatchPR: created=%v err=%v", created, err)
	}

	if reg.Len() != 2 {
		t.Fatalf("registry entries = %d, want 2", reg.Len())
	}
	if first.SessionID != "ses_session-A" || second.SessionID != "ses_session-B" {
		t.Fatalf("session IDs = %q, %q; want session-A, session-B", first.SessionID, second.SessionID)
	}
	snap := reg.Snapshot()
	if _, ok := snap["ses_session-A"]; !ok {
		t.Error("no entry keyed by session-A")
	}
	if _, ok := snap["ses_session-B"]; !ok {
		t.Error("no entry keyed by session-B")
	}
	if _, ok := snap[in.Notify.Adapter]; ok {
		t.Error("adapter name used as a registry key")
	}
	if _, ok := snap[in.Notify.Target]; ok {
		t.Error("notify target used as a key while _meta carried a session ID")
	}
}

// A request without _meta still registers under notify.target.
func TestWatchSessionRegistryMissingMetaDoesNotBreak(t *testing.T) {
	reg := NewWatchSessionRegistry()
	in := watchPRTestInput("fallback-sha", domain.RoleExecutor)
	out := WatchPROutput{WatchID: "watch-1", Revision: 1}

	got, created, err := reg.BindWatchPR(nil, in, out)
	if err != nil {
		t.Fatalf("BindWatchPR without _meta: %v", err)
	}
	if !created {
		t.Fatal("first binding reported created=false")
	}
	if got.SessionID != in.Notify.Target {
		t.Errorf("binding session ID = %q, want %q", got.SessionID, in.Notify.Target)
	}
	if _, ok := reg.Lookup(in.Notify.Target); !ok {
		t.Error("no entry keyed by notify.target")
	}
}

func TestWatchSessionRegistryIdempotentAndRebinding(t *testing.T) {
	reg := NewWatchSessionRegistry()
	in := watchPRTestInput("rebind-sha", domain.RoleExecutor)

	first, created, err := reg.BindWatchPR(sessionReq("ses_session-A"), in, WatchPROutput{WatchID: "watch-1", Revision: 1})
	if err != nil || !created {
		t.Fatalf("first BindWatchPR: created=%v err=%v", created, err)
	}
	again, created, err := reg.BindWatchPR(sessionReq("ses_session-A"), in, WatchPROutput{WatchID: "watch-1", Revision: 1})
	if err != nil {
		t.Fatalf("repeat BindWatchPR: %v", err)
	}
	if created {
		t.Error("repeat of the same binding reported created=true")
	}
	if again != first {
		t.Errorf("repeat changed the binding: %+v -> %+v", first, again)
	}
	if reg.Len() != 1 {
		t.Fatalf("entries after repeat = %d, want 1", reg.Len())
	}

	next, created, err := reg.BindWatchPR(sessionReq("ses_session-A"), in, WatchPROutput{WatchID: "watch-1", Revision: 2})
	if err != nil || !created {
		t.Fatalf("rebind: created=%v err=%v", created, err)
	}
	if next.Revision != 2 || reg.Len() != 1 {
		t.Fatalf("rebind gave revision=%d entries=%d; want 2, 1", next.Revision, reg.Len())
	}
	if stored, ok := reg.Lookup("ses_session-A"); !ok || stored.Revision != 2 {
		t.Errorf("stored binding after rebind = %+v, ok=%v", stored, ok)
	}
}

func TestWatchSessionRegistryRejectsMissingAddress(t *testing.T) {
	reg := NewWatchSessionRegistry()
	in := watchPRTestInput("reject-sha", domain.RoleExecutor)
	in.Notify = nil

	_, _, err := reg.BindWatchPR(nil, in, WatchPROutput{WatchID: "watch-1", Revision: 1})
	assertCode(t, err, domain.CodeInvalidInput)
	if reg.Len() != 0 {
		t.Errorf("failed bind wrote %d entries, want 0", reg.Len())
	}

	_, _, err = reg.BindWatchPR(sessionReq("ses_session-A"), in, WatchPROutput{})
	assertCode(t, err, domain.CodeInvalidInput)
	if reg.Len() != 0 {
		t.Errorf("empty watchID wrote %d entries, want 0", reg.Len())
	}
}

// RegisterAndBind makes the registry entry exist before the caller can report
// "waiting, watchID". A missing _meta still registers under notify.target.
func TestRegisterAndBindLinksWatchToSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  *mcpsdk.CallToolRequest
		key  string
	}{
		{"with session meta", sessionReq("ses_session-X"), "ses_session-X"},
		{"without session meta", nil, "ses_delivery-target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewWatchSessionRegistry()
			h := NewWatchPRHandler(newMemStore(), nil, &stepClock{t: time.Now()})
			in := watchPRTestInput("link-sha", domain.RoleExecutor)

			out, err := RegisterAndBind(context.Background(), h, reg, tc.req, in)
			if err != nil {
				t.Fatalf("RegisterAndBind: %v", err)
			}
			b, ok := reg.Lookup(tc.key)
			if !ok {
				t.Fatalf("no registry entry keyed by %q", tc.key)
			}
			if b.WatchID != out.WatchID || b.Revision != out.Revision {
				t.Errorf("binding = %+v, want watchID=%q revision=%d", b, out.WatchID, out.Revision)
			}
		})
	}
}

func TestRegisterAndBindFailsOnRegistryError(t *testing.T) {
	h := NewWatchPRHandler(newMemStore(), nil, &stepClock{t: time.Now()})
	in := watchPRTestInput("fail-sha", domain.RoleExecutor)
	in.Notify = nil
	_, err := RegisterAndBind(context.Background(), h, nil, sessionReq("ses_session-X"), in)
	assertCode(t, err, domain.CodePersistFailed)

	_, err = RegisterAndBind(context.Background(), nil, NewWatchSessionRegistry(), sessionReq("ses_session-X"), in)
	assertCode(t, err, domain.CodePersistFailed)
}

// TestWatchPRInputSchemaHasOrchestratorID keeps the optional parent session in
// the derived MCP input schema and out of the required list. A caller written
// before this field existed still sends a valid watch_pr call.
func TestWatchPRInputSchemaHasOrchestratorID(t *testing.T) {
	schema, err := jsonschema.For[WatchPRInput](nil)
	if err != nil {
		t.Fatalf("input schema: %v", err)
	}
	if _, ok := schema.Properties["orchestratorID"]; !ok {
		t.Fatal("input schema has no orchestratorID property")
	}
	for _, name := range schema.Required {
		if name == "orchestratorID" {
			t.Fatal("orchestratorID must stay optional")
		}
	}
}

// TestRegisterStoresOrchestratorID proves the parent session is persisted on
// both the Watch and the registering Subscriber.
func TestRegisterStoresOrchestratorID(t *testing.T) {
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
	in := watchPRTestInput("orchestrator-id-sha", domain.RoleExecutor)
	in.OrchestratorID = "ses_parent_1"

	out, err := h.Register(context.Background(), sessionReq("ses_executor_1"), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	w, err := store.Watch(out.WatchID)
	if err != nil {
		t.Fatalf("stored Watch: %v", err)
	}
	if w.OrchestratorID != in.OrchestratorID {
		t.Errorf("Watch.OrchestratorID = %q, want %q", w.OrchestratorID, in.OrchestratorID)
	}
	var found bool
	for _, sub := range w.Subscribers {
		if sub.ID == "ses_executor_1" {
			found = true
			if sub.OrchestratorID != in.OrchestratorID {
				t.Errorf("Subscriber.OrchestratorID = %q, want %q", sub.OrchestratorID, in.OrchestratorID)
			}
		}
	}
	if !found {
		t.Fatal("executor subscriber not stored")
	}
}

// TestRegisterOrchestratorIDIsNotAWakeAddress proves the field never becomes
// a routing subscriber: blocked wakes still follow the orchestrator role.
func TestRegisterOrchestratorIDIsNotAWakeAddress(t *testing.T) {
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
	in := watchPRTestInput("no-route-sha", domain.RoleExecutor)
	in.OrchestratorID = "ses_parent_2"

	out, err := h.Register(context.Background(), sessionReq("ses_executor_2"), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	w, err := store.Watch(out.WatchID)
	if err != nil {
		t.Fatalf("stored Watch: %v", err)
	}
	for _, sub := range w.Subscribers {
		if sub.Role == domain.RoleOrchestrator {
			t.Fatalf("orchestratorID created a routing subscriber: %+v", sub)
		}
	}

	// The reverse inference is also absent: an orchestrator subscriber alone
	// does not populate the stored field.
	h2 := NewWatchPRHandler(newMemStore(), nil, &stepClock{t: time.Now()})
	in2 := watchPRTestInput("role-only-sha", domain.RoleOrchestrator)
	out2, err := h2.Register(context.Background(), sessionReq("ses_orchestrator_2"), in2)
	if err != nil {
		t.Fatalf("Register role only: %v", err)
	}
	w2, err := h2.store.Watch(out2.WatchID)
	if err != nil {
		t.Fatalf("stored Watch: %v", err)
	}
	if w2.OrchestratorID != "" {
		t.Errorf("orchestratorID = %q, want empty when not supplied", w2.OrchestratorID)
	}
}

// TestRegisterOrchestratorIDOnRenew covers the alias-reuse path, where Fanout
// rebuilds the target from the stored Watch and would otherwise drop the new
// field.
func TestRegisterOrchestratorIDOnRenew(t *testing.T) {
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
	in := watchPRTestInput("renew-orchestrator-sha", domain.RoleExecutor)

	out, err := h.Register(context.Background(), sessionReq("ses_executor_3"), in)
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	in.OrchestratorID = "ses_parent_3"
	if _, err := h.Register(context.Background(), sessionReq("ses_executor_3"), in); err != nil {
		t.Fatalf("renew Register: %v", err)
	}
	w, err := store.Watch(out.WatchID)
	if err != nil {
		t.Fatalf("stored Watch: %v", err)
	}
	if w.OrchestratorID != "ses_parent_3" {
		t.Errorf("Watch.OrchestratorID after renew = %q, want ses_parent_3", w.OrchestratorID)
	}
	for _, sub := range w.Subscribers {
		if sub.ID == "ses_executor_3" && sub.OrchestratorID != "ses_parent_3" {
			t.Errorf("Subscriber.OrchestratorID after renew = %q, want ses_parent_3", sub.OrchestratorID)
		}
	}
}

// ---- role fixer ----

// seedBlockedWatch stores the Watch a fixer will repair. The fixer registers a
// separate Watch for its own PR head.
func seedBlockedWatch(t *testing.T, store *memStore, id string) domain.Watch {
	t.Helper()
	w := domain.Watch{
		ID:      id,
		Repo:    "refusedguy/beacon",
		PR:      41,
		HeadSHA: "blocked-" + id,
		Status:  domain.StatusBlocked,
		Verdict: domain.VerdictBlocked,
		Blocked: &domain.BlockedInfo{Reason: "чужой тест", Since: time.Now()},
	}
	if err := store.SaveWatch(w); err != nil {
		t.Fatalf("seed blocked watch: %v", err)
	}
	return w
}

func assertNoWatchSaved(t *testing.T, store *memStore) {
	t.Helper()
	watches, err := store.AllWatches()
	if err != nil {
		t.Fatalf("AllWatches: %v", err)
	}
	if len(watches) != 0 {
		t.Fatalf("rejected registration wrote %d Watches, want 0", len(watches))
	}
}

// A fixer keeps the ID of the blocked Watch it repairs on both the Watch and
// its own subscriber, so a fix can be attributed later.
func TestRegisterFixerStoresFixFor(t *testing.T) {
	store := newMemStore()
	seedBlockedWatch(t, store, "watch-blocked-1")
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
	in := watchPRTestInput("fixer-sha-1", domain.RoleFixer)
	in.FixFor = "watch-blocked-1"

	out, err := h.Register(context.Background(), sessionReq("ses_fixer_1"), in)
	if err != nil {
		t.Fatalf("Register fixer: %v", err)
	}
	w, err := store.Watch(out.WatchID)
	if err != nil {
		t.Fatalf("stored Watch: %v", err)
	}
	if w.FixFor != in.FixFor {
		t.Errorf("Watch.FixFor = %q, want %q", w.FixFor, in.FixFor)
	}
	var found bool
	for _, sub := range w.Subscribers {
		if sub.ID != "ses_fixer_1" {
			continue
		}
		found = true
		if sub.Role != domain.RoleFixer {
			t.Errorf("Subscriber.Role = %q, want fixer", sub.Role)
		}
		if sub.FixFor != in.FixFor {
			t.Errorf("Subscriber.FixFor = %q, want %q", sub.FixFor, in.FixFor)
		}
	}
	if !found {
		t.Fatal("fixer subscriber not stored")
	}
}

func TestRegisterFixerRequiresFixFor(t *testing.T) {
	store := newMemStore()
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
	in := watchPRTestInput("fixer-sha-2", domain.RoleFixer)

	_, err := h.Register(context.Background(), sessionReq("ses_fixer_2"), in)
	assertCode(t, err, domain.CodeInvalidInput)
	assertNoWatchSaved(t, store)
}

func TestRegisterFixerRejectsUnknownFixFor(t *testing.T) {
	store := newMemStore()
	seedBlockedWatch(t, store, "watch-blocked-3")
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
	in := watchPRTestInput("fixer-sha-3", domain.RoleFixer)
	in.FixFor = "watch-that-does-not-exist"

	_, err := h.Register(context.Background(), sessionReq("ses_fixer_3"), in)
	assertCode(t, err, domain.CodeWatchNotFound)
	// Only the seeded Watch remains: the fixer created nothing.
	watches, err := store.AllWatches()
	if err != nil || len(watches) != 1 {
		t.Fatalf("Watches after refusal = %d, err=%v; want only the seeded Watch", len(watches), err)
	}
}

func TestRegisterFixerRejectsNonBlockedFixFor(t *testing.T) {
	store := newMemStore()
	active := domain.Watch{ID: "watch-active-4", Repo: "refusedguy/beacon", PR: 42,
		HeadSHA: "active-sha", Status: domain.StatusWatching, Verdict: domain.VerdictWatching}
	if err := store.SaveWatch(active); err != nil {
		t.Fatalf("seed active watch: %v", err)
	}
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
	in := watchPRTestInput("fixer-sha-4", domain.RoleFixer)
	in.FixFor = active.ID

	_, err := h.Register(context.Background(), sessionReq("ses_fixer_4"), in)
	assertCode(t, err, domain.CodeInvalidInput)
	watches, err := store.AllWatches()
	if err != nil || len(watches) != 1 {
		t.Fatalf("Watches after refusal = %d, err=%v; want only the seeded Watch", len(watches), err)
	}
}

func TestRegisterRejectsFixForWithoutFixerRole(t *testing.T) {
	store := newMemStore()
	seedBlockedWatch(t, store, "watch-blocked-5")
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
	in := watchPRTestInput("executor-fixfor-sha", domain.RoleExecutor)
	in.FixFor = "watch-blocked-5"

	_, err := h.Register(context.Background(), sessionReq("ses_executor_5"), in)
	assertCode(t, err, domain.CodeInvalidInput)
	watches, err := store.AllWatches()
	if err != nil || len(watches) != 1 {
		t.Fatalf("Watches after refusal = %d, err=%v; want only the seeded Watch", len(watches), err)
	}
}

// Fanout clones the stored Watch on a same-head renewal, so a changed fixFor
// has to be written back to both the Watch and the existing subscriber.
func TestRegisterFixerFixForOnRenew(t *testing.T) {
	store := newMemStore()
	seedBlockedWatch(t, store, "watch-blocked-6a")
	seedBlockedWatch(t, store, "watch-blocked-6b")
	h := NewWatchPRHandler(store, nil, &stepClock{t: time.Now()})
	in := watchPRTestInput("fixer-sha-6", domain.RoleFixer)
	in.FixFor = "watch-blocked-6a"

	out, err := h.Register(context.Background(), sessionReq("ses_fixer_6"), in)
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	in.FixFor = "watch-blocked-6b"
	if _, err := h.Register(context.Background(), sessionReq("ses_fixer_6"), in); err != nil {
		t.Fatalf("renew Register: %v", err)
	}
	w, err := store.Watch(out.WatchID)
	if err != nil {
		t.Fatalf("stored Watch: %v", err)
	}
	if w.FixFor != "watch-blocked-6b" {
		t.Errorf("Watch.FixFor after renew = %q, want watch-blocked-6b", w.FixFor)
	}
	for _, sub := range w.Subscribers {
		if sub.ID == "ses_fixer_6" && sub.FixFor != "watch-blocked-6b" {
			t.Errorf("Subscriber.FixFor after renew = %q, want watch-blocked-6b", sub.FixFor)
		}
	}
}

func TestWatchPRInputSchemaHasFixFor(t *testing.T) {
	schema, err := jsonschema.For[WatchPRInput](nil)
	if err != nil {
		t.Fatalf("input schema: %v", err)
	}
	if _, ok := schema.Properties["fixFor"]; !ok {
		t.Fatal("input schema has no fixFor property")
	}
	for _, name := range schema.Required {
		if name == "fixFor" {
			t.Fatal("fixFor must stay optional in the schema; role fixer enforces it")
		}
	}
}
