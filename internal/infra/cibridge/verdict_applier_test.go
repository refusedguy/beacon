package cibridge

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// countingSaveStore counts SaveWatch calls and delegates every other store
// method to the real DB. It lets the watching test prove that a repeat with the
// same observation performs no write.
type countingSaveStore struct {
	*store.DB
	saves int
}

func (s *countingSaveStore) SaveWatch(w domain.Watch) error {
	s.saves++
	return s.DB.SaveWatch(w)
}

// TestApplyVerdictWatchingPersistsObservationsIdempotently pins the fix for
// watches stuck with verdict=watching and checks=null. A watching verdict now
// persists fresh CI facts without staging an obligation or waking anyone, and a
// repeat with identical facts performs no write.
func TestApplyVerdictWatchingPersistsObservationsIdempotently(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const head = "abc9a799a08f0c90de8675d4dbe74d9628324f8f"
	w := domain.Watch{
		ID:      "watch:refusedguy/beacon:1:" + head,
		Repo:    "refusedguy/beacon",
		PR:      1,
		HeadSHA: head,
		Status:  domain.StatusWatching, Verdict: domain.VerdictWatching,
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("seed SaveWatch: %v", err)
	}

	cs := &countingSaveStore{DB: db}
	b := New(cs, nil, nil, time.Second)
	obs := ciObservation{pr: domain.PRState{
		State: "OPEN", HeadRefOid: head,
		Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		Checks: map[string]domain.Check{
			"ci": {State: "COMPLETED", Conclusion: "SUCCESS", Required: true},
		},
	}}
	now := time.Unix(2, 0)

	if got := b.verdictApplier.applyVerdict(context.Background(), w, obs, domain.VerdictWatching, nil, now, "cycle-1"); got != 0 {
		t.Fatalf("first applyVerdict updated %d watches, want 0", got)
	}
	if cs.saves != 1 {
		t.Fatalf("first applyVerdict SaveWatch calls = %d, want 1", cs.saves)
	}
	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if len(got.Checks) != 1 || got.Checks["ci"].Conclusion != "SUCCESS" {
		t.Fatalf("checks = %+v, want the observed ci success", got.Checks)
	}
	if got.Merge.MergeStateStatus != "CLEAN" || got.Merge.Mergeable != "MERGEABLE" {
		t.Fatalf("merge = %+v, want the observed clean merge state", got.Merge)
	}
	if got.Revision != w.Revision {
		t.Fatalf("revision = %d, want %d (watching must not bump it)", got.Revision, w.Revision)
	}
	if got.Verdict != domain.VerdictWatching || got.Status != domain.StatusWatching {
		t.Fatalf("status/verdict = %q/%q, want watching/watching", got.Status, got.Verdict)
	}
	obligations, err := db.WatchObligations(w.ID)
	if err != nil {
		t.Fatalf("WatchObligations: %v", err)
	}
	if len(obligations) != 0 {
		t.Fatalf("obligations = %d, want 0 for a watching verdict", len(obligations))
	}

	savesAfterFirst := cs.saves
	if got := b.verdictApplier.applyVerdict(context.Background(), w, obs, domain.VerdictWatching, nil, now, "cycle-2"); got != 0 {
		t.Fatalf("second applyVerdict updated %d watches, want 0", got)
	}
	if cs.saves != savesAfterFirst {
		t.Fatalf("second applyVerdict wrote the Watch (%d calls), want no write", cs.saves-savesAfterFirst)
	}
}

// greenBaseBatchCI reports a fully observed, fully green base for every
// candidate. The PR state stays OPEN with no checks, so the verdict engine
// leaves the probe Watch watching and nobody wakes.
type greenBaseBatchCI struct {
	head         string
	checksKnown  bool
	failedChecks map[string]bool
}

func (g greenBaseBatchCI) PRState(context.Context, string, int) (domain.PRState, error) {
	return domain.PRState{}, nil
}

func (g greenBaseBatchCI) FetchBatch(_ context.Context, watches []domain.Watch) (map[string]domain.PRSnapshot, error) {
	out := make(map[string]domain.PRSnapshot, len(watches))
	for _, w := range watches {
		out[w.ID] = domain.PRSnapshot{
			PR: domain.PRState{
				State: "OPEN", HeadRefOid: g.head,
				Mergeable: "UNKNOWN", MergeStateStatus: "UNKNOWN",
			},
			Base: domain.BaseBranchState{
				BaseRef: "main", HeadSHA: "green-main",
				ChecksKnown: g.checksKnown, FailedChecks: g.failedChecks,
			},
		}
	}
	return out, nil
}

func (g greenBaseBatchCI) PointStats() (int, int) { return 1, 500 }

type recordingGenerations struct{ ids []string }

func (g *recordingGenerations) ResolveBlocked(id string) (string, bool) {
	g.ids = append(g.ids, id)
	return "gen-" + id, true
}

