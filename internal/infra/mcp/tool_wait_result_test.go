package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// waitTestStore is the read side of the store. obligationWrites counts any
// attempt to persist an Obligation so the departure tests can prove that no
// new record is created.
type waitTestStore struct {
	watch            domain.Watch
	err              error
	reads            int
	obligationWrites int
}

func (s *waitTestStore) Watch(id string) (domain.Watch, error) {
	s.reads++
	if s.err != nil {
		return domain.Watch{}, s.err
	}
	if s.watch.ID != id {
		return domain.Watch{}, errMissing
	}
	return s.watch, nil
}

func (s *waitTestStore) SaveObligation(domain.Obligation) error {
	s.obligationWrites++
	return nil
}

type waitRead struct {
	id string
	at time.Time
}

// waitTestWriter records readAt mutations. block and entered let a test prove
// the disk write finishes before the response is returned.
type waitTestWriter struct {
	reads   []waitRead
	err     error
	entered chan struct{}
	block   chan struct{}
}

func (w *waitTestWriter) MarkRead(_ context.Context, id string, at time.Time) error {
	if w.entered != nil {
		close(w.entered)
	}
	if w.block != nil {
		<-w.block
	}
	if w.err != nil {
		return w.err
	}
	w.reads = append(w.reads, waitRead{id: id, at: at})
	return nil
}

type waitTestSubscription struct {
	ch     chan WaitVerdictUpdate
	closed bool
}

func (s *waitTestSubscription) Updates() <-chan WaitVerdictUpdate { return s.ch }
func (s *waitTestSubscription) Close()                            { s.closed = true }

type waitTestSource struct {
	sub        *waitTestSubscription
	subscribed []string
}

func (s *waitTestSource) Subscribe(watchID string) VerdictSubscription {
	s.subscribed = append(s.subscribed, watchID)
	if s.sub == nil {
		s.sub = &waitTestSubscription{ch: make(chan WaitVerdictUpdate, 8)}
	}
	return s.sub
}

func (s *waitTestSource) publish(u WaitVerdictUpdate) {
	if s.sub == nil {
		s.sub = &waitTestSubscription{ch: make(chan WaitVerdictUpdate, 8)}
	}
	s.sub.ch <- u
}

type waitTestClock struct{ at time.Time }

func (c waitTestClock) Now() time.Time { return c.at }

func newWaitTestService() (*WaitResultService, *waitTestStore, *waitTestWriter, *waitTestSource, chan time.Time) {
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	store := &waitTestStore{watch: domain.Watch{
		ID:        "watch-1",
		Repo:      "org/repo",
		PR:        7,
		HeadSHA:   "abc123",
		Status:    domain.StatusWatching,
		Verdict:   domain.VerdictWatching,
		Revision:  7,
		CreatedAt: at,
	}}
	writer := &waitTestWriter{}
	source := &waitTestSource{}
	svc := NewWaitResultService(store, writer, source, waitTestClock{at: at})
	timeout := make(chan time.Time)
	svc.after = func(time.Duration) <-chan time.Time { return timeout }
	return svc, store, writer, source, timeout
}

// TestWaitResultTimeoutIsSideEffectFree covers the timeout path: status
// watching, a poll hint, no readAt write and no obligation record.
func TestWaitResultTimeoutIsSideEffectFree(t *testing.T) {
	svc, store, writer, source, timeout := newWaitTestService()
	close(timeout)

	got, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got.Status != string(domain.StatusWatching) || got.Verdict != string(domain.VerdictWatching) {
		t.Errorf("status/verdict = %q/%q, want watching/watching", got.Status, got.Verdict)
	}
	if got.Revision != 7 {
		t.Errorf("revision = %d, want 7", got.Revision)
	}
	if got.PollAfterMs == nil || *got.PollAfterMs != waitResultPollAfterMs {
		t.Errorf("pollAfterMs = %v, want %d", got.PollAfterMs, waitResultPollAfterMs)
	}
	if len(writer.reads) != 0 {
		t.Errorf("timeout wrote readAt %d times, want 0", len(writer.reads))
	}
	if store.obligationWrites != 0 {
		t.Errorf("timeout created %d obligations, want 0", store.obligationWrites)
	}
	if len(source.subscribed) != 1 || source.subscribed[0] != "watch-1" {
		t.Errorf("subscriptions = %v, want [watch-1]", source.subscribed)
	}
	if !source.sub.closed {
		t.Error("subscription was not closed on timeout")
	}
}

