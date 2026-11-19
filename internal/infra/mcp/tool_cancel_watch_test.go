package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// cancelTestStore is an in-memory cancelWatchStore. saves counts persisted
// Watch writes so idempotent repeats can be proven to write nothing.
type cancelTestStore struct {
	watches map[string]domain.Watch
	saves   int
}

func newCancelTestStore(w domain.Watch) *cancelTestStore {
	return &cancelTestStore{watches: map[string]domain.Watch{w.ID: w}}
}

func (s *cancelTestStore) Watch(id string) (domain.Watch, error) {
	w, ok := s.watches[id]
	if !ok {
		return domain.Watch{}, domain.NewAppError(domain.CodeWatchNotFound, "watch "+id+" not found")
	}
	return w, nil
}

func (s *cancelTestStore) SaveWatch(w domain.Watch) error {
	s.watches[w.ID] = w
	s.saves++
	return nil
}

// cancelTestLog is an in-memory append-only event history.
type cancelTestLog struct {
	events []CancelAuditEvent
}

func (l *cancelTestLog) Append(_ context.Context, e CancelAuditEvent) error {
	l.events = append(l.events, e)
	return nil
}

type cancelTestClock struct{ at time.Time }

func (c cancelTestClock) Now() time.Time { return c.at }

func cancelFixtureWatch() domain.Watch {
	return domain.Watch{
		ID:       "watch-1",
		Repo:     "org/repo",
		PR:       7,
		HeadSHA:  "abc123",
		Status:   domain.StatusWatching,
		Verdict:  domain.VerdictWatching,
		Revision: 3,
		Subscribers: []domain.Subscriber{
			{ID: "exec-1", Role: domain.RoleExecutor},
			{ID: "orch-1", Role: domain.RoleOrchestrator},
		},
		CreatedAt: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC),
	}
}

func cancelTestService() (*CancelWatcher, *cancelTestStore, *cancelTestLog, time.Time) {
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	store := newCancelTestStore(cancelFixtureWatch())
	log := &cancelTestLog{}
	svc := NewCancelWatcher(store, log, cancelTestClock{at: at})
	return svc, store, log, at
}

func cancelWatchRequest(sessionID string) *mcpsdk.CallToolRequest {
	meta := mcpsdk.Meta{}
	if sessionID != "" {
		meta[cancelWatchMetaKey] = sessionID
	}
	return &mcpsdk.CallToolRequest{
		Params: &mcpsdk.CallToolParamsRaw{Meta: meta},
	}
}

func TestCancelWatchAllTwiceIsIdempotent(t *testing.T) {
	svc, store, log, at := cancelTestService()
	handler := CancelWatchHandler(svc)
	req := cancelWatchRequest("")
	in := CancelWatchInput{WatchID: "watch-1", Scope: CancelScopeAll}

	for i := 1; i <= 2; i++ {
		result, out, err := handler(context.Background(), req, in)
		if err != nil {
			t.Fatalf("cancel call %d: %v", i, err)
		}
		if result == nil {
			t.Fatalf("cancel call %d: nil result", i)
		}
		if !out.OK {
			t.Fatalf("cancel call %d: ok = false", i)
		}
	}

	w := store.watches["watch-1"]
	if w.Status != domain.StatusClosed {
		t.Errorf("status = %q, want closed", w.Status)
	}
	if w.CloseReason != string(domain.CloseReasonCancelled) {
		t.Errorf("closeReason = %q, want %q", w.CloseReason, domain.CloseReasonCancelled)
	}
	if !w.ClosedAt.Equal(at) {
		t.Errorf("closedAt = %v, want %v", w.ClosedAt, at)
	}
	if store.saves != 1 {
		t.Errorf("persisted %d times, want 1 for two identical cancels", store.saves)
	}
	if len(log.events) != 1 {
		t.Fatalf("cancel events = %d, want 1 for two identical cancels", len(log.events))
	}
	if log.events[0].Scope != CancelScopeAll {
		t.Errorf("event scope = %q, want %q", log.events[0].Scope, CancelScopeAll)
	}
}

