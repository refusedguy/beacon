package cibridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// mergeOutcome is one scripted result of a merge call.
type mergeOutcome struct {
	merged bool
	held   bool
	reason string
	err    error
}

// scriptedMerger returns outcomes in order so a test can drive a stale first
// merge and a successful retry without touching the GitHub adapter.
type scriptedMerger struct {
	outcomes []mergeOutcome
	calls    int
}

func (m *scriptedMerger) Merge(context.Context, string, int) (bool, bool, string, error) {
	if m.calls >= len(m.outcomes) {
		return false, true, "unexpected extra merge call", nil
	}
	o := m.outcomes[m.calls]
	m.calls++
	return o.merged, o.held, o.reason, o.err
}

// fakeRebaser records every rebase so a test can prove that a stale Watch was
// fixed without waking the executor.
type fakeRebaser struct {
	head  string
	err   error
	calls int
}

func (r *fakeRebaser) Rebase(context.Context, string, int) (string, error) {
	r.calls++
	if r.err != nil {
		return "", r.err
	}
	return r.head, nil
}

// autoRebaseSetup builds a Bridge around the shared merge-test Watch. The
// rebaser is only bound when withRebaser is true, so the no-port case keeps the
// legacy path.
func autoRebaseSetup(t *testing.T, merger domain.Merger, withRebaser bool) (*Bridge, domain.Watch, *recordingWaker, *fakeRebaser) {
	t.Helper()
	db, w := mergeTestWatch(t)
	t.Cleanup(func() { db.Close() })
	waker := &recordingWaker{}
	rebaser := &fakeRebaser{head: "newsha"}
	b := New(db, fakeCI{state: mergeTestState()}, fixedClock{at: time.Unix(1, 0)}, time.Second).
		WithMerger(merger).WithWaker(waker)
	if withRebaser {
		b.WithAutoRebase(rebaser)
	}
	return b, w, waker, rebaser
}

func watchFrom(t *testing.T, b *Bridge, id string) domain.Watch {
	t.Helper()
	w, err := b.store.Watch(id)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	return w
}

// A stale BEHIND base is rebased once, the merge is retried once with the new
// head and the Watch closes without a wake.
func TestAutoRebaseBehindRetriesMergeToSuccess(t *testing.T) {
	merger := &scriptedMerger{outcomes: []mergeOutcome{
		{held: true, reason: "behind"},
		{merged: true},
	}}
	b, w, waker, rebaser := autoRebaseSetup(t, merger, true)

	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	got := watchFrom(t, b, w.ID)
	if got.Status != domain.StatusClosed || got.Verdict != domain.VerdictMerged ||
		got.CloseReason != string(domain.CloseReasonMerged) {
		t.Fatalf("closed watch = %q/%q/%q", got.Status, got.Verdict, got.CloseReason)
	}
	if got.HeadSHA != "newsha" {
		t.Fatalf("head = %q, want newsha", got.HeadSHA)
	}
	if merger.calls != 2 {
		t.Fatalf("merge calls = %d, want 2", merger.calls)
	}
	if rebaser.calls != 1 {
		t.Fatalf("rebase calls = %d, want 1", rebaser.calls)
	}
	if len(waker.deliveries) != 0 {
		t.Fatalf("wakes = %d, want 0", len(waker.deliveries))
	}
}

// A failed rebase leaves the original verdict alone: the executor still gets
// rebase and the merge is not retried.
func TestAutoRebaseFailureKeepsRebaseVerdict(t *testing.T) {
	merger := &scriptedMerger{outcomes: []mergeOutcome{{held: true, reason: "behind"}}}
	b, w, waker, rebaser := autoRebaseSetup(t, merger, true)
	rebaser.err = errors.New("gh rebase failed")

	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	got := watchFrom(t, b, w.ID)
	if got.Verdict != domain.VerdictRebase {
		t.Fatalf("verdict = %q, want rebase", got.Verdict)
	}
	if merger.calls != 1 {
		t.Fatalf("merge calls = %d, want 1", merger.calls)
	}
	if rebaser.calls != 1 {
		t.Fatalf("rebase calls = %d, want 1", rebaser.calls)
	}
	if len(waker.deliveries) != 1 {
		t.Fatalf("wakes = %d, want 1", len(waker.deliveries))
	}
}