// TestWaitResultTimeoutCap checks the pure default/cap helper and that the
// service actually uses a 540000ms timer when the caller asks for more.
func TestWaitResultTimeoutCap(t *testing.T) {
	cases := []struct {
		name    string
		request *int64
		wantMs  int64
	}{
		{name: "nil uses default", request: nil, wantMs: 50000},
		{name: "zero uses default", request: int64Ptr(0), wantMs: 50000},
		{name: "negative uses default", request: int64Ptr(-1), wantMs: 50000},
		{name: "below cap", request: int64Ptr(1000), wantMs: 1000},
		{name: "at cap", request: int64Ptr(540000), wantMs: 540000},
		{name: "above cap is clamped", request: int64Ptr(600000), wantMs: 540000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveTimeoutMs(tc.request); got != tc.wantMs {
				t.Errorf("effectiveTimeoutMs = %d, want %d", got, tc.wantMs)
			}
		})
	}

	svc, _, _, _, timeout := newWaitTestService()
	var recorded time.Duration
	svc.after = func(d time.Duration) <-chan time.Time {
		recorded = d
		return timeout
	}
	close(timeout)
	if _, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1", TimeoutMs: int64Ptr(900000)}, CallerExit{}); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if recorded != 540000*time.Millisecond {
		t.Errorf("timer = %v, want 9m", recorded)
	}
}

// TestWaitResultTerminalVerdictWritesReadAt drives a terminal update through
// the subscription and checks the readAt key and the returned result.
func TestWaitResultTerminalVerdictWritesReadAt(t *testing.T) {
	svc, store, writer, source, _ := newWaitTestService()
	at := time.Date(2026, 11, 19, 10, 1, 0, 0, time.UTC)
	checks := map[string]domain.Check{"ci/test": {State: "COMPLETED", Conclusion: "SUCCESS"}}
	source.publish(WaitVerdictUpdate{
		WatchID:  "watch-1",
		Status:   domain.StatusClosed,
		Verdict:  domain.VerdictMerged,
		Revision: 8,
		Checks:   checks,
		At:       at,
	})

	got, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got.Status != string(domain.StatusClosed) || got.Verdict != string(domain.VerdictMerged) || got.Revision != 8 {
		t.Errorf("result = %+v, want closed/merged/8", got)
	}
	if got.Checks["ci/test"].Conclusion != "SUCCESS" {
		t.Errorf("checks = %+v, want ci/test SUCCESS", got.Checks)
	}
	wantID := domain.ObligationKey("watch-1", 8, "sub-A")
	if len(writer.reads) != 1 || writer.reads[0].id != wantID || !writer.reads[0].at.Equal(at) {
		t.Errorf("readAt writes = %+v, want one write of %q at %v", writer.reads, wantID, at)
	}
	if store.obligationWrites != 0 {
		t.Errorf("terminal verdict created %d obligations, want 0 (readAt is an update)", store.obligationWrites)
	}
}

