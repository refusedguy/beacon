package cibridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// chaosWaker records each delivery and can fail one session. A test uses it to
// kill an executor while leaving the orchestrator reachable for escalation.
type chaosWaker struct {
	mu         sync.Mutex
	deliveries []adapterDelivery
	failFor    map[string]error
}

func (w *chaosWaker) Deliver(_ context.Context, adapter, session string, p domain.WakePayload) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deliveries = append(w.deliveries, adapterDelivery{adapter: adapter, session: session, payload: p})
	return w.failFor[session]
}

func (w *chaosWaker) forSession(id string) []adapterDelivery {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []adapterDelivery
	for _, d := range w.deliveries {
		if d.session == id {
			out = append(out, d)
		}
	}
	return out
}

// TestChaosDuplicateBlockedWakeAcrossRestartDeliversOnce delivers a blocked
// notice, restarts from the same store file, and delivers the same notice
// again. The orchestrator must be steered once in total.
func TestChaosDuplicateBlockedWakeAcrossRestartDeliversOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	w := blockedTestWatch("watch:chaos:blocked:abc", 401,
		domain.Subscriber{ID: "ses_orch", Role: domain.RoleOrchestrator, Adapter: "opencode"},
		domain.Subscriber{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"},
	)
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	firstWaker := &chaosWaker{}
	first := New(db, &countingCI{}, fixedClock{at: time.Unix(10, 0)}, time.Second).WithWaker(firstWaker)
	if got := first.Refresh(context.Background()); got != 0 {
		t.Fatalf("first Refresh updated %d watches, want 0", got)
	}
	firstOrch := firstWaker.forSession("ses_orch")
	if len(firstOrch) != 1 {
		t.Fatalf("first orchestrator wakes = %d, want 1", len(firstOrch))
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	restarted, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer restarted.Close()
	secondWaker := &chaosWaker{}
	second := New(restarted, &countingCI{}, fixedClock{at: time.Unix(11, 0)}, time.Second).WithWaker(secondWaker)
	if got := second.Refresh(context.Background()); got != 0 {
		t.Fatalf("second Refresh updated %d watches, want 0", got)
	}
	if got := secondWaker.forSession("ses_orch"); len(got) != 0 {
		t.Fatalf("orchestrator wakes after restart = %d, want 0: %+v", len(got), got)
	}

	total := len(firstOrch) + len(secondWaker.forSession("ses_orch"))
	if total != 1 {
		t.Fatalf("total orchestrator wakes = %d, want 1", total)
	}
	o, err := restarted.Obligation(firstOrch[0].payload.ObligationID)
	if err != nil {
		t.Fatalf("dedup obligation missing: %v", err)
	}
	if o.DeliveredAt.IsZero() || len(o.Attempts) != 1 {
		t.Fatalf("dedup obligation = %+v, want one delivered attempt", o)
	}
}

type chaosLadderState struct {
	ObligationID string `json:"obligationID"`
	Step         int    `json:"step"`
}

// persistentChaosLadder keeps its position in a file so a new Bridge instance
// can load it after a restart. Stage never moves a running episode back, which
// is the idempotency contract Bridge.stage documents.
type persistentChaosLadder struct {
	path string
}

func (l *persistentChaosLadder) read() chaosLadderState {
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return chaosLadderState{}
	}
	var s chaosLadderState
	if err := json.Unmarshal(raw, &s); err != nil {
		return chaosLadderState{}
	}
	return s
}

func (l *persistentChaosLadder) write(s chaosLadderState) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(l.path, raw, 0o600)
}

func (l *persistentChaosLadder) Stage(_ context.Context, o domain.Obligation, _ domain.Verdict, _ time.Time) error {
	s := l.read()
	if s.ObligationID == o.ID {
		return nil
	}
	return l.write(chaosLadderState{ObligationID: o.ID, Step: 1})
}

func (l *persistentChaosLadder) Record(_ context.Context, id string, _ int, outcome domain.AttemptOutcome, _ time.Time) error {
	s := l.read()
	if s.ObligationID != id {
		return errors.New("ladder: no staged episode for obligation")
	}
	switch outcome {
	case domain.OutcomeTransportFailed:
		if s.Step < 3 {
			s.Step++
		}
	default:
		s.Step = 3
	}
	return l.write(s)
}

