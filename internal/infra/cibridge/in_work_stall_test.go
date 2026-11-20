package cibridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
)

const stallWatchID = "watch:o/r:7:abc"

func stallWatch(subs ...domain.Subscriber) domain.Watch {
	return domain.Watch{
		ID:          stallWatchID,
		Repo:        "o/r",
		PR:          7,
		HeadSHA:     "abc",
		BaseRef:     "main",
		Status:      domain.StatusWatching,
		Verdict:     domain.VerdictKeepWorking,
		Revision:    3,
		Subscribers: subs,
	}
}

func inWorkSub(id string, last time.Time) domain.Subscriber {
	return domain.Subscriber{
		ID: id, Role: domain.RoleExecutor, Adapter: "opencode",
		Phase: domain.WorkerPhaseInWork, LastActivity: last,
	}
}

func openStallDB(t *testing.T, w domain.Watch) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	return db
}

// newStallBridgeOn wires a bridge over an existing store so a second Refresh
// can be run against the same SQLite file. The CI state is deliberately
// PENDING: an ordinary verdict pass would not wake anyone, so every delivery
// in these tests comes from the stall watchdog.
func newStallBridgeOn(t *testing.T, db *store.DB, now time.Time, head string, lines *[]string, orchestrator string) (*Bridge, *recordingAdapterWaker) {
	t.Helper()
	waker := &recordingAdapterWaker{}
	b := New(db, fakeCI{state: domain.PRState{
		HeadRefOid: head, Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		Checks: map[string]domain.Check{"build": {Conclusion: "PENDING", Required: true}},
	}}, fixedClock{at: now}, time.Second).
		WithWaker(waker).
		WithEscalationWriter(func(line string) { *lines = append(*lines, line) })
	if orchestrator != "" {
		b.WithOrchestratorIDSource(func() string { return orchestrator })
	}
	return b, waker
}