func TestCancelWatchCancelledNeverBecomesReady(t *testing.T) {
	svc, store, _, at := cancelTestService()
	if err := svc.Cancel(context.Background(), "watch-1", CancelScopeAll, ""); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	closed := store.watches["watch-1"]
	next, effects := domain.Transition(closed, domain.VerdictEvent(domain.VerdictReady, at.Add(time.Minute)))
	if next.Status != domain.StatusClosed {
		t.Errorf("status after ready verdict = %q, want closed", next.Status)
	}
	if next.Verdict != domain.VerdictWatching {
		t.Errorf("verdict after ready verdict = %q, want unchanged %q", next.Verdict, domain.VerdictWatching)
	}
	if next.CloseReason != string(domain.CloseReasonCancelled) {
		t.Errorf("closeReason after ready verdict = %q, want cancelled", next.CloseReason)
	}
	if len(effects) != 0 {
		t.Errorf("closed Watch produced %d wake effects, want 0", len(effects))
	}
}

func TestCancelWatchKeepsHistory(t *testing.T) {
	svc, _, log, at := cancelTestService()
	earlier := CancelAuditEvent{
		Kind:     "verdict",
		WatchID:  "watch-1",
		Scope:    CancelScopeAll,
		Revision: 3,
		At:       at.Add(-time.Hour),
	}
	if err := log.Append(context.Background(), earlier); err != nil {
		t.Fatalf("seed history: %v", err)
	}

	if err := svc.Cancel(context.Background(), "watch-1", CancelScopeAll, ""); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	if len(log.events) != 2 {
		t.Fatalf("history length = %d, want 2 (earlier event + cancel)", len(log.events))
	}
	if log.events[0] != earlier {
		t.Errorf("earlier history row changed: %+v", log.events[0])
	}
	if log.events[1].Kind != cancelWatchEventKind {
		t.Errorf("appended row kind = %q, want %q", log.events[1].Kind, cancelWatchEventKind)
	}
}

func TestCancelWatchScopeSubscriberDiffersFromAll(t *testing.T) {
	subSvc, subStore, subLog, _ := cancelTestService()
	if err := subSvc.Cancel(context.Background(), "watch-1", CancelScopeSubscriber, "exec-1"); err != nil {
		t.Fatalf("subscriber cancel: %v", err)
	}

	subWatch := subStore.watches["watch-1"]
	if subWatch.Status != domain.StatusWatching {
		t.Errorf("subscriber scope status = %q, want watching", subWatch.Status)
	}
	if len(subWatch.Subscribers) != 1 || subWatch.Subscribers[0].ID != "orch-1" {
		t.Errorf("subscriber scope subscribers = %+v, want only orch-1", subWatch.Subscribers)
	}
	if len(subLog.events) != 1 || subLog.events[0].Scope != CancelScopeSubscriber || subLog.events[0].SubscriberID != "exec-1" {
		t.Errorf("subscriber scope event = %+v", subLog.events)
	}

	allSvc, allStore, _, _ := cancelTestService()
	if err := allSvc.Cancel(context.Background(), "watch-1", CancelScopeAll, ""); err != nil {
		t.Fatalf("all cancel: %v", err)
	}
	allWatch := allStore.watches["watch-1"]
	if allWatch.Status != domain.StatusClosed {
		t.Errorf("all scope status = %q, want closed", allWatch.Status)
	}
	if len(allWatch.Subscribers) != 2 {
		t.Errorf("all scope removed subscribers: %+v", allWatch.Subscribers)
	}
}

func TestCancelWatchSubscriberTwiceIsIdempotent(t *testing.T) {
	svc, store, log, _ := cancelTestService()
	for i := 1; i <= 2; i++ {
		if err := svc.Cancel(context.Background(), "watch-1", CancelScopeSubscriber, "exec-1"); err != nil {
			t.Fatalf("subscriber cancel %d: %v", i, err)
		}
	}
	if store.saves != 1 {
		t.Errorf("persisted %d times, want 1", store.saves)
	}
	if len(log.events) != 1 {
		t.Errorf("cancel events = %d, want 1", len(log.events))
	}
}

