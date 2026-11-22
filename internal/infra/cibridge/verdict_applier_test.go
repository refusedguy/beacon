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

// blockedTestChecks builds the card's observation: one required failure that
// blocks and one informational failure that must stay out of the incident.
func blockedTestChecks(infoName string) map[string]domain.Check {
	return map[string]domain.Check{
		"ci-build": {State: "COMPLETED", Conclusion: "FAILURE", Required: true},
		infoName:   {State: "COMPLETED", Conclusion: "FAILURE", Required: false},
	}
}

func blockedTestObservation(head, infoName string) ciObservation {
	return ciObservation{pr: domain.PRState{
		State: "OPEN", HeadRefOid: head,
		Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		Checks: blockedTestChecks(infoName),
	}}
}

// countingBlockerControl records every failure list handed to the incident
// factory. It proves the call count and the exact signature sent downstream.
type countingBlockerControl struct {
	blockerControl
	signatures [][]string
}

func (c *countingBlockerControl) GetOrCreateOpenBlocker(repo, baseRef string, failedChecks []string, watchID string, now time.Time) (domain.BlockerIncident, bool, error) {
	c.signatures = append(c.signatures, append([]string(nil), failedChecks...))
	return c.blockerControl.GetOrCreateOpenBlocker(repo, baseRef, failedChecks, watchID, now)
}

// TestApplyVerdictBlockedDropsInformationalCheck drives the block path through
// applyVerdict with a fresh observation, the gap the existing block tests miss.
// Only the required name may reach BlockedInfo, the incident signature and the
// FIX-agent wake.
func TestApplyVerdictBlockedDropsInformationalCheck(t *testing.T) {
	const head = "abc9a799a08f0c90de8675d4dbe74d9628324f8f"
	now := time.Unix(2, 0)

	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	w := domain.Watch{
		ID:   "watch:refusedguy/beacon:1:" + head,
		Repo: "refusedguy/beacon", PR: 1,
		HeadSHA: head, BaseRef: "main",
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
		Subscribers: []domain.Subscriber{
			{ID: "ses_orch", Role: domain.RoleOrchestrator, Adapter: "opencode"},
		},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("seed SaveWatch: %v", err)
	}

	control := &countingBlockerControl{blockerControl: db}
	waker := &recordingAdapterWaker{}
	b := New(db, nil, fixedClock{at: now}, time.Second).WithWaker(waker).WithBlockerControl(control)

	obs := blockedTestObservation(head, "ci-info")
	if got := b.verdictApplier.applyVerdict(context.Background(), w, obs, domain.VerdictBlocked, nil, now, "cycle-1"); got != 1 {
		t.Fatalf("applyVerdict updated %d watches, want 1", got)
	}

	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got.Status != domain.StatusBlocked || got.Blocked == nil {
		t.Fatalf("status/blocked = %q/%+v, want blocked with info", got.Status, got.Blocked)
	}
	if names := got.Blocked.FailedChecks; len(names) != 1 || names[0] != "ci-build" {
		t.Fatalf("BlockedInfo.FailedChecks = %v, want only ci-build", names)
	}

	if len(control.signatures) != 1 {
		t.Fatalf("GetOrCreateOpenBlocker calls = %d, want 1", len(control.signatures))
	}
	if names := control.signatures[0]; len(names) != 1 || names[0] != "ci-build" {
		t.Fatalf("incident signature = %v, want only ci-build", names)
	}
	active, err := db.ActiveBlockers()
	if err != nil {
		t.Fatalf("ActiveBlockers: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("ActiveBlockers = %d, want 1", len(active))
	}
	if names := active[0].FailedChecks; len(names) != 1 || names[0] != "ci-build" {
		t.Fatalf("incident FailedChecks = %v, want only ci-build", names)
	}

	deliveries := waker.forSession("ses_orch")
	if len(deliveries) != 1 {
		t.Fatalf("orchestrator wakes = %d, want 1", len(deliveries))
	}
	if text := deliveries[0].payload.Verdict; !strings.Contains(text, "ci-build") || strings.Contains(text, "ci-info") {
		t.Fatalf("wake text = %q, want ci-build and no ci-info", text)
	}
	if notice := blockedNotice(got); !strings.Contains(notice, "ci-build") || strings.Contains(notice, "ci-info") {
		t.Fatalf("blockedNotice = %q, want ci-build and no ci-info", notice)
	}
}

// TestApplyVerdictBlockedSharesIncidentOnInfoSwap proves that swapping the
// informational failure across two Watches does not fork the incident or steer
// the orchestrator twice. Both Watches carry the same required ci-build failure.
func TestApplyVerdictBlockedSharesIncidentOnInfoSwap(t *testing.T) {
	const nowUnix = 4
	now := time.Unix(nowUnix, 0)

	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	heads := []string{
		"abc9a799a08f0c90de8675d4dbe74d9628324f8f",
		"def9a799a08f0c90de8675d4dbe74d9628324f8f",
	}
	infoNames := []string{"ci-info", "ci-other"}
	watchIDs := []string{
		"watch:refusedguy/beacon:1:" + heads[0],
		"watch:refusedguy/beacon:2:" + heads[1],
	}

	control := &countingBlockerControl{blockerControl: db}
	waker := &recordingAdapterWaker{}
	b := New(db, nil, fixedClock{at: now}, time.Second).WithWaker(waker).WithBlockerControl(control)

	for i, head := range heads {
		w := domain.Watch{
			ID:   watchIDs[i],
			Repo: "refusedguy/beacon", PR: i + 1,
			HeadSHA: head, BaseRef: "main",
			Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
			Subscribers: []domain.Subscriber{
				{ID: "ses_orch", Role: domain.RoleOrchestrator, Adapter: "opencode"},
			},
		}
		if err := db.SaveWatch(w); err != nil {
			t.Fatalf("seed SaveWatch %d: %v", i, err)
		}
		obs := blockedTestObservation(head, infoNames[i])
		if got := b.verdictApplier.applyVerdict(context.Background(), w, obs, domain.VerdictBlocked, nil, now, "cycle"); got != 1 {
			t.Fatalf("applyVerdict %d updated %d watches, want 1", i, got)
		}
		got, err := db.Watch(w.ID)
		if err != nil {
			t.Fatalf("Watch %d: %v", i, err)
		}
		if names := got.Blocked.FailedChecks; len(names) != 1 || names[0] != "ci-build" {
			t.Fatalf("watch %d FailedChecks = %v, want only ci-build", i, names)
		}
		if got.BlockerID == "" {
			t.Fatalf("watch %d has no BlockerID", i)
		}
	}

	if len(control.signatures) != 2 {
		t.Fatalf("GetOrCreateOpenBlocker calls = %d, want 2", len(control.signatures))
	}
	for i, names := range control.signatures {
		if len(names) != 1 || names[0] != "ci-build" {
			t.Fatalf("incident signature %d = %v, want only ci-build", i, names)
		}
	}
	active, err := db.ActiveBlockers()
	if err != nil {
		t.Fatalf("ActiveBlockers: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("ActiveBlockers = %d, want one shared incident", len(active))
	}
	deliveries := waker.forSession("ses_orch")
	if len(deliveries) != 1 {
		t.Fatalf("orchestrator wakes = %d, want 1 for the shared incident", len(deliveries))
	}
	if text := deliveries[0].payload.Verdict; strings.Contains(text, "ci-info") || strings.Contains(text, "ci-other") {
		t.Fatalf("wake text = %q, want no informational names", text)
	}
}