// TestInWorkStallFreshSubscriberUntouched proves the default twenty-minute
// window keeps a recently active in_work subscriber out of the watchdog.
func TestInWorkStallFreshSubscriberUntouched(t *testing.T) {
	now := time.Unix(1000, 0)
	sub := inWorkSub("ses_exec", now.Add(-5*time.Minute))
	db := openStallDB(t, stallWatch(sub))
	defer db.Close()

	var lines []string
	b, waker := newStallBridgeOn(t, db, now, "abc", &lines, "ses_orch")
	if got := b.Refresh(context.Background()); got != 0 {
		t.Fatalf("Refresh updated %d watches, want 0", got)
	}
	got, err := db.Watch(stallWatchID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got.Subscribers[0].Phase != domain.WorkerPhaseInWork {
		t.Fatalf("phase = %q, want %q", got.Subscribers[0].Phase, domain.WorkerPhaseInWork)
	}
	if len(waker.deliveries) != 0 || len(lines) != 0 {
		t.Fatalf("fresh subscriber: %d wakes, %d escalation lines, want 0 and 0", len(waker.deliveries), len(lines))
	}
}

// TestInWorkStaleSubscriberOrphanedAndEscalatedOnce is the core watchdog case:
// a quiet in_work subscriber is orphaned, the configured orchestrator receives
// one wake, and ESCALATIONS.md gets one line.
func TestInWorkStaleSubscriberOrphanedAndEscalatedOnce(t *testing.T) {
	now := time.Unix(1000, 0)
	sub := inWorkSub("ses_exec", now.Add(-2*time.Hour))
	db := openStallDB(t, stallWatch(sub))
	defer db.Close()

	var lines []string
	b, waker := newStallBridgeOn(t, db, now, "abc", &lines, "ses_orch")
	b.WithInWorkTimeout(time.Minute)
	if got := b.Refresh(context.Background()); got != 0 {
		t.Fatalf("Refresh updated %d watches, want 0 for a stalled Watch", got)
	}

	got, err := db.Watch(stallWatchID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got.Subscribers[0].Phase != domain.WorkerPhaseOrphaned {
		t.Fatalf("phase = %q, want %q", got.Subscribers[0].Phase, domain.WorkerPhaseOrphaned)
	}
	if got.Subscribers[0].LastError == "" {
		t.Error("stalled subscriber has no last error")
	}

	deliveries := waker.forSession("ses_orch")
	if len(deliveries) != 1 {
		t.Fatalf("orchestrator escalations = %d, want 1: %+v", len(deliveries), deliveries)
	}
	msg := deliveries[0].payload.Verdict
	for _, want := range []string{"in_work_stalled", sub.ID, stallWatchID} {
		if !strings.Contains(msg, want) {
			t.Errorf("escalation %q missing %q", msg, want)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("escalation lines = %d, want 1: %+v", len(lines), lines)
	}
	for _, want := range []string{sub.ID, stallWatchID} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("escalation line %q missing %q", lines[0], want)
		}
	}

	id := domain.ObligationKey("inwork-stall:"+stallWatchID, 3, sub.ID)
	o, err := db.Obligation(id)
	if err != nil {
		t.Fatalf("dedup obligation %s missing: %v", id, err)
	}
	if o.DeliveredAt.IsZero() || len(o.Attempts) != 1 {
		t.Fatalf("dedup obligation = %+v, want one delivered attempt", o)
	}
}

// TestInWorkStallRepeatRefreshDoesNotDuplicate restarts the bridge over the
// same SQLite file and ticks again. The delivered obligation and the orphaned
// phase must both suppress a second escalation.
func TestInWorkStallRepeatRefreshDoesNotDuplicate(t *testing.T) {
	now := time.Unix(1000, 0)
	sub := inWorkSub("ses_exec", now.Add(-2*time.Hour))
	db := openStallDB(t, stallWatch(sub))
	defer db.Close()

	var lines []string
	first, firstWaker := newStallBridgeOn(t, db, now, "abc", &lines, "ses_orch")
	first.WithInWorkTimeout(time.Minute)
	first.Refresh(context.Background())
	if len(firstWaker.forSession("ses_orch")) != 1 || len(lines) != 1 {
		t.Fatalf("first tick: %d wakes, %d lines, want 1 and 1", len(firstWaker.forSession("ses_orch")), len(lines))
	}

	second, secondWaker := newStallBridgeOn(t, db, now.Add(time.Minute), "abc", &lines, "ses_orch")
	second.WithInWorkTimeout(time.Minute)
	second.Refresh(context.Background())
	if got := secondWaker.forSession("ses_orch"); len(got) != 0 {
		t.Fatalf("second tick escalated again: %+v", got)
	}
	if len(lines) != 1 {
		t.Fatalf("escalation lines after second tick = %d, want 1: %+v", len(lines), lines)
	}
}

// TestInWorkStallWithoutOrchestratorIsSilent proves a Watch with no
// orchestrator address is left alone: no wake, no journal line, no phase flip.
func TestInWorkStallWithoutOrchestratorIsSilent(t *testing.T) {
	now := time.Unix(1000, 0)
	sub := inWorkSub("ses_exec", now.Add(-2*time.Hour))
	db := openStallDB(t, stallWatch(sub))
	defer db.Close()

	var lines []string
	b, waker := newStallBridgeOn(t, db, now, "abc", &lines, "")
	b.WithInWorkTimeout(time.Minute)
	b.Refresh(context.Background())

	got, err := db.Watch(stallWatchID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got.Subscribers[0].Phase != domain.WorkerPhaseInWork {
		t.Fatalf("phase = %q, want %q", got.Subscribers[0].Phase, domain.WorkerPhaseInWork)
	}
	if len(waker.deliveries) != 0 || len(lines) != 0 {
		t.Fatalf("unbound orchestrator: %d wakes, %d lines, want 0 and 0", len(waker.deliveries), len(lines))
	}
}

// TestInWorkStallIgnoresOtherPhases keeps every non-in_work subscriber out of
// the watchdog, including a zero-value phase from a legacy Watch.
func TestInWorkStallIgnoresOtherPhases(t *testing.T) {
	now := time.Unix(1000, 0)
	old := now.Add(-2 * time.Hour)
	subs := []domain.Subscriber{
		{ID: "waiting", Role: domain.RoleExecutor, Phase: domain.WorkerPhaseWaiting, LastActivity: old},
		{ID: "blocked", Role: domain.RoleFixer, Phase: domain.WorkerPhaseBlocked, LastActivity: old},
		{ID: "finished", Role: domain.RoleExecutor, Phase: domain.WorkerPhaseFinished, LastActivity: old},
		{ID: "orphan", Role: domain.RoleExecutor, Phase: domain.WorkerPhaseOrphaned, LastActivity: old},
		{ID: "zero", Role: domain.RoleExecutor, LastActivity: old},
	}
	db := openStallDB(t, stallWatch(subs...))
	defer db.Close()

	var lines []string
	b, waker := newStallBridgeOn(t, db, now, "abc", &lines, "ses_orch")
	b.WithInWorkTimeout(time.Minute)
	b.Refresh(context.Background())

	got, err := db.Watch(stallWatchID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	for i, sub := range subs {
		if got.Subscribers[i].Phase != sub.Phase {
			t.Errorf("subscriber %s phase = %q, want %q", sub.ID, got.Subscribers[i].Phase, sub.Phase)
		}
	}
	if len(waker.deliveries) != 0 || len(lines) != 0 {
		t.Fatalf("non-in_work subscribers: %d wakes, %d lines, want 0 and 0", len(waker.deliveries), len(lines))
	}
}

// TestInWorkStallMCPActivityKeepsSubscriber proves an MCP claim or read inside
// the window counts as activity even when the subscriber's own stamp is old.
func TestInWorkStallMCPActivityKeepsSubscriber(t *testing.T) {
	for _, stamp := range []string{"claimed", "read"} {
		t.Run(stamp, func(t *testing.T) {
			now := time.Unix(1000, 0)
			sub := inWorkSub("ses_exec", now.Add(-2*time.Hour))
			db := openStallDB(t, stallWatch(sub))
			defer db.Close()

			o := domain.Obligation{
				ID:      domain.ObligationKey(stallWatchID, 3, sub.ID),
				WatchID: stallWatchID, Revision: 3, SubscriberID: sub.ID,
				VerdictPointer: domain.VerdictKeepWorking, StagedAt: now.Add(-2 * time.Hour),
			}
			if stamp == "claimed" {
				o.ClaimedAt = now.Add(-30 * time.Second)
			} else {
				o.ReadAt = now.Add(-30 * time.Second)
			}
			if err := db.SaveObligation(o); err != nil {
				t.Fatalf("SaveObligation: %v", err)
			}

			var lines []string
			b, waker := newStallBridgeOn(t, db, now, "abc", &lines, "ses_orch")
			b.WithInWorkTimeout(time.Minute)
			b.Refresh(context.Background())

			got, err := db.Watch(stallWatchID)
			if err != nil {
				t.Fatalf("Watch: %v", err)
			}
			if got.Subscribers[0].Phase != domain.WorkerPhaseInWork {
				t.Fatalf("phase = %q, want %q", got.Subscribers[0].Phase, domain.WorkerPhaseInWork)
			}
			if len(waker.deliveries) != 0 || len(lines) != 0 {
				t.Fatalf("fresh MCP %s: %d wakes, %d lines, want 0 and 0", stamp, len(waker.deliveries), len(lines))
			}
		})
	}
}

// TestInWorkStallHeadChangeClosesWatchInstead proves a new head is handled by
// the head-change path first: the Watch closes and no stall escalation is sent.
func TestInWorkStallHeadChangeClosesWatchInstead(t *testing.T) {
	now := time.Unix(1000, 0)
	sub := inWorkSub("ses_exec", now.Add(-2*time.Hour))
	db := openStallDB(t, stallWatch(sub))
	defer db.Close()

	var lines []string
	waker := &recordingAdapterWaker{}
	b := New(db, fakeCI{state: domain.PRState{
		HeadRefOid: "new",
		Checks:     map[string]domain.Check{"build": {Conclusion: "PENDING", Required: true}},
	}}, fixedClock{at: now}, time.Second).
		WithWaker(waker).
		WithEscalationWriter(func(line string) { *lines = append(*lines, line) })
	b.WithInWorkTimeout(time.Minute).WithOrchestratorIDSource(func() string { return "ses_orch" })
	b.Refresh(context.Background())

	got, err := db.Watch(stallWatchID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got.Status != domain.StatusClosed || got.CloseReason != string(domain.CloseReasonHeadChanged) {
		t.Fatalf("watch = %q/%q, want closed/head_changed", got.Status, got.CloseReason)
	}
	if len(waker.deliveries) != 0 || len(lines) != 0 {
		t.Fatalf("head change: %d wakes, %d lines, want 0 and 0", len(waker.deliveries), len(lines))
	}
}