// TestRefreshResolvesBlockerWhenBaseTurnsGreenWithoutMerge reproduces the live
// incident: a blocker incident is open, a Watch is blocked on it, and the base
// turns green while no Watch is merged. Refresh must resolve the incident,
// unblock the Watch and rotate its generation. No MERGED PR is involved.
func TestRefreshResolvesBlockerWhenBaseTurnsGreenWithoutMerge(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const repo = "refusedguy/beacon"
	const head = "abc9a799a08f0c90de8675d4dbe74d9628324f8f"
	now := time.Unix(2, 0)

	blocked := domain.Watch{
		ID:   "watch:" + repo + ":1:" + head,
		Repo: repo, PR: 1, HeadSHA: head,
		Status: domain.StatusBlocked, Verdict: domain.VerdictBlocked,
		Blocked: &domain.BlockedInfo{
			Reason:       domain.BlockReasonBaseChecksFailed,
			FailedChecks: []string{"build", "e2e", "registry-check", "registry-live", "test"},
		},
	}
	if err := db.SaveWatch(blocked); err != nil {
		t.Fatalf("seed blocked Watch: %v", err)
	}
	incident, _, err := db.GetOrCreateOpenBlocker(repo, "main", blocked.Blocked.FailedChecks, blocked.ID, now)
	if err != nil {
		t.Fatalf("GetOrCreateOpenBlocker: %v", err)
	}
	blocked.BlockerID = incident.ID
	if err := db.SaveWatch(blocked); err != nil {
		t.Fatalf("link blocker: %v", err)
	}

	probe := domain.Watch{
		ID:   "watch:" + repo + ":2:" + head,
		Repo: repo, PR: 2, HeadSHA: head,
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
		Subscribers: []domain.Subscriber{{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"}},
	}
	if err := db.SaveWatch(probe); err != nil {
		t.Fatalf("seed probe Watch: %v", err)
	}

	gen := &recordingGenerations{}
	ci := greenBaseBatchCI{head: head, checksKnown: true, failedChecks: map[string]bool{}}
	b := New(db, ci, fixedClock{at: now}, time.Second).
		WithWaker(&recordingWaker{}).WithGenerationStore(gen)
	b.Refresh(context.Background())

	open, err := db.ActiveBlockers()
	if err != nil {
		t.Fatalf("ActiveBlockers: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open incidents = %d, want 0 after base turned green: %+v", len(open), open)
	}
	got, err := db.Watch(blocked.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got.Status != domain.StatusWatching || got.Blocked != nil || got.BlockerID != "" {
		t.Fatalf("blocked Watch = status %q, blocked %v, blockerID %q; want watching/nil/empty",
			got.Status, got.Blocked, got.BlockerID)
	}
	if got.Revision != blocked.Revision+1 {
		t.Fatalf("revision = %d, want %d", got.Revision, blocked.Revision+1)
	}
	if len(gen.ids) != 1 || gen.ids[0] != blocked.ID {
		t.Fatalf("rotated generations = %v, want [%s]", gen.ids, blocked.ID)
	}
}

// TestRefreshKeepsBlockerWhenBaseChecksUnknown pins the fail-closed side: a
// provider that never observed the base must not release a blocker incident.
func TestRefreshKeepsBlockerWhenBaseChecksUnknown(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const repo = "refusedguy/beacon"
	const head = "abc9a799a08f0c90de8675d4dbe74d9628324f8f"
	now := time.Unix(2, 0)

	blocked := domain.Watch{
		ID:   "watch:" + repo + ":1:" + head,
		Repo: repo, PR: 1, HeadSHA: head,
		Status: domain.StatusBlocked, Verdict: domain.VerdictBlocked,
		Blocked: &domain.BlockedInfo{
			Reason:       domain.BlockReasonBaseChecksFailed,
			FailedChecks: []string{"build"},
		},
	}
	if err := db.SaveWatch(blocked); err != nil {
		t.Fatalf("seed blocked Watch: %v", err)
	}
	incident, _, err := db.GetOrCreateOpenBlocker(repo, "main", blocked.Blocked.FailedChecks, blocked.ID, now)
	if err != nil {
		t.Fatalf("GetOrCreateOpenBlocker: %v", err)
	}
	blocked.BlockerID = incident.ID
	if err := db.SaveWatch(blocked); err != nil {
		t.Fatalf("link blocker: %v", err)
	}
	probe := domain.Watch{
		ID:   "watch:" + repo + ":2:" + head,
		Repo: repo, PR: 2, HeadSHA: head,
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
		Subscribers: []domain.Subscriber{{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"}},
	}
	if err := db.SaveWatch(probe); err != nil {
		t.Fatalf("seed probe Watch: %v", err)
	}

	gen := &recordingGenerations{}
	ci := greenBaseBatchCI{head: head, checksKnown: false, failedChecks: map[string]bool{}}
	b := New(db, ci, fixedClock{at: now}, time.Second).
		WithWaker(&recordingWaker{}).WithGenerationStore(gen)
	b.Refresh(context.Background())

	open, err := db.ActiveBlockers()
	if err != nil {
		t.Fatalf("ActiveBlockers: %v", err)
	}
	if len(open) != 1 || open[0].ID != incident.ID {
		t.Fatalf("open incidents = %+v, want the original incident still open", open)
	}
	got, err := db.Watch(blocked.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got.Status != domain.StatusBlocked {
		t.Fatalf("status = %q, want blocked", got.Status)
	}
	if len(gen.ids) != 0 {
		t.Fatalf("rotated generations = %v, want none", gen.ids)
	}
}
