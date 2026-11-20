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

const terminalHead = "abc9a799a08f0c90de8675d4dbe74d9628324f8f"

// terminalSnapshot builds one batch snapshot with a fixed PR state and checks.
// The head matches terminalFixture so the Watch is not treated as head-changed.
func terminalSnapshot(state string, checks map[string]domain.Check) domain.PRSnapshot {
	return domain.PRSnapshot{
		PR: domain.PRState{
			State: state, HeadRefOid: terminalHead,
			Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", Checks: checks,
		},
		Base: domain.BaseBranchState{BaseRef: "main", ChecksKnown: true},
	}
}

// terminalFixture opens a fresh SQLite store with one watching Watch and the
// given subscribers. It owns cleanup so no test leaves a database behind.
func terminalFixture(t *testing.T, subs ...domain.Subscriber) (*store.DB, domain.Watch) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	w := domain.Watch{
		ID:   "watch:refusedguy/beacon:406:" + terminalHead,
		Repo: "refusedguy/beacon", PR: 406, HeadSHA: terminalHead,
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
		Subscribers: subs,
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	return db, w
}

// wantTerminalFields are the machine-readable fields the orchestrator reads
// from the wake text. They are asserted verbatim: the envelope is the contract.
var wantTerminalFields = []string{
	"watchID=watch:refusedguy/beacon:406:" + terminalHead,
	"pr=refusedguy/beacon#406",
	"verdict=merged",
	"closeReason=merged",
	"revision=1",
	"nextAction: report_to_orchestrator",
	"nextHint: wave item done; proceed to next wave",
}

