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

// blockedChecks builds the two-check observation from the card: a required
// ci-build failure that blocks and an informational failure that must not.
func blockedChecks(infoName string) map[string]domain.Check {
	return map[string]domain.Check{
		"ci-build": {State: "COMPLETED", Conclusion: "FAILURE", Required: true},
		infoName:   {State: "COMPLETED", Conclusion: "FAILURE", Required: false},
	}
}

func blockedObservation(head, infoName string) ciObservation {
	return ciObservation{pr: domain.PRState{
		State: "OPEN", HeadRefOid: head,
		Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		Checks: blockedChecks(infoName),
	}}
}

func openBlockedTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestApplyBlockedDropsInformationalFailure pins the core contract: the
// informational name stays out of BlockedInfo, the incident signature and the
// FIX-agent wake text, while the required name reaches all three.
func TestApplyBlockedDropsInformationalFailure(t *testing.T) {
	db := openBlockedTestDB(t)
	const head = "abc9a799a08f0c90de8675d4dbe74d9628324f8f"
	checks := blockedChecks("ci-info")
	now := time.Unix(2, 0)
	w := domain.Watch{
		ID:   "watch:refusedguy/beacon:1:" + head,
		Repo: "refusedguy/beacon", PR: 1, HeadSHA: head, BaseRef: "main",
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
		Checks: checks,
		Subscribers: []domain.Subscriber{
			{ID: "ses_orch", Role: domain.RoleOrchestrator, Adapter: "opencode"},
		},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("seed SaveWatch: %v", err)
	}

	waker := &recordingAdapterWaker{}
	b := New(db, nil, fixedClock{at: now}, time.Second).WithWaker(waker)
	if got := b.verdictApplier.applyBlocked(context.Background(), w, blockedObservation(head, "ci-info"), now, "cycle-1"); got != 1 {
		t.Fatalf("applyBlocked updated %d watches, want 1", got)
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
	notice := blockedNotice(got)
	if !strings.Contains(notice, "ci-build") || strings.Contains(notice, "ci-info") {
		t.Fatalf("blockedNotice = %q, want ci-build and no ci-info", notice)
	}
	for _, d := range waker.forSession("ses_orch") {
		if strings.Contains(d.payload.Verdict, "ci-info") {
			t.Fatalf("FIX-agent wake text = %q, want no ci-info", d.payload.Verdict)
		}
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
}

// TestBlockerIncidentSignatureIgnoresInformationalSwap proves that replacing
// one optional failure with another does not fork the incident: both watches
// land on the same BlockerIncident through the required ci-build failure.
func TestBlockerIncidentSignatureIgnoresInformationalSwap(t *testing.T) {
	db := openBlockedTestDB(t)
	now := time.Unix(4, 0)
	heads := []string{
		"abc9a799a08f0c90de8675d4dbe74d9628324f8f",
		"def9a799a08f0c90de8675d4dbe74d9628324f8f",
	}
	infoNames := []string{"ci-info", "ci-other"}
	watchIDs := []string{
		"watch:refusedguy/beacon:1:" + heads[0],
		"watch:refusedguy/beacon:2:" + heads[1],
	}
	blockerIDs := make([]string, 0, 2)

	for i, head := range heads {
		w := domain.Watch{
			ID:   watchIDs[i],
			Repo: "refusedguy/beacon", PR: i + 1, HeadSHA: head, BaseRef: "main",
			Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
			Checks: blockedChecks(infoNames[i]),
		}
		if err := db.SaveWatch(w); err != nil {
			t.Fatalf("seed SaveWatch %d: %v", i, err)
		}
		b := New(db, nil, fixedClock{at: now}, time.Second).WithWaker(&recordingAdapterWaker{})
		if got := b.verdictApplier.applyBlocked(context.Background(), w, blockedObservation(head, infoNames[i]), now, "cycle"); got != 1 {
			t.Fatalf("applyBlocked %d updated %d watches, want 1", i, got)
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
		blockerIDs = append(blockerIDs, got.BlockerID)
	}

	if blockerIDs[0] != blockerIDs[1] {
		t.Fatalf("BlockerIDs = %v, want one shared incident", blockerIDs)
	}
	active, err := db.ActiveBlockers()
	if err != nil {
		t.Fatalf("ActiveBlockers: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("ActiveBlockers = %d, want 1 after the optional-failure swap", len(active))
	}
}

// TestHandleBlockedFiltersStoredInformationalNames covers an already blocked
// Watch whose persisted BlockedInfo still carries an old informational name.
// The repeat must not fork the incident or print that name in the wake.
func TestHandleBlockedFiltersStoredInformationalNames(t *testing.T) {
	db := openBlockedTestDB(t)
	const head = "abc9a799a08f0c90de8675d4dbe74d9628324f8f"
	now := time.Unix(6, 0)
	w := domain.Watch{
		ID:   "watch:refusedguy/beacon:3:" + head,
		Repo: "refusedguy/beacon", PR: 3, HeadSHA: head, BaseRef: "main",
		Status: domain.StatusBlocked, Verdict: domain.VerdictBlocked,
		Revision: 3,
		Blocked: &domain.BlockedInfo{
			Reason:       domain.BlockReasonBaseChecksFailed,
			FailedChecks: []string{"ci-build", "ci-info"},
			Since:        time.Unix(1, 0),
			BlockedBy:    "cibridge",
		},
		Checks: blockedChecks("ci-info"),
		Subscribers: []domain.Subscriber{
			{ID: "ses_orch", Role: domain.RoleOrchestrator, Adapter: "opencode"},
		},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("seed SaveWatch: %v", err)
	}

	waker := &recordingAdapterWaker{}
	b := New(db, nil, fixedClock{at: now}, time.Second).WithWaker(waker)
	b.verdictApplier.handleBlocked(context.Background(), w, "cycle")

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
	if notice := blockedNotice(w); strings.Contains(notice, "ci-info") {
		t.Fatalf("blockedNotice = %q, want no ci-info", notice)
	}
	for _, d := range waker.forSession("ses_orch") {
		if strings.Contains(d.payload.Verdict, "ci-info") {
			t.Fatalf("FIX-agent wake text = %q, want no ci-info", d.payload.Verdict)
		}
	}
}

// TestBlockingFailedNamesKeepsUnknownNames pins the fail-closed half of the
// filter. A name the provider marked informational is dropped, but a name with
// no per-check data stays: the verdict layer treats such a failure as blocking
// and hiding it would silence a real gate.
func TestBlockingFailedNamesKeepsUnknownNames(t *testing.T) {
	checks := map[string]domain.Check{
		"ci-info":  {Conclusion: "FAILURE", Required: false},
		"ci-build": {Conclusion: "FAILURE", Required: true},
	}
	got := strings.Join(blockingFailedNames([]string{"ci-info", "ci-build", "ci-no-data"}, checks), ",")
	if want := "ci-build,ci-no-data"; got != want {
		t.Fatalf("blockingFailedNames = %q, want %q", got, want)
	}
}