// TestWaitResultAlreadyTerminalInStore covers a verdict that landed before the
// call: no subscription is opened, but readAt is still written.
func TestWaitResultAlreadyTerminalInStore(t *testing.T) {
	svc, store, writer, source, _ := newWaitTestService()
	at := time.Date(2026, 11, 19, 10, 2, 0, 0, time.UTC)
	store.watch.Status = domain.StatusClosed
	store.watch.Verdict = domain.VerdictClosed
	store.watch.ClosedAt = at

	got, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got.Verdict != string(domain.VerdictClosed) {
		t.Errorf("verdict = %q, want closed", got.Verdict)
	}
	wantID := domain.ObligationKey("watch-1", 7, "sub-A")
	if len(writer.reads) != 1 || writer.reads[0].id != wantID || !writer.reads[0].at.Equal(at) {
		t.Errorf("readAt writes = %+v, want one write of %q at %v", writer.reads, wantID, at)
	}
	if len(source.subscribed) != 0 {
		t.Errorf("opened %d subscriptions for an already terminal watch", len(source.subscribed))
	}
}

// TestWaitResultDepartureChannels drives each of the three caller-departure
// channels on its own. Every one must return without a readAt write and
// without creating an obligation.
func TestWaitResultDepartureChannels(t *testing.T) {
	cases := []struct {
		name      string
		makeExits func(chan struct{}) CallerExit
		wantCode  domain.ErrorCode
	}{
		{
			name: "notifications/cancelled",
			makeExits: func(ch chan struct{}) CallerExit {
				return CallerExit{Cancelled: ch}
			},
			wantCode: domain.CodeCancelled,
		},
		{
			name: "closed session",
			makeExits: func(ch chan struct{}) CallerExit {
				return CallerExit{SessionClosed: ch}
			},
			wantCode: domain.CodeGone,
		},
		{
			name: "broken connection",
			makeExits: func(ch chan struct{}) CallerExit {
				return CallerExit{ConnectionBroken: ch}
			},
			wantCode: domain.CodeGone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, writer, _, _ := newWaitTestService()
			ch := make(chan struct{})
			close(ch)

			_, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, tc.makeExits(ch))
			assertCode(t, err, tc.wantCode)
			if len(writer.reads) != 0 {
				t.Errorf("departure wrote readAt %d times, want 0", len(writer.reads))
			}
			if store.obligationWrites != 0 {
				t.Errorf("departure created %d obligations, want 0", store.obligationWrites)
			}
		})
	}
}

// TestWaitResultWatchingTickKeepsWaiting shows that a watching update does not
// end the wait or write readAt; the timeout still decides the outcome.
func TestWaitResultWatchingTickKeepsWaiting(t *testing.T) {
	svc, store, writer, source, timeout := newWaitTestService()
	source.publish(WaitVerdictUpdate{
		WatchID:  "watch-1",
		Status:   domain.StatusWatching,
		Verdict:  domain.VerdictWatching,
		Revision: 7,
	})
	close(timeout)

	got, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got.Status != string(domain.StatusWatching) || got.Verdict != string(domain.VerdictWatching) {
		t.Errorf("status/verdict = %q/%q, want watching/watching", got.Status, got.Verdict)
	}
	if len(writer.reads) != 0 || store.obligationWrites != 0 {
		t.Errorf("watching tick wrote %d readAt and %d obligations, want 0/0", len(writer.reads), store.obligationWrites)
	}
}

// TestWaitResultReadAtBeforeResponse blocks the writer to prove Wait does not
// return while the readAt mutation is still in flight.
func TestWaitResultReadAtBeforeResponse(t *testing.T) {
	svc, _, writer, source, _ := newWaitTestService()
	writer.entered = make(chan struct{})
	writer.block = make(chan struct{})
	source.publish(WaitVerdictUpdate{
		WatchID:  "watch-1",
		Status:   domain.StatusClosed,
		Verdict:  domain.VerdictMerged,
		Revision: 9,
		At:       time.Now(),
	})

	type response struct {
		out WaitResultOutput
		err error
	}
	done := make(chan response, 1)
	go func() {
		out, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{})
		done <- response{out: out, err: err}
	}()

	<-writer.entered
	select {
	case <-done:
		t.Fatal("Wait returned before the readAt write committed")
	default:
	}
	close(writer.block)
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Wait: %v", got.err)
		}
		if got.out.Verdict != string(domain.VerdictMerged) {
			t.Errorf("verdict = %q, want merged", got.out.Verdict)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after the write committed")
	}
}

