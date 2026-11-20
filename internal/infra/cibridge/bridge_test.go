package cibridge

import (
	"context"
	"path/filepath"
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
