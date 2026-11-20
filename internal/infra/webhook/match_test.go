package webhook

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

type stubWatchSource struct {
	watches []domain.Watch
	err     error
	calls   int
	lastKey RoutingKey
}

func (s *stubWatchSource) CandidateWatches(_ context.Context, key RoutingKey) ([]domain.Watch, error) {
	s.calls++
	s.lastKey = key
	return s.watches, s.err
}

type stubRefreshIntent struct {
	refreshes []Refresh
	err       error
}

func (s *stubRefreshIntent) RecordRefresh(_ context.Context, refresh Refresh) error {
	if s.err != nil {
		return s.err
	}
	s.refreshes = append(s.refreshes, refresh)
	return nil
}

func mappingWatch(id, repo string, pr int, head string) domain.Watch {
	return domain.Watch{ID: id, Repo: repo, PR: pr, HeadSHA: head}
}

func mappingEvent() Event {
	return Event{
		Repo:    " refusedguy/beacon ",
		PR:      27,
		HeadSHA: " headsha-27 ",
		Action:  " completed ",
		At:      time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC),
	}
}

func TestMapEventMatch(t *testing.T) {
	watch := mappingWatch("watch:refusedguy/beacon:27:headsha-27", "refusedguy/beacon", 27, "headsha-27")
	source := &stubWatchSource{watches: []domain.Watch{
		mappingWatch("watch:refusedguy/beacon:28:headsha-27", "refusedguy/beacon", 28, "headsha-27"),
		watch,
	}}
	intent := &stubRefreshIntent{}

	outcome, err := MapEvent(context.Background(), mappingEvent(), source, intent)
	if err != nil {
		t.Fatalf("MapEvent: %v", err)
	}
	if !outcome.Matched || outcome.WatchID != watch.ID || outcome.Reason != reasonMatched {
		t.Fatalf("outcome = %+v, want matched watch %s", outcome, watch.ID)
	}
	if source.calls != 1 {
		t.Fatalf("source calls = %d, want 1", source.calls)
	}
	wantKey := RoutingKey{Repo: "refusedguy/beacon", PR: 27, HeadSHA: "headsha-27"}
	if source.lastKey != wantKey {
		t.Fatalf("source key = %+v, want %+v", source.lastKey, wantKey)
	}
	wantRefresh := Refresh{
		WatchID: watch.ID,
		Repo:    "refusedguy/beacon",
		PR:      27,
		HeadSHA: "headsha-27",
		Action:  "completed",
		At:      mappingEvent().At,
	}
	if !reflect.DeepEqual(intent.refreshes, []Refresh{wantRefresh}) {
		t.Fatalf("refreshes = %+v, want %+v", intent.refreshes, []Refresh{wantRefresh})
	}
}

func TestMapEventForeignPR(t *testing.T) {
	source := &stubWatchSource{watches: []domain.Watch{
		mappingWatch("watch:refusedguy/beacon:28:headsha-27", "refusedguy/beacon", 28, "headsha-27"),
	}}
	intent := &stubRefreshIntent{}

	outcome, err := MapEvent(context.Background(), mappingEvent(), source, intent)
	if err != nil {
		t.Fatalf("MapEvent: %v", err)
	}
	if outcome.Matched || outcome.Reason != reasonNoMatch {
		t.Fatalf("outcome = %+v, want no match", outcome)
	}
	if len(intent.refreshes) != 0 {
		t.Fatalf("refreshes = %+v, want none: a foreign PR must not change state", intent.refreshes)
	}
}

func TestMapEventStaleHeadSHA(t *testing.T) {
	source := &stubWatchSource{watches: []domain.Watch{
		mappingWatch("watch:refusedguy/beacon:27:headsha-new", "refusedguy/beacon", 27, "headsha-new"),
	}}
	intent := &stubRefreshIntent{}

	outcome, err := MapEvent(context.Background(), mappingEvent(), source, intent)
	if err != nil {
		t.Fatalf("MapEvent: %v", err)
	}
	if outcome.Matched || outcome.Reason != reasonNoMatch {
		t.Fatalf("outcome = %+v, want no match for a stale head", outcome)
	}
	if len(intent.refreshes) != 0 {
		t.Fatalf("refreshes = %+v, want none for a stale head", intent.refreshes)
	}
}

func TestMapEventEmptySource(t *testing.T) {
	source := &stubWatchSource{}
	intent := &stubRefreshIntent{}

	outcome, err := MapEvent(context.Background(), mappingEvent(), source, intent)
	if err != nil {
		t.Fatalf("missing match must not be an error, got %v", err)
	}
	if outcome.Matched || outcome.Reason != reasonNoMatch {
		t.Fatalf("outcome = %+v, want no match", outcome)
	}
	if len(intent.refreshes) != 0 {
		t.Fatalf("refreshes = %+v, want none", intent.refreshes)
	}
}

func TestMapEventSourceError(t *testing.T) {
	wantErr := errors.New("store unavailable")
	source := &stubWatchSource{err: wantErr}
	intent := &stubRefreshIntent{}

	outcome, err := MapEvent(context.Background(), mappingEvent(), source, intent)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if outcome.Matched {
		t.Fatalf("outcome = %+v, want no match on error", outcome)
	}
	if len(intent.refreshes) != 0 {
		t.Fatalf("refreshes = %+v, want none on error", intent.refreshes)
	}
}

func TestMapEventIntentError(t *testing.T) {
	wantErr := errors.New("intent store down")
	source := &stubWatchSource{watches: []domain.Watch{
		mappingWatch("watch:refusedguy/beacon:27:headsha-27", "refusedguy/beacon", 27, "headsha-27"),
	}}
	intent := &stubRefreshIntent{err: wantErr}

	outcome, err := MapEvent(context.Background(), mappingEvent(), source, intent)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if outcome.Matched {
		t.Fatalf("outcome = %+v, want no match when the intent fails", outcome)
	}
}

func TestMapEventIncompleteKey(t *testing.T) {
	source := &stubWatchSource{}
	intent := &stubRefreshIntent{}
	event := mappingEvent()
	event.PR = 0

	outcome, err := MapEvent(context.Background(), event, source, intent)
	if err != nil {
		t.Fatalf("incomplete key must not be an error, got %v", err)
	}
	if outcome.Matched || outcome.Reason != reasonIncomplete {
		t.Fatalf("outcome = %+v, want incomplete key", outcome)
	}
	if source.calls != 0 || len(intent.refreshes) != 0 {
		t.Fatalf("an incomplete key must not reach the ports: source=%d refreshes=%d",
			source.calls, len(intent.refreshes))
	}
}

func TestMatchWatchPure(t *testing.T) {
	key := RoutingKey{Repo: "refusedguy/beacon", PR: 27, HeadSHA: "headsha-27"}
	watches := []domain.Watch{
		mappingWatch("foreign", "refusedguy/beacon", 28, "headsha-27"),
		mappingWatch("stale", "refusedguy/beacon", 27, "headsha-old"),
		mappingWatch("match", "refusedguy/beacon", 27, "headsha-27"),
	}
	before := append([]domain.Watch(nil), watches...)

	outcome := matchWatch(key, watches)
	if !outcome.Matched || outcome.WatchID != "match" {
		t.Fatalf("outcome = %+v, want the exact key match", outcome)
	}
	if !reflect.DeepEqual(watches, before) {
		t.Fatalf("matchWatch mutated its input")
	}
	if got := matchWatch(key, nil); got.Matched || got.Reason != reasonNoMatch {
		t.Fatalf("matchWatch(nil) = %+v, want no match", got)
	}
}
