package cibridge

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// qb01Sink keeps both the log records and the counter increments of one
// Refresh. The shared captureSink drops Inc, so it cannot see a metric.
type qb01Sink struct {
	mu     sync.Mutex
	events []qb01Event
	incs   map[string]uint64
}

type qb01Event struct {
	name   string
	fields observe.Fields
}

func (s *qb01Sink) Log(_ observe.Level, event string, fields observe.Fields) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := make(observe.Fields, len(fields))
	for k, v := range fields {
		snapshot[k] = v
	}
	s.events = append(s.events, qb01Event{name: event, fields: snapshot})
}

func (s *qb01Sink) Inc(name, labelValue string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.incs == nil {
		s.incs = make(map[string]uint64)
	}
	s.incs[name+":"+labelValue]++
}

func (s *qb01Sink) count(name, labelValue string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.incs[name+":"+labelValue]
}

func (s *qb01Sink) snapshot() []qb01Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]qb01Event(nil), s.events...)
}

// TestQB01CycleChainAndDeliveryOutcomeMetric checks the two merged observability
// changes in one flow. One Refresh must emit a chain that a single cycleID
// selects, and the delivery in that chain must increment the outcome counter
// exactly once. The merged tests cover each half on its own; this one proves
// they meet on the same cycle and the same delivery.
func TestQB01CycleChainAndDeliveryOutcomeMetric(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	w := domain.Watch{
		ID: "watch:qb/01:1:sha", Repo: "qb/01", PR: 1, HeadSHA: "sha",
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
		Subscribers: []domain.Subscriber{{ID: "ses_exec", Role: domain.RoleExecutor, Adapter: "opencode"}},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	state := domain.PRState{
		State: "OPEN", HeadRefOid: "sha", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		Checks: map[string]domain.Check{"build": {Conclusion: "FAILURE", Required: true}},
	}

	sink := &qb01Sink{}
	observe.SetDefault(sink)
	defer observe.SetDefault(nil)

	waker := &recordingWaker{}
	b := New(db, fakeCI{state: state}, fixedClock{at: time.Unix(1, 0)}, time.Second).WithWaker(waker)
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	if len(waker.deliveries) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(waker.deliveries))
	}

	events := sink.snapshot()
	cycleID := ""
	for _, e := range events {
		if e.name == "cycle_start" {
			cycleID, _ = e.fields[observe.FieldCycleID].(string)
		}
	}
	if cycleID == "" {
		t.Fatalf("no cycleID on cycle_start: %+v", events)
	}

	wantEvents := []string{"cycle_start", "observe", "wake", "verdict", "cycle_end"}
	seen := make(map[string]bool, len(wantEvents))
	for _, e := range events {
		if e.fields[observe.FieldCycleID] != cycleID {
			continue
		}
		seen[e.name] = true
		if e.name == "wake" {
			for _, key := range []string{
				observe.FieldWatchID, observe.FieldObligationID, observe.FieldRevision,
				observe.FieldRole, observe.FieldAdapter, observe.FieldAttempt,
			} {
				if _, ok := e.fields[key]; !ok {
					t.Errorf("wake event has no %s: %+v", key, e.fields)
				}
			}
		}
	}
	for _, name := range wantEvents {
		if !seen[name] {
			t.Errorf("cycle %s has no %s event: %+v", cycleID, name, events)
		}
	}

	outcomes := 0
	for _, e := range events {
		if e.name != "wake_outcome" || e.fields[observe.FieldCycleID] != cycleID {
			continue
		}
		outcomes++
		for _, key := range []string{
			observe.FieldWatchID, observe.FieldObligationID, observe.FieldRole, observe.FieldAttempt,
		} {
			if _, ok := e.fields[key]; !ok {
				t.Errorf("wake_outcome has no %s: %+v", key, e.fields)
			}
		}
	}
	if outcomes != 1 {
		t.Fatalf("correlated wake_outcome events = %d, want 1: %+v", outcomes, events)
	}

	label := "opencode:" + observe.WakeOutcomePosted
	if got := sink.count(observe.CounterWakeOutcome, label); got != 1 {
		t.Fatalf("%s{%s} = %d, want 1", observe.CounterWakeOutcome, label, got)
	}
}
