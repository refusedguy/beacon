package main

import (
	"context"
	"errors"
	"testing"

	"github.com/refusedguy/beacon/internal/app/notify"
	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

// notifyDelivery records one call into the wake target port.
type notifyDelivery struct {
	target  string
	payload string
}

// notifyTargetHarness stands in for wake.TargetDeliverer and records calls.
type notifyTargetHarness struct {
	calls []notifyDelivery
	err   error
}

func (h *notifyTargetHarness) deliver(_ context.Context, target, payload string) error {
	h.calls = append(h.calls, notifyDelivery{target: target, payload: payload})
	return h.err
}

func TestDaemonNotifyRouterDeliversToKnownRecipient(t *testing.T) {
	h := &notifyTargetHarness{}
	router := newDaemonNotifyRouter("ses_owner", h.deliver)

	report, err := router.Route(context.Background(), notify.Event{
		Type:      "retry",
		Recipient: "ses_owner",
		Payload:   []byte("verdict=retry watchID=w1"),
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if report.Delivered != 1 || report.NoRoute || len(report.Failures) != 0 {
		t.Fatalf("report = %+v, want one delivery", report)
	}
	if len(h.calls) != 1 {
		t.Fatalf("delivery calls = %d, want 1", len(h.calls))
	}
	if h.calls[0].target != "ses_owner" {
		t.Fatalf("target = %q, want ses_owner", h.calls[0].target)
	}
	if h.calls[0].payload != "verdict=retry watchID=w1" {
		t.Fatalf("payload = %q", h.calls[0].payload)
	}
}

func TestDaemonNotifyRouterHasNoRouteForUnknownRecipient(t *testing.T) {
	h := &notifyTargetHarness{}
	router := newDaemonNotifyRouter("ses_owner", h.deliver)

	report, err := router.Route(context.Background(), notify.Event{
		Type:      "retry",
		Recipient: "ses_other",
		Payload:   []byte("x"),
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !report.NoRoute || report.Delivered != 0 {
		t.Fatalf("report = %+v, want no route", report)
	}
	if len(h.calls) != 0 {
		t.Fatalf("delivery calls = %d, want 0", len(h.calls))
	}
}

func TestDaemonNotifyRouterHasNoRouteForServiceSubscriber(t *testing.T) {
	h := &notifyTargetHarness{}
	router := newDaemonNotifyRouter("beacon-daemon-checkall", h.deliver)

	report, err := router.Route(context.Background(), notify.Event{
		Type:      "retry",
		Recipient: "beacon-daemon-checkall",
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !report.NoRoute || report.Delivered != 0 || len(h.calls) != 0 {
		t.Fatalf("report = %+v, calls = %d, want no route and no delivery", report, len(h.calls))
	}
}

func TestDaemonNotifyResolverAcceptsOnlyTheWatchSubscriber(t *testing.T) {
	r := daemonNotifyResolver{subscriberID: "ses_owner"}
	targets, err := r.Resolve(context.Background(), notify.Key{EventType: "retry", Recipient: "ses_owner"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(targets) != 1 || targets[0] != notify.Target("ses_owner") {
		t.Fatalf("targets = %v, want ses_owner", targets)
	}
	targets, err = r.Resolve(context.Background(), notify.Key{EventType: "retry", Recipient: "ses_intruder"})
	if err != nil || len(targets) != 0 {
		t.Fatalf("Resolve intruder = %v, %v, want no targets", targets, err)
	}
}

func TestRouteDaemonEventWithMockPorts(t *testing.T) {
	wantKey := notify.Key{EventType: "rebase", Recipient: "ses_owner"}
	var seen []notify.Notification
	resolver := notify.ResolverFunc(func(_ context.Context, key notify.Key) ([]notify.Target, error) {
		if key != wantKey {
			t.Fatalf("resolver key = %+v, want %+v", key, wantKey)
		}
		return []notify.Target{"ses_owner"}, nil
	})
	deliverer := notify.DelivererFunc(func(_ context.Context, n notify.Notification) error {
		seen = append(seen, n)
		return nil
	})
	router := notify.New(resolver, deliverer)

	if err := routeDaemonEvent(context.Background(), router, "rebase", "ses_owner", "pointer"); err != nil {
		t.Fatalf("routeDaemonEvent: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("notifications = %d, want 1", len(seen))
	}
	if seen[0].Target != "ses_owner" || string(seen[0].Payload) != "pointer" {
		t.Fatalf("notification = %+v", seen[0])
	}
}

func TestRouteDaemonEventReportsDeliveryFailure(t *testing.T) {
	boom := errors.New("prompt refused")
	router := notify.New(
		notify.ResolverFunc(func(context.Context, notify.Key) ([]notify.Target, error) {
			return []notify.Target{"ses_owner"}, nil
		}),
		notify.DelivererFunc(func(context.Context, notify.Notification) error { return boom }),
	)
	err := routeDaemonEvent(context.Background(), router, "retry", "ses_owner", "pointer")
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
}

func TestDaemonWakeRouteBuildsRouterPerWatch(t *testing.T) {
	h := &notifyTargetHarness{}
	route := daemonWakeRoute(h.deliver)
	w := orchestrator.DaemonWatch{ID: "w1", PR: 42, Adapter: wake.AdapterOpenCode, SubscriberID: "ses_route"}
	if err := route(context.Background(), w, 2, "retry", "pointer"); err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(h.calls) != 1 || h.calls[0].target != "ses_route" || h.calls[0].payload != "pointer" {
		t.Fatalf("delivery calls = %+v, want one pointer to ses_route", h.calls)
	}
}

var _ wake.TargetDeliverer = (&notifyTargetHarness{}).deliver