// TestTerminalVerdictConfirmsBoundOrchestrator pins the whole hand-off: one
// terminal obligation and one wake to the bound orchestrator, nothing to the
// executor, and the machine-readable fields in the wake text.
func TestTerminalVerdictConfirmsBoundOrchestrator(t *testing.T) {
	db, w := terminalFixture(t,
		domain.Subscriber{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"},
	)
	ci := mergedBatchCI{snaps: map[string]domain.PRSnapshot{w.ID: terminalSnapshot("MERGED", nil)}}
	waker := &recordingAdapterWaker{}
	b := New(db, ci, fixedClock{at: time.Unix(1, 0)}, time.Second).
		WithWaker(waker).
		WithOrchestratorIDSource(func() string { return "ses_orch" })

	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	if len(waker.deliveries) != 1 {
		t.Fatalf("wake deliveries = %d, want 1: %+v", len(waker.deliveries), waker.deliveries)
	}
	d := waker.deliveries[0]
	if d.session != "ses_orch" {
		t.Fatalf("wake session = %q, want ses_orch", d.session)
	}
	if d.adapter != "opencode" {
		t.Fatalf("wake adapter = %q, want opencode", d.adapter)
	}
	for _, field := range wantTerminalFields {
		if !strings.Contains(d.payload.Verdict, field) {
			t.Errorf("wake text %q missing field %q", d.payload.Verdict, field)
		}
	}
	wantObligation := domain.ObligationKey(w.ID, w.Revision+1, "ses_orch")
	if d.payload.ObligationID != wantObligation {
		t.Fatalf("obligation = %q, want %q", d.payload.ObligationID, wantObligation)
	}
	if d.payload.Revision != w.Revision+1 {
		t.Fatalf("payload revision = %d, want %d", d.payload.Revision, w.Revision+1)
	}
	o, err := db.Obligation(wantObligation)
	if err != nil {
		t.Fatalf("confirmation obligation: %v", err)
	}
	if o.DeliveredAt.IsZero() {
		t.Fatal("confirmation obligation not marked delivered")
	}
	if o.SubscriberID != "ses_orch" {
		t.Fatalf("confirmation obligation subscriber = %q, want ses_orch", o.SubscriberID)
	}
	// The executor stays finished and receives no wake.
	for _, got := range waker.deliveries {
		if got.session == "ses_exec" {
			t.Fatalf("executor received a wake: %+v", got)
		}
	}
	closed, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if closed.Status != domain.StatusClosed || closed.Verdict != domain.VerdictMerged {
		t.Fatalf("status/verdict = %q/%q, want closed/merged", closed.Status, closed.Verdict)
	}
	if len(closed.Subscribers) != 1 || closed.Subscribers[0].Phase != domain.WorkerPhaseFinished {
		t.Fatalf("subscribers = %+v, want finished executor", closed.Subscribers)
	}
}

// TestClosedVerdictConfirmsWithClosedReason covers the second terminal verdict.
func TestClosedVerdictConfirmsWithClosedReason(t *testing.T) {
	db, w := terminalFixture(t,
		domain.Subscriber{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"},
	)
	ci := mergedBatchCI{snaps: map[string]domain.PRSnapshot{w.ID: terminalSnapshot("CLOSED", nil)}}
	waker := &recordingAdapterWaker{}
	b := New(db, ci, fixedClock{at: time.Unix(1, 0)}, time.Second).
		WithWaker(waker).
		WithOrchestratorIDSource(func() string { return "ses_orch" })

	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	if len(waker.deliveries) != 1 {
		t.Fatalf("wake deliveries = %d, want 1", len(waker.deliveries))
	}
	text := waker.deliveries[0].payload.Verdict
	for _, field := range []string{"verdict=closed", "closeReason=closed", "nextAction: report_to_orchestrator"} {
		if !strings.Contains(text, field) {
			t.Errorf("wake text %q missing field %q", text, field)
		}
	}
}

// TestTerminalConfirmationIdempotent proves the dedup lives in the obligation,
// not in a closed-Watch skip or an in-memory flag. After the first Refresh a
// second Refresh and a direct repeat of the helper both stay at one delivery.
func TestTerminalConfirmationIdempotent(t *testing.T) {
	db, w := terminalFixture(t,
		domain.Subscriber{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"},
	)
	ci := mergedBatchCI{snaps: map[string]domain.PRSnapshot{w.ID: terminalSnapshot("MERGED", nil)}}
	waker := &recordingAdapterWaker{}
	b := New(db, ci, fixedClock{at: time.Unix(1, 0)}, time.Second).
		WithWaker(waker).
		WithOrchestratorIDSource(func() string { return "ses_orch" })

	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("first Refresh updated %d watches, want 1", got)
	}
	if len(waker.deliveries) != 1 {
		t.Fatalf("first Refresh deliveries = %d, want 1", len(waker.deliveries))
	}
	if got := b.Refresh(context.Background()); got != 0 {
		t.Fatalf("second Refresh updated %d watches, want 0", got)
	}
	if len(waker.deliveries) != 1 {
		t.Fatalf("second Refresh deliveries = %d, want 1", len(waker.deliveries))
	}
	// Re-run the notification on the closed Watch at the same revision: the
	// delivered obligation must suppress a second wake.
	closed, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	b.notifyOrchestratorTerminal(context.Background(), closed, domain.VerdictMerged, time.Unix(2, 0), "replay")
	if len(waker.deliveries) != 1 {
		t.Fatalf("replayed notification deliveries = %d, want 1", len(waker.deliveries))
	}
}

// TestTerminalVerdictWithoutBoundOrchestratorSendsNothing covers both an absent
// source and an empty binding: the Watch still closes, but no confirmation goes
// out and no orchestrator obligation is created.
func TestTerminalVerdictWithoutBoundOrchestratorSendsNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source OrchestratorIDSource
	}{
		{name: "no source", source: nil},
		{name: "empty binding", source: func() string { return "  " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, w := terminalFixture(t,
				domain.Subscriber{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"},
			)
			ci := mergedBatchCI{snaps: map[string]domain.PRSnapshot{w.ID: terminalSnapshot("MERGED", nil)}}
			waker := &recordingAdapterWaker{}
			b := New(db, ci, fixedClock{at: time.Unix(1, 0)}, time.Second).WithWaker(waker)
			if tc.source != nil {
				b = b.WithOrchestratorIDSource(tc.source)
			}

			if got := b.Refresh(context.Background()); got != 1 {
				t.Fatalf("Refresh updated %d watches, want 1", got)
			}
			if len(waker.deliveries) != 0 {
				t.Fatalf("wake deliveries = %d, want 0: %+v", len(waker.deliveries), waker.deliveries)
			}
			closed, err := db.Watch(w.ID)
			if err != nil {
				t.Fatalf("Watch: %v", err)
			}
			if closed.Status != domain.StatusClosed || closed.Verdict != domain.VerdictMerged {
				t.Fatalf("status/verdict = %q/%q, want closed/merged", closed.Status, closed.Verdict)
			}
		})
	}
}

// conflictMerger is a ready Watch whose merge is held for a conflict, which the
// bridge translates to a rebase verdict.
type conflictMerger struct{}

func (conflictMerger) Merge(context.Context, string, int) (bool, bool, string, error) {
	return false, true, "merge conflict", nil
}

// TestNonTerminalVerdictsDoNotConfirm proves keep_working and rebase steer the
// usual executor wake but never send a terminal confirmation.
func TestNonTerminalVerdictsDoNotConfirm(t *testing.T) {
	t.Run("keep_working", func(t *testing.T) {
		db, w := terminalFixture(t,
			domain.Subscriber{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"},
		)
		checks := map[string]domain.Check{"build": {Conclusion: "FAILURE", Required: true}}
		ci := mergedBatchCI{snaps: map[string]domain.PRSnapshot{w.ID: terminalSnapshot("OPEN", checks)}}
		waker := &recordingAdapterWaker{}
		b := New(db, ci, fixedClock{at: time.Unix(1, 0)}, time.Second).
			WithWaker(waker).
			WithOrchestratorIDSource(func() string { return "ses_orch" })

		if got := b.Refresh(context.Background()); got != 1 {
			t.Fatalf("Refresh updated %d watches, want 1", got)
		}
		assertNoTerminalConfirmation(t, waker.deliveries)
	})

	t.Run("rebase", func(t *testing.T) {
		db, w := terminalFixture(t,
			domain.Subscriber{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"},
			domain.Subscriber{ID: "ses_fix", Role: domain.RoleFixer, Adapter: "opencode"},
		)
		w.MergePolicy = domain.MergePolicy{AutoMerge: true}
		if err := db.SaveWatch(w); err != nil {
			t.Fatalf("SaveWatch: %v", err)
		}
		ci := mergedBatchCI{snaps: map[string]domain.PRSnapshot{w.ID: terminalSnapshot("OPEN", nil)}}
		waker := &recordingAdapterWaker{}
		b := New(db, ci, fixedClock{at: time.Unix(1, 0)}, time.Second).
			WithWaker(waker).
			WithMerger(conflictMerger{}).
			WithOrchestratorIDSource(func() string { return "ses_orch" })

		if got := b.Refresh(context.Background()); got != 1 {
			t.Fatalf("Refresh updated %d watches, want 1", got)
		}
		assertNoTerminalConfirmation(t, waker.deliveries)
	})
}

func assertNoTerminalConfirmation(t *testing.T, deliveries []adapterDelivery) {
	t.Helper()
	if len(deliveries) == 0 {
		t.Fatal("no wake delivered: expected the usual non-terminal wake")
	}
	for _, d := range deliveries {
		if strings.Contains(d.payload.Verdict, "terminal_ack") ||
			strings.Contains(d.payload.Verdict, "report_to_orchestrator") {
			t.Fatalf("non-terminal verdict sent a confirmation: %+v", d)
		}
	}
}
