package tasks

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakePort is a mock Port. Each field overrides one method; a nil field returns
// a harmless default. Tests drive status and events through closures.
type fakePort struct {
	card        func(ctx context.Context, id string) (Card, bool, error)
	eventsAfter func(ctx context.Context, id string, cursor uint64, limit int) ([]RecordedEvent, error)
	serialize   func(change Change) ([]byte, error)
}

func (f *fakePort) Card(ctx context.Context, id string) (Card, bool, error) {
	if f.card != nil {
		return f.card(ctx, id)
	}
	return Card{}, false, nil
}

func (f *fakePort) EventsAfter(ctx context.Context, id string, cursor uint64, limit int) ([]RecordedEvent, error) {
	if f.eventsAfter != nil {
		return f.eventsAfter(ctx, id, cursor, limit)
	}
	return nil, nil
}

func (f *fakePort) Serialize(change Change) ([]byte, error) {
	if f.serialize != nil {
		return f.serialize(change)
	}
	return []byte("payload"), nil
}

func TestMonitorStatusChangeEmitsOnce(t *testing.T) {
	status := "todo"
	port := &fakePort{
		card: func(_ context.Context, id string) (Card, bool, error) {
			return Card{ID: id, Status: status}, true, nil
		},
		serialize: func(c Change) ([]byte, error) {
			return []byte(c.From + "->" + c.To), nil
		},
	}
	m := NewMonitor(port)
	if err := m.Subscribe("BS.53"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	base, err := m.Poll(context.Background(), "BS.53")
	if err != nil {
		t.Fatalf("baseline Poll: %v", err)
	}
	if len(base.Changes) != 0 {
		t.Fatalf("baseline changes = %+v, want none", base.Changes)
	}

	status = "done"
	first, err := m.Poll(context.Background(), "BS.53")
	if err != nil {
		t.Fatalf("first Poll: %v", err)
	}
	if len(first.Changes) != 1 {
		t.Fatalf("changes = %+v, want exactly one", first.Changes)
	}
	c := first.Changes[0]
	if c.CardID != "BS.53" || c.From != "todo" || c.To != "done" || c.Type != EventTypeStatusChanged {
		t.Fatalf("change = %+v, want BS.53 todo->done %s", c, EventTypeStatusChanged)
	}
	if string(c.Payload) != "todo->done" {
		t.Fatalf("payload = %q, want %q", c.Payload, "todo->done")
	}
	if c.Seq == 0 {
		t.Fatal("change seq must be positive")
	}

	second, err := m.Poll(context.Background(), "BS.53")
	if err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	if len(second.Changes) != 0 {
		t.Fatalf("repeat Poll changes = %+v, want none", second.Changes)
	}
}

func TestMonitorNewEvent(t *testing.T) {
	page := []RecordedEvent{{Seq: 1, Type: "card.updated"}, {Seq: 2, Type: "card.status_changed"}}
	port := &fakePort{
		card: func(_ context.Context, id string) (Card, bool, error) {
			return Card{ID: id, Status: "todo"}, true, nil
		},
		eventsAfter: func(_ context.Context, _ string, cursor uint64, _ int) ([]RecordedEvent, error) {
			var out []RecordedEvent
			for _, ev := range page {
				if ev.Seq > cursor {
					out = append(out, ev)
				}
			}
			return out, nil
		},
	}
	m := NewMonitor(port)
	if err := m.Subscribe("BS.53"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	first, err := m.Poll(context.Background(), "BS.53")
	if err != nil {
		t.Fatalf("first Poll: %v", err)
	}
	if !reflect.DeepEqual(first.Events, page) {
		t.Fatalf("events = %+v, want %+v", first.Events, page)
	}

	second, err := m.Poll(context.Background(), "BS.53")
	if err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	if len(second.Events) != 0 {
		t.Fatalf("events after cursor = %+v, want none", second.Events)
	}

	page = append(page, RecordedEvent{Seq: 3, Type: "card.closed"})
	third, err := m.Poll(context.Background(), "BS.53")
	if err != nil {
		t.Fatalf("third Poll: %v", err)
	}
	if len(third.Events) != 1 || third.Events[0].Seq != 3 {
		t.Fatalf("new events = %+v, want only seq 3", third.Events)
	}
}

func TestMonitorStaleEventsIgnored(t *testing.T) {
	port := &fakePort{
		card: func(_ context.Context, id string) (Card, bool, error) {
			return Card{ID: id, Status: "todo"}, true, nil
		},
		// The port returns a stale row on every call. The monitor must not
		// surface it twice or move the cursor backwards.
		eventsAfter: func(_ context.Context, _ string, _ uint64, _ int) ([]RecordedEvent, error) {
			return []RecordedEvent{{Seq: 1, Type: "card.updated"}}, nil
		},
	}
	m := NewMonitor(port)
	if err := m.Subscribe("BS.53"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	for i := 0; i < 3; i++ {
		res, err := m.Poll(context.Background(), "BS.53")
		if err != nil {
			t.Fatalf("Poll %d: %v", i, err)
		}
		if i == 0 && len(res.Events) != 1 {
			t.Fatalf("first events = %+v, want one", res.Events)
		}
		if i > 0 && len(res.Events) != 0 {
			t.Fatalf("poll %d events = %+v, want none", i, res.Events)
		}
	}
}

func TestMonitorEmptyState(t *testing.T) {
	found := false
	status := ""
	port := &fakePort{
		card: func(_ context.Context, id string) (Card, bool, error) {
			return Card{ID: id, Status: status}, found, nil
		},
	}
	m := NewMonitor(port)
	if err := m.Subscribe("BS.53"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	empty, err := m.Poll(context.Background(), "BS.53")
	if err != nil {
		t.Fatalf("Poll on absent card: %v", err)
	}
	if len(empty.Changes) != 0 || len(empty.Events) != 0 {
		t.Fatalf("absent card result = %+v, want empty", empty)
	}

	// Absence must not set a baseline. The first observation of a real card
	// still only records status and emits no change.
	found = true
	status = "todo"
	first, err := m.Poll(context.Background(), "BS.53")
	if err != nil {
		t.Fatalf("Poll on new card: %v", err)
	}
	if len(first.Changes) != 0 {
		t.Fatalf("first observation changes = %+v, want none", first.Changes)
	}
}

func TestMonitorCardErrorKeepsState(t *testing.T) {
	boom := errors.New("store down")
	fail := false
	status := "todo"
	port := &fakePort{
		card: func(_ context.Context, id string) (Card, bool, error) {
			if fail {
				return Card{}, false, boom
			}
			return Card{ID: id, Status: status}, true, nil
		},
	}
	m := NewMonitor(port)
	if err := m.Subscribe("BS.53"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := m.Poll(context.Background(), "BS.53"); err != nil {
		t.Fatalf("baseline Poll: %v", err)
	}

	fail = true
	if _, err := m.Poll(context.Background(), "BS.53"); !errors.Is(err, boom) {
		t.Fatalf("Poll error = %v, want wrapped %v", err, boom)
	}

	fail = false
	status = "done"
	next, err := m.Poll(context.Background(), "BS.53")
	if err != nil {
		t.Fatalf("Poll after error: %v", err)
	}
	if len(next.Changes) != 1 || next.Changes[0].From != "todo" || next.Changes[0].To != "done" {
		t.Fatalf("changes after error = %+v, want one todo->done", next.Changes)
	}
}

func TestMonitorEventsErrorKeepsState(t *testing.T) {
	boom := errors.New("reader down")
	fail := false
	status := "todo"
	port := &fakePort{
		card: func(_ context.Context, id string) (Card, bool, error) {
			return Card{ID: id, Status: status}, true, nil
		},
		eventsAfter: func(_ context.Context, _ string, _ uint64, _ int) ([]RecordedEvent, error) {
			if fail {
				return nil, boom
			}
			return nil, nil
		},
	}
	m := NewMonitor(port)
	if err := m.Subscribe("BS.53"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := m.Poll(context.Background(), "BS.53"); err != nil {
		t.Fatalf("baseline Poll: %v", err)
	}

	fail = true
	if _, err := m.Poll(context.Background(), "BS.53"); !errors.Is(err, boom) {
		t.Fatalf("Poll error = %v, want wrapped %v", err, boom)
	}

	fail = false
	status = "done"
	next, err := m.Poll(context.Background(), "BS.53")
	if err != nil {
		t.Fatalf("Poll after error: %v", err)
	}
	if len(next.Changes) != 1 {
		t.Fatalf("changes after error = %+v, want one", next.Changes)
	}
}

func TestMonitorSerializeErrorKeepsState(t *testing.T) {
	boom := errors.New("encoder down")
	fail := false
	status := "todo"
	port := &fakePort{
		card: func(_ context.Context, id string) (Card, bool, error) {
			return Card{ID: id, Status: status}, true, nil
		},
		serialize: func(_ Change) ([]byte, error) {
			if fail {
				return nil, boom
			}
			return []byte("ok"), nil
		},
	}
	m := NewMonitor(port)
	if err := m.Subscribe("BS.53"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := m.Poll(context.Background(), "BS.53"); err != nil {
		t.Fatalf("baseline Poll: %v", err)
	}

	status = "done"
	fail = true
	if _, err := m.Poll(context.Background(), "BS.53"); !errors.Is(err, boom) {
		t.Fatalf("Poll error = %v, want wrapped %v", err, boom)
	}

	fail = false
	next, err := m.Poll(context.Background(), "BS.53")
	if err != nil {
		t.Fatalf("Poll after error: %v", err)
	}
	if len(next.Changes) != 1 {
		t.Fatalf("changes after error = %+v, want one", next.Changes)
	}
	if next.Changes[0].From != "todo" {
		t.Fatalf("change From = %q, want todo after failed serialize", next.Changes[0].From)
	}
}

func TestMonitorSubscribeIdempotent(t *testing.T) {
	m := NewMonitor(&fakePort{})
	for i := 0; i < 3; i++ {
		if err := m.Subscribe("BS.53"); err != nil {
			t.Fatalf("Subscribe %d: %v", i, err)
		}
	}
	if m.SubscriptionCount() != 1 {
		t.Fatalf("subscription count = %d, want 1", m.SubscriptionCount())
	}
	if !m.Subscribed("BS.53") {
		t.Fatal("BS.53 must be subscribed")
	}
	if err := m.Subscribe("  "); !errors.Is(err, ErrEmptyCardID) {
		t.Fatalf("Subscribe blank = %v, want %v", err, ErrEmptyCardID)
	}

	m.Unsubscribe("BS.53")
	if m.Subscribed("BS.53") || m.SubscriptionCount() != 0 {
		t.Fatal("Unsubscribe must remove the card")
	}
}

func TestMonitorNotSubscribed(t *testing.T) {
	m := NewMonitor(&fakePort{})
	if _, err := m.Poll(context.Background(), "BS.53"); !errors.Is(err, ErrNotSubscribed) {
		t.Fatalf("Poll error = %v, want %v", err, ErrNotSubscribed)
	}
	if _, err := m.Poll(context.Background(), " "); !errors.Is(err, ErrEmptyCardID) {
		t.Fatalf("Poll blank error = %v, want %v", err, ErrEmptyCardID)
	}
}

func TestMonitorNilPort(t *testing.T) {
	m := NewMonitor(nil)
	if err := m.Subscribe("BS.53"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := m.Poll(context.Background(), "BS.53"); err == nil {
		t.Fatal("Poll with nil port must report an error")
	}
}

func TestMonitorEventLimit(t *testing.T) {
	var gotLimit int
	port := &fakePort{
		eventsAfter: func(_ context.Context, _ string, _ uint64, limit int) ([]RecordedEvent, error) {
			gotLimit = limit
			return nil, nil
		},
	}
	m := NewMonitorWithLimit(port, 7)
	if err := m.Subscribe("BS.53"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := m.Poll(context.Background(), "BS.53"); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if gotLimit != 7 {
		t.Fatalf("event limit = %d, want 7", gotLimit)
	}
}
