package cibridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
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
