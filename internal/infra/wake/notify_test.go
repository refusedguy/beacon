package wake

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
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
		n := &OrchestratorNotifier{Deliverer: &notifyFakeDeliverer{}, Watches: notifyFakeWatches{watch: watch}}
		ctx := WithOrchestratorNotifier(context.Background(), n)
		err := NotifyOrchestrator(ctx, ReasonMainBroken, p)
		if err == nil || !strings.Contains(err.Error(), "no orchestrator subscriber") {
			t.Fatalf("err = %v, want missing subscriber error", err)
		}
	})
}
