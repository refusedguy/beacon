package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// handleLeaseStore is the read side needed by the wait. reads counts store
// lookups so the tests can prove an expired handle never reaches the store.
type handleLeaseStore struct {
	watch domain.Watch
	err   error
	reads int
}

func (s *handleLeaseStore) Watch(id string) (domain.Watch, error) {
	s.reads++
	if s.err != nil {
		return domain.Watch{}, s.err
	}
	if s.watch.ID != id {
		return domain.Watch{}, errors.New("watch not found")
	}
	return s.watch, nil
}

type handleLeaseClock struct{ at time.Time }

func (c handleLeaseClock) Now() time.Time { return c.at }

type handleLeaseWriter struct{}

func (handleLeaseWriter) MarkRead(context.Context, string, time.Time) error { return nil }

type handleLeaseSubscription struct{ ch chan WaitVerdictUpdate }

func (s *handleLeaseSubscription) Updates() <-chan WaitVerdictUpdate { return s.ch }
func (s *handleLeaseSubscription) Close()                            {}

type handleLeaseSource struct{}

func (handleLeaseSource) Subscribe(string) VerdictSubscription {
	return &handleLeaseSubscription{ch: make(chan WaitVerdictUpdate)}
}

func newHandleLeaseService(at time.Time, watchID string) (*WaitResultService, *handleLeaseStore) {
	store := &handleLeaseStore{watch: domain.Watch{
		ID:        watchID,
		Repo:      "org/repo",
		PR:        7,
		HeadSHA:   "abc123",
		Status:    domain.StatusWatching,
		Verdict:   domain.VerdictWatching,
		Revision:  1,
		CreatedAt: at,
	}}
	svc := NewWaitResultService(store, handleLeaseWriter{}, handleLeaseSource{}, handleLeaseClock{at: at})
	timedOut := make(chan time.Time)
	close(timedOut)
	svc.after = func(time.Duration) <-chan time.Time { return timedOut }
	return svc, store
}

// TestWaitResultLiveHandle covers the live handle: the wait runs and reaches
// the store, then ends on the timeout with no error.
func TestWaitResultLiveHandle(t *testing.T) {
	at := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	handle := domain.NewWatchHandle("org/repo", 7, "abc123", at)
	svc, store := newHandleLeaseService(at, handle)

	got, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: handle}, CallerExit{})
	if err != nil {
		t.Fatalf("Wait on live handle: %v", err)
	}
	if got.Status != string(domain.StatusWatching) {
		t.Errorf("status = %q, want watching", got.Status)
	}
	if store.reads != 1 {
		t.Errorf("store reads = %d, want 1", store.reads)
	}
}

// TestWaitResultExpiredHandle covers the dead handle: it returns an AppError
// with WATCH_EXPIRED before any store read, so it never becomes a watching
// result and never falls through to the CodeWatchNotFound branch.
func TestWaitResultExpiredHandle(t *testing.T) {
	at := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	issuedAt := at.Add(-domain.WatchHandleTTL - time.Second)
	handle := domain.NewWatchHandle("org/repo", 7, "abc123", issuedAt)
	svc, store := newHandleLeaseService(at, handle)

	_, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: handle}, CallerExit{})
	if err == nil {
		t.Fatal("Wait on expired handle must return an error")
	}
	var app *domain.AppError
	if !errors.As(err, &app) {
		t.Fatalf("error %v is not *domain.AppError", err)
	}
	if app.Code != domain.CodeWatchExpired {
		t.Errorf("code = %q, want %q", app.Code, domain.CodeWatchExpired)
	}
	if store.reads != 0 {
		t.Errorf("expired handle reached the store %d times, want 0", store.reads)
	}
}

// TestWaitResultUnknownHandleStillNotFound proves the existing unknown-watch
// check is unchanged: a handle inside its lease but absent from the store still
// returns CodeWatchNotFound.
func TestWaitResultUnknownHandleStillNotFound(t *testing.T) {
	at := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	known := domain.NewWatchHandle("org/repo", 7, "abc123", at)
	svc, _ := newHandleLeaseService(at, known)
	unknown := domain.NewWatchHandle("org/repo", 8, "abc123", at)

	_, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: unknown}, CallerExit{})
	if err == nil {
		t.Fatal("Wait on unknown handle must return an error")
	}
	var app *domain.AppError
	if !errors.As(err, &app) {
		t.Fatalf("error %v is not *domain.AppError", err)
	}
	if app.Code != domain.CodeWatchNotFound {
		t.Errorf("code = %q, want %q", app.Code, domain.CodeWatchNotFound)
	}
}
