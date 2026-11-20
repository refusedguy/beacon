package cibridge

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// mergedBatchCI stands in for github.BatchProvider: one snapshot per Watch,
// keyed by Watch.ID, with the same PointStats shape the bridge logs.
type mergedBatchCI struct {
	snaps map[string]domain.PRSnapshot
}

func (m mergedBatchCI) PRState(context.Context, string, int) (domain.PRState, error) {
	return domain.PRState{}, nil
}

func (m mergedBatchCI) FetchBatch(_ context.Context, watches []domain.Watch) (map[string]domain.PRSnapshot, error) {
	out := make(map[string]domain.PRSnapshot, len(watches))
	for _, w := range watches {
		if s, ok := m.snaps[w.ID]; ok {
			out[w.ID] = s
		}
	}
	return out, nil
}

func (m mergedBatchCI) PointStats() (int, int) { return 1, 500 }

func mergedSnapshot(head string) domain.PRSnapshot {
	return domain.PRSnapshot{
		PR: domain.PRState{
			State: "MERGED", HeadRefOid: head,
			Mergeable: "UNKNOWN", MergeStateStatus: "UNKNOWN",
		},
		Base: domain.BaseBranchState{BaseRef: "main", ChecksKnown: true},
	}
}

// TestMergedSnapshotReachesVerdictEngine pins the exact hand-off that broke:
// PRSnapshot.PR.State must survive snapshotFor so EvaluatePR applies its
// terminal MERGED rule instead of falling through to watching.
func TestMergedSnapshotReachesVerdictEngine(t *testing.T) {
	const head = "abc9a799a08f0c90de8675d4dbe74d9628324f8f"
	w := domain.Watch{
		ID:   "watch:refusedguy/beacon:406:" + head,
		Repo: "refusedguy/beacon", PR: 406, HeadSHA: head,
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
	}
	obs := ciObservation{pr: mergedSnapshot(head).PR, base: mergedSnapshot(head).Base}

	snap := snapshotFor(w, obs, time.Unix(1, 0))
	if snap.State != "MERGED" {
		t.Fatalf("snapshot state = %q, want MERGED", snap.State)
	}
	verdict, targets := domain.EvaluatePR(snap, w.Policy)
	if verdict != domain.VerdictMerged {
		t.Fatalf("verdict = %q, want merged", verdict)
	}
	if len(targets) != 0 {
		t.Fatalf("terminal verdict must wake nobody, got %v", targets)
	}
}

// TestBridgeRefreshClosesWatchOnMergedBatchSnapshot is the end-to-end guard for
// the live failure: a batch provider reporting MERGED must close the Watch with
// closeReason=merged, finish the subscriber and deliver no wake.
func TestBridgeRefreshClosesWatchOnMergedBatchSnapshot(t *testing.T) {
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
		MergePolicy: domain.MergePolicy{AutoMerge: true},
		Subscribers: []domain.Subscriber{{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"}},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	waker := &recordingWaker{}
	ci := mergedBatchCI{snaps: map[string]domain.PRSnapshot{w.ID: mergedSnapshot(head)}}
	b := New(db, ci, fixedClock{at: time.Unix(1, 0)}, time.Second).WithWaker(waker)
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
	if got.CloseReason != string(domain.CloseReasonMerged) {
		t.Fatalf("close reason = %q, want %q", got.CloseReason, domain.CloseReasonMerged)
	}
	if got.Revision != w.Revision+1 {
		t.Fatalf("revision = %d, want %d", got.Revision, w.Revision+1)
	}
	if len(got.Subscribers) != 1 || got.Subscribers[0].Phase != domain.WorkerPhaseFinished {
		t.Fatalf("subscribers = %+v, want finished", got.Subscribers)
	}
	if len(waker.deliveries) != 0 {
		t.Fatalf("wakes = %d, want 0", len(waker.deliveries))
	}
}
