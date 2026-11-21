package wake

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// w2bObligationStore is the narrow obligation port in memory. A real SQLite
// store satisfies the same interface; the test only checks the contract.
type w2bObligationStore struct {
	mu    sync.Mutex
	items map[string]domain.Obligation
	marks []string
}

func newW2BObligationStore() *w2bObligationStore {
	return &w2bObligationStore{items: make(map[string]domain.Obligation)}
}

func (s *w2bObligationStore) add(o domain.Obligation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[o.ID] = o
}

func (s *w2bObligationStore) Obligation(id string) (domain.Obligation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.items[id]
	if !ok {
		return domain.Obligation{}, fmt.Errorf("no obligation: %w", sql.ErrNoRows)
	}
	return o, nil
}

func (s *w2bObligationStore) MarkObligationDelivered(id string, at time.Time, _ int, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marks = append(s.marks, id)
	if o, ok := s.items[id]; ok {
		o.DeliveredAt = at
		s.items[id] = o
	}
	return nil
}

func w2bPayload(watchID, sessionID string, revision uint64) domain.WakePayload {
	return domain.WakePayload{
		WatchID:      watchID,
		ObligationID: domain.ObligationKey(watchID, revision, sessionID),
		Revision:     revision,
		Verdict:      "retry",
	}
}

func w2bAck() []byte { return []byte(`{"data":{"id":"msg_w2b"}}`) }

// The HTTP port wins over the CLI, and the delivered fact lands in SQLite.
func TestW2BPromptFuncFirstAndDurableFact(t *testing.T) {
	const sessionID = "ses_w2b_http"
	p := w2bPayload("w2b-http", sessionID, 1)
	store := newW2BObligationStore()
	store.add(domain.Obligation{ID: p.ObligationID, WatchID: p.WatchID, Revision: p.Revision, SubscriberID: sessionID})

	var calls int
	var gotSession, gotText, gotDelivery string
	d := &OpenCodeDeliverer{
		Runner: func(context.Context, ...string) ([]byte, error) {
			t.Fatal("CLI fallback must not run when PromptFunc is installed")
			return nil, nil
		},
		PromptFunc: func(_ context.Context, sid, text, delivery string) error {
			calls++
			gotSession, gotText, gotDelivery = sid, text, delivery
			return nil
		},
		Obligations: store,
	}
	if err := d.Deliver(context.Background(), sessionID, p); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if calls != 1 || gotSession != sessionID {
		t.Fatalf("prompt calls = %d, session = %q", calls, gotSession)
	}
	if gotDelivery != wakeDeliverySteer {
		t.Fatalf("delivery = %q, want steer", gotDelivery)
	}
	if !strings.Contains(gotText, p.ObligationID) {
		t.Fatalf("prompt text missing obligation: %q", gotText)
	}
	if len(store.marks) != 1 || store.marks[0] != p.ObligationID {
		t.Fatalf("marked obligations = %v, want [%s]", store.marks, p.ObligationID)
	}

	// A restarted process has no seen map. The SQLite fact still stops a
	// second prompt for the same obligation.
	fresh := &OpenCodeDeliverer{
		Runner: func(context.Context, ...string) ([]byte, error) {
			t.Fatal("a delivered obligation must not prompt again")
			return nil, nil
		},
		Obligations: store,
	}
	if err := fresh.Deliver(context.Background(), sessionID, p); err != nil {
		t.Fatalf("repeat Deliver: %v", err)
	}
	if calls != 1 {
		t.Fatalf("prompt calls after restart = %d, want 1", calls)
	}
}

// A busy session gets queue so the current dialogue is not interrupted.
func TestW2BBusySessionQueues(t *testing.T) {
	const sessionID = "ses_w2b_busy"
	var deliveries []string
	d := &OpenCodeDeliverer{
		SessionActiveFunc: func(context.Context, string) (bool, bool, error) { return true, true, nil },
		PromptFunc: func(_ context.Context, _, _, delivery string) error {
			deliveries = append(deliveries, delivery)
			return nil
		},
	}
	if err := d.Deliver(context.Background(), sessionID, w2bPayload("w2b-busy", sessionID, 1)); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(deliveries) != 1 || deliveries[0] != wakeDeliveryQueue {
		t.Fatalf("deliveries = %v, want [queue]", deliveries)
	}
}

// An idle session is steered with resume=true through the CLI fallback.
func TestW2BIdleSessionSteersWithResume(t *testing.T) {
	const sessionID = "ses_w2b_idle"
	var got struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
		Resume   bool   `json:"resume"`
	}
	d := &OpenCodeDeliverer{
		SessionActiveFunc: func(context.Context, string) (bool, bool, error) { return false, true, nil },
		Runner: func(ctx context.Context, args ...string) ([]byte, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("CLI fallback has no deadline")
			} else if remaining := time.Until(deadline); remaining <= 0 || remaining > opencodeCLITimeout {
				t.Errorf("CLI deadline in %v, want at most %v", remaining, opencodeCLITimeout)
			}
			if err := json.Unmarshal([]byte(args[4]), &got); err != nil {
				t.Fatalf("decode --data: %v", err)
			}
			return w2bAck(), nil
		},
	}
	p := w2bPayload("w2b-idle", sessionID, 1)
	if err := d.Deliver(context.Background(), sessionID, p); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got.Delivery != wakeDeliverySteer || !got.Resume {
		t.Fatalf("body = %+v, want steer with resume", got)
	}
	if !strings.Contains(got.Text, p.ObligationID) {
		t.Fatalf("body text missing obligation: %q", got.Text)
	}
}