func TestCancelWatchSubscriberLastOneClosesWatch(t *testing.T) {
	store := newCancelTestStore(cancelFixtureWatch())
	log := &cancelTestLog{}
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	svc := NewCancelWatcher(store, log, cancelTestClock{at: at})

	if err := svc.Cancel(context.Background(), "watch-1", CancelScopeSubscriber, "exec-1"); err != nil {
		t.Fatalf("cancel exec: %v", err)
	}
	if err := svc.Cancel(context.Background(), "watch-1", CancelScopeSubscriber, "orch-1"); err != nil {
		t.Fatalf("cancel orch: %v", err)
	}

	w := store.watches["watch-1"]
	if w.Status != domain.StatusClosed || w.CloseReason != string(domain.CloseReasonCancelled) {
		t.Errorf("last subscriber cancel: status=%q reason=%q, want closed/cancelled", w.Status, w.CloseReason)
	}
	if len(w.Subscribers) != 0 {
		t.Errorf("subscribers = %+v, want empty", w.Subscribers)
	}
	if len(log.events) != 2 {
		t.Errorf("events = %d, want 2", len(log.events))
	}
}

func TestCancelWatchHandlerReadsSessionIDFromMeta(t *testing.T) {
	const sessionID = "session-exec-1"
	w := cancelFixtureWatch()
	w.Subscribers[0].ID = sessionID
	store := newCancelTestStore(w)
	log := &cancelTestLog{}
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	svc := NewCancelWatcher(store, log, cancelTestClock{at: at})
	handler := CancelWatchHandler(svc)
	req := cancelWatchRequest(sessionID)
	in := CancelWatchInput{WatchID: "watch-1", Scope: CancelScopeSubscriber}

	_, out, err := handler(context.Background(), req, in)
	if err != nil || !out.OK {
		t.Fatalf("handler: ok=%v err=%v", out.OK, err)
	}
	if len(log.events) != 1 || log.events[0].SubscriberID != sessionID {
		t.Fatalf("event subscriberID = %+v, want %q", log.events, sessionID)
	}
	if log.events[0].Scope != CancelScopeSubscriber {
		t.Errorf("event scope = %q, want %q", log.events[0].Scope, CancelScopeSubscriber)
	}
	for _, s := range store.watches["watch-1"].Subscribers {
		if s.ID == sessionID {
			t.Fatal("caller subscriber was not removed")
		}
	}
}

func TestCancelWatchSubscriberWithoutMetaIsRejected(t *testing.T) {
	svc, store, log, _ := cancelTestService()
	handler := CancelWatchHandler(svc)
	in := CancelWatchInput{WatchID: "watch-1", Scope: CancelScopeSubscriber}

	_, out, err := handler(context.Background(), cancelWatchRequest(""), in)
	if out.OK {
		t.Fatal("ok = true for subscriber scope without a sessionID")
	}
	var appErr *domain.AppError
	if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
		t.Fatalf("error = %v, want INVALID_INPUT", err)
	}
	if len(log.events) != 0 || store.saves != 0 {
		t.Fatalf("rejected call mutated state: events=%d saves=%d", len(log.events), store.saves)
	}
}

func TestCancelWatchRejectsBadInput(t *testing.T) {
	svc, _, _, _ := cancelTestService()
	tests := []struct {
		name         string
		watchID      string
		scope        string
		subscriberID string
		want         domain.ErrorCode
	}{
		{"missing watchID", "", CancelScopeAll, "", domain.CodeInvalidInput},
		{"bad scope", "watch-1", "other", "", domain.CodeInvalidInput},
		{"subscriber without caller", "watch-1", CancelScopeSubscriber, "", domain.CodeInvalidInput},
		{"unknown watch", "missing", CancelScopeAll, "", domain.CodeWatchNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.Cancel(context.Background(), tc.watchID, tc.scope, tc.subscriberID)
			var appErr *domain.AppError
			if !errors.As(err, &appErr) {
				t.Fatalf("error = %v, want *domain.AppError", err)
			}
			if appErr.Code != tc.want {
				t.Errorf("code = %q, want %q", appErr.Code, tc.want)
			}
		})
	}
}
