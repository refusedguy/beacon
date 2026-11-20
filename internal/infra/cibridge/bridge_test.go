package cibridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/ghci"
	"github.com/refusedguy/beacon/internal/infra/observe"
	"github.com/refusedguy/beacon/internal/infra/store"
)

type fakeCI struct {
	state domain.PRState
}

func (f fakeCI) PRState(context.Context, string, int) (domain.PRState, error) {
	return f.state, nil
}

// countingCI proves that a blocked Watch is never polled: each call is counted.
type countingCI struct {
	calls int
	state domain.PRState
}

func (f *countingCI) PRState(context.Context, string, int) (domain.PRState, error) {
	f.calls++
	return f.state, nil
}

// adapterDelivery records one adapter-aware wake delivery in tests.
type adapterDelivery struct {
	adapter string
	session string
	payload domain.WakePayload
}

// recordingAdapterWaker stands in for the wake registry. It records every
// delivery per subscriber so tests can prove who was steered and with what.
type recordingAdapterWaker struct {
	deliveries []adapterDelivery
	err        error
}

func (r *recordingAdapterWaker) Deliver(_ context.Context, adapter, sessionID string, p domain.WakePayload) error {
	r.deliveries = append(r.deliveries, adapterDelivery{adapter: adapter, session: sessionID, payload: p})
	return r.err
}

func (r *recordingAdapterWaker) forSession(id string) []adapterDelivery {
	var out []adapterDelivery
	for _, d := range r.deliveries {
		if d.session == id {
			out = append(out, d)
		}
	}
	return out
}

func TestVerdictFromStateNoChecksCleanMergeable(t *testing.T) {
	state := domain.PRState{
		State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
	}
	if got := verdictFromState(state); got != domain.VerdictReady {
		t.Fatalf("doc-only clean PR verdict = %q, want ready", got)
	}
	dirty := state
	dirty.MergeStateStatus = "DIRTY"
	if got := verdictFromState(dirty); got != domain.VerdictWatching {
		t.Fatalf("dirty no-check PR verdict = %q, want watching", got)
	}
	draft := state
	draft.IsDraft = true
	if got := verdictFromState(draft); got != domain.VerdictWatching {
		t.Fatalf("draft no-check PR verdict = %q, want watching", got)
	}
}

