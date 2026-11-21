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

// TestW2AWakeCapParksWatch proves the circuit breaker counts durable wake
// attempts, parks the Watch at the cap and reports one escalation line.
func TestW2AWakeCapParksWatch(t *testing.T) {
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
			{ID: "ses_two", Role: domain.RoleExecutor, Adapter: "opencode"},
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
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got.Status != domain.StatusParked {
		t.Fatalf("status = %q, want parked", got.Status)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], w.ID) {
		t.Fatalf("escalations = %v, want one line naming the watch", lines)
	}
	if len(waker.deliveries) != 1 {
		t.Fatalf("wakes = %d, want 1 before the cap", len(waker.deliveries))
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
