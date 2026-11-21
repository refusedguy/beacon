package cibridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// fakeBatchCI implements domain.BatchCIProvider without importing the real
// GitHub adapter, so the batch path can be exercised in this package.
type fakeBatchCI struct {
	snaps map[string]domain.PRSnapshot
	calls int
}

func (f *fakeBatchCI) FetchBatch(_ context.Context, _ []domain.Watch) (map[string]domain.PRSnapshot, error) {
	f.calls++
	return f.snaps, nil
}

func (f *fakeBatchCI) PointStats() (int, int) { return f.calls, 1000 }

// TestW2ABatchProviderDrivesRefresh proves the bridge reads one batch instead
// of polling each Watch and still publishes the verdict from EvaluatePR.
func TestW2ABatchProviderDrivesRefresh(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	w := domain.Watch{
		ID: "watch:o/r:1:sha", Repo: "o/r", PR: 1, HeadSHA: "sha",
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
		Subscribers: []domain.Subscriber{{ID: "ses_orch", Role: domain.RoleOrchestrator}},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	batch := &fakeBatchCI{snaps: map[string]domain.PRSnapshot{
		w.ID: {
			PR: domain.PRState{
				State: "OPEN", HeadRefOid: "sha", Mergeable: "MERGEABLE",
				MergeStateStatus: "CLEAN",
				Checks:           map[string]domain.Check{"build": {Conclusion: "SUCCESS"}},
			},
			Base: domain.BaseBranchState{BaseRef: "main"},
		},
	}}
	legacy := &countingCI{}
	b := New(db, legacy, fixedClock{at: time.Unix(1, 0)}, time.Second).WithBatchCI(batch)
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	if batch.calls != 1 {
		t.Fatalf("FetchBatch calls = %d, want 1", batch.calls)
	}
	if legacy.calls != 0 {
		t.Fatalf("legacy PRState calls = %d, want 0", legacy.calls)
	}
	got, err := db.Watch(w.ID)
	if err != nil || got.Verdict != domain.VerdictReady {
		t.Fatalf("verdict = %q, %v; want ready", got.Verdict, err)
	}
}

type fakeMerger struct {
	merged bool
	held   bool
	reason string
	err    error
}

func (m fakeMerger) Merge(context.Context, string, int) (bool, bool, string, error) {
	return m.merged, m.held, m.reason, m.err
}

func mergeTestWatch(t *testing.T) (*store.DB, domain.Watch) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w := domain.Watch{
		ID: "watch:o/r:2:sha", Repo: "o/r", PR: 2, HeadSHA: "sha",
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
		MergePolicy: domain.MergePolicy{AutoMerge: true},
		Subscribers: []domain.Subscriber{{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"}},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	return db, w
}

func mergeTestState() domain.PRState {
	return domain.PRState{
		State: "OPEN", HeadRefOid: "sha", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		Checks: map[string]domain.Check{"build": {Conclusion: "SUCCESS"}},
	}
}

// TestW2AAutoMergeSuccessClosesWatch proves a confirmed merge closes the Watch
// with closeReason=merged, finishes the subscriber and wakes nobody.
func TestW2AAutoMergeSuccessClosesWatch(t *testing.T) {
	db, w := mergeTestWatch(t)
	defer db.Close()
	waker := &recordingWaker{}
	b := New(db, fakeCI{state: mergeTestState()}, fixedClock{at: time.Unix(1, 0)}, time.Second).
		WithMerger(fakeMerger{merged: true}).WithWaker(waker)
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got.Status != domain.StatusClosed || got.Verdict != domain.VerdictMerged ||
		got.CloseReason != string(domain.CloseReasonMerged) {
		t.Fatalf("closed watch = %q/%q/%q", got.Status, got.Verdict, got.CloseReason)
	}
	if len(got.Subscribers) != 1 || got.Subscribers[0].Phase != domain.WorkerPhaseFinished {
		t.Fatalf("subscribers = %+v, want finished", got.Subscribers)
	}
	if len(waker.deliveries) != 0 {
		t.Fatalf("wakes = %d, want 0", len(waker.deliveries))
	}
}

// TestW2AAutoMergeConflictBecomesRebase proves a held merge becomes a rebase
// verdict with the concrete rebase command in the executor wake.
func TestW2AAutoMergeConflictBecomesRebase(t *testing.T) {
	db, w := mergeTestWatch(t)
	defer db.Close()
	waker := &recordingWaker{}
	b := New(db, fakeCI{state: mergeTestState()}, fixedClock{at: time.Unix(1, 0)}, time.Second).
		WithMerger(fakeMerger{held: true, reason: "merge conflict"}).WithWaker(waker)
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	got, err := db.Watch(w.ID)
	if err != nil || got.Verdict != domain.VerdictRebase {
		t.Fatalf("verdict = %q, %v; want rebase", got.Verdict, err)
	}
	if len(waker.deliveries) != 1 {
		t.Fatalf("wakes = %d, want 1", len(waker.deliveries))
	}
	if !strings.Contains(waker.deliveries[0].Verdict, "git rebase origin/main") {
		t.Fatalf("wake text = %q, want the rebase command", waker.deliveries[0].Verdict)
	}
}

// TestW2AWakeCapIsPerSubscriber proves one subscriber cannot spend another's
// wake budget, and that parking writes a structured escalation with the Watch
// and the incident. With a cap of one, two subscribers both get a wake in the
// first cycle. The Watch parks only after each has used its own budget.
func TestW2AWakeCapIsPerSubscriber(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	w := domain.Watch{
		ID: "watch:o/r:3:sha", Repo: "o/r", PR: 3, HeadSHA: "sha",
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
		Policy: domain.Policy{MaxWakesPerWatch: 1},
		Subscribers: []domain.Subscriber{
			{ID: "ses_one", Role: domain.RoleExecutor, Adapter: "opencode"},
			{ID: "ses_two", Role: domain.RoleOrchestrator, Adapter: "opencode"},
		},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	state := domain.PRState{
		State: "OPEN", HeadRefOid: "sha", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		Checks: map[string]domain.Check{"build": {Conclusion: "FAILURE", Required: true}},
	}
	var lines []string
	waker := &recordingWaker{}
	b := New(db, fakeCI{state: state}, fixedClock{at: time.Unix(1, 0)}, time.Second).
		WithWaker(waker).WithEscalationWriter(func(line string) { lines = append(lines, line) })
	ctx := context.Background()

	// One verdict names the executor, the next names the orchestrator. Each
	// subscriber spends its own budget, so neither cycle parks the Watch.
	b.wakeOne(ctx, w, w.Subscribers[0], "keep_working: continue", time.Unix(1, 0), "cycle-one", true)
	b.wakeOne(ctx, w, w.Subscribers[1], "keep_working: continue", time.Unix(1, 0), "cycle-one", true)
	current, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if current.Status == domain.StatusParked {
		t.Fatalf("status after first cycle = parked, want a live Watch")
	}
	if got := len(waker.deliveries); got != 2 {
		t.Fatalf("wakes after first cycle = %d, want one per subscriber", got)
	}
	if len(lines) != 0 {
		t.Fatalf("escalations after first cycle = %v, want none", lines)
	}

	// A second actionable revision spends each subscriber's remaining unit.
	b.wakeOne(ctx, current, current.Subscribers[0], "keep_working: continue", time.Unix(2, 0), "cycle-two", true)
	b.wakeOne(ctx, current, current.Subscribers[1], "keep_working: continue", time.Unix(2, 0), "cycle-two", true)

	parked, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if parked.Status != domain.StatusParked {
		t.Fatalf("status = %q, want parked", parked.Status)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], w.ID) || !strings.Contains(lines[0], "incident") {
		t.Fatalf("escalations = %v, want one structured line naming the Watch and incident", lines)
	}
	if got := len(waker.deliveries); got != 4 {
		t.Fatalf("wakes = %d, want 2 verdict wakes plus 2 park notices", got)
	}
}

// TestW2ACycleIDTagsObserveEvents proves one Refresh emits one cycle id on all
// of its lifecycle events, so an operator can group the events of a pass.
func TestW2ACycleIDTagsObserveEvents(t *testing.T) {
	db, _ := mergeTestWatch(t)
	defer db.Close()
	sink := &captureSink{}
	observe.SetDefault(sink)
	defer observe.SetDefault(nil)

	b := New(db, fakeCI{state: mergeTestState()}, fixedClock{at: time.Unix(1, 0)}, time.Second)
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	cycleID := ""
	for _, e := range sink.events {
		if e.name == "cycle_start" {
			cycleID, _ = e.fields[fieldCycleID].(string)
		}
	}
	if cycleID == "" {
		t.Fatalf("no cycleID on cycle_start: %+v", sink.events)
	}
	for _, e := range sink.events {
		if e.name == "cycle_start" || e.name == "cycle_end" {
			if e.fields[fieldCycleID] != cycleID {
				t.Fatalf("%s cycleID = %v, want %q", e.name, e.fields[fieldCycleID], cycleID)
			}
		}
	}
}

// TestW2AWakeAddresseeAcceptsFixerRole proves fixer subscribers can receive a
// wake, while a non-session or unknown subscriber still cannot.
func TestW2AWakeAddresseeAcceptsFixerRole(t *testing.T) {
	w := domain.Watch{
		ID: "watch:o/r:4:sha",
		Subscribers: []domain.Subscriber{
			{ID: "ses_fixer", Role: domain.RoleFixer},
		},
	}
	id, err := wakeAddressee(w, w.ID, "ses_fixer")
	if err != nil || id != "ses_fixer" {
		t.Fatalf("wakeAddressee = %q, %v; want ses_fixer", id, err)
	}
	if _, err := wakeAddressee(w, w.ID, "not-a-session"); err == nil {
		t.Fatal("wakeAddressee accepted a non-session")
	}
}