func TestVerdictFromChecks(t *testing.T) {
	check := func(conclusion string) map[string]domain.Check {
		return map[string]domain.Check{"build": {Conclusion: conclusion, Required: true}}
	}
	cases := []struct {
		name   string
		checks map[string]domain.Check
		want   domain.Verdict
	}{
		{"no checks", nil, domain.VerdictWatching},
		{"all green", check("SUCCESS"), domain.VerdictReady},
		{"failure", check("FAILURE"), domain.VerdictRetry},
		{"cancelled", check("CANCELLED"), domain.VerdictRetry},
		{"pending", check("PENDING"), domain.VerdictWatching},
		{"mixed", map[string]domain.Check{"a": {Conclusion: "SUCCESS"}, "b": {Conclusion: "PENDING"}}, domain.VerdictWatching},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := verdictFromChecks(tc.checks); got != tc.want {
				t.Fatalf("verdict = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBridgePublishesReadyVerdict(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	w := domain.Watch{
		ID:          "watch:refusedguy/beacon:42:abc",
		Repo:        "refusedguy/beacon",
		PR:          42,
		HeadSHA:     "abc",
		Status:      domain.StatusWatching,
		Verdict:     domain.VerdictWatching,
		Subscribers: []domain.Subscriber{{ID: "subscriber-1", Role: domain.RoleOrchestrator}},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	state := domain.PRState{
		HeadRefOid:       "abc",
		Mergeable:        "MERGEABLE",
		MergeStateStatus: "CLEAN",
		Checks: map[string]domain.Check{
			"build": {Conclusion: "SUCCESS", Required: true},
			"test":  {Conclusion: "SUCCESS", Required: true},
		},
	}
	b := New(db, fakeCI{state: state}, fixedClock{at: time.Unix(1, 0)}, time.Second)
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}

	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got.Verdict != domain.VerdictReady {
		t.Fatalf("verdict = %q, want ready", got.Verdict)
	}
	if got.Revision != w.Revision+1 {
		t.Fatalf("revision = %d, want %d", got.Revision, w.Revision+1)
	}
	if len(got.Checks) != 2 {
		t.Fatalf("checks = %d, want 2", len(got.Checks))
	}
	obligationID := domain.ObligationKey(got.ID, got.Revision, "subscriber-1")
	if _, err := db.Obligation(obligationID); err != nil {
		t.Fatalf("obligation for new revision missing: %v", err)
	}
}

func TestBridgeIgnoresStaleHeadAndKeepsPending(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	w := domain.Watch{
		ID:          "watch:refusedguy/beacon:43:old",
		Repo:        "refusedguy/beacon",
		PR:          43,
		HeadSHA:     "old",
		Status:      domain.StatusWatching,
		Verdict:     domain.VerdictWatching,
		Subscribers: []domain.Subscriber{{ID: "sub"}},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	b := New(db, fakeCI{state: domain.PRState{
		HeadRefOid: "new",
		Checks:     map[string]domain.Check{"build": {Conclusion: "SUCCESS", Required: true}},
	}}, fixedClock{at: time.Unix(1, 0)}, time.Second)
	if got := b.Refresh(context.Background()); got != 0 {
		t.Fatalf("stale head changed %d watches, want 0", got)
	}
	got, _ := db.Watch(w.ID)
	if got.Verdict != domain.VerdictWatching {
		t.Fatalf("stale head verdict = %q", got.Verdict)
	}
}

// T3.229: the orchestrator's own Watch is registered, the CI bridge moves it
// to ready at revision 1, and reading that verdict stamps readAt in the store.
func TestSelfWatchChainReadsReadyVerdictAndStampsReadAt(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const cardID = "T3.229"
	b := New(db, fakeCI{state: domain.PRState{
		HeadRefOid: "177bf240abc",
		Checks: map[string]domain.Check{
			"build": {Conclusion: "SUCCESS", Required: true},
		},
	}}, fixedClock{at: time.Unix(1, 0)}, time.Second)
	b.WithReader(dbReadStamper{db: db})

	w, err := b.RegisterSelfWatch(cardID, "refusedguy/beacon", 286, "177bf240abc")
	if err != nil {
		t.Fatalf("RegisterSelfWatch: %v", err)
	}
	if got := len(w.Subscribers); got != 1 || w.Subscribers[0].Role != domain.RoleOrchestrator {
		t.Fatalf("subscribers = %+v, want one orchestrator", w.Subscribers)
	}
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}

	ctx := context.Background()
	res, err := b.ReadVerdict(ctx, w.ID, cardID)
	if err != nil {
		t.Fatalf("ReadVerdict: %v", err)
	}
	if res.Verdict != domain.VerdictReady || res.Revision != 1 {
		t.Fatalf("verdict = %q revision = %d, want ready/1", res.Verdict, res.Revision)
	}
	if res.ReadAt.IsZero() {
		t.Fatal("readAt was not stamped")
	}
	o, err := db.Obligation(res.ObligationID)
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if !o.ReadAt.Equal(res.ReadAt) {
		t.Fatalf("stored readAt = %v, want %v", o.ReadAt, res.ReadAt)
	}

	again, err := b.ReadVerdict(ctx, w.ID, cardID)
	if err != nil {
		t.Fatalf("second ReadVerdict: %v", err)
	}
	if !again.ReadAt.Equal(res.ReadAt) {
		t.Fatalf("second read changed readAt: %v -> %v", res.ReadAt, again.ReadAt)
	}

	// A repeated registration must not reset the revision or the verdict.
	repeated, err := b.RegisterSelfWatch(cardID, "refusedguy/beacon", 286, "177bf240abc")
	if err != nil {
		t.Fatalf("repeated RegisterSelfWatch: %v", err)
	}
	if repeated.Revision != res.Revision || repeated.Verdict != domain.VerdictReady {
		t.Fatalf("repeated registration changed the watch: %+v", repeated)
	}

	// A substituted subscriber has no obligation and cannot confirm a read.
	if _, err := b.ReadVerdict(ctx, w.ID, "stubbed-card"); err == nil {
		t.Fatal("ReadVerdict accepted a subscriber without an obligation")
	}
}

// dbReadStamper is the same narrow port the MCP stamper exposes, backed by the
// real store writer so the test can read readAt back from disk.
type dbReadStamper struct{ db *store.DB }

func (s dbReadStamper) StampRead(ctx context.Context, id string, at time.Time) (domain.Obligation, bool, error) {
	var out domain.Obligation
	applied := false
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var payload []byte
		if err := tx.QueryRow(`SELECT payload FROM obligations WHERE id = ?`, id).Scan(&payload); err != nil {
			return err
		}
		var o domain.Obligation
		if err := json.Unmarshal(payload, &o); err != nil {
			return err
		}
		if !o.ReadAt.IsZero() {
			out = o
			return nil
		}
		o.ReadAt = at
		next, err := json.Marshal(o)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE obligations SET payload = ? WHERE id = ?`, next, id); err != nil {
			return err
		}
		out, applied = o, true
		return nil
	})
	return out, applied, err
}

type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

// recordingWaker counts wake deliveries so terminal-verdict tests can prove
// that a closed Watch does not steer a subscriber session.
type recordingWaker struct {
	deliveries []domain.WakePayload
}

func (r *recordingWaker) Deliver(_ context.Context, _ string, p domain.WakePayload) error {
	r.deliveries = append(r.deliveries, p)
	return nil
}

func TestBridgePRStateTerminal(t *testing.T) {
	cases := []struct {
		name        string
		prState     string
		wantUpdated int
		wantStatus  domain.Status
		wantVerdict domain.Verdict
		wantRev     uint64
	}{
		{"merged", "MERGED", 1, domain.StatusClosed, domain.VerdictMerged, 1},
		{"closed", "CLOSED", 1, domain.StatusClosed, domain.VerdictClosed, 1},
		{"open", "OPEN", 0, domain.StatusWatching, domain.VerdictWatching, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer db.Close()

			w := domain.Watch{
				ID:          "watch:refusedguy/beacon:44:abc",
				Repo:        "refusedguy/beacon",
				PR:          44,
				HeadSHA:     "abc",
				Status:      domain.StatusWatching,
				Verdict:     domain.VerdictWatching,
				Subscribers: []domain.Subscriber{{ID: "subscriber-1", Role: domain.RoleExecutor}},
			}
			if err := db.SaveWatch(w); err != nil {
				t.Fatalf("SaveWatch: %v", err)
			}

			state := domain.PRState{
				State:      tc.prState,
				HeadRefOid: "abc",
				Checks:     map[string]domain.Check{"build": {Conclusion: "PENDING", Required: true}},
			}
			waker := &recordingWaker{}
			b := New(db, fakeCI{state: state}, fixedClock{at: time.Unix(1, 0)}, time.Second).WithWaker(waker)
			if got := b.Refresh(context.Background()); got != tc.wantUpdated {
				t.Fatalf("Refresh updated %d watches, want %d", got, tc.wantUpdated)
			}

			got, err := db.Watch(w.ID)
			if err != nil {
				t.Fatalf("Watch: %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Verdict != tc.wantVerdict {
				t.Fatalf("verdict = %q, want %q", got.Verdict, tc.wantVerdict)
			}
			if got.Revision != w.Revision+tc.wantRev {
				t.Fatalf("revision = %d, want %d", got.Revision, w.Revision+tc.wantRev)
			}
			if len(waker.deliveries) != 0 {
				t.Fatalf("wake delivered %d times, want 0", len(waker.deliveries))
			}
		})
	}
}

func TestBridgeClosesReadyWatchOnMerge(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// The common case: the Watch already reached ready, which used to keep it
	// out of Refresh entirely and leave it active after the PR merged.
	w := domain.Watch{
		ID:          "watch:refusedguy/beacon:45:abc",
		Repo:        "refusedguy/beacon",
		PR:          45,
		HeadSHA:     "abc",
		Status:      domain.StatusNotifying,
		Verdict:     domain.VerdictReady,
		Revision:    2,
		Subscribers: []domain.Subscriber{{ID: "subscriber-1", Role: domain.RoleOrchestrator}},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	waker := &recordingWaker{}
	b := New(db, fakeCI{state: domain.PRState{
		State:      "MERGED",
		HeadRefOid: "abc",
	}}, fixedClock{at: time.Unix(1, 0)}, time.Second).WithWaker(waker)
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}

	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got.Status != domain.StatusClosed || got.Verdict != domain.VerdictMerged {
		t.Fatalf("status/verdict = %q/%q, want closed/merged", got.Status, got.Verdict)
	}
	if got.Revision != w.Revision+1 {
		t.Fatalf("revision = %d, want %d", got.Revision, w.Revision+1)
	}
	if len(waker.deliveries) != 0 {
		t.Fatalf("wake delivered %d times, want 0", len(waker.deliveries))
	}
}

// blockedTestWatch builds a Watch already in the blocked state with the given
// subscribers. It mirrors the payload written by domain.BlockEvent.
func blockedTestWatch(id string, pr int, subscribers ...domain.Subscriber) domain.Watch {
	return domain.Watch{
		ID:      id,
		Repo:    "refusedguy/beacon",
		PR:      pr,
		HeadSHA: "abc",
		Status:  domain.StatusBlocked,
		Verdict: domain.VerdictBlocked,
		Revision: 3,
		Blocked: &domain.BlockedInfo{
			Reason:       "two required checks failed outside executor's diff",
			FailedChecks: []string{"ci/test", "gates"},
			Since:        time.Unix(1, 0),
			BlockedBy:    "ci-bridge",
		},
		Subscribers: subscribers,
	}
}

// TestBridgeSkipsBlockedWatch proves that Refresh neither polls CI nor
// publishes a retry for a blocked Watch, so the executor is never woken with a
// false retry. The Watch must be read from the real store, whose ActiveWatches
// excludes StatusBlocked: the bridge has to observe it anyway.
func TestBridgeSkipsBlockedWatch(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	w := blockedTestWatch("watch:refusedguy/beacon:60:abc", 60,
		domain.Subscriber{ID: "ses_executor", Role: domain.RoleExecutor, Adapter: "opencode"},
	)
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	ci := &countingCI{state: domain.PRState{
		HeadRefOid: "abc",
		Checks:     map[string]domain.Check{"build": {Conclusion: "FAILURE", Required: true}},
	}}
	waker := &recordingAdapterWaker{}
	b := New(db, ci, fixedClock{at: time.Unix(2, 0)}, time.Second).WithWaker(waker)
	if got := b.Refresh(context.Background()); got != 0 {
		t.Fatalf("Refresh updated %d watches, want 0 for a blocked Watch", got)
	}
	if ci.calls != 0 {
		t.Fatalf("CI polled %d times for a blocked Watch, want 0", ci.calls)
	}
	if got := len(waker.deliveries); got != 0 {
		t.Fatalf("executor received %d wakes, want 0", got)
	}
	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got.Verdict != domain.VerdictBlocked || got.Revision != w.Revision {
		t.Fatalf("blocked Watch changed: verdict=%q revision=%d, want blocked/%d", got.Verdict, got.Revision, w.Revision)
	}
}

// TestBridgeWakesOrchestratorOnBlockedWatch proves the orchestrator receives a
// single FIX-agent wake through the adapter-aware registry with the PR, reason,
// failed checks and watch id, while the executor receives none. It also proves
// the wake is idempotent across ticks and stages a blocked obligation.
func TestBridgeWakesOrchestratorOnBlockedWatch(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	w := blockedTestWatch("watch:refusedguy/beacon:61:abc", 61,
		domain.Subscriber{ID: "ses_orchestrator", Role: domain.RoleOrchestrator, Adapter: "opencode"},
		domain.Subscriber{ID: "ses_executor", Role: domain.RoleExecutor, Adapter: "opencode"},
	)
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	waker := &recordingAdapterWaker{}
	b := New(db, &countingCI{}, fixedClock{at: time.Unix(2, 0)}, time.Second).WithWaker(waker)
	ctx := context.Background()
	if got := b.Refresh(ctx); got != 0 {
		t.Fatalf("Refresh updated %d watches, want 0", got)
	}

	orch := waker.forSession("ses_orchestrator")
	if len(orch) != 1 {
		t.Fatalf("orchestrator wakes = %d, want 1: %+v", len(orch), waker.deliveries)
	}
	if got := waker.forSession("ses_executor"); len(got) != 0 {
		t.Fatalf("executor wakes = %d, want 0", len(got))
	}
	d := orch[0]
	if d.adapter != "opencode" {
		t.Fatalf("delivery adapter = %q, want opencode", d.adapter)
	}
	msg := d.payload.Verdict
	for _, want := range []string{
		"нужен FIX-агент",
		"refusedguy/beacon#61",
		"two required checks failed outside executor's diff",
		"ci/test",
		"gates",
		w.ID,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("wake message %q missing %q", msg, want)
		}
	}
	if want := domain.ObligationKey(w.ID, w.Revision, "ses_orchestrator"); d.payload.ObligationID != want {
		t.Fatalf("obligation = %q, want %q", d.payload.ObligationID, want)
	}
	if d.payload.WatchID != w.ID || d.payload.Revision != w.Revision {
		t.Fatalf("payload identifiers = %q/%d, want %q/%d", d.payload.WatchID, d.payload.Revision, w.ID, w.Revision)
	}
	o, err := db.Obligation(d.payload.ObligationID)
	if err != nil {
		t.Fatalf("blocked obligation missing: %v", err)
	}
	if o.VerdictPointer != domain.VerdictBlocked {
		t.Fatalf("obligation verdict = %q, want blocked", o.VerdictPointer)
	}

	// A second tick over the same block must not steer the orchestrator again.
	if got := b.Refresh(ctx); got != 0 {
		t.Fatalf("second Refresh updated %d watches, want 0", got)
	}
	if got := len(waker.forSession("ses_orchestrator")); got != 1 {
		t.Fatalf("orchestrator wakes after second tick = %d, want 1", got)
	}
}

// TestBridgeResumesPollingAfterUnblock proves that once the block is cleared
// the bridge polls CI again and publishes the verdict. The unblock goes through
// the domain transition so the Watch shape matches production.
func TestBridgeResumesPollingAfterUnblock(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	w := blockedTestWatch("watch:refusedguy/beacon:62:abc", 62,
		domain.Subscriber{ID: "ses_orchestrator", Role: domain.RoleOrchestrator, Adapter: "opencode"},
	)
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	ci := &countingCI{state: domain.PRState{
		HeadRefOid: "abc",
		Checks:     map[string]domain.Check{"build": {Conclusion: "SUCCESS", Required: true}},
	}}
	waker := &recordingAdapterWaker{}
	b := New(db, ci, fixedClock{at: time.Unix(2, 0)}, time.Second).WithWaker(waker)
	ctx := context.Background()

	if got := b.Refresh(ctx); got != 0 || ci.calls != 0 {
		t.Fatalf("blocked tick: updated=%d ciCalls=%d, want 0/0", got, ci.calls)
	}

	current, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	unblocked, _ := domain.Transition(current, domain.UnblockEvent(time.Unix(3, 0)))
	if unblocked.Blocked != nil || unblocked.Status != domain.StatusWatching || unblocked.Verdict != domain.VerdictWatching {
		t.Fatalf("unblock transition produced %+v, want watching without block", unblocked)
	}
	if err := db.SaveWatch(unblocked); err != nil {
		t.Fatalf("SaveWatch(unblocked): %v", err)
	}

	if got := b.Refresh(ctx); got != 1 {
		t.Fatalf("Refresh after unblock updated %d watches, want 1", got)
	}
	if ci.calls != 1 {
		t.Fatalf("CI polled %d times after unblock, want 1", ci.calls)
	}
	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch after unblock: %v", err)
	}
	if got.Verdict != domain.VerdictReady {
		t.Fatalf("verdict after unblock = %q, want ready", got.Verdict)
	}
	if got.Revision != unblocked.Revision+1 {
		t.Fatalf("revision after unblock = %d, want %d", got.Revision, unblocked.Revision+1)
	}
}

// TestBridgeBlockedWakeFailureIsRetried proves a failed delivery is not marked
// as notified, so the next tick can deliver the FIX-agent wake again.
func TestBridgeBlockedWakeFailureIsRetried(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	w := blockedTestWatch("watch:refusedguy/beacon:63:abc", 63,
		domain.Subscriber{ID: "ses_orchestrator", Role: domain.RoleOrchestrator, Adapter: "opencode"},
	)
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	waker := &recordingAdapterWaker{err: errors.New("transport down")}
	b := New(db, &countingCI{}, fixedClock{at: time.Unix(2, 0)}, time.Second).WithWaker(waker)
	ctx := context.Background()
	b.Refresh(ctx)
	if got := len(waker.deliveries); got != 1 {
		t.Fatalf("deliveries after failed wake = %d, want 1 attempt", got)
	}
	waker.err = nil
	b.Refresh(ctx)
	if got := len(waker.deliveries); got != 2 {
		t.Fatalf("deliveries after retry = %d, want 2 attempts", got)
	}
}

// fixerStore carries the fixFor link next to a real store. It stands in for
// the domain.Watch.FixFor field until the fixer-role change reaches main;
// cibridge reads the link through the optional fixForSource capability.
type fixerStore struct {
	*store.DB
	links map[string]string
}

func (s *fixerStore) FixForOf(watchID string) string {
	return s.links[watchID]
}

// perPRCI answers with a different PR state per number, so a fixer PR can be
// MERGED while the source Watch keeps reporting a normal CI state.
type perPRCI struct {
	states map[int]domain.PRState
	calls  map[int]int
}

func (c *perPRCI) PRState(_ context.Context, _ string, pr int) (domain.PRState, error) {
	if c.calls != nil {
		c.calls[pr]++
	}
	return c.states[pr], nil
}

// fixerTestWatch builds a live Watch on the fix PR. It is the Watch that
// carries fixFor and whose PR the bridge observes as MERGED.
func fixerTestWatch(id string, pr int) domain.Watch {
	return domain.Watch{
		ID:      id,
		Repo:    "refusedguy/beacon",
		PR:      pr,
		HeadSHA: "fix",
		Status:  domain.StatusWatching,
		Verdict: domain.VerdictWatching,
		Subscribers: []domain.Subscriber{{
			ID: "ses_fixer", Role: domain.RoleExecutor, Adapter: "opencode",
		}},
	}
}

// TestBridgeUnblocksSourceOnFixerMerge proves that a merged fixer PR releases
// the blocked Watch named by fixFor: status and verdict return to watching,
// the block details are cleared, and the next tick polls CI for the source.
func TestBridgeUnblocksSourceOnFixerMerge(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const sourceID = "watch:refusedguy/beacon:60:abc"
	const fixerID = "watch:refusedguy/beacon:70:fix"

	source := blockedTestWatch(sourceID, 60,
		domain.Subscriber{ID: "ses_orchestrator", Role: domain.RoleOrchestrator, Adapter: "opencode"},
	)
	if err := db.SaveWatch(source); err != nil {
		t.Fatalf("SaveWatch(source): %v", err)
	}
	if err := db.SaveWatch(fixerTestWatch(fixerID, 70)); err != nil {
		t.Fatalf("SaveWatch(fixer): %v", err)
	}

	fs := &fixerStore{DB: db, links: map[string]string{fixerID: sourceID}}
	ci := &perPRCI{
		states: map[int]domain.PRState{
			70: {State: "MERGED", HeadRefOid: "fix"},
			60: {HeadRefOid: "abc", Checks: map[string]domain.Check{
				"build": {Conclusion: "SUCCESS", Required: true},
			}},
		},
		calls: map[int]int{},
	}
	b := New(fs, ci, fixedClock{at: time.Unix(4, 0)}, time.Second)
	ctx := context.Background()

	if got := b.Refresh(ctx); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1 (the merged fixer)", got)
	}

	got, err := db.Watch(sourceID)
	if err != nil {
		t.Fatalf("Watch(source): %v", err)
	}
	if got.Status != domain.StatusWatching || got.Verdict != domain.VerdictWatching {
		t.Fatalf("source status/verdict = %q/%q, want watching/watching", got.Status, got.Verdict)
	}
	if got.Blocked != nil {
		t.Fatalf("source kept block details: %+v", got.Blocked)
	}
	if got.Revision != source.Revision+1 {
		t.Fatalf("source revision = %d, want %d", got.Revision, source.Revision+1)
	}
	if ci.calls[60] != 0 {
		t.Fatalf("source CI polled %d times in the merge tick, want 0", ci.calls[60])
	}
	fixed, err := db.Watch(fixerID)
	if err != nil {
		t.Fatalf("Watch(fixer): %v", err)
	}
	if fixed.Status != domain.StatusClosed || fixed.Verdict != domain.VerdictMerged {
		t.Fatalf("fixer status/verdict = %q/%q, want closed/merged", fixed.Status, fixed.Verdict)
	}

	// Back in the normal loop: the source is polled and reaches ready.
	if got := b.Refresh(ctx); got != 1 {
		t.Fatalf("second Refresh updated %d watches, want 1", got)
	}
	if ci.calls[60] != 1 {
		t.Fatalf("source CI polled %d times after unblock, want 1", ci.calls[60])
	}
	got, err = db.Watch(sourceID)
	if err != nil {
		t.Fatalf("Watch(source) after poll: %v", err)
	}
	if got.Verdict != domain.VerdictReady {
		t.Fatalf("source verdict after poll = %q, want ready", got.Verdict)
	}
}

// TestBridgeMergeLeavesNonBlockedSourceAlone proves fixFor only resumes a
// blocked Watch; a source that is already live is not changed or re-polled
// with a merged verdict by the fixer's merge.
func TestBridgeMergeLeavesNonBlockedSourceAlone(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const sourceID = "watch:refusedguy/beacon:60:abc"
	const fixerID = "watch:refusedguy/beacon:71:fix"

	source := domain.Watch{
		ID: sourceID, Repo: "refusedguy/beacon", PR: 60, HeadSHA: "abc",
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching, Revision: 7,
		Subscribers: []domain.Subscriber{{ID: "ses_executor", Role: domain.RoleExecutor}},
	}
	if err := db.SaveWatch(source); err != nil {
		t.Fatalf("SaveWatch(source): %v", err)
	}
	if err := db.SaveWatch(fixerTestWatch(fixerID, 71)); err != nil {
		t.Fatalf("SaveWatch(fixer): %v", err)
	}

	fs := &fixerStore{DB: db, links: map[string]string{fixerID: sourceID}}
	ci := &perPRCI{
		states: map[int]domain.PRState{
			71: {State: "MERGED", HeadRefOid: "fix"},
			60: {HeadRefOid: "abc", Checks: map[string]domain.Check{
				"build": {Conclusion: "PENDING", Required: true},
			}},
		},
		calls: map[int]int{},
	}
	b := New(fs, ci, fixedClock{at: time.Unix(4, 0)}, time.Second)
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}

	got, err := db.Watch(sourceID)
	if err != nil {
		t.Fatalf("Watch(source): %v", err)
	}
	if got.Status != domain.StatusWatching || got.Verdict != domain.VerdictWatching {
		t.Fatalf("source status/verdict = %q/%q, want watching/watching", got.Status, got.Verdict)
	}
	if got.Revision != source.Revision || got.Blocked != nil {
		t.Fatalf("non-blocked source changed: revision=%d blocked=%+v, want %d/nil", got.Revision, got.Blocked, source.Revision)
	}
}

// TestBridgeRepeatedMergeUnblocksOnce proves one MERGE produces one unblock:
// the fixer Watch is closed by its own verdict, and a later tick cannot bump
// the source revision again even though the fixer PR still reports MERGED.
func TestBridgeRepeatedMergeUnblocksOnce(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const sourceID = "watch:refusedguy/beacon:60:abc"
	const fixerID = "watch:refusedguy/beacon:72:fix"

	source := blockedTestWatch(sourceID, 60,
		domain.Subscriber{ID: "ses_orchestrator", Role: domain.RoleOrchestrator, Adapter: "opencode"},
	)
	if err := db.SaveWatch(source); err != nil {
		t.Fatalf("SaveWatch(source): %v", err)
	}
	if err := db.SaveWatch(fixerTestWatch(fixerID, 72)); err != nil {
		t.Fatalf("SaveWatch(fixer): %v", err)
	}

	fs := &fixerStore{DB: db, links: map[string]string{fixerID: sourceID}}
	ci := &perPRCI{
		states: map[int]domain.PRState{
			72: {State: "MERGED", HeadRefOid: "fix"},
			60: {HeadRefOid: "abc", Checks: map[string]domain.Check{
				"build": {Conclusion: "PENDING", Required: true},
			}},
		},
		calls: map[int]int{},
	}
	b := New(fs, ci, fixedClock{at: time.Unix(4, 0)}, time.Second)
	ctx := context.Background()

	if got := b.Refresh(ctx); got != 1 {
		t.Fatalf("first Refresh updated %d watches, want 1", got)
	}
	afterFirst, err := db.Watch(sourceID)
	if err != nil {
		t.Fatalf("Watch(source): %v", err)
	}
	if afterFirst.Revision != source.Revision+1 {
		t.Fatalf("source revision after merge = %d, want %d", afterFirst.Revision, source.Revision+1)
	}

	for i := 0; i < 3; i++ {
		if got := b.Refresh(ctx); got != 0 {
			t.Fatalf("Refresh %d updated %d watches, want 0", i+2, got)
		}
	}
	again, err := db.Watch(sourceID)
	if err != nil {
		t.Fatalf("Watch(source) after repeats: %v", err)
	}
	if again.Revision != afterFirst.Revision {
		t.Fatalf("source revision after repeated MERGE = %d, want %d", again.Revision, afterFirst.Revision)
	}
}

// TestBridgeMergeWithoutFixForDoesNothing proves a merged PR with no fixFor
// link never touches any other Watch.
func TestBridgeMergeWithoutFixForDoesNothing(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const sourceID = "watch:refusedguy/beacon:60:abc"
	const fixerID = "watch:refusedguy/beacon:73:fix"

	source := blockedTestWatch(sourceID, 60,
		domain.Subscriber{ID: "ses_orchestrator", Role: domain.RoleOrchestrator, Adapter: "opencode"},
	)
	if err := db.SaveWatch(source); err != nil {
		t.Fatalf("SaveWatch(source): %v", err)
	}
	if err := db.SaveWatch(fixerTestWatch(fixerID, 73)); err != nil {
		t.Fatalf("SaveWatch(fixer): %v", err)
	}

	fs := &fixerStore{DB: db, links: map[string]string{}}
	ci := &perPRCI{
		states: map[int]domain.PRState{
			73: {State: "MERGED", HeadRefOid: "fix"},
			60: {HeadRefOid: "abc", Checks: map[string]domain.Check{
				"build": {Conclusion: "PENDING", Required: true},
			}},
		},
		calls: map[int]int{},
	}
	b := New(fs, ci, fixedClock{at: time.Unix(4, 0)}, time.Second)
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}

	got, err := db.Watch(sourceID)
	if err != nil {
		t.Fatalf("Watch(source): %v", err)
	}
	if got.Status != domain.StatusBlocked || got.Verdict != domain.VerdictBlocked {
		t.Fatalf("source status/verdict = %q/%q, want blocked/blocked", got.Status, got.Verdict)
	}
	if got.Revision != source.Revision || got.Blocked == nil {
		t.Fatalf("source changed without fixFor: revision=%d blocked=%+v", got.Revision, got.Blocked)
	}
}

// TestPollIntervalFromEnv holds the new cadence contract: 30s by default and
// BEACON_CI_POLL_INTERVAL as an explicit override.
func TestPollIntervalFromEnv(t *testing.T) {
	t.Setenv(ciPollIntervalEnv, "")
	if got := PollInterval(); got != DefaultPollInterval {
		t.Fatalf("default poll interval = %s, want %s", got, DefaultPollInterval)
	}
	t.Setenv(ciPollIntervalEnv, "45s")
	if got := PollInterval(); got != 45*time.Second {
		t.Fatalf("configured poll interval = %s, want 45s", got)
	}
	for _, bad := range []string{"nonsense", "0s", "-5s"} {
		t.Setenv(ciPollIntervalEnv, bad)
		if got := PollInterval(); got != DefaultPollInterval {
			t.Errorf("poll interval for %q = %s, want %s", bad, got, DefaultPollInterval)
		}
	}
	if b := New(nil, nil, nil, 0); b.interval != DefaultPollInterval {
		t.Fatalf("New interval = %s, want %s", b.interval, DefaultPollInterval)
	}
}

const (
	ghViewJSON   = `{"headRefOid":"abc","baseRefName":"main","state":"OPEN","isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"APPROVED"}`
	ghChecksJSON = `[{"name":"build","state":"PASS","link":"https://example.test/build"}]`
)

// ghRunner counts gh invocations and routes view and checks to their payloads.
// fail toggles a transient checks failure without rebuilding the provider.
func ghRunner(calls *int, fail *bool) ghci.Runner {
	return func(_ context.Context, args ...string) ([]byte, error) {
		*calls = *calls + 1
		if len(args) >= 2 && args[1] == "view" {
			return []byte(ghViewJSON), nil
		}
		if *fail {
			return nil, errors.New("exit status 1")
		}
		return []byte(ghChecksJSON), nil
	}
}

// TestProviderCacheTTL proves that repeated PRState calls inside the TTL make
// no gh calls, and that the pair of calls resumes once the TTL expires.
func TestProviderCacheTTL(t *testing.T) {
	now := time.Unix(100, 0)
	calls, fail := 0, false
	p := ghci.New(ghci.Config{Now: func() time.Time { return now }, Run: ghRunner(&calls, &fail)})
	ctx := context.Background()

	first, err := p.PRState(ctx, "o/r", 7)
	if err != nil {
		t.Fatalf("first PRState: %v", err)
	}
	if calls != 2 {
		t.Fatalf("first PRState made %d gh calls, want 2", calls)
	}
	second, err := p.PRState(ctx, "o/r", 7)
	if err != nil {
		t.Fatalf("cached PRState: %v", err)
	}
	if calls != 2 || second.HeadRefOid != first.HeadRefOid {
		t.Fatalf("cached PRState made %d gh calls and state %+v, want 2 and %+v", calls, second, first)
	}

	now = now.Add(19 * time.Second)
	if _, err := p.PRState(ctx, "o/r", 7); err != nil || calls != 2 {
		t.Fatalf("PRState at TTL-1: err=%v ghCalls=%d, want nil/2", err, calls)
	}
	now = now.Add(2 * time.Second)
	if _, err := p.PRState(ctx, "o/r", 7); err != nil || calls != 4 {
		t.Fatalf("PRState after TTL: err=%v ghCalls=%d, want nil/4", err, calls)
	}
}

// TestProviderMinimumInterval proves an expired cache does not let the same PR
// be polled again immediately. The cached observation is served until the
// minimum interval elapses.
func TestProviderMinimumInterval(t *testing.T) {
	now := time.Unix(200, 0)
	calls, fail := 0, false
	p := ghci.New(ghci.Config{
		CacheTTL: time.Nanosecond, MinInterval: 5 * time.Second,
		Now: func() time.Time { return now }, Run: ghRunner(&calls, &fail),
	})
	ctx := context.Background()
	if _, err := p.PRState(ctx, "o/r", 8); err != nil || calls != 2 {
		t.Fatalf("first PRState: err=%v ghCalls=%d, want nil/2", err, calls)
	}
	now = now.Add(time.Second)
	if _, err := p.PRState(ctx, "o/r", 8); err != nil || calls != 2 {
		t.Fatalf("PRState inside min interval: err=%v ghCalls=%d, want nil/2", err, calls)
	}
	now = now.Add(4 * time.Second)
	if _, err := p.PRState(ctx, "o/r", 8); err != nil || calls != 4 {
		t.Fatalf("PRState after min interval: err=%v ghCalls=%d, want nil/4", err, calls)
	}
}

type capturedEvent struct {
	name   string
	fields observe.Fields
}

// captureSink records provider_call fields so the test can see consecutive_errors
// grow during a transient outage instead of only observing missing verdicts.
type captureSink struct {
	mu     sync.Mutex
	events []capturedEvent
}

func (s *captureSink) Log(_ observe.Level, event string, fields observe.Fields) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := make(observe.Fields, len(fields))
	for k, v := range fields {
		snapshot[k] = v
	}
	s.events = append(s.events, capturedEvent{name: event, fields: snapshot})
}

func (s *captureSink) Inc(string, string) {}

func (s *captureSink) providerCalls() []capturedEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []capturedEvent
	for _, e := range s.events {
		if e.name == "provider_call" {
			out = append(out, e)
		}
	}
	return out
}

// TestProviderBackoffAfterError covers the reported failure mode: gh answers
// exit status 1, the provider waits before retrying, the wait doubles on the
// next failure and a success clears both the backoff and the error count.
func TestProviderBackoffAfterError(t *testing.T) {
	now := time.Unix(300, 0)
	calls, fail := 0, true
	sink := &captureSink{}
	observe.SetDefault(sink)
	defer observe.SetDefault(nil)
	p := ghci.New(ghci.Config{
		Backoff: 5 * time.Second, MaxBackoff: 20 * time.Second,
		Now: func() time.Time { return now }, Run: ghRunner(&calls, &fail),
	})
	ctx := context.Background()

	if _, err := p.PRState(ctx, "o/r", 9); err == nil || calls != 2 {
		t.Fatalf("first failing PRState: err=%v ghCalls=%d, want error/2", err, calls)
	}
	for _, step := range []time.Duration{0, 4 * time.Second} {
		now = now.Add(step)
		if _, err := p.PRState(ctx, "o/r", 9); err == nil || calls != 2 {
			t.Fatalf("PRState inside backoff (+%s): err=%v ghCalls=%d, want error/2", step, err, calls)
		}
	}
	now = now.Add(2 * time.Second)
	if _, err := p.PRState(ctx, "o/r", 9); err == nil || calls != 4 {
		t.Fatalf("PRState after first backoff: err=%v ghCalls=%d, want error/4", err, calls)
	}
	if _, err := p.PRState(ctx, "o/r", 9); err == nil || calls != 4 {
		t.Fatalf("PRState inside doubled backoff: err=%v ghCalls=%d, want error/4", err, calls)
	}

	fail = false
	now = now.Add(11 * time.Second)
	state, err := p.PRState(ctx, "o/r", 9)
	if err != nil || calls != 6 || len(state.Checks) != 1 {
		t.Fatalf("PRState after recovery: err=%v ghCalls=%d checks=%d, want nil/6/1", err, calls, len(state.Checks))
	}
	if _, err := p.PRState(ctx, "o/r", 9); err != nil || calls != 6 {
		t.Fatalf("PRState after recovery hit: err=%v ghCalls=%d, want nil/6", err, calls)
	}

	events := sink.providerCalls()
	want := []int{1, 1, 1, 2, 2, 0, 0}
	if len(events) != len(want) {
		t.Fatalf("provider_call events = %d, want %d", len(events), len(want))
	}
	for i, w := range want {
		if got := events[i].fields["consecutive_errors"]; got != w {
			t.Errorf("provider_call[%d].consecutive_errors = %v, want %d", i, got, w)
		}
	}
}