// A missing session is a typed error from either the status port or the prompt
// port, and nothing is sent to it.
func TestW2BSessionDeadIsTyped(t *testing.T) {
	const sessionID = "ses_w2b_dead"
	var prompts int
	d := &OpenCodeDeliverer{
		SessionActiveFunc: func(context.Context, string) (bool, bool, error) {
			return false, false, fmt.Errorf("GET /api/session: 404")
		},
		PromptFunc: func(context.Context, string, string, string) error {
			prompts++
			return nil
		},
	}
	err := d.Deliver(context.Background(), sessionID, w2bPayload("w2b-dead", sessionID, 1))
	if !errors.Is(err, ErrSessionDead) {
		t.Fatalf("error = %v, want ErrSessionDead", err)
	}
	if prompts != 0 {
		t.Fatalf("prompts = %d, want 0 for a dead session", prompts)
	}

	// A 404 from the prompt port is typed too.
	d2 := &OpenCodeDeliverer{
		PromptFunc: func(context.Context, string, string, string) error {
			return fmt.Errorf("opencode api: 404 session not found")
		},
	}
	err = d2.Deliver(context.Background(), sessionID, w2bPayload("w2b-dead2", sessionID, 1))
	if !errors.Is(err, ErrSessionDead) {
		t.Fatalf("prompt 404 error = %v, want ErrSessionDead", err)
	}
}

// Without an HTTP port the deliverer uses the bounded CLI fallback. The same
// happens when the port reports that service.json is gone at call time.
func TestW2BCLIFallback(t *testing.T) {
	const sessionID = "ses_w2b_cli"
	var calls int
	runner := func(context.Context, ...string) ([]byte, error) {
		calls++
		return w2bAck(), nil
	}
	d := &OpenCodeDeliverer{Runner: runner}
	if err := d.Deliver(context.Background(), sessionID, w2bPayload("w2b-cli", sessionID, 1)); err != nil {
		t.Fatalf("Deliver without port: %v", err)
	}
	if calls != 1 {
		t.Fatalf("CLI calls = %d, want 1", calls)
	}

	d2 := &OpenCodeDeliverer{
		Runner: runner,
		PromptFunc: func(context.Context, string, string, string) error {
			return fmt.Errorf("service.json missing: %w", ErrNoPromptPort)
		},
	}
	if err := d2.Deliver(context.Background(), sessionID, w2bPayload("w2b-cli", sessionID, 2)); err != nil {
		t.Fatalf("Deliver with absent port: %v", err)
	}
	if calls != 2 {
		t.Fatalf("CLI calls after fallback = %d, want 2", calls)
	}
}

// A fixer owns its own obligation and is a valid wake target. A foreign
// obligation or watch is still refused.
func TestW2BRoleFixerWakeTarget(t *testing.T) {
	const (
		watchID  = "w2b-fix"
		fixer    = "ses_w2b_fixer"
		executor = "ses_w2b_exec"
	)
	w := domain.Watch{
		ID: watchID,
		Subscribers: []domain.Subscriber{
			{ID: executor, Role: domain.RoleExecutor, Adapter: AdapterOpenCode},
			{ID: fixer, Role: domain.RoleFixer, FixFor: executor, Adapter: AdapterOpenCode},
		},
	}
	got, err := SubscriberTarget(w, watchID, fixer)
	if err != nil || got != fixer {
		t.Fatalf("SubscriberTarget(fixer) = %q, %v", got, err)
	}

	var delivered string
	d := &recordingWakeDeliverer{fn: func(_ context.Context, sid string, _ domain.WakePayload) error {
		delivered = sid
		return nil
	}}
	p := w2bPayload(watchID, fixer, 2)
	if err := DeliverToSubscriber(context.Background(), d, w, p, fixer); err != nil {
		t.Fatalf("DeliverToSubscriber(fixer): %v", err)
	}
	if delivered != fixer {
		t.Fatalf("delivered to %q, want %q", delivered, fixer)
	}

	foreign := p
	foreign.ObligationID = domain.ObligationKey(watchID, 2, executor)
	if err := DeliverToSubscriber(context.Background(), d, w, foreign, fixer); err == nil {
		t.Fatal("a foreign obligation must not wake the fixer")
	}
	substituted := w2bPayload("another-watch", fixer, 2)
	if err := DeliverToSubscriber(context.Background(), d, w, substituted, fixer); err == nil {
		t.Fatal("a substituted watchID must not wake the fixer")
	}
	if delivered != fixer {
		t.Fatalf("refused wakes changed delivery to %q", delivered)
	}
}

// fixMetricSink records the counters the wake layer emits. It keeps the test
// independent of the Recorder and the Prometheus exporter.
type fixMetricSink struct {
	counts map[string]uint64
	events []string
}