// Without a bound Rebaser a BEHIND hold keeps the old behaviour: one merge
// attempt and a rebase verdict for the executor.
func TestAutoRebaseNoPortKeepsLegacyRebase(t *testing.T) {
	merger := &scriptedMerger{outcomes: []mergeOutcome{{held: true, reason: "behind"}}}
	b, w, waker, rebaser := autoRebaseSetup(t, merger, false)

	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	got := watchFrom(t, b, w.ID)
	if got.Verdict != domain.VerdictRebase {
		t.Fatalf("verdict = %q, want rebase", got.Verdict)
	}
	if merger.calls != 1 {
		t.Fatalf("merge calls = %d, want 1", merger.calls)
	}
	if rebaser.calls != 0 {
		t.Fatalf("rebase calls = %d, want 0", rebaser.calls)
	}
	if len(waker.deliveries) != 1 {
		t.Fatalf("wakes = %d, want 1", len(waker.deliveries))
	}
}

// A content conflict is not a stale base. Even with a Rebaser bound the Watch
// goes straight to a rebase verdict and no rebase is attempted.
func TestAutoRebaseSkipsContentConflict(t *testing.T) {
	for _, reason := range []string{"merge conflict", "dirty", "merge_refused (409)"} {
		t.Run(reason, func(t *testing.T) {
			merger := &scriptedMerger{outcomes: []mergeOutcome{{held: true, reason: reason}}}
			b, w, _, rebaser := autoRebaseSetup(t, merger, true)

			if got := b.Refresh(context.Background()); got != 1 {
				t.Fatalf("Refresh updated %d watches, want 1", got)
			}
			got := watchFrom(t, b, w.ID)
			if got.Verdict != domain.VerdictRebase {
				t.Fatalf("verdict = %q, want rebase", got.Verdict)
			}
			if rebaser.calls != 0 {
				t.Fatalf("rebase calls = %d, want 0", rebaser.calls)
			}
			if merger.calls != 1 {
				t.Fatalf("merge calls = %d, want 1", merger.calls)
			}
		})
	}
}

// A second tick that observes the same (watchID, revision) must not rebase
// again, even when the first rebase was followed by a failed retry merge.
func TestAutoRebaseRepeatedTickDoesNotDuplicate(t *testing.T) {
	merger := &scriptedMerger{outcomes: []mergeOutcome{
		{held: true, reason: "behind"},
		{held: true, reason: "behind"},
		{held: true, reason: "behind"},
	}}
	b, w, _, rebaser := autoRebaseSetup(t, merger, true)
	obs := ciObservation{pr: mergeTestState()}
	now := time.Unix(1, 0)

	b.applyMerge(context.Background(), w, obs, now, "cycle-1")
	b.applyMerge(context.Background(), w, obs, now, "cycle-2")

	if rebaser.calls != 1 {
		t.Fatalf("rebase calls = %d, want 1", rebaser.calls)
	}
	if merger.calls != 3 {
		t.Fatalf("merge calls = %d, want 3", merger.calls)
	}
	got := watchFrom(t, b, w.ID)
	if got.Verdict != domain.VerdictRebase {
		t.Fatalf("verdict = %q, want rebase", got.Verdict)
	}
}

// isMergeBehind separates the stale-base case from the other conflict causes
// that isMergeConflict also maps to a rebase.
func TestIsMergeBehind(t *testing.T) {
	cases := []struct {
		reason string
		want   bool
	}{
		{"behind", true},
		{"BEHIND and requireUpToDate", true},
		{"merge conflict", false},
		{"dirty", false},
		{"409", false},
		{"422 conflict", false},
		{"merge_pending", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isMergeBehind(tc.reason, nil); got != tc.want {
			t.Errorf("isMergeBehind(%q) = %v, want %v", tc.reason, got, tc.want)
		}
	}
}
