package wake

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// sourceStub is a StateSource built from two functions. Each test supplies only
// the behaviour it cares about.
type sourceStub struct {
	list  func(ctx context.Context) (Snapshot, error)
	watch func(ctx context.Context, cursor string) (Event, error)
}

func (s sourceStub) List(ctx context.Context) (Snapshot, error) {
	if s.list == nil {
		return Snapshot{}, nil
	}
	return s.list(ctx)
}

func (s sourceStub) Watch(ctx context.Context, cursor string) (Event, error) {
	if s.watch == nil {
		return Event{}, nil
	}
	return s.watch(ctx, cursor)
}

func fixedList(snap Snapshot) func(context.Context) (Snapshot, error) {
	return func(context.Context) (Snapshot, error) { return snap, nil }
}

func TestMissedEventRecoveredByFullRead(t *testing.T) {
	src := sourceStub{list: fixedList(Snapshot{
		Revision: 1,
		Cursor:   "c1",
		Obligations: []Obligation{
			{ID: "ob-1", Revision: 1},
		},
	})}
	d := NewDeliverer(src, nil)

	// No event is ever delivered. The full read alone discovers the obligation.
	n, applied, err := d.Reconcile(context.Background())
	if err != nil || !applied || n != 1 {
		t.Fatalf("Reconcile = (%d, %v, %v), want (1, true, nil)", n, applied, err)
	}
	if !d.Has("ob-1") {
		t.Fatal("full read missed obligation ob-1")
	}
}

func TestRepeatedEventDoesNotChangeState(t *testing.T) {
	src := sourceStub{list: fixedList(Snapshot{
		Revision:    1,
		Obligations: []Obligation{{ID: "ob-1", Revision: 1}},
	})}
	d := NewDeliverer(src, nil)
	ev := Event{ID: "e1", Seq: 1}

	if !d.Apply(ev) {
		t.Fatal("first Apply should accept the event")
	}
	if d.Apply(ev) {
		t.Fatal("second Apply of the same id must be a no-op")
	}
	if _, _, dirty := d.State(); !dirty {
		t.Fatal("a fresh event should set the dirty signal")
	}

	if _, _, err := d.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, dirty := d.State(); dirty {
		t.Fatal("Reconcile should clear the dirty signal")
	}

	// Replaying the event and re-listing the same revision changes nothing.
	if d.Apply(ev) {
		t.Fatal("replayed event must be dropped")
	}
	_, applied, _ := d.Reconcile(context.Background())
	if applied {
		t.Fatal("a snapshot with an unchanged revision must not be applied")
	}
	if got := d.Relists(); got != 1 {
		t.Fatalf("Relists = %d, want 1", got)
	}
	if ob := d.Obligations(); len(ob) != 1 || ob[0].ID != "ob-1" {
		t.Fatalf("state changed after replay: %+v", ob)
	}
}

func TestOldSeqDiscarded(t *testing.T) {
	d := NewDeliverer(sourceStub{}, nil)
	if !d.Apply(Event{ID: "e10", Seq: 10}) {
		t.Fatal("seq 10 should be accepted")
	}
	if d.Apply(Event{ID: "e5", Seq: 5}) {
		t.Fatal("older seq must be discarded")
	}
	if d.Apply(Event{ID: "e10b", Seq: 10}) {
		t.Fatal("equal seq must be discarded")
	}
	if !d.Apply(Event{ID: "e11", Seq: 11}) {
		t.Fatal("newer seq should be accepted")
	}
	if _, _, dirty := d.State(); !dirty {
		t.Fatal("accepted hints should keep the dirty signal set")
	}
}