// TestWaitResultErrors covers invalid input, a missing Watch, a cancelled Watch
// and a failed readAt write. None of these creates an obligation.
func TestWaitResultErrors(t *testing.T) {
	t.Run("missing watchID", func(t *testing.T) {
		svc, _, _, _, _ := newWaitTestService()
		_, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{}, CallerExit{})
		assertCode(t, err, domain.CodeInvalidInput)
	})

	t.Run("watch not found", func(t *testing.T) {
		svc, store, _, _, _ := newWaitTestService()
		store.err = errMissing
		_, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{})
		assertCode(t, err, domain.CodeWatchNotFound)
	})

	t.Run("cancelled watch at start", func(t *testing.T) {
		svc, store, writer, source, _ := newWaitTestService()
		store.watch.Status = domain.StatusClosed
		store.watch.CloseReason = string(domain.CloseReasonCancelled)
		_, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{})
		assertCode(t, err, domain.CodeCancelled)
		if len(writer.reads) != 0 || len(source.subscribed) != 0 {
			t.Error("cancelled watch must not write readAt or subscribe")
		}
	})

	t.Run("persist failure", func(t *testing.T) {
		svc, _, writer, source, _ := newWaitTestService()
		writer.err = errors.New("disk full")
		source.publish(WaitVerdictUpdate{
			WatchID:  "watch-1",
			Status:   domain.StatusClosed,
			Verdict:  domain.VerdictMerged,
			Revision: 8,
		})
		_, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{})
		assertCode(t, err, domain.CodePersistFailed)
	})
}

