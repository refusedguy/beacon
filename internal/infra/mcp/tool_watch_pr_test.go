package mcp

import (
	"context"
	"errors"
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