func TestOldRevisionDiscarded(t *testing.T) {
	snaps := []Snapshot{
		{Revision: 5, Obligations: []Obligation{{ID: "A", Revision: 5}}},
		{Revision: 3, Obligations: []Obligation{{ID: "B", Revision: 3}}},
	}
	i := 0
	src := sourceStub{list: func(context.Context) (Snapshot, error) {
		s := snaps[i]
		i++
		return s, nil
	}}
	d := NewDeliverer(src, nil)

	if _, applied, _ := d.Reconcile(context.Background()); !applied {
		t.Fatal("first snapshot should be applied")
	}
	if _, applied, _ := d.Reconcile(context.Background()); applied {
		t.Fatal("older revision must be discarded")
	}
	if !d.Has("A") || d.Has("B") {
		t.Fatalf("old revision rolled state back: A=%v B=%v", d.Has("A"), d.Has("B"))
	}
}

func TestGoneTriggersRelist(t *testing.T) {
	src := sourceStub{
		list: fixedList(Snapshot{
			Revision:    1,
			Cursor:      "fresh-cursor",
			Obligations: []Obligation{{ID: "ob-1", Revision: 1}},
		}),
		watch: func(context.Context, string) (Event, error) {
			return Event{}, &GoneError{Status: httpStatusGone}
		},
	}
	d := NewDeliverer(src, nil)

	_, relisted, err := d.WatchOnce(context.Background())
	if err != nil || !relisted {
		t.Fatalf("WatchOnce = (_, %v, %v), want (true, nil)", relisted, err)
	}
	if !d.Has("ob-1") {
		t.Fatal("410 Gone did not recover the obligation via re-list")
	}
	if _, cursor, _ := d.State(); cursor != "fresh-cursor" {
		t.Fatalf("cursor = %q, want fresh-cursor after re-list", cursor)
	}
	if got := d.GoneRelists(); got != 1 {
		t.Fatalf("GoneRelists = %d, want 1", got)
	}
}

func TestCursorExpiredTriggersRelist(t *testing.T) {
	src := sourceStub{
		list: fixedList(Snapshot{
			Revision:    1,
			Cursor:      "c2",
			Obligations: []Obligation{{ID: "ob-2", Revision: 1}},
		}),
		watch: func(context.Context, string) (Event, error) {
			return Event{}, &GoneError{Code: CursorExpired}
		},
	}
	d := NewDeliverer(src, nil)
	if _, relisted, err := d.WatchOnce(context.Background()); err != nil || !relisted {
		t.Fatalf("WatchOnce = (_, %v, %v), want (true, nil)", relisted, err)
	}
	if !d.Has("ob-2") {
		t.Fatal("cursor_expired did not recover the obligation via re-list")
	}
}

func TestNonGoneWatchErrorDoesNotRelist(t *testing.T) {
	src := sourceStub{
		watch: func(context.Context, string) (Event, error) {
			return Event{}, errors.New("network down")
		},
	}
	d := NewDeliverer(src, nil)
	if _, relisted, err := d.WatchOnce(context.Background()); err == nil || relisted {
		t.Fatalf("WatchOnce = (_, %v, %v), want (false, error)", relisted, err)
	}
	if got := d.Relists(); got != 0 {
		t.Fatalf("Relists = %d, want 0 for a non-gone error", got)
	}
}

func TestCursorNotPersistedAcrossProcesses(t *testing.T) {
	src := sourceStub{list: fixedList(Snapshot{
		Revision:    7,
		Cursor:      "c7",
		Obligations: []Obligation{{ID: "ob-7", Revision: 7}},
	})}
	d := NewDeliverer(src, nil)
	if rev, cursor, _ := d.State(); rev != 0 || cursor != "" {
		t.Fatalf("fresh process state = (%d, %q), want (0, empty)", rev, cursor)
	}
	_, _, _ = d.Reconcile(context.Background())
	if _, cursor, _ := d.State(); cursor != "c7" {
		t.Fatalf("cursor = %q, want c7", cursor)
	}

	// A new Deliverer models a restarted process: no persisted cursor, no state
	// until it performs its own full list.
	restarted := NewDeliverer(src, nil)
	if rev, cursor, _ := restarted.State(); rev != 0 || cursor != "" {
		t.Fatalf("restarted process kept cursor/state: (%d, %q)", rev, cursor)
	}
	if restarted.Has("ob-7") {
		t.Fatal("restarted process must not inherit obligations")
	}
}