func (s *fixMetricSink) Log(_ observe.Level, event string, _ observe.Fields) {
	s.events = append(s.events, event)
}

func (s *fixMetricSink) Inc(name, label string) {
	if s.counts == nil {
		s.counts = make(map[string]uint64)
	}
	s.counts[name+":"+label]++
}

func TestATX43FixCounterRisesOnFixerWake(t *testing.T) {
	sink := &fixMetricSink{}
	observe.SetDefault(sink)
	defer observe.SetDefault(nil)

	const (
		watchID = "atx43-fix"
		fixer   = "ses_atx43_fixer"
	)
	w := domain.Watch{
		ID: watchID,
		Subscribers: []domain.Subscriber{
			{ID: fixer, Role: domain.RoleFixer, FixFor: "ses_atx43_exec", Adapter: AdapterOpenCode},
		},
	}
	d := &recordingWakeDeliverer{fn: func(context.Context, string, domain.WakePayload) error {
		return nil
	}}
	if err := DeliverToSubscriber(context.Background(), d, w, w2bPayload(watchID, fixer, 3), fixer); err != nil {
		t.Fatalf("DeliverToSubscriber(fixer): %v", err)
	}
	if got := sink.counts[observe.CounterFixTotal+":fixer"]; got != 1 {
		t.Errorf("fix_total{role=fixer} = %d, want 1", got)
	}
	found := false
	for _, event := range sink.events {
		if event == "fix_delivered" {
			found = true
		}
	}
	if !found {
		t.Errorf("fix_delivered event missing: %+v", sink.events)
	}
}

func TestATX43FixCounterStaysZeroForExecutorAndFailedWake(t *testing.T) {
	sink := &fixMetricSink{}
	observe.SetDefault(sink)
	defer observe.SetDefault(nil)

	const executor = "ses_atx43_exec"
	execWatch := domain.Watch{
		ID: "atx43-exec",
		Subscribers: []domain.Subscriber{
			{ID: executor, Role: domain.RoleExecutor, Adapter: AdapterOpenCode},
		},
	}
	ok := &recordingWakeDeliverer{fn: func(context.Context, string, domain.WakePayload) error {
		return nil
	}}
	if err := DeliverToSubscriber(context.Background(), ok, execWatch, w2bPayload(execWatch.ID, executor, 1), executor); err != nil {
		t.Fatalf("DeliverToSubscriber(executor): %v", err)
	}
	if got := sink.counts[observe.CounterFixTotal+":fixer"]; got != 0 {
		t.Errorf("fix_total after executor wake = %d, want 0", got)
	}

	const fixer = "ses_atx43_fixer"
	fixWatch := domain.Watch{
		ID: "atx43-failed",
		Subscribers: []domain.Subscriber{
			{ID: fixer, Role: domain.RoleFixer, FixFor: executor, Adapter: AdapterOpenCode},
		},
	}
	failing := &recordingWakeDeliverer{fn: func(context.Context, string, domain.WakePayload) error {
		return errors.New("transport refused")
	}}
	if err := DeliverToSubscriber(context.Background(), failing, fixWatch, w2bPayload(fixWatch.ID, fixer, 1), fixer); err == nil {
		t.Fatal("failed delivery must surface the error")
	}
	if got := sink.counts[observe.CounterFixTotal+":fixer"]; got != 0 {
		t.Errorf("fix_total after failed fixer wake = %d, want 0", got)
	}
}

// The CI bridge selects DeliverToSubscriber on the production waker. The fix
// counter must rise there once, not on a replay of the same obligation.
func TestATX43ProductionWakerCountsFixerOnce(t *testing.T) {
	sink := &fixMetricSink{}
	observe.SetDefault(sink)
	defer observe.SetDefault(nil)

	const (
		watchID = "atx43-prod"
		fixer   = "ses_atx43_prod_fixer"
	)
	w := domain.Watch{
		ID: watchID,
		Subscribers: []domain.Subscriber{
			{ID: fixer, Role: domain.RoleFixer, FixFor: "ses_atx43_prod_exec", Adapter: AdapterOpenCode},
		},
	}
	p := w2bPayload(watchID, fixer, 7)
	inner := &adapterDelivererFn{fn: func(context.Context, string, string, domain.WakePayload) error {
		return nil
	}}
	waker := &AdapterDedupDeliverer{Inner: inner, Dedup: NewDeliveryDedup()}
	if err := waker.DeliverToSubscriber(context.Background(), w, p, fixer); err != nil {
		t.Fatalf("DeliverToSubscriber(fixer): %v", err)
	}
	if got := sink.counts[observe.CounterFixTotal+":fixer"]; got != 1 {
		t.Errorf("fix_total{role=fixer} = %d, want 1", got)
	}
	if err := waker.DeliverToSubscriber(context.Background(), w, p, fixer); err != nil {
		t.Fatalf("repeated DeliverToSubscriber(fixer): %v", err)
	}
	if got := sink.counts[observe.CounterFixTotal+":fixer"]; got != 1 {
		t.Errorf("fix_total after replay = %d, want 1", got)
	}
}