// TestWaitResultHandleUsesSessionID checks the typed handler adapter: the
// opaque sessionID from _meta identifies the obligation whose readAt is set.
func TestWaitResultHandleUsesSessionID(t *testing.T) {
	svc, _, writer, source, _ := newWaitTestService()
	svc.SetDepartureProvider(func(*mcpsdk.CallToolRequest) CallerExit { return CallerExit{} })
	source.publish(WaitVerdictUpdate{
		WatchID:  "watch-1",
		Status:   domain.StatusClosed,
		Verdict:  domain.VerdictMerged,
		Revision: 11,
	})

	req := &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{
		Meta: mcpsdk.Meta{sessionIDMetaKey: "session-X"},
	}}
	_, _, err := svc.Handle(context.Background(), req, WaitResultInput{WatchID: "watch-1"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	wantID := domain.ObligationKey("watch-1", 11, "session-X")
	if len(writer.reads) != 1 || writer.reads[0].id != wantID {
		t.Errorf("readAt writes = %+v, want one write of %q", writer.reads, wantID)
	}
}

// TestWaitResultInputSchemaHasClientRequestID verifies that clientRequestId is
// part of the derived input schema, so MCP clients can send the idempotency key.
func TestWaitResultInputSchemaHasClientRequestID(t *testing.T) {
	schema, err := jsonschema.For[WaitResultInput](nil)
	if err != nil {
		t.Fatalf("input schema: %v", err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"clientRequestId"`)) {
		t.Fatalf("input schema has no clientRequestId property: %s", raw)
	}
}

// TestWaitResultClientRequestIDIdempotent proves ADR 0028 for wait_result: a
// repeat with the same clientRequestId after a broken connection returns the
// first result, runs no second wait and marks no obligation read twice.
func TestWaitResultClientRequestIDIdempotent(t *testing.T) {
	svc, store, writer, source, timeout := newWaitTestService()
	at := time.Date(2026, 11, 19, 11, 0, 0, 0, time.UTC)
	checks := map[string]domain.Check{"ci/test": {State: "COMPLETED", Conclusion: "SUCCESS"}}
	source.publish(WaitVerdictUpdate{
		WatchID:  "watch-1",
		Status:   domain.StatusClosed,
		Verdict:  domain.VerdictMerged,
		Revision: 8,
		Checks:   checks,
		At:       at,
	})

	in := WaitResultInput{WatchID: "watch-1", ClientRequestID: "req-abc"}
	first, err := svc.Wait(context.Background(), "sub-A", in, CallerExit{})
	if err != nil {
		t.Fatalf("first Wait: %v", err)
	}

	// The retry arrives after a broken connection with no new verdict on the
	// subscription. Arm the timeout so a regression that re-runs the wait fails
	// cleanly instead of hanging. It must still return the recorded result.
	close(timeout)
	second, err := svc.Wait(context.Background(), "sub-A", in, CallerExit{})
	if err != nil {
		t.Fatalf("repeat Wait: %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Errorf("repeat result = %+v, want %+v", second, first)
	}
	if second.Verdict != string(domain.VerdictMerged) || second.Revision != 8 {
		t.Errorf("repeat result = %+v, want merged/8", second)
	}
	if len(writer.reads) != 1 {
		t.Errorf("readAt writes = %d, want 1", len(writer.reads))
	}
	if store.obligationWrites != 0 {
		t.Errorf("obligation writes = %d, want 0", store.obligationWrites)
	}
	if len(source.subscribed) != 1 {
		t.Errorf("subscriptions = %d, want 1 (repeat must not open a second wait)", len(source.subscribed))
	}
}

// TestWaitResultDistinctClientRequestIDsCreateDistinctEffects shows that the
// key scopes idempotency: a new clientRequestId runs a new wait and returns its
// own result.
func TestWaitResultDistinctClientRequestIDsCreateDistinctEffects(t *testing.T) {
	svc, _, writer, source, _ := newWaitTestService()
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Status: domain.StatusClosed, Verdict: domain.VerdictMerged,
		Revision: 8, At: time.Now(),
	})
	if _, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1", ClientRequestID: "req-1"}, CallerExit{}); err != nil {
		t.Fatalf("first Wait: %v", err)
	}

	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Status: domain.StatusClosed, Verdict: domain.VerdictReady,
		Revision: 9, At: time.Now(),
	})
	second, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1", ClientRequestID: "req-2"}, CallerExit{})
	if err != nil {
		t.Fatalf("second Wait: %v", err)
	}
	if second.Verdict != string(domain.VerdictReady) || second.Revision != 9 {
		t.Errorf("second result = %+v, want ready/9", second)
	}
	if len(writer.reads) != 2 {
		t.Errorf("readAt writes = %d, want 2", len(writer.reads))
	}
}

// TestWaitResultClientRequestIDFailureIsNotCached proves a severed first call
// leaves no key: a retry with the same clientRequestId can still run the wait
// and return a result.
func TestWaitResultClientRequestIDFailureIsNotCached(t *testing.T) {
	svc, _, writer, source, _ := newWaitTestService()
	broken := make(chan struct{})
	close(broken)
	_, err := svc.Wait(context.Background(), "sub-A",
		WaitResultInput{WatchID: "watch-1", ClientRequestID: "req-1"},
		CallerExit{ConnectionBroken: broken})
	assertCode(t, err, domain.CodeGone)

	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Status: domain.StatusClosed, Verdict: domain.VerdictMerged,
		Revision: 8, At: time.Now(),
	})
	got, err := svc.Wait(context.Background(), "sub-A",
		WaitResultInput{WatchID: "watch-1", ClientRequestID: "req-1"}, CallerExit{})
	if err != nil {
		t.Fatalf("retry Wait: %v", err)
	}
	if got.Verdict != string(domain.VerdictMerged) {
		t.Errorf("retry verdict = %q, want merged", got.Verdict)
	}
	if len(writer.reads) != 1 {
		t.Errorf("readAt writes = %d, want 1", len(writer.reads))
	}
}

func int64Ptr(v int64) *int64 { return &v }
