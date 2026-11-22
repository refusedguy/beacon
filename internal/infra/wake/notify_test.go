package wake

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/cibridge"
)

// notifyFakeDeliverer records the session and pointer it was asked to steer.
type notifyFakeDeliverer struct {
	sessionID string
	payload   domain.WakePayload
	calls     int
}

func (d *notifyFakeDeliverer) Deliver(_ context.Context, sessionID string, p domain.WakePayload) error {
	d.calls++
	d.sessionID = sessionID
	d.payload = p
	return nil
}

// notifyFakeWatches is the WatchReader slice of a Store, with no other methods.
type notifyFakeWatches struct {
	watch domain.Watch
	err   error
}

func (s notifyFakeWatches) Watch(string) (domain.Watch, error) {
	return s.watch, s.err
}

func notifyWatch() domain.Watch {
	return domain.Watch{
		ID: "watch-1",
		Subscribers: []domain.Subscriber{
			{ID: "ses_executor", Role: domain.RoleExecutor},
			{ID: "ses_orchestrator", Role: domain.RoleOrchestrator},
		},
	}
}

func TestClassifyReasonTrimsWhitespace(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want TriageClass
	}{
		{"my fault with padding", "  \n\t моя вина, чиню \r\n ", TriageMyFault},
		{"main broken with padding", "\n\tбратан иди чини main\t", TriageMainBroken},
		{"unclear with padding", "  \n  ", TriageUnclear},
		{"unknown text", "  не понял  ", TriageUnclear},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyReason(tc.raw); got != tc.want {
				t.Fatalf("ClassifyReason(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestClassifyReasonClasses(t *testing.T) {
	cases := []struct {
		raw  string
		want TriageClass
	}{
		{"братан иди чини main", TriageMainBroken},
		{"broken main", TriageMainBroken},
		{"флейк", TriageMainBroken},
		{"flake", TriageMainBroken},
		{"моя вина, чиню", TriageMyFault},
		{"my fault", TriageMyFault},
		{"проверил, изменений моих нет", TriageUnclear},
	}
	for _, tc := range cases {
		if got := ClassifyReason(tc.raw); got != tc.want {
			t.Errorf("ClassifyReason(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestReasonForReturnsCanonicalMessages(t *testing.T) {
	cases := map[TriageClass]string{
		TriageMainBroken: ReasonMainBroken,
		TriageMyFault:    ReasonMyFault,
		TriageUnclear:    ReasonUnclear,
		TriageClass("x"): ReasonUnclear,
	}
	for class, want := range cases {
		if got := ReasonFor(class); got != want {
			t.Errorf("ReasonFor(%q) = %q, want %q", class, got, want)
		}
	}
}

func TestNotifyOrchestratorUsesOrchestratorSession(t *testing.T) {
	d := &notifyFakeDeliverer{}
	n := &OrchestratorNotifier{Deliverer: d, Watches: notifyFakeWatches{watch: notifyWatch()}}
	ctx := WithOrchestratorNotifier(context.Background(), n)
	p := domain.WakePayload{
		WatchID:      "watch-1",
		ObligationID: "obl-1",
		Revision:     7,
		Verdict:      "retry",
	}

	if err := NotifyOrchestrator(ctx, "  братан иди чини main \n", p); err != nil {
		t.Fatalf("NotifyOrchestrator: %v", err)
	}
	if d.calls != 1 {
		t.Fatalf("deliver calls = %d, want 1", d.calls)
	}
	if d.sessionID != "ses_orchestrator" {
		t.Fatalf("sessionID = %q, want ses_orchestrator", d.sessionID)
	}
	if d.payload.WatchID != p.WatchID ||
		d.payload.ObligationID != p.ObligationID ||
		d.payload.Revision != p.Revision {
		t.Fatalf("payload identifiers changed: %+v", d.payload)
	}
	if d.payload.Verdict != "братан иди чини main" {
		t.Fatalf("reason = %q, want trimmed reason", d.payload.Verdict)
	}
}

func TestNotifyOrchestratorTrimsWhitespaceOnlyReason(t *testing.T) {
	d := &notifyFakeDeliverer{}
	n := &OrchestratorNotifier{Deliverer: d, Watches: notifyFakeWatches{watch: notifyWatch()}}
	ctx := WithOrchestratorNotifier(context.Background(), n)

	if err := NotifyOrchestrator(ctx, "\tмоя вина, чиню\n", domain.WakePayload{WatchID: "watch-1"}); err != nil {
		t.Fatalf("NotifyOrchestrator: %v", err)
	}
	if d.payload.Verdict != ReasonMyFault {
		t.Fatalf("verdict = %q, want %q", d.payload.Verdict, ReasonMyFault)
	}
}

func TestOrchestratorSession(t *testing.T) {
	if got := OrchestratorSession(notifyWatch()); got != "ses_orchestrator" {
		t.Fatalf("OrchestratorSession = %q, want ses_orchestrator", got)
	}
	noOrchestrator := domain.Watch{
		Subscribers: []domain.Subscriber{{ID: "ses_executor", Role: domain.RoleExecutor}},
	}
	if got := OrchestratorSession(noOrchestrator); got != "" {
		t.Fatalf("OrchestratorSession = %q, want empty", got)
	}
}

func TestNotifyOrchestratorRejectsBadInputs(t *testing.T) {
	p := domain.WakePayload{WatchID: "watch-1"}
	t.Run("no notifier in context", func(t *testing.T) {
		if err := NotifyOrchestrator(context.Background(), ReasonMainBroken, p); err == nil {
			t.Fatal("missing notifier must fail")
		}
	})
	t.Run("empty reason", func(t *testing.T) {
		n := &OrchestratorNotifier{Deliverer: &notifyFakeDeliverer{}, Watches: notifyFakeWatches{watch: notifyWatch()}}
		ctx := WithOrchestratorNotifier(context.Background(), n)
		if err := NotifyOrchestrator(ctx, " \n ", p); err == nil {
			t.Fatal("whitespace-only reason must fail")
		}
	})
	t.Run("no ports", func(t *testing.T) {
		ctx := WithOrchestratorNotifier(context.Background(), &OrchestratorNotifier{})
		if err := NotifyOrchestrator(ctx, ReasonMainBroken, p); err == nil {
			t.Fatal("missing ports must fail")
		}
	})
	t.Run("watch read error", func(t *testing.T) {
		n := &OrchestratorNotifier{
			Deliverer: &notifyFakeDeliverer{},
			Watches:   notifyFakeWatches{err: errors.New("store down")},
		}
		ctx := WithOrchestratorNotifier(context.Background(), n)
		err := NotifyOrchestrator(ctx, ReasonMainBroken, p)
		if err == nil || !strings.Contains(err.Error(), "store down") {
			t.Fatalf("err = %v, want wrapped store error", err)
		}
	})
	t.Run("no orchestrator subscriber", func(t *testing.T) {
		watch := domain.Watch{Subscribers: []domain.Subscriber{{ID: "ses_executor", Role: domain.RoleExecutor}}}
		d := &notifyFakeDeliverer{}
		n := &OrchestratorNotifier{Deliverer: d, Watches: notifyFakeWatches{watch: watch}}
		ctx := WithOrchestratorNotifier(context.Background(), n)
		err := NotifyOrchestrator(ctx, ReasonMainBroken, p)
		if err == nil || !strings.Contains(err.Error(), "no orchestrator subscriber") {
			t.Fatalf("err = %v, want missing subscriber error", err)
		}
		if d.calls != 0 {
			t.Fatalf("executor received %d wakes, want 0", d.calls)
		}
	})
	t.Run("empty config fallback", func(t *testing.T) {
		watch := domain.Watch{Subscribers: []domain.Subscriber{{ID: "ses_executor", Role: domain.RoleExecutor}}}
		n := (&OrchestratorNotifier{
			Deliverer: &notifyFakeDeliverer{},
			Watches:   notifyFakeWatches{watch: watch},
		}).WithConfigOrchestrator(func() string { return "  " })
		ctx := WithOrchestratorNotifier(context.Background(), n)
		if err := NotifyOrchestrator(ctx, ReasonMainBroken, p); err == nil {
			t.Fatal("empty config fallback must fail")
		}
	})
}

func TestOrchestratorSessionUsesWatchFieldWithoutSubscriber(t *testing.T) {
	w := domain.Watch{
		Subscribers:    []domain.Subscriber{{ID: "ses_executor", Role: domain.RoleExecutor}},
		OrchestratorID: "ses_parent",
	}
	if got := OrchestratorSession(w); got != "ses_parent" {
		t.Fatalf("OrchestratorSession = %q, want ses_parent", got)
	}
}

func TestNotifyOrchestratorUsesWatchOrchestratorID(t *testing.T) {
	d := &notifyFakeDeliverer{}
	w := domain.Watch{
		ID:             "watch-1",
		Subscribers:    []domain.Subscriber{{ID: "ses_executor", Role: domain.RoleExecutor}},
		OrchestratorID: "ses_parent",
	}
	n := &OrchestratorNotifier{Deliverer: d, Watches: notifyFakeWatches{watch: w}}
	ctx := WithOrchestratorNotifier(context.Background(), n)
	p := domain.WakePayload{WatchID: "watch-1", ObligationID: "obl-1", Revision: 7}

	if err := NotifyOrchestrator(ctx, "  нужен FIX-агент \n", p); err != nil {
		t.Fatalf("NotifyOrchestrator: %v", err)
	}
	if d.calls != 1 || d.sessionID != "ses_parent" {
		t.Fatalf("delivery = %d calls to %q, want 1 to ses_parent", d.calls, d.sessionID)
	}
	if d.payload.Verdict != "нужен FIX-агент" {
		t.Fatalf("reason = %q, want trimmed reason", d.payload.Verdict)
	}
	if d.payload.ObligationID != p.ObligationID || d.payload.Revision != p.Revision {
		t.Fatalf("payload identifiers changed: %+v", d.payload)
	}
}

func TestNotifyOrchestratorUsesConfigFallback(t *testing.T) {
	d := &notifyFakeDeliverer{}
	w := domain.Watch{
		ID:          "watch-1",
		Subscribers: []domain.Subscriber{{ID: "ses_executor", Role: domain.RoleExecutor}},
	}
	n := (&OrchestratorNotifier{
		Deliverer: d,
		Watches:   notifyFakeWatches{watch: w},
	}).WithConfigOrchestrator(func() string { return " ses_config " })
	ctx := WithOrchestratorNotifier(context.Background(), n)

	if err := NotifyOrchestrator(ctx, ReasonMainBroken, domain.WakePayload{WatchID: "watch-1"}); err != nil {
		t.Fatalf("NotifyOrchestrator: %v", err)
	}
	if d.calls != 1 || d.sessionID != "ses_config" {
		t.Fatalf("delivery = %d calls to %q, want 1 to ses_config", d.calls, d.sessionID)
	}
}

func TestNotifyOrchestratorSkipsExecutorNamedByStaleField(t *testing.T) {
	d := &notifyFakeDeliverer{}
	w := domain.Watch{
		ID:             "watch-1",
		Subscribers:    []domain.Subscriber{{ID: "ses_executor", Role: domain.RoleExecutor}},
		OrchestratorID: "ses_executor",
	}
	n := (&OrchestratorNotifier{
		Deliverer: d,
		Watches:   notifyFakeWatches{watch: w},
	}).WithConfigOrchestrator(func() string { return "ses_config" })
	ctx := WithOrchestratorNotifier(context.Background(), n)

	if err := NotifyOrchestrator(ctx, ReasonMainBroken, domain.WakePayload{WatchID: "watch-1"}); err != nil {
		t.Fatalf("NotifyOrchestrator: %v", err)
	}
	if d.calls != 1 || d.sessionID != "ses_config" {
		t.Fatalf("delivery = %d calls to %q, want 1 to ses_config", d.calls, d.sessionID)
	}
}

// The blocked-notice route lives in cibridge, but these tests stay in this file
// because notify_test.go is the only test file owned by this task. They drive
// the exported Refresh so the production handleBlocked path is exercised.

type wakeBlockedStore struct {
	watch       domain.Watch
	obligations map[string]domain.Obligation
}

func newWakeBlockedStore(w domain.Watch) *wakeBlockedStore {
	return &wakeBlockedStore{watch: w, obligations: make(map[string]domain.Obligation)}
}

func (s *wakeBlockedStore) SaveWatch(w domain.Watch) error { s.watch = w; return nil }

func (s *wakeBlockedStore) Watch(id string) (domain.Watch, error) {
	if id != s.watch.ID {
		return domain.Watch{}, errors.New("no such watch")
	}
	return s.watch, nil
}

func (s *wakeBlockedStore) ActiveWatches() ([]domain.Watch, error) { return nil, nil }

func (s *wakeBlockedStore) AllWatches() ([]domain.Watch, error) {
	return []domain.Watch{s.watch}, nil
}

func (s *wakeBlockedStore) SaveObligation(o domain.Obligation) error {
	s.obligations[o.ID] = o
	return nil
}

func (s *wakeBlockedStore) Obligation(id string) (domain.Obligation, error) {
	o, ok := s.obligations[id]
	if !ok {
		return domain.Obligation{}, errors.New("no such obligation")
	}
	return o, nil
}

func (s *wakeBlockedStore) PendingObligations() ([]domain.Obligation, error) { return nil, nil }

type wakeBlockedClock struct{ at time.Time }

func (c wakeBlockedClock) Now() time.Time { return c.at }

type wakeBlockedCI struct{}

func (wakeBlockedCI) PRState(context.Context, string, int) (domain.PRState, error) {
	return domain.PRState{}, errors.New("blocked watch must not poll CI")
}

type wakeBlockedDelivery struct {
	adapter string
	session string
	payload domain.WakePayload
}

type wakeBlockedWaker struct{ deliveries []wakeBlockedDelivery }

func (w *wakeBlockedWaker) Deliver(_ context.Context, adapter, sessionID string, p domain.WakePayload) error {
	w.deliveries = append(w.deliveries, wakeBlockedDelivery{adapter: adapter, session: sessionID, payload: p})
	return nil
}

func wakeBlockedWatch(id, orchestratorID string, subs ...domain.Subscriber) domain.Watch {
	return domain.Watch{
		ID:             id,
		Repo:           "refusedguy/beacon",
		PR:             77,
		HeadSHA:        "abc",
		Status:         domain.StatusBlocked,
		Verdict:        domain.VerdictBlocked,
		Blocked:        &domain.BlockedInfo{Reason: domain.BlockReasonNotMine, FailedChecks: []string{"gates"}, Since: time.Unix(1, 0), BlockedBy: "ses_executor"},
		Revision:       9,
		Subscribers:    subs,
		OrchestratorID: orchestratorID,
	}
}

func wakeExecutorSub() domain.Subscriber {
	return domain.Subscriber{ID: "ses_executor", Role: domain.RoleExecutor, Adapter: "opencode"}
}

func TestCibridgeBlockedWatchSteersWatchOrchestratorID(t *testing.T) {
	w := wakeBlockedWatch("watch-blocked-1", "ses_parent", wakeExecutorSub())
	store := newWakeBlockedStore(w)
	waker := &wakeBlockedWaker{}
	b := cibridge.New(store, wakeBlockedCI{}, wakeBlockedClock{at: time.Unix(2, 0)}, time.Second).WithWaker(waker)
	ctx := context.Background()

	if got := b.Refresh(ctx); got != 0 {
		t.Fatalf("Refresh updated %d watches, want 0", got)
	}
	if len(waker.deliveries) != 1 {
		t.Fatalf("deliveries = %d, want 1: %+v", len(waker.deliveries), waker.deliveries)
	}
	d := waker.deliveries[0]
	if d.session != "ses_parent" {
		t.Fatalf("wake target = %q, want ses_parent", d.session)
	}
	if d.adapter != "opencode" {
		t.Fatalf("adapter = %q, want opencode", d.adapter)
	}
	if d.payload.WatchID != w.ID || d.payload.Revision != w.Revision {
		t.Fatalf("payload = %+v, want watch %q revision %d", d.payload, w.ID, w.Revision)
	}
	wantObligation := domain.ObligationKey(w.ID, w.Revision, "ses_parent")
	if d.payload.ObligationID != wantObligation {
		t.Fatalf("obligation = %q, want %q", d.payload.ObligationID, wantObligation)
	}
	o, err := store.Obligation(wantObligation)
	if err != nil || o.VerdictPointer != domain.VerdictBlocked {
		t.Fatalf("blocked obligation = %+v err %v, want blocked", o, err)
	}
	if !strings.Contains(d.payload.Verdict, w.ID) {
		t.Fatalf("notice = %q, want watch id", d.payload.Verdict)
	}

	// A repeated block tick must not steer the same revision again.
	b.Refresh(ctx)
	if len(waker.deliveries) != 1 {
		t.Fatalf("deliveries after second tick = %d, want 1", len(waker.deliveries))
	}
}

func TestCibridgeBlockedWatchPrefersOrchestratorSubscriber(t *testing.T) {
	w := wakeBlockedWatch("watch-blocked-2", "ses_parent",
		domain.Subscriber{ID: "ses_orchestrator", Role: domain.RoleOrchestrator, Adapter: "opencode"},
		wakeExecutorSub(),
	)
	store := newWakeBlockedStore(w)
	waker := &wakeBlockedWaker{}
	b := cibridge.New(store, wakeBlockedCI{}, wakeBlockedClock{at: time.Unix(2, 0)}, time.Second).WithWaker(waker)

	b.Refresh(context.Background())
	if len(waker.deliveries) != 1 || waker.deliveries[0].session != "ses_orchestrator" {
		t.Fatalf("deliveries = %+v, want one to ses_orchestrator", waker.deliveries)
	}
}

func TestCibridgeBlockedWatchUsesConfigFallback(t *testing.T) {
	w := wakeBlockedWatch("watch-blocked-3", "", wakeExecutorSub())
	store := newWakeBlockedStore(w)
	waker := &wakeBlockedWaker{}
	b := cibridge.New(store, wakeBlockedCI{}, wakeBlockedClock{at: time.Unix(2, 0)}, time.Second).
		WithWaker(waker).
		WithOrchestratorIDSource(func() string { return "ses_config" })
	ctx := context.Background()

	b.Refresh(ctx)
	if len(waker.deliveries) != 1 || waker.deliveries[0].session != "ses_config" {
		t.Fatalf("deliveries = %+v, want one to ses_config", waker.deliveries)
	}
	b.Refresh(ctx)
	if len(waker.deliveries) != 1 {
		t.Fatalf("deliveries after second tick = %d, want 1", len(waker.deliveries))
	}
}

func TestCibridgeBlockedWatchWithoutOrchestratorAddressIsSilent(t *testing.T) {
	w := wakeBlockedWatch("watch-blocked-4", "", wakeExecutorSub())
	store := newWakeBlockedStore(w)
	waker := &wakeBlockedWaker{}
	b := cibridge.New(store, wakeBlockedCI{}, wakeBlockedClock{at: time.Unix(2, 0)}, time.Second).WithWaker(waker)

	b.Refresh(context.Background())
	if len(waker.deliveries) != 0 {
		t.Fatalf("deliveries = %+v, want none when no orchestrator is named", waker.deliveries)
	}
	if _, err := store.Obligation(domain.ObligationKey(w.ID, w.Revision, "ses_executor")); err != nil {
		t.Fatalf("executor obligation was not staged: %v", err)
	}
}
