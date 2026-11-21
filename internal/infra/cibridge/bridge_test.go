package cibridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/ghci"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/observe"
	"github.com/refusedguy/beacon/internal/infra/store"
	"github.com/refusedguy/beacon/internal/infra/worktree"
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

// checkFacts only normalizes CI facts. The verdict itself belongs to
// domain.EvaluatePR, so this test asserts the facts and then the engine's
// decision for the same observation.
func TestCheckFactsAndEvaluatePR(t *testing.T) {
	check := func(conclusion string) map[string]domain.Check {
		return map[string]domain.Check{"build": {Conclusion: conclusion, Required: true}}
	}
	cases := []struct {
		name        string
		checks      map[string]domain.Check
		wantRollup  string
		wantFail    []string
		wantVerdict domain.Verdict
	}{
		{"no checks", nil, "SUCCESS", nil, domain.VerdictReady},
		{"all green", check("SUCCESS"), "SUCCESS", nil, domain.VerdictReady},
		{"failure", check("FAILURE"), "FAILURE", []string{"build"}, domain.VerdictKeepWorking},
		{"cancelled", check("CANCELLED"), "FAILURE", []string{"build"}, domain.VerdictKeepWorking},
		{"pending", check("PENDING"), "PENDING", nil, domain.VerdictWatching},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rollup, failed := checkFacts(tc.checks)
			if rollup != tc.wantRollup || strings.Join(failed, ",") != strings.Join(tc.wantFail, ",") {
				t.Fatalf("facts = %q/%v, want %q/%v", rollup, failed, tc.wantRollup, tc.wantFail)
			}
			s := domain.Snapshot{
				State: "OPEN", Rollup: rollup, Failures: failed, Checks: tc.checks,
				Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", SuitesValid: true,
			}
			got, _ := domain.EvaluatePR(s, domain.DefaultPolicy())
			if got != tc.wantVerdict {
				t.Fatalf("EvaluatePR = %q, want %q", got, tc.wantVerdict)
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

func TestBridgeHandlesStaleHeadWithHeadChangedEvent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	deadline := time.Unix(100, 0)
	since := deadline.Add(-time.Minute)
	w := domain.Watch{
		ID:              "watch:refusedguy/beacon:43:old",
		Repo:            "refusedguy/beacon",
		PR:              43,
		HeadSHA:         "old",
		BaseRef:         "main",
		Status:          domain.StatusWatching,
		Verdict:         domain.VerdictWatching,
		OrchestratorID:  "ses_parent",
		BlockerID:       "block-1",
		FixBlockerID:    "fix-1",
		DeadlineAt:      deadline,
		NoChecksSince:   since,
		NoChecksTimeout: 2 * time.Minute,
		ConfigID:        "cfg-1",
		Subscribers: []domain.Subscriber{
			{ID: "ses_executor", Role: domain.RoleExecutor, Adapter: "opencode"},
		},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	b := New(db, fakeCI{state: domain.PRState{
		HeadRefOid: "new",
		Checks:     map[string]domain.Check{"build": {Conclusion: "SUCCESS", Required: true}},
	}}, fixedClock{at: time.Unix(1, 0)}, time.Second)
	// Head change closes the old Watch rather than publishing a verdict, so it
	// is counted as skipped, not updated.
	if got := b.Refresh(context.Background()); got != 0 {
		t.Fatalf("stale head changed %d watches, want 0", got)
	}
	old, _ := db.Watch(w.ID)
	if old.Status != domain.StatusClosed || old.CloseReason != string(domain.CloseReasonHeadChanged) {
		t.Fatalf("old watch = %q/%q, want closed/head_changed", old.Status, old.CloseReason)
	}
	successor, err := db.Watch("watch:refusedguy/beacon:43:new")
	if err != nil {
		t.Fatalf("successor watch missing: %v", err)
	}
	if successor.HeadSHA != "new" || successor.OrchestratorID != "ses_parent" {
		t.Fatalf("successor = %+v, want new SHA and carried OrchestratorID", successor)
	}
	if len(successor.Subscribers) != 1 || successor.Subscribers[0].ID != "ses_executor" {
		t.Fatalf("successor subscribers = %+v, want the executor carried over", successor.Subscribers)
	}
	if !successor.DeadlineAt.Equal(deadline) || !successor.NoChecksSince.Equal(since) ||
		successor.NoChecksTimeout != 2*time.Minute {
		t.Fatalf("successor deadline = %v/%v/%v, want %v/%v/%v",
			successor.DeadlineAt, successor.NoChecksSince, successor.NoChecksTimeout,
			deadline, since, 2*time.Minute)
	}
	if successor.BlockerID != "block-1" || successor.FixBlockerID != "fix-1" || successor.ConfigID != "cfg-1" {
		t.Fatalf("successor ids = %q/%q/%q, want block-1/fix-1/cfg-1",
			successor.BlockerID, successor.FixBlockerID, successor.ConfigID)
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
		Mergeable:  "MERGEABLE", MergeStateStatus: "CLEAN",
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
		ID:       id,
		Repo:     "refusedguy/beacon",
		PR:       pr,
		HeadSHA:  "abc",
		Status:   domain.StatusBlocked,
		Verdict:  domain.VerdictBlocked,
		Revision: 3,
		Blocked: &domain.BlockedInfo{
			Reason:       domain.BlockReasonBaseChecksFailed,
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
		HeadRefOid: "abc", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		Checks: map[string]domain.Check{"build": {Conclusion: "SUCCESS", Required: true}},
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
			60: {HeadRefOid: "abc", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", Checks: map[string]domain.Check{
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

// ===== T3.225 сквозной прогон (#320) =====

type chainWaker struct {
	calls    int
	sessions []string
	payloads []domain.WakePayload
}

func (w *chainWaker) Deliver(_ context.Context, sessionID string, p domain.WakePayload) error {
	w.calls++
	w.sessions = append(w.sessions, sessionID)
	w.payloads = append(w.payloads, p)
	return nil
}

// TestChainWatchCIPointerWakeRead drives watch -> CI verdict -> pointer ->
// wake -> readAt through one real store. Every assertion reads a fact back
// from the store or from the recorded wake call, never from a test fixture
// masquerading as the result.
func TestChainWatchCIPointerWakeRead(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// EvaluatePR routes a ready verdict to the orchestrator, so the chain's
	// subscriber must carry that role to receive the wake.
	sub := domain.Subscriber{ID: "ses_chain", Role: domain.RoleOrchestrator, Adapter: "opencode", Target: "ses_chain"}
	w := domain.Watch{
		ID:          "watch:refusedguy/beacon:42:chain",
		Repo:        "refusedguy/beacon",
		PR:          42,
		HeadSHA:     "chain-sha",
		Status:      domain.StatusWatching,
		Verdict:     domain.VerdictWatching,
		Subscribers: []domain.Subscriber{sub},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	at := time.Unix(100, 0).UTC()
	ci := fakeCI{state: domain.PRState{
		HeadRefOid: "chain-sha",
		Mergeable:  "MERGEABLE", MergeStateStatus: "CLEAN",
		Checks: map[string]domain.Check{"build": {Conclusion: "SUCCESS", Required: true}},
	}}
	waker := &chainWaker{}
	b := New(db, ci, fixedClock{at: at}, time.Second).WithWaker(waker)
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}

	watch, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if watch.Verdict != domain.VerdictReady || watch.Revision != w.Revision+1 {
		t.Fatalf("verdict=%q revision=%d, want ready and %d", watch.Verdict, watch.Revision, w.Revision+1)
	}
	obligationID := domain.ObligationKey(w.ID, watch.Revision, sub.ID)
	o, err := db.Obligation(obligationID)
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if o.VerdictPointer != domain.VerdictReady || !o.StagedAt.Equal(at) {
		t.Fatalf("pointer=%q stagedAt=%v, want ready and %v", o.VerdictPointer, o.StagedAt, at)
	}
	if waker.calls != 1 || waker.sessions[0] != sub.Target {
		t.Fatalf("wake calls=%d sessions=%v, want one to %q", waker.calls, waker.sessions, sub.Target)
	}
	if waker.payloads[0].WatchID != w.ID || waker.payloads[0].ObligationID != obligationID {
		t.Fatalf("wake payload = %+v, want the watch and obligation ids", waker.payloads[0])
	}

	// beacon_get_result stamps readAt through the production SQL stamper.
	// Using it instead of a hand-written update keeps this an external fact.
	stamper := mcp.NewSQLObligationStamper(db)
	stamped, applied, err := stamper.StampRead(context.Background(), obligationID, at)
	if err != nil || !applied || stamped.ReadAt.IsZero() {
		t.Fatalf("StampRead = %+v, applied=%v, %v; want a stamped readAt", stamped, applied, err)
	}

	facts, err := b.ConfirmRead(w.ID, obligationID)
	if err != nil {
		t.Fatalf("ConfirmRead: %v", err)
	}
	if facts.WatchID != w.ID || facts.ObligationID != obligationID || facts.Revision != watch.Revision {
		t.Fatalf("facts = %+v, want the stored watch, obligation and revision", facts)
	}
	if facts.Verdict != domain.VerdictReady || facts.WakeTarget != sub.Target {
		t.Fatalf("facts verdict=%q target=%q, want ready and %q", facts.Verdict, facts.WakeTarget, sub.Target)
	}
	if !facts.ReadAt.Equal(at) {
		t.Fatalf("readAt = %v, want %v", facts.ReadAt, at)
	}
	read, err := db.Obligation(obligationID)
	if err != nil || read.ReadAt.IsZero() || !read.ReadAt.Equal(at) {
		t.Fatalf("stored readAt = %v, %v; want %v", read.ReadAt, err, at)
	}

	b.clock = fixedClock{at: at.Add(time.Hour)}
	again, err := b.ConfirmRead(w.ID, obligationID)
	if err != nil {
		t.Fatalf("repeat ConfirmRead: %v", err)
	}
	if !again.ReadAt.Equal(at) {
		t.Fatalf("repeat readAt = %v, want the first read unchanged", again.ReadAt)
	}
}

// scriptedAdapterWaker returns one queued result per delivery and records the
// adapters it was asked to wake. A drained queue means success.
type scriptedAdapterWaker struct {
	results []error
	calls   []string
}

func (w *scriptedAdapterWaker) Deliver(_ context.Context, adapter, _ string, _ domain.WakePayload) error {
	w.calls = append(w.calls, adapter)
	if len(w.results) == 0 {
		return nil
	}
	err := w.results[0]
	w.results = w.results[1:]
	return err
}

// breakerTestBridge builds a bridge with one executor subscriber and a
// scripted waker. No orchestrator is present, so a dead session cannot trigger
// an extra orphan notification delivery through the same waker.
func breakerTestBridge(t *testing.T, results []error) (*Bridge, *scriptedAdapterWaker, domain.Watch, domain.Subscriber) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	exec := domain.Subscriber{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"}
	w := domain.Watch{
		ID: "watch:breaker:1:abc", Repo: "o/r", PR: 1, HeadSHA: "abc",
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching, Revision: 1,
		Subscribers: []domain.Subscriber{exec},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	waker := &scriptedAdapterWaker{results: results}
	b := New(db, fakeCI{}, fixedClock{at: time.Unix(0, 0)}, time.Second).WithWaker(waker)
	return b, waker, w, exec
}

func breakerWake(b *Bridge, w domain.Watch, sub domain.Subscriber, at time.Time, cycle string) {
	b.wakeOne(context.Background(), w, sub, "keep_working: continue", at, cycle, true)
}

// TestAdapterBreakerOpensAfterMinimumFailedCalls proves a broken adapter stops
// receiving a delivery on every tick. One or two failures leave the breaker
// closed; the third opens it and later ticks are skipped until the wait ends.
func TestAdapterBreakerOpensAfterMinimumFailedCalls(t *testing.T) {
	transport := errors.New("transport down")
	b, waker, w, exec := breakerTestBridge(t, []error{transport, transport, transport})
	base := time.Unix(100, 0)

	for i := 0; i < 3; i++ {
		breakerWake(b, w, exec, base.Add(time.Duration(i)*time.Second), "cycle-open")
	}
	if got := len(waker.calls); got != 3 {
		t.Fatalf("deliveries before open = %d, want 3", got)
	}
	s := b.breakerFor("opencode")
	if s.state != breakerOpen {
		t.Fatalf("state after failures = %v, want open", s.state)
	}

	for i := 3; i < 6; i++ {
		breakerWake(b, w, exec, base.Add(time.Duration(i)*time.Second), "cycle-open")
	}
	if got := len(waker.calls); got != 3 {
		t.Fatalf("deliveries while OPEN = %d, want 3 (no retry each tick)", got)
	}
}

// TestAdapterBreakerHalfOpenProbeClosesBreaker proves one successful probe
// after the open wait returns the adapter to normal delivery.
func TestAdapterBreakerHalfOpenProbeClosesBreaker(t *testing.T) {
	transport := errors.New("transport down")
	b, waker, w, exec := breakerTestBridge(t, []error{transport, transport, transport, nil, nil})
	base := time.Unix(200, 0)
	for i := 0; i < 3; i++ {
		breakerWake(b, w, exec, base.Add(time.Duration(i)*time.Second), "cycle-half")
	}
	openedAt := b.breakerFor("opencode").openedAt

	probeAt := openedAt.Add(breakerOpenWait + time.Second)
	breakerWake(b, w, exec, probeAt, "cycle-half")
	if got := len(waker.calls); got != 4 {
		t.Fatalf("deliveries after probe = %d, want 4", got)
	}
	if s := b.breakerFor("opencode"); s.state != breakerClosed {
		t.Fatalf("state after successful probe = %v, want closed", s.state)
	}

	breakerWake(b, w, exec, probeAt.Add(time.Second), "cycle-half")
	if got := len(waker.calls); got != 5 {
		t.Fatalf("deliveries after close = %d, want 5", got)
	}
}

// TestAdapterBreakerHalfOpenFailedProbeReopens proves HALF_OPEN permits one
// probe: a failed probe reopens the breaker and the next tick is skipped.
func TestAdapterBreakerHalfOpenFailedProbeReopens(t *testing.T) {
	transport := errors.New("transport down")
	b, waker, w, exec := breakerTestBridge(t, []error{transport, transport, transport, transport})
	base := time.Unix(300, 0)
	for i := 0; i < 3; i++ {
		breakerWake(b, w, exec, base.Add(time.Duration(i)*time.Second), "cycle-reopen")
	}
	openedAt := b.breakerFor("opencode").openedAt

	probeAt := openedAt.Add(breakerOpenWait + time.Second)
	breakerWake(b, w, exec, probeAt, "cycle-reopen")
	if s := b.breakerFor("opencode"); s.state != breakerOpen {
		t.Fatalf("state after failed probe = %v, want open", s.state)
	}
	breakerWake(b, w, exec, probeAt, "cycle-reopen")
	if got := len(waker.calls); got != 4 {
		t.Fatalf("deliveries after failed probe = %d, want 4", got)
	}
}

// TestAdapterBreakerIgnoresDeadSessionFailures proves ignoreExceptions:
// a dead session is an indeterminate ladder outcome, not a backend failure,
// so it never opens the breaker.
func TestAdapterBreakerIgnoresDeadSessionFailures(t *testing.T) {
	results := []error{
		errors.New("session not found: HTTP 404"),
		errors.New("no such session"),
		errors.New("session gone"),
		errors.New("410 gone"),
		errors.New("session not found"),
	}
	b, waker, w, exec := breakerTestBridge(t, results)
	base := time.Unix(400, 0)
	for i := range results {
		breakerWake(b, w, exec, base.Add(time.Duration(i)*time.Second), "cycle-ignore")
	}
	if got := len(waker.calls); got != len(results) {
		t.Fatalf("deliveries = %d, want %d (dead sessions must not open the breaker)", got, len(results))
	}
	if s := b.breakerFor("opencode"); s.state != breakerClosed {
		t.Fatalf("state after dead sessions = %v, want closed", s.state)
	}
}

// TestAdapterBreakerKeepsWatchWakeCap proves the durable per-subscriber cap still
// parks a Watch even while the per-adapter breaker permits the delivery. Parking
// also notifies the subscriber, so the recorded deliveries include that notice.
func TestAdapterBreakerKeepsWatchWakeCap(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	exec := domain.Subscriber{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"}
	w := domain.Watch{
		ID: "watch:breaker:cap:abc", Repo: "o/r", PR: 7, HeadSHA: "abc",
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching, Revision: 1,
		Policy:      domain.Policy{MaxWakesPerWatch: 1},
		Subscribers: []domain.Subscriber{exec},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	waker := &scriptedAdapterWaker{}
	b := New(db, fakeCI{}, fixedClock{at: time.Unix(500, 0)}, time.Second).
		WithWaker(waker).WithEscalationWriter(func(string) {})
	breakerWake(b, w, exec, time.Unix(500, 0), "cycle-cap")
	breakerWake(b, w, exec, time.Unix(501, 0), "cycle-cap")
	if got := len(waker.calls); got != 2 {
		t.Fatalf("deliveries = %d, want 1 wake plus 1 park notice", got)
	}
	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got.Status != domain.StatusParked {
		t.Fatalf("status = %q, want parked by the wake cap", got.Status)
	}
}

// TestWakeTextStaleBaseRebaseIncludesReason proves that a stale-base rebase
// wake carries both the concrete nextCommand and the machine-readable reason.
// The executor can rebase at once instead of triaging red checks by hand.
func TestWakeTextStaleBaseRebaseIncludesReason(t *testing.T) {
	targets := []domain.WakeTarget{{
		Role:   domain.RoleExecutor,
		Kind:   domain.WakePointer,
		Reason: domain.ReasonStaleBaseAfterMainFix,
	}}
	obs := ciObservation{base: domain.BaseBranchState{HeadSHA: "main-after-fix"}}
	text := wakeText(domain.VerdictRebase, obs, targets, "")
	for _, want := range []string{
		"git rebase origin/main",
		"reason: " + domain.ReasonStaleBaseAfterMainFix,
		"mainHeadSHA=main-after-fix",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("wake text %q missing %q", text, want)
		}
	}
}

// cardCloseCall records one CloseMerged invocation. It carries the arguments
// the card-links port needs to find the card and write its confirmation.
type cardCloseCall struct {
	watchID string
	pr      int
	at      time.Time
}

// recordingCardLinks stands in for the production card-links adapter. It
// writes a real card file the same way: flip the status to done and append the
// PR to the confirmation section. Calls are recorded so a test can prove that
// cibridge invoked the port with the right Watch and PR.
type recordingCardLinks struct {
	cardID string
	dir    string
	calls  []cardCloseCall
	err    error
}

func (r *recordingCardLinks) CloseMerged(_ context.Context, watchID string, pr int, at time.Time) error {
	r.calls = append(r.calls, cardCloseCall{watchID: watchID, pr: pr, at: at})
	if r.err != nil {
		return r.err
	}
	path := filepath.Join(r.dir, r.cardID+".md")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := strings.Replace(string(raw), "Статус: todo", "Статус: done", 1)
	text += fmt.Sprintf("\n## Подтверждение\n- PR #%d MERGED: %s (squash).\n", pr, at.UTC().Format("2006-01-02"))
	return os.WriteFile(path, []byte(text), 0644)
}

// linkedCard creates a fresh todo card and a port that closes it.
func linkedCard(t *testing.T, id string) (*recordingCardLinks, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, id+".md")
	card := fmt.Sprintf("# %s. тестовая карточка\nСтатус: todo\n\nЦель: тест\n", id)
	if err := os.WriteFile(path, []byte(card), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return &recordingCardLinks{cardID: id, dir: dir}, path
}

func assertCardClosed(t *testing.T, links *recordingCardLinks, path, watchID string, pr int) {
	t.Helper()
	if len(links.calls) != 1 {
		t.Fatalf("CloseMerged calls = %d, want 1", len(links.calls))
	}
	call := links.calls[0]
	if call.watchID != watchID || call.pr != pr {
		t.Fatalf("CloseMerged call = %+v, want watch %s pr %d", call, watchID, pr)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(raw), "Статус: done") {
		t.Fatalf("card status is not done: %q", raw)
	}
	if !strings.Contains(string(raw), fmt.Sprintf("PR #%d MERGED", pr)) {
		t.Fatalf("card confirmation lacks PR %d: %q", pr, raw)
	}
}

// TestMergedReportClosesLinkedCard covers the path where GitHub reports a
// merged PR. applyMerged must close the Watch and the linked card.
func TestMergedReportClosesLinkedCard(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const head = "abc9a799a08f0c90de8675d4dbe74d9628324f8f"
	w := domain.Watch{
		ID:   "watch:refusedguy/beacon:406:" + head,
		Repo: "refusedguy/beacon", PR: 406, HeadSHA: head,
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
		Subscribers: []domain.Subscriber{{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"}},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	if _, err := db.Link("IDX-08", w.ID, w.PR); err != nil {
		t.Fatalf("Link: %v", err)
	}
	links, cardPath := linkedCard(t, "IDX-08")
	ci := mergedBatchCI{snaps: map[string]domain.PRSnapshot{w.ID: mergedSnapshot(head)}}
	b := New(db, ci, fixedClock{at: time.Unix(1, 0)}, time.Second).WithCardLinks(links)
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	assertCardClosed(t, links, cardPath, w.ID, w.PR)
}

// TestAutoMergeSuccessClosesLinkedCard covers the path where the bridge merges
// the PR itself. A confirmed merge is terminal and must close the card too.
func TestAutoMergeSuccessClosesLinkedCard(t *testing.T) {
	db, w := mergeTestWatch(t)
	defer db.Close()
	if _, err := db.Link("IDX-08", w.ID, w.PR); err != nil {
		t.Fatalf("Link: %v", err)
	}
	links, cardPath := linkedCard(t, "IDX-08")
	b := New(db, fakeCI{state: mergeTestState()}, fixedClock{at: time.Unix(1, 0)}, time.Second).
		WithMerger(fakeMerger{merged: true}).WithWaker(&recordingWaker{}).WithCardLinks(links)
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	assertCardClosed(t, links, cardPath, w.ID, w.PR)
}

// TestHeldMergeLeavesCardUnchanged proves a held merge is not terminal: the
// card stays todo and no confirmation is written.
func TestHeldMergeLeavesCardUnchanged(t *testing.T) {
	db, w := mergeTestWatch(t)
	defer db.Close()
	if _, err := db.Link("IDX-08", w.ID, w.PR); err != nil {
		t.Fatalf("Link: %v", err)
	}
	links, cardPath := linkedCard(t, "IDX-08")
	b := New(db, fakeCI{state: mergeTestState()}, fixedClock{at: time.Unix(1, 0)}, time.Second).
		WithMerger(fakeMerger{held: true, reason: "merge conflict"}).
		WithWaker(&recordingWaker{}).WithCardLinks(links)
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	if len(links.calls) != 0 {
		t.Fatalf("CloseMerged calls = %d, want 0 on hold", len(links.calls))
	}
	raw, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(raw), "Статус: todo") || strings.Contains(string(raw), "Подтверждение") {
		t.Fatalf("hold changed the card: %q", raw)
	}
}

// gcRunnerFake is a worktree.Runner that reports one clean stale worktree and
// records every git command. Nothing touches a real repository.
type gcRunnerFake struct {
	mu    sync.Mutex
	calls [][]string
}

func (f *gcRunnerFake) run(dir string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{dir}, args...))
	if len(args) >= 2 && args[0] == "worktree" && args[1] == "list" {
		return "worktree /main\nHEAD aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nbranch refs/heads/main\n\n" +
			"worktree /stale\nHEAD bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\nbranch refs/heads/fix/stale\n\n", nil
	}
	return "", nil
}

func (f *gcRunnerFake) hasRemove() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if len(call) >= 4 && call[1] == "worktree" && call[2] == "remove" {
			return true
		}
	}
	return false
}

// bindFakeGC wires a WorktreeGC port that runs the real collector over a fake
// git runner and counts invocations.
func bindFakeGC(b *Bridge, fake *gcRunnerFake, calls *int) *Bridge {
	return b.WithWorktreeGC(func(context.Context) (int, error) {
		*calls = *calls + 1
		res, err := worktree.Collect(fake.run, nil)
		return len(res.Removed), err
	})
}

// TestMergedClosesCardThenRunsWorktreeGC proves the terminal merged verdict
// collects worktrees after the linked card closes, and that the collector
// really issues a git worktree remove through the fake runner.
func TestMergedClosesCardThenRunsWorktreeGC(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const head = "abc9a799a08f0c90de8675d4dbe74d9628324f8f"
	w := domain.Watch{
		ID:   "watch:refusedguy/beacon:406:" + head,
		Repo: "refusedguy/beacon", PR: 406, HeadSHA: head,
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
		Subscribers: []domain.Subscriber{{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"}},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	if _, err := db.Link("IDX-08", w.ID, w.PR); err != nil {
		t.Fatalf("Link: %v", err)
	}
	links, cardPath := linkedCard(t, "IDX-08")
	fake := &gcRunnerFake{}
	gcCalls := 0
	ci := mergedBatchCI{snaps: map[string]domain.PRSnapshot{w.ID: mergedSnapshot(head)}}
	b := bindFakeGC(New(db, ci, fixedClock{at: time.Unix(1, 0)}, time.Second).
		WithCardLinks(links), fake, &gcCalls)

	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	if gcCalls != 1 {
		t.Fatalf("WorktreeGC calls = %d, want 1 on merged", gcCalls)
	}
	if !fake.hasRemove() {
		t.Fatalf("fake runner saw no worktree remove, calls: %v", fake.calls)
	}
	assertCardClosed(t, links, cardPath, w.ID, w.PR)
}

// TestNonMergedVerdictSkipsWorktreeGC proves a held merge, which is not
// terminal, never reaches the collector.
func TestNonMergedVerdictSkipsWorktreeGC(t *testing.T) {
	db, w := mergeTestWatch(t)
	defer db.Close()
	if _, err := db.Link("IDX-08", w.ID, w.PR); err != nil {
		t.Fatalf("Link: %v", err)
	}
	fake := &gcRunnerFake{}
	gcCalls := 0
	b := bindFakeGC(New(db, fakeCI{state: mergeTestState()}, fixedClock{at: time.Unix(1, 0)}, time.Second).
		WithMerger(fakeMerger{held: true, reason: "merge conflict"}).
		WithWaker(&recordingWaker{}), fake, &gcCalls)

	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	if gcCalls != 0 {
		t.Fatalf("WorktreeGC calls = %d, want 0 on a held merge", gcCalls)
	}
}

// TestCardCloseErrorSkipsWorktreeGC proves a failed card close keeps the
// worktrees untouched: GC only runs after a successful closeLinkedCard.
func TestCardCloseErrorSkipsWorktreeGC(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const head = "abc9a799a08f0c90de8675d4dbe74d9628324f8f"
	w := domain.Watch{
		ID:   "watch:refusedguy/beacon:406:" + head,
		Repo: "refusedguy/beacon", PR: 406, HeadSHA: head,
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
		Subscribers: []domain.Subscriber{{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"}},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	links, _ := linkedCard(t, "IDX-08")
	links.err = errors.New("card write failed")
	fake := &gcRunnerFake{}
	gcCalls := 0
	ci := mergedBatchCI{snaps: map[string]domain.PRSnapshot{w.ID: mergedSnapshot(head)}}
	b := bindFakeGC(New(db, ci, fixedClock{at: time.Unix(1, 0)}, time.Second).
		WithCardLinks(links), fake, &gcCalls)

	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	if gcCalls != 0 {
		t.Fatalf("WorktreeGC calls = %d, want 0 when card close fails", gcCalls)
	}
}

// TestResponsibilityPartsAreWired guards the ARDH-11 split. Bridge stays the
// single public surface, but each responsibility must be a distinct part bound
// back to the coordinator so its methods read the same configuration the
// With* setters mutate.
func TestResponsibilityPartsAreWired(t *testing.T) {
	b := New(nil, nil, nil, 0)
	parts := []struct {
		name  string
		owner *Bridge
	}{
		{"CIRefresh", b.ciRefresh.Bridge},
		{"VerdictApplier", b.verdictApplier.Bridge},
		{"WakeDispatcher", b.wakeDispatcher.Bridge},
		{"ReadAPI", b.readAPI.Bridge},
	}
	for _, p := range parts {
		if p.owner != b {
			t.Fatalf("%s part is bound to %p, want coordinator %p", p.name, p.owner, b)
		}
	}
}
