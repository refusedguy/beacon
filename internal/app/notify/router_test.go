package notify

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type stubResolver struct {
	targets []Target
	err     error
	calls   []Key
}

func (s *stubResolver) Resolve(_ context.Context, key Key) ([]Target, error) {
	s.calls = append(s.calls, key)
	return s.targets, s.err
}

type recordingDeliverer struct {
	calls       []Notification
	errByTarget map[Target]error
}

func (d *recordingDeliverer) Deliver(_ context.Context, n Notification) error {
	d.calls = append(d.calls, n)
	return d.errByTarget[n.Target]
}

func TestRouteFound(t *testing.T) {
	key := Key{EventType: "pr.ready", Recipient: "ses_caller"}
	resolver := &stubResolver{targets: []Target{"ses_caller"}}
	deliverer := &recordingDeliverer{}

	report, err := New(resolver, deliverer).Route(context.Background(), Event{
		Type:      key.EventType,
		Recipient: key.Recipient,
		Payload:   []byte("payload"),
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if report.NoRoute || report.Delivered != 1 || len(report.Failures) != 0 {
		t.Fatalf("report = %+v, want one delivery", report)
	}
	if len(resolver.calls) != 1 || resolver.calls[0] != key {
		t.Fatalf("resolver calls = %+v, want %+v", resolver.calls, key)
	}
	if len(deliverer.calls) != 1 {
		t.Fatalf("deliverer calls = %d, want 1", len(deliverer.calls))
	}
	got := deliverer.calls[0]
	if got.Key != key || got.Target != "ses_caller" || string(got.Payload) != "payload" {
		t.Fatalf("notification = %+v, want key %+v, target ses_caller and payload", got, key)
	}
}

func TestRouteNoRoute(t *testing.T) {
	resolver := &stubResolver{}
	deliverer := &recordingDeliverer{}

	report, err := New(resolver, deliverer).Route(context.Background(), Event{
		Type:      "pr.ready",
		Recipient: "ses_nobody",
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !report.NoRoute || report.Delivered != 0 || len(report.Failures) != 0 {
		t.Fatalf("report = %+v, want no route", report)
	}
	if len(deliverer.calls) != 0 {
		t.Fatalf("deliverer calls = %d, want 0", len(deliverer.calls))
	}
}

func TestRouteMultipleTargets(t *testing.T) {
	targets := []Target{"ses_one", "ses_two", "ses_three"}
	resolver := &stubResolver{targets: targets}
	deliverer := &recordingDeliverer{}

	report, err := New(resolver, deliverer).Route(context.Background(), Event{
		Type:      "watch.result",
		Recipient: "ses_owner",
		Payload:   []byte("x"),
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if report.NoRoute || report.Delivered != 3 || len(report.Failures) != 0 {
		t.Fatalf("report = %+v, want three deliveries", report)
	}
	var got []Target
	for _, n := range deliverer.calls {
		got = append(got, n.Target)
		if string(n.Payload) != "x" {
			t.Fatalf("notification payload = %q, want x", n.Payload)
		}
	}
	if !reflect.DeepEqual(got, targets) {
		t.Fatalf("delivery order = %v, want %v", got, targets)
	}
}

func TestRouteDeliveryFailure(t *testing.T) {
	boom := errors.New("delivery failed")
	targets := []Target{"ses_good", "ses_bad", "ses_also_good"}
	resolver := &stubResolver{targets: targets}
	deliverer := &recordingDeliverer{errByTarget: map[Target]error{"ses_bad": boom}}

	report, err := New(resolver, deliverer).Route(context.Background(), Event{Type: "watch.result"})
	if err != nil {
		t.Fatalf("Route returned a function error for a delivery failure: %v", err)
	}
	if report.NoRoute || report.Delivered != 2 || len(report.Failures) != 1 {
		t.Fatalf("report = %+v, want two deliveries and one failure", report)
	}
	f := report.Failures[0]
	if f.Target != "ses_bad" || !errors.Is(f.Err, boom) {
		t.Fatalf("failure = %+v, want ses_bad and %v", f, boom)
	}
	if len(deliverer.calls) != 3 {
		t.Fatalf("deliverer calls = %d, want 3 (delivery continues after failure)", len(deliverer.calls))
	}
}

func TestRouteResolverErrorStopsDelivery(t *testing.T) {
	boom := errors.New("resolver failed")
	resolver := &stubResolver{err: boom}
	deliverer := &recordingDeliverer{}

	report, err := New(resolver, deliverer).Route(context.Background(), Event{Type: "watch.result"})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
	if report.Delivered != 0 || report.NoRoute || len(report.Failures) != 0 {
		t.Fatalf("report = %+v, want zero", report)
	}
	if len(deliverer.calls) != 0 {
		t.Fatalf("deliverer calls = %d, want 0", len(deliverer.calls))
	}
}

func TestRouteRejectsNilPorts(t *testing.T) {
	deliverer := &recordingDeliverer{}
	if _, err := New(nil, deliverer).Route(context.Background(), Event{}); !errors.Is(err, ErrNilResolver) {
		t.Fatalf("nil resolver error = %v, want %v", err, ErrNilResolver)
	}
	resolver := &stubResolver{}
	if _, err := New(resolver, nil).Route(context.Background(), Event{}); !errors.Is(err, ErrNilDeliverer) {
		t.Fatalf("nil deliverer error = %v, want %v", err, ErrNilDeliverer)
	}
	var r *Router
	if _, err := r.Route(context.Background(), Event{}); !errors.Is(err, ErrNilResolver) {
		t.Fatalf("nil router error = %v, want %v", err, ErrNilResolver)
	}
}

func TestEventKey(t *testing.T) {
	e := Event{Type: "pr.ready", Recipient: "ses_owner"}
	want := Key{EventType: "pr.ready", Recipient: "ses_owner"}
	if got := e.Key(); got != want {
		t.Fatalf("Key() = %+v, want %+v", got, want)
	}
}

func TestRouteTableAddAndResolve(t *testing.T) {
	table := NewRouteTable()
	a := Key{EventType: "pr.ready", Recipient: "ses_one"}
	b := Key{EventType: "pr.ready", Recipient: "ses_two"}
	table.Add(a, "ses_one", "ses_backup")
	table.Add(b, "ses_two")
	table.Add(a, "ses_extra")

	for _, tc := range []struct {
		key  Key
		want []Target
	}{
		{a, []Target{"ses_one", "ses_backup", "ses_extra"}},
		{b, []Target{"ses_two"}},
		{Key{EventType: "pr.ready", Recipient: "ses_missing"}, nil},
	} {
		got, err := table.Resolve(context.Background(), tc.key)
		if err != nil {
			t.Fatalf("Resolve(%+v): %v", tc.key, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("Resolve(%+v) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestRouteTableReturnsCopy(t *testing.T) {
	table := NewRouteTable()
	key := Key{EventType: "pr.ready", Recipient: "ses_one"}
	table.Add(key, "ses_one")

	got, err := table.Resolve(context.Background(), key)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got[0] = "tampered"

	again, err := table.Resolve(context.Background(), key)
	if err != nil {
		t.Fatalf("Resolve again: %v", err)
	}
	if again[0] != "ses_one" {
		t.Fatalf("stored target = %q, want ses_one", again[0])
	}
}

func TestRouterWithRouteTable(t *testing.T) {
	table := NewRouteTable()
	key := Key{EventType: "pr.ready", Recipient: "ses_caller"}
	table.Add(key, "ses_caller")

	deliverer := &recordingDeliverer{}
	report, err := New(table, deliverer).Route(context.Background(), Event{
		Type:      key.EventType,
		Recipient: key.Recipient,
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if report.Delivered != 1 || len(deliverer.calls) != 1 || deliverer.calls[0].Target != "ses_caller" {
		t.Fatalf("report = %+v, calls = %+v, want one delivery to ses_caller", report, deliverer.calls)
	}
}

func TestPortFuncAdapters(t *testing.T) {
	key := Key{EventType: "pr.ready", Recipient: "ses_caller"}
	resolver := ResolverFunc(func(_ context.Context, k Key) ([]Target, error) {
		if k != key {
			t.Fatalf("resolver key = %+v, want %+v", k, key)
		}
		return []Target{"ses_caller"}, nil
	})
	delivered := false
	deliverer := DelivererFunc(func(_ context.Context, n Notification) error {
		delivered = n.Target == "ses_caller"
		return nil
	})

	report, err := New(resolver, deliverer).Route(context.Background(), Event{
		Type:      key.EventType,
		Recipient: key.Recipient,
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if report.Delivered != 1 || !delivered {
		t.Fatalf("report = %+v, delivered = %v, want one delivery", report, delivered)
	}
}