func TestWatchUsesProcessLocalCursor(t *testing.T) {
	var seen string
	src := sourceStub{
		list: fixedList(Snapshot{Revision: 1, Cursor: "c1"}),
		watch: func(_ context.Context, cursor string) (Event, error) {
			seen = cursor
			return Event{}, errors.New("stop")
		},
	}
	d := NewDeliverer(src, nil)
	_, _, _ = d.Reconcile(context.Background())
	_, _, _ = d.WatchOnce(context.Background())
	if seen != "c1" {
		t.Fatalf("Watch cursor = %q, want c1 from the full read", seen)
	}
}

func TestLostWebhookKeepsObligation(t *testing.T) {
	src := sourceStub{
		list: fixedList(Snapshot{
			Revision:    1,
			Obligations: []Obligation{{ID: "ob-1", Revision: 1}},
		}),
		watch: func(context.Context, string) (Event, error) {
			return Event{}, errors.New("webhook never arrived")
		},
	}
	d := NewDeliverer(src, nil)
	_, _, _ = d.Reconcile(context.Background())
	if !d.Has("ob-1") {
		t.Fatal("obligation missing after full read")
	}
	// The stream never delivers anything, yet the obligation survives both a
	// failed watch and a later no-op reconcile.
	_, _, _ = d.WatchOnce(context.Background())
	_, _, _ = d.Reconcile(context.Background())
	if !d.Has("ob-1") {
		t.Fatal("lost webhook lost the obligation")
	}
}

func TestEventHintAppliedByWatchOnce(t *testing.T) {
	ev := Event{ID: "e1", Seq: 1}
	src := sourceStub{
		list:  fixedList(Snapshot{Revision: 1, Obligations: []Obligation{{ID: "ob-1"}}}),
		watch: func(context.Context, string) (Event, error) { return ev, nil },
	}
	d := NewDeliverer(src, nil)

	got, relisted, err := d.WatchOnce(context.Background())
	if err != nil || relisted || got.ID != "e1" {
		t.Fatalf("WatchOnce = (%+v, %v, %v)", got, relisted, err)
	}
	if _, _, dirty := d.State(); !dirty {
		t.Fatal("a received hint should set the dirty signal")
	}
	// The hint itself carries no obligation; only the full read supplies it.
	if d.Has("ob-1") {
		t.Fatal("event must not carry state into the deliverer")
	}
	_, _, _ = d.Reconcile(context.Background())
	if !d.Has("ob-1") {
		t.Fatal("full read after hint should supply the obligation")
	}
}

func TestRunRelistsPeriodicallyWithJitter(t *testing.T) {
	lists := make(chan struct{}, 8)
	var jitterCalls atomic.Int64
	rev := uint64(0)
	src := sourceStub{
		list: func(context.Context) (Snapshot, error) {
			rev++
			lists <- struct{}{}
			return Snapshot{Revision: rev, Cursor: "c"}, nil
		},
		watch: func(context.Context, string) (Event, error) {
			return Event{}, errors.New("no stream")
		},
	}
	d := NewDeliverer(src, func() time.Duration {
		jitterCalls.Add(1)
		return 0
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx, time.Millisecond)
		close(done)
	}()
	<-lists // startup full read
	<-lists // one periodic full read with jitter
	cancel()
	<-done

	if jitterCalls.Load() < 1 {
		t.Fatalf("jitter was called %d times, want at least 1", jitterCalls.Load())
	}
	if d.Relists() < 2 {
		t.Fatalf("Relists = %d, want at least 2", d.Relists())
	}
}

func TestIsGone(t *testing.T) {
	if !IsGone(&GoneError{Status: httpStatusGone}) {
		t.Fatal("410 should be gone")
	}
	if !IsGone(&GoneError{Code: CursorExpired}) {
		t.Fatal("cursor_expired should be gone")
	}
	if IsGone(&GoneError{Status: 500}) {
		t.Fatal("500 must not be gone")
	}
	if IsGone(errors.New("plain error")) {
		t.Fatal("plain error must not be gone")
	}
}