// TestChaosRestartMidLadderResumesAtSameStep stages an episode, records a
// transport failure, then drops the bridge and reopens the store. The next
// bridge must keep the saved step and continue from there.
func TestChaosRestartMidLadderResumesAtSameStep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	sub := domain.Subscriber{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"}
	w := domain.Watch{
		ID:          "watch:chaos:ladder:abc",
		Repo:        "o/r",
		PR:          5,
		HeadSHA:     "abc",
		Status:      domain.StatusWatching,
		Verdict:     domain.VerdictKeepWorking,
		Revision:    2,
		Subscribers: []domain.Subscriber{sub},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	obID := domain.ObligationKey(w.ID, w.Revision, sub.ID)
	o := domain.Obligation{
		ID: obID, WatchID: w.ID, Revision: w.Revision,
		SubscriberID: sub.ID, VerdictPointer: domain.VerdictKeepWorking,
		StagedAt: time.Unix(1, 0),
	}
	if err := db.SaveObligation(o); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}

	ctx := context.Background()
	now := time.Unix(2, 0)
	statePath := filepath.Join(t.TempDir(), "ladder.json")
	firstLadder := &persistentChaosLadder{path: statePath}
	first := New(db, &countingCI{}, fixedClock{at: now}, time.Second).WithLadder(firstLadder)
	first.stage(ctx, o, domain.VerdictKeepWorking, now)
	first.recordDelivery(ctx, obID, 1, domain.OutcomeTransportFailed, now)
	if got := firstLadder.read().Step; got != 2 {
		t.Fatalf("step before restart = %d, want 2", got)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	restarted, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer restarted.Close()

	secondLadder := &persistentChaosLadder{path: statePath}
	if got := secondLadder.read().Step; got != 2 {
		t.Fatalf("step after restart = %d, want 2", got)
	}
	second := New(restarted, &countingCI{}, fixedClock{at: now}, time.Second).WithLadder(secondLadder)
	second.stage(ctx, o, domain.VerdictKeepWorking, now)
	if got := secondLadder.read().Step; got != 2 {
		t.Fatalf("duplicate stage moved step to %d, want 2", got)
	}
	second.recordDelivery(ctx, obID, 2, domain.OutcomePosted, now)
	if got := secondLadder.read().Step; got != 3 {
		t.Fatalf("step after resume = %d, want 3", got)
	}
}

// TestChaosDeadExecutorOrphansOnceAndEscalatesOnce kills an executor session
// during a long fix. The subscriber becomes orphaned, the escalation reaches
// the orchestrator once, and a duplicate wake does not escalate again.
func TestChaosDeadExecutorOrphansOnceAndEscalatesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	orch := domain.Subscriber{ID: "ses_orch", Role: domain.RoleOrchestrator, Adapter: "opencode"}
	exec := domain.Subscriber{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode", Phase: domain.WorkerPhaseInWork}
	w := domain.Watch{
		ID:             "watch:chaos:orphan:abc",
		Repo:           "o/r",
		PR:             6,
		HeadSHA:        "abc",
		BaseRef:        "main",
		Status:         domain.StatusWatching,
		Verdict:        domain.VerdictKeepWorking,
		Revision:       4,
		Subscribers:    []domain.Subscriber{orch, exec},
		OrchestratorID: "ses_orch",
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	obID := domain.ObligationKey(w.ID, w.Revision, exec.ID)
	if err := db.SaveObligation(domain.Obligation{
		ID: obID, WatchID: w.ID, Revision: w.Revision,
		SubscriberID: exec.ID, VerdictPointer: domain.VerdictKeepWorking,
		StagedAt: time.Unix(1, 0),
	}); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}

	waker := &chaosWaker{failFor: map[string]error{
		"ses_exec": errors.New("session not found: HTTP 404"),
	}}
	ladder := &recordingWakeLadder{}
	b := New(db, &countingCI{}, fixedClock{at: time.Unix(2, 0)}, time.Second).
		WithWaker(waker).WithLadder(ladder)
	ctx := context.Background()
	b.wakeOne(ctx, w, exec, "keep_working: continue long fix", time.Unix(2, 0), "cycle-1", false)
	b.wakeOne(ctx, w, exec, "keep_working: continue long fix", time.Unix(2, 0), "cycle-2", false)

	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	found := false
	for _, sub := range got.Subscribers {
		if sub.ID != exec.ID {
			continue
		}
		found = true
		if sub.Phase != domain.WorkerPhaseOrphaned {
			t.Fatalf("executor phase = %q, want %q", sub.Phase, domain.WorkerPhaseOrphaned)
		}
		if sub.LastError == "" {
			t.Error("orphaned executor has no last error")
		}
	}
	if !found {
		t.Fatal("executor subscriber missing after wake")
	}

	orchWakes := waker.forSession(orch.ID)
	if len(orchWakes) != 1 {
		t.Fatalf("orchestrator escalations = %d, want 1: %+v", len(orchWakes), orchWakes)
	}
	msg := orchWakes[0].payload.Verdict
	for _, want := range []string{"executor_orphaned", string(domain.RoleExecutor), exec.ID, w.ID} {
		if !strings.Contains(msg, want) {
			t.Errorf("escalation %q missing %q", msg, want)
		}
	}

	orphanID := domain.ObligationKey("orphan:"+w.ID, 0, orch.ID)
	o, err := db.Obligation(orphanID)
	if err != nil {
		t.Fatalf("orphan obligation missing: %v", err)
	}
	if o.DeliveredAt.IsZero() || len(o.Attempts) != 1 {
		t.Fatalf("orphan obligation = %+v, want one delivered attempt", o)
	}

	indeterminate, transport := 0, 0
	for _, c := range ladder.calls {
		if c.kind != "record" || c.obligationID != obID {
			continue
		}
		if c.outcome == domain.OutcomeIndeterminate {
			indeterminate++
		}
		if c.outcome == domain.OutcomeTransportFailed {
			transport++
		}
	}
	if indeterminate != 2 || transport != 0 {
		t.Fatalf("executor outcomes = indeterminate %d, transport %d, want 2 and 0", indeterminate, transport)
	}
}
