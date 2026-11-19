package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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
	svc, store, writer, source, _ := newWaitTestService()
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
	// subscription. It must still return the recorded result.
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

// TestWaitResultInputSchemaHasContinueOf keeps continueOf in the derived MCP
// input schema so a client can resume a parked task after a human answer.
func TestWaitResultInputSchemaHasContinueOf(t *testing.T) {
	schema, err := jsonschema.For[WaitResultInput](nil)
	if err != nil {
		t.Fatalf("input schema: %v", err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"continueOf"`)) {
		t.Fatalf("input schema has no continueOf property: %s", raw)
	}
}

// TestWaitResultNeedsInputParksWithoutRead covers a Watch already parked when
// the wait starts: it returns early with needs_input and writes no readAt.
func TestWaitResultNeedsInputParksWithoutRead(t *testing.T) {
	svc, store, writer, source, _ := newWaitTestService()
	store.watch.Status = domain.StatusNeedsInput
	store.watch.Verdict = domain.VerdictNeedsInput

	got, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got.Status != string(domain.StatusNeedsInput) || got.Verdict != string(domain.VerdictNeedsInput) {
		t.Errorf("status/verdict = %q/%q, want needs_input/needs_input", got.Status, got.Verdict)
	}
	if got.Revision != 7 || got.PollAfterMs == nil {
		t.Errorf("parked result = %+v, want revision 7 with a poll hint", got)
	}
	if len(writer.reads) != 0 || store.obligationWrites != 0 {
		t.Errorf("parking wrote %d readAt and %d obligations, want 0/0", len(writer.reads), store.obligationWrites)
	}
	if len(source.subscribed) != 0 {
		t.Errorf("parking opened %d subscriptions, want 0", len(source.subscribed))
	}
}

// TestWaitResultNeedsInputUpdateParks proves that a question arriving during
// the long-poll stops the wait without delivering a verdict or marking readAt.
func TestWaitResultNeedsInputUpdateParks(t *testing.T) {
	svc, store, writer, source, _ := newWaitTestService()
	checks := map[string]domain.Check{"ci/test": {State: "COMPLETED"}}
	source.publish(WaitVerdictUpdate{
		WatchID:  "watch-1",
		Status:   domain.StatusNeedsInput,
		Verdict:  domain.VerdictNeedsInput,
		Revision: 8,
		Checks:   checks,
	})

	got, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got.Status != string(domain.StatusNeedsInput) || got.Revision != 8 {
		t.Errorf("result = %+v, want needs_input at revision 8", got)
	}
	if len(writer.reads) != 0 || store.obligationWrites != 0 {
		t.Errorf("question wrote %d readAt and %d obligations, want 0/0", len(writer.reads), store.obligationWrites)
	}
	if !source.sub.closed {
		t.Error("subscription was not closed when the wait parked")
	}
}

// TestWaitResultContinuesAfterAnswerOnSameWatch covers T3.21: after the human
// answers through ack(continueOf), a wait with continueOf resumes the same
// Watch and delivers its verdict. No new Watch is created and clientRequestId
// idempotency still holds for both waits.
func TestWaitResultContinuesAfterAnswerOnSameWatch(t *testing.T) {
	svc, store, writer, source, _ := newWaitTestService()

	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Status: domain.StatusNeedsInput,
		Verdict: domain.VerdictNeedsInput, Revision: 8,
	})
	parked, err := svc.Wait(context.Background(), "sub-A",
		WaitResultInput{WatchID: "watch-1", ClientRequestID: "req-park"}, CallerExit{})
	if err != nil {
		t.Fatalf("parking Wait: %v", err)
	}
	if parked.Status != string(domain.StatusNeedsInput) {
		t.Fatalf("parking result = %+v, want needs_input", parked)
	}

	// The answer resumed the same Watch: no successor Watch appears in the
	// store and the next updates arrive on that Watch's revision.
	store.watch.Status = domain.StatusWatching
	store.watch.Verdict = domain.VerdictWatching
	store.watch.Revision = 9
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Status: domain.StatusWatching,
		Verdict: domain.VerdictWatching, Revision: 9,
	})
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Status: domain.StatusClosed,
		Verdict: domain.VerdictMerged, Revision: 10,
		At: time.Date(2026, 11, 20, 5, 0, 0, 0, time.UTC),
	})

	in := WaitResultInput{WatchID: "watch-1", ContinueOf: "watch-1", ClientRequestID: "req-done"}
	got, err := svc.Wait(context.Background(), "sub-A", in, CallerExit{})
	if err != nil {
		t.Fatalf("continued Wait: %v", err)
	}
	if got.Status != string(domain.StatusClosed) || got.Verdict != string(domain.VerdictMerged) || got.Revision != 10 {
		t.Errorf("continued result = %+v, want closed/merged/10", got)
	}
	if store.watch.ID != "watch-1" || len(source.subscribed) != 2 {
		t.Errorf("continuation changed the task: watch %q, %d subscriptions", store.watch.ID, len(source.subscribed))
	}
	if len(writer.reads) != 1 || writer.reads[0].id != domain.ObligationKey("watch-1", 10, "sub-A") {
		t.Errorf("readAt writes = %+v, want one write at the continued revision", writer.reads)
	}

	// The clientRequestId contract from T2.22 still holds after a continuation.
	repeat, err := svc.Wait(context.Background(), "sub-A", in, CallerExit{})
	if err != nil {
		t.Fatalf("repeat continued Wait: %v", err)
	}
	if !reflect.DeepEqual(repeat, got) || len(writer.reads) != 1 || len(source.subscribed) != 2 {
		t.Errorf("repeat = %+v, writes %+v, subscriptions %d", repeat, writer.reads, len(source.subscribed))
	}
}

// TestWaitResultContinueOfMismatchRejected keeps continueOf scoped to one
// Watch: it cannot be used to wait on a different task.
func TestWaitResultContinueOfMismatchRejected(t *testing.T) {
	svc, store, writer, source, _ := newWaitTestService()
	_, err := svc.Wait(context.Background(), "sub-A",
		WaitResultInput{WatchID: "watch-1", ContinueOf: "other-watch"}, CallerExit{})
	assertCode(t, err, domain.CodeInvalidInput)
	if len(writer.reads) != 0 || store.obligationWrites != 0 || len(source.subscribed) != 0 {
		t.Errorf("rejected continueOf had effects: reads %d writes %d subscriptions %d",
			len(writer.reads), store.obligationWrites, len(source.subscribed))
	}
}

// TestWaitResultByteStable asserts ADR 0036 §2: two wait_result calls against
// the same state marshal to identical bytes. The three cases cover a terminal
// verdict with checks (T2.22/T2 deliver path), a parked needs_input (T3.21)
// and a watching timeout, so a future field or map change cannot burn the
// prefix-cache of all 15 agents unnoticed.
func TestWaitResultByteStable(t *testing.T) {
	at := time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)
	checks := map[string]domain.Check{
		"ci/test":    {State: "COMPLETED", Conclusion: "SUCCESS", Required: true},
		"deploy/stg": {State: "COMPLETED", Conclusion: "SUCCESS", DetailsURL: "https://example.invalid/run/1"},
	}
	cases := []struct {
		name string
		run  func(*testing.T) WaitResultOutput
	}{
		{
			name: "terminal verdict",
			run: func(t *testing.T) WaitResultOutput {
				svc, _, _, source, _ := newWaitTestService()
				source.publish(WaitVerdictUpdate{
					WatchID: "watch-1", Status: domain.StatusClosed,
					Verdict: domain.VerdictMerged, Revision: 8, Checks: checks, At: at,
				})
				out, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{})
				if err != nil {
					t.Fatalf("Wait: %v", err)
				}
				return out
			},
		},
		{
			name: "needs_input",
			run: func(t *testing.T) WaitResultOutput {
				svc, store, _, _, _ := newWaitTestService()
				store.watch.Status = domain.StatusNeedsInput
				store.watch.Verdict = domain.VerdictNeedsInput
				out, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{})
				if err != nil {
					t.Fatalf("Wait: %v", err)
				}
				return out
			},
		},
		{
			name: "watching timeout",
			run: func(t *testing.T) WaitResultOutput {
				svc, _, _, _, timeout := newWaitTestService()
				close(timeout)
				out, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{})
				if err != nil {
					t.Fatalf("Wait: %v", err)
				}
				return out
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, err := json.Marshal(tc.run(t))
			if err != nil {
				t.Fatalf("marshal first: %v", err)
			}
			second, err := json.Marshal(tc.run(t))
			if err != nil {
				t.Fatalf("marshal second: %v", err)
			}
			if !bytes.Equal(first, second) {
				t.Fatalf("wait_result bytes are not stable:\nfirst  %s\nsecond %s", first, second)
			}
		})
	}
}

// TestWaitResultFieldOrderIsCanonical locks the exact field order and the
// omitempty rules into a byte string. A reorder or an added field fails here
// even when two calls still happen to agree with each other.
func TestWaitResultFieldOrderIsCanonical(t *testing.T) {
	ms := int64(waitResultPollAfterMs)
	out := WaitResultOutput{
		Status:      string(domain.StatusClosed),
		Verdict:     string(domain.VerdictMerged),
		Revision:    8,
		Checks:      map[string]domain.Check{"ci/test": {State: "COMPLETED", Conclusion: "SUCCESS", Required: true}},
		PollAfterMs: &ms,
	}
	got, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"status":"closed","verdict":"merged","revision":8,"checks":{"ci/test":{"state":"COMPLETED","conclusion":"SUCCESS","required":true,"detailsUrl":""}},"pollAfterMs":5000}`
	if string(got) != want {
		t.Fatalf("canonical wait_result bytes:\n got  %s\n want %s", got, want)
	}
}

// newTestCapabilityTree builds a capability context with a tree of tasks. Each
// pair is {taskID, parentID}; an empty parent registers a root.
func newTestCapabilityTree(t *testing.T, owner CapabilityID, pairs ...[2]string) (*CapabilityContext, *memoryCancelJournal) {
	t.Helper()
	journal := &memoryCancelJournal{}
	ctx := NewCapabilityContext(owner, journal, waitTestClock{at: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)})
	for _, pair := range pairs {
		if err := ctx.AddTask(pair[0], pair[1]); err != nil {
			t.Fatalf("AddTask(%q, %q): %v", pair[0], pair[1], err)
		}
	}
	return ctx, journal
}

// signalSource opens one subscription and signals when it is ready, so a test
// can cancel a capability context after the wait has reached its select loop.
type signalSource struct {
	updates    chan WaitVerdictUpdate
	subscribed chan struct{}
	once       sync.Once
}

func (s *signalSource) Subscribe(string) VerdictSubscription {
	s.once.Do(func() { close(s.subscribed) })
	return &waitTestSubscription{ch: s.updates}
}

// TestCapabilityContextCancelTearsDownTree proves ADR 0040 §1 on the context
// itself: Cancel delivers one reason to every node of the tree, journals each
// one, closes Done, refuses new work and is idempotent.
func TestCapabilityContextCancelTearsDownTree(t *testing.T) {
	ctx, journal := newTestCapabilityTree(t, "cap-review",
		[2]string{"root", ""},
		[2]string{"child-a", "root"},
		[2]string{"child-b", "root"},
		[2]string{"grandchild", "child-a"},
	)

	ctx.Cancel(CancelCallerCancelled)

	if !ctx.IsCancelled() {
		t.Fatal("context not cancelled")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("Done not closed")
	}
	if got := ctx.Reason(); got != CancelCallerCancelled {
		t.Fatalf("reason = %q, want %q", got, CancelCallerCancelled)
	}

	wantIDs := []string{"root", "child-a", "child-b", "grandchild"}
	tasks := ctx.CancelledTasks()
	if len(tasks) != len(wantIDs) {
		t.Fatalf("cancelled tasks = %d, want %d", len(tasks), len(wantIDs))
	}
	for i, got := range tasks {
		if got.TaskID != wantIDs[i] || got.Reason != CancelCallerCancelled {
			t.Errorf("task[%d] = %+v, want %q/%q", i, got, wantIDs[i], CancelCallerCancelled)
		}
	}

	records := journal.Records()
	if len(records) != len(wantIDs) {
		t.Fatalf("journal records = %d, want %d", len(records), len(wantIDs))
	}
	for i, r := range records {
		if r.Owner != "cap-review" || r.TaskID != wantIDs[i] || r.Reason != CancelCallerCancelled || r.At.IsZero() {
			t.Errorf("record[%d] = %+v", i, r)
		}
	}

	assertCode(t, ctx.AddTask("late", "root"), domain.CodeCancelled)
	ctx.Cancel(CancelSessionClosed)
	if got := ctx.Reason(); got != CancelCallerCancelled {
		t.Errorf("reason changed on repeat cancel: %q", got)
	}
	if n := len(journal.Records()); n != len(wantIDs) {
		t.Errorf("repeat cancel wrote %d extra records", n-len(wantIDs))
	}
}

// TestWaitResultDepartureCancelsCapabilityTree drives each departure channel
// on a wait bound to a tree. Every departure tears down the whole tree with the
// matching reason and still writes no readAt and no obligation (T2.5).
func TestWaitResultDepartureCancelsCapabilityTree(t *testing.T) {
	cases := []struct {
		name      string
		makeExits func(chan struct{}) CallerExit
		reason    CancelReason
		code      domain.ErrorCode
	}{
		{"cancelled", func(ch chan struct{}) CallerExit { return CallerExit{Cancelled: ch} }, CancelCallerCancelled, domain.CodeCancelled},
		{"session closed", func(ch chan struct{}) CallerExit { return CallerExit{SessionClosed: ch} }, CancelSessionClosed, domain.CodeGone},
		{"connection broken", func(ch chan struct{}) CallerExit { return CallerExit{ConnectionBroken: ch} }, CancelConnectionBroken, domain.CodeGone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, writer, _, _ := newWaitTestService()
			ctx, journal := newTestCapabilityTree(t, "cap-agent",
				[2]string{"root", ""},
				[2]string{"child-a", "root"},
				[2]string{"child-b", "root"},
			)
			ch := make(chan struct{})
			close(ch)

			_, err := svc.WaitInCapability(context.Background(), "sub-A",
				WaitResultInput{WatchID: "watch-1"}, tc.makeExits(ch), ctx)
			assertCode(t, err, tc.code)
			if !ctx.IsCancelled() || ctx.Reason() != tc.reason {
				t.Fatalf("cancelled=%v reason=%q, want true/%q", ctx.IsCancelled(), ctx.Reason(), tc.reason)
			}
			for _, got := range ctx.CancelledTasks() {
				if got.Reason != tc.reason {
					t.Errorf("task %q reason = %q, want %q", got.TaskID, got.Reason, tc.reason)
				}
			}
			records := journal.Records()
			if len(records) != 3 {
				t.Fatalf("journal records = %d, want 3", len(records))
			}
			for _, r := range records {
				if r.Reason != tc.reason || r.Owner != "cap-agent" {
					t.Errorf("record = %+v, want cap-agent/%q", r, tc.reason)
				}
			}
			if len(writer.reads) != 0 || store.obligationWrites != 0 {
				t.Errorf("departure wrote %d readAt and %d obligations, want 0/0", len(writer.reads), store.obligationWrites)
			}
		})
	}
}

// TestWaitResultExternalCapabilityCancelStopsWait proves that a descendant
// already blocked in the long-poll stops when the context is cancelled from
// outside, without any departure channel of its own.
func TestWaitResultExternalCapabilityCancelStopsWait(t *testing.T) {
	svc, _, writer, _, _ := newWaitTestService()
	src := &signalSource{updates: make(chan WaitVerdictUpdate, 1), subscribed: make(chan struct{})}
	svc.source = src
	ctx, journal := newTestCapabilityTree(t, "cap-agent",
		[2]string{"root", ""},
		[2]string{"child", "root"},
	)

	type result struct {
		out WaitResultOutput
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := svc.WaitInCapability(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{}, ctx)
		done <- result{out: out, err: err}
	}()

	<-src.subscribed
	ctx.Cancel(CancelSessionClosed)

	select {
	case got := <-done:
		assertCode(t, got.err, domain.CodeGone)
		if !strings.Contains(got.err.Error(), string(CancelSessionClosed)) {
			t.Errorf("error %q does not carry the cancellation reason %q", got.err, CancelSessionClosed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not stop after the capability context was cancelled")
	}
	if len(writer.reads) != 0 {
		t.Errorf("capability cancel wrote %d readAt, want 0", len(writer.reads))
	}
	if len(journal.Records()) != 2 {
		t.Errorf("journal records = %d, want 2", len(journal.Records()))
	}
}

// TestWaitResultContextEndCancelsTree covers the fourth reason: the go-sdk
// cancelling the handler context also tears down the tree with its own reason.
func TestWaitResultContextEndCancelsTree(t *testing.T) {
	svc, _, _, _, _ := newWaitTestService()
	ctx, journal := newTestCapabilityTree(t, "cap-agent", [2]string{"root", ""})
	callCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := svc.WaitInCapability(callCtx, "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{}, ctx)
	assertCode(t, err, domain.CodeCancelled)
	if ctx.Reason() != CancelContextEnded {
		t.Errorf("reason = %q, want %q", ctx.Reason(), CancelContextEnded)
	}
	if len(journal.Records()) != 1 {
		t.Errorf("journal records = %d, want 1", len(journal.Records()))
	}
}

// TestWaitResultTimeoutDoesNotCancelCapabilityTree keeps the timeout a private
// case (ADR 0040 §1): it returns watching and leaves the whole tree untouched.
func TestWaitResultTimeoutDoesNotCancelCapabilityTree(t *testing.T) {
	svc, _, writer, _, timeout := newWaitTestService()
	ctx, journal := newTestCapabilityTree(t, "cap-agent",
		[2]string{"root", ""},
		[2]string{"child", "root"},
	)
	close(timeout)

	out, err := svc.WaitInCapability(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{}, ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if out.Status != string(domain.StatusWatching) || out.Verdict != string(domain.VerdictWatching) {
		t.Errorf("status/verdict = %q/%q, want watching/watching", out.Status, out.Verdict)
	}
	if ctx.IsCancelled() {
		t.Error("timeout cancelled the capability tree")
	}
	if len(journal.Records()) != 0 {
		t.Errorf("timeout wrote %d cancellation records, want 0", len(journal.Records()))
	}
	if len(writer.reads) != 0 {
		t.Errorf("timeout wrote %d readAt, want 0", len(writer.reads))
	}
}

// TestWaitResultHandleCancelsTreeOnDeparture proves the handler wires the
// capability scope: a departure seen through Handle tears down the bound tree.
func TestWaitResultHandleCancelsTreeOnDeparture(t *testing.T) {
	svc, _, writer, _, _ := newWaitTestService()
	closed := make(chan struct{})
	close(closed)
	svc.SetDepartureProvider(func(*mcpsdk.CallToolRequest) CallerExit {
		return CallerExit{Cancelled: closed}
	})
	ctx, journal := newTestCapabilityTree(t, "cap-agent",
		[2]string{"root", ""},
		[2]string{"child", "root"},
	)
	svc.SetCapabilityScope(func(*mcpsdk.CallToolRequest) *CapabilityContext { return ctx })

	req := &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{
		Meta: mcpsdk.Meta{sessionIDMetaKey: "session-X"},
	}}
	_, _, err := svc.Handle(context.Background(), req, WaitResultInput{WatchID: "watch-1"})
	assertCode(t, err, domain.CodeCancelled)
	if !ctx.IsCancelled() || ctx.Reason() != CancelCallerCancelled {
		t.Fatalf("cancelled=%v reason=%q, want true/%q", ctx.IsCancelled(), ctx.Reason(), CancelCallerCancelled)
	}
	if len(journal.Records()) != 2 {
		t.Errorf("journal records = %d, want 2", len(journal.Records()))
	}
	if len(writer.reads) != 0 {
		t.Errorf("departure wrote %d readAt, want 0", len(writer.reads))
	}
}

// TestWaitResultHandleSkipsWorkWhenTreeAlreadyCancelled proves a descendant
// arriving after the initiator disappeared does no work at all.
func TestWaitResultHandleSkipsWorkWhenTreeAlreadyCancelled(t *testing.T) {
	svc, store, writer, _, _ := newWaitTestService()
	svc.SetDepartureProvider(func(*mcpsdk.CallToolRequest) CallerExit { return CallerExit{} })
	ctx, journal := newTestCapabilityTree(t, "cap-agent", [2]string{"root", ""})
	ctx.Cancel(CancelConnectionBroken)
	svc.SetCapabilityScope(func(*mcpsdk.CallToolRequest) *CapabilityContext { return ctx })

	req := &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{
		Meta: mcpsdk.Meta{sessionIDMetaKey: "session-X"},
	}}
	_, _, err := svc.Handle(context.Background(), req, WaitResultInput{WatchID: "watch-1"})
	assertCode(t, err, domain.CodeGone)
	if store.reads != 0 || len(writer.reads) != 0 {
		t.Errorf("cancelled tree did work: store reads %d, readAt writes %d", store.reads, len(writer.reads))
	}
	if len(journal.Records()) != 1 {
		t.Errorf("journal records = %d, want 1 (no duplicate cancel)", len(journal.Records()))
	}
}

// coalescingCIWaiter is the test stand-in for the store gate: the first caller
// runs the expensive operation, the rest join the active round and share its
// end. It keeps the acceptance test inside mcp without importing a sibling
// infra package (ADR 0005).
type coalescingCIWaiter struct {
	mu    sync.Mutex
	calls map[string]*coalescingCIRound
}

type coalescingCIRound struct {
	done chan struct{}
}

func newCoalescingCIWaiter() *coalescingCIWaiter {
	return &coalescingCIWaiter{calls: make(map[string]*coalescingCIRound)}
}

func (g *coalescingCIWaiter) wait(ctx context.Context, key string, fn func(context.Context) error) error {
	g.mu.Lock()
	if round, ok := g.calls[key]; ok {
		g.mu.Unlock()
		select {
		case <-round.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	round := &coalescingCIRound{done: make(chan struct{})}
	g.calls[key] = round
	g.mu.Unlock()

	err := fn(ctx)

	g.mu.Lock()
	delete(g.calls, key)
	close(round.done)
	g.mu.Unlock()
	return err
}

// TestWaitResultSharedCIWaitCoalescesOneCI is the T3.76 acceptance check.
// Fifteen units waiting on one CI (same repo/PR/headSHA) run the expensive
// shared wait once. Removing the gate call from wait makes the operation never
// run and this test fails.
// TestWaitResultPrecheckFailureReasonInOutputAndJournal proves ADR 0041 §6:
// the T3.81 reason is delivered in the wait_result response and written to the
// journal before any wait starts. stderr is no longer the only destination.
func TestWaitResultPrecheckFailureReasonInOutputAndJournal(t *testing.T) {
	svc, store, writer, source, _ := newWaitTestService()
	reason := "bypass mode не настроен для init event control-plane"
	journal := &memoryPrecheckJournal{}
	svc.SetPrecheckJournal(journal)
	svc.SetPrecheckFailure(reason)

	out, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if out.Status != PrecheckFailureOutcome || out.Verdict != PrecheckFailureOutcome {
		t.Fatalf("status/verdict = %q/%q, want %q", out.Status, out.Verdict, PrecheckFailureOutcome)
	}
	check, ok := out.Checks[precheckCheckName]
	if !ok || check.Conclusion != reason {
		t.Fatalf("причина не доставлена в wait_result: %#v", out.Checks)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), reason) {
		t.Fatalf("причины нет в JSON wait_result: %s", raw)
	}
	if store.reads != 0 || len(writer.reads) != 0 || len(source.subscribed) != 0 {
		t.Fatalf(" precheck должен отказать до ожидания: reads=%d writes=%d subs=%v",
			store.reads, len(writer.reads), source.subscribed)
	}
	records := journal.Records()
	if len(records) != 1 {
		t.Fatalf("записей журнала %d, ожидалась 1", len(records))
	}
	if records[0].Outcome != PrecheckFailureOutcome || records[0].Reason != reason {
		t.Fatalf("запись журнала = %#v", records[0])
	}
}

// TestWaitResultHandleDeliversPrecheckReason checks the same delivery through
// the typed go-sdk handler the MCP client actually calls.
func TestWaitResultHandleDeliversPrecheckReason(t *testing.T) {
	svc, _, _, _, _ := newWaitTestService()
	journal := &memoryPrecheckJournal{}
	svc.SetPrecheckJournal(journal)
	svc.SetPrecheckFailure("нет bypass")

	result, out, err := svc.Handle(context.Background(), nil, WaitResultInput{WatchID: "watch-1"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("wait_result вернул ошибку: %#v", result)
	}
	if out.Status != PrecheckFailureOutcome {
		t.Fatalf("status = %q, want %q", out.Status, PrecheckFailureOutcome)
	}
	if got := out.Checks[precheckCheckName].Conclusion; got != "нет bypass" {
		t.Fatalf("причина в wait_result = %q", got)
	}
	if records := journal.Records(); len(records) != 1 || records[0].Reason != "нет bypass" {
		t.Fatalf("журнал = %#v", records)
	}
}

// TestWaitResultPrecheckFailureCanBeCleared keeps the normal wait path intact
// once the precheck reason is removed (T3.66 and T3.76 rely on that path).
func TestWaitResultPrecheckFailureCanBeCleared(t *testing.T) {
	svc, _, _, _, timeout := newWaitTestService()
	svc.SetPrecheckFailure("временный отказ")
	svc.SetPrecheckFailure("")
	close(timeout)

	out, err := svc.Wait(context.Background(), "sub-A", WaitResultInput{WatchID: "watch-1"}, CallerExit{})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if out.Status != string(domain.StatusWatching) {
		t.Fatalf("status = %q, want watching", out.Status)
	}
}

func TestWaitResultSharedCIWaitCoalescesOneCI(t *testing.T) {
	const units = 15
	var (
		runs          atomic.Int32
		entered       atomic.Int32
		leaderEntered = make(chan struct{})
		release       = make(chan struct{})
	)
	op := func(context.Context) error {
		if runs.Add(1) == 1 {
			close(leaderEntered)
		}
		<-release
		return nil
	}

	gate := newCoalescingCIWaiter()
	start := make(chan struct{})
	type result struct {
		out WaitResultOutput
		err error
	}
	results := make([]result, units)
	timeouts := make([]chan time.Time, units)
	var wg sync.WaitGroup
	for i := 0; i < units; i++ {
		svc, _, _, _, timeout := newWaitTestService()
		svc.SetCIWait(op)
		svc.SetSharedCIWaiter(gate.wait)
		timeouts[i] = timeout
		wg.Add(1)
		go func(i int, svc *WaitResultService) {
			defer wg.Done()
			<-start
			entered.Add(1)
			results[i].out, results[i].err = svc.Wait(context.Background(), "sub-A",
				WaitResultInput{WatchID: "watch-1"}, CallerExit{})
		}(i, svc)
	}
	close(start)

	deadline := time.Now().Add(2 * time.Second)
	for entered.Load() < units && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	select {
	case <-leaderEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("no unit entered the shared CI wait")
	}
	// Let the other units join the active round before it is released.
	time.Sleep(250 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("expensive CI wait ran %d times before release, want 1", got)
	}

	for _, timeout := range timeouts {
		close(timeout)
	}
	close(release)
	wg.Wait()

	if got := runs.Load(); got != 1 {
		t.Fatalf("expensive CI wait ran %d times, want 1 (one CI, one operation)", got)
	}
	for i, r := range results {
		if r.err != nil {
			t.Errorf("unit %d: Wait = %v, want nil", i, r.err)
		}
		if r.out.Status != string(domain.StatusWatching) {
			t.Errorf("unit %d: status = %q, want watching", i, r.out.Status)
		}
	}
}

// ---- ADR 0045 p1 / T3.102: фильтр событий и ключ на старте ожидания ----

// TestWaitResultEventFilterReturnsOnFirstListedEvent проверяет, что ожидание
// возвращается по первому событию из фильтра, а не по таймауту опроса.
func TestWaitResultEventFilterReturnsOnFirstListedEvent(t *testing.T) {
	svc, _, writer, source, timeout := newWaitTestService()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Kind: WaitEventProgress, Status: domain.StatusWatching,
		Verdict: domain.VerdictWatching, Revision: 8, At: at,
	})
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Kind: WaitEventCheckpoint, Status: domain.StatusNotifying,
		Verdict: domain.VerdictWatching, Revision: 9, At: at,
	})
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Kind: WaitEventCompleted, Status: domain.StatusClosed,
		Verdict: domain.VerdictMerged, Revision: 10, At: at,
	})

	got, err := svc.Wait(context.Background(), "sub-A",
		WaitResultInput{WatchID: "watch-1", EventKinds: []string{"checkpoint"}}, CallerExit{})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got.Revision != 9 {
		t.Errorf("revision = %d, want 9 (checkpoint, not progress or completed)", got.Revision)
	}
	if got.Status != string(domain.StatusNotifying) {
		t.Errorf("status = %q, want notifying", got.Status)
	}
	if len(writer.reads) != 0 {
		t.Errorf("readAt writes = %d, want 0: checkpoint is not a terminal verdict", len(writer.reads))
	}
	select {
	case <-timeout:
		t.Fatal("poll timeout fired; the wait returned by timeout, not by the event")
	default:
	}
	if len(source.subscribed) != 1 {
		t.Errorf("subscriptions = %d, want 1", len(source.subscribed))
	}
}

// TestWaitResultEventFilterCompletedDelivers проверяет, что несколько kinds в
// фильтре работают: несписочный progress пропускается, а completed с
// терминальным вердиктом пишет readAt.
func TestWaitResultEventFilterCompletedDelivers(t *testing.T) {
	svc, _, writer, source, _ := newWaitTestService()
	at := time.Date(2026, 11, 20, 12, 5, 0, 0, time.UTC)
	checks := map[string]domain.Check{"ci/test": {State: "COMPLETED", Conclusion: "SUCCESS", Required: true}}
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Kind: WaitEventProgress, Status: domain.StatusWatching,
		Verdict: domain.VerdictWatching, Revision: 8, At: at,
	})
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Kind: WaitEventCompleted, Status: domain.StatusClosed,
		Verdict: domain.VerdictMerged, Revision: 9, Checks: checks, At: at,
	})

	got, err := svc.Wait(context.Background(), "sub-A",
		WaitResultInput{WatchID: "watch-1", EventKinds: []string{"checkpoint", "completed"}}, CallerExit{})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got.Verdict != string(domain.VerdictMerged) || got.Revision != 9 {
		t.Errorf("result = %+v, want merged/9", got)
	}
	if len(writer.reads) != 1 {
		t.Errorf("readAt writes = %d, want 1", len(writer.reads))
	}
}

// TestWaitResultEventFilterOverridesVerdict отличает T3.102 от T3.48: без
// фильтра терминальный вердикт заканчивает ожидание, с фильтром его может
// закончить только событие нужного вида.
func TestWaitResultEventFilterOverridesVerdict(t *testing.T) {
	svc, _, writer, source, _ := newWaitTestService()
	at := time.Date(2026, 11, 20, 12, 10, 0, 0, time.UTC)
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Kind: WaitEventCompleted, Status: domain.StatusClosed,
		Verdict: domain.VerdictMerged, Revision: 8, At: at,
	})
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Kind: WaitEventProgress, Status: domain.StatusWatching,
		Verdict: domain.VerdictWatching, Revision: 9, At: at,
	})

	got, err := svc.Wait(context.Background(), "sub-A",
		WaitResultInput{WatchID: "watch-1", EventKinds: []string{"progress"}}, CallerExit{})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got.Revision != 9 || got.Verdict != string(domain.VerdictWatching) {
		t.Errorf("result = %+v, want watching/9 from the progress event", got)
	}
	if len(writer.reads) != 0 {
		t.Errorf("readAt writes = %d, want 0", len(writer.reads))
	}
}

// TestWaitResultEventFilterRejectsUnknownKind проверяет, что неверный kind
// отклоняется до подписки и до любой записи.
func TestWaitResultEventFilterRejectsUnknownKind(t *testing.T) {
	svc, _, _, source, _ := newWaitTestService()
	_, err := svc.Wait(context.Background(), "sub-A",
		WaitResultInput{WatchID: "watch-1", EventKinds: []string{"bogus"}}, CallerExit{})
	assertCode(t, err, domain.CodeInvalidInput)
	if len(source.subscribed) != 0 {
		t.Errorf("subscriptions = %d, want 0 for a rejected filter", len(source.subscribed))
	}
}

// TestWaitResultInputSchemaHasEventFilterAndStartKey проверяет, что оба новых
// поля видны MCP-клиенту в выведенной схеме.
func TestWaitResultInputSchemaHasEventFilterAndStartKey(t *testing.T) {
	schema, err := jsonschema.For[WaitResultInput](nil)
	if err != nil {
		t.Fatalf("input schema: %v", err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	for _, field := range []string{"eventKinds", "startKey"} {
		if !bytes.Contains(raw, []byte(`"`+field+`"`)) {
			t.Errorf("input schema has no %q property: %s", field, raw)
		}
	}
}

// TestWaitResultStartKeyJoinsFirstWait проверяет, что повторный старт с тем же
// ключом не открывает вторую задачу и возвращает первый исход.
func TestWaitResultStartKeyJoinsFirstWait(t *testing.T) {
	svc, _, writer, source, _ := newWaitTestService()
	at := time.Date(2026, 11, 20, 12, 15, 0, 0, time.UTC)
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Status: domain.StatusClosed, Verdict: domain.VerdictMerged,
		Revision: 8, At: at,
	})
	in := WaitResultInput{WatchID: "watch-1", StartKey: "start-1"}
	first, err := svc.Wait(context.Background(), "sub-A", in, CallerExit{})
	if err != nil {
		t.Fatalf("first Wait: %v", err)
	}

	// A lost response retries the start. Even a fresh update on the source must
	// not open a second wait.
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Status: domain.StatusClosed, Verdict: domain.VerdictReady,
		Revision: 9, At: at,
	})
	second, err := svc.Wait(context.Background(), "sub-A", in, CallerExit{})
	if err != nil {
		t.Fatalf("repeat Wait: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("repeat result = %+v, want %+v", second, first)
	}
	if second.Revision != 8 {
		t.Errorf("repeat revision = %d, want 8", second.Revision)
	}
	if len(source.subscribed) != 1 {
		t.Errorf("subscriptions = %d, want 1", len(source.subscribed))
	}
	if len(writer.reads) != 1 {
		t.Errorf("readAt writes = %d, want 1", len(writer.reads))
	}
}

// TestWaitResultDistinctStartKeysCreateDistinctTasks проверяет, что разные
// ключи дают разные ожидания и разные результаты.
func TestWaitResultDistinctStartKeysCreateDistinctTasks(t *testing.T) {
	svc, _, writer, source, _ := newWaitTestService()
	at := time.Date(2026, 11, 20, 12, 20, 0, 0, time.UTC)
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Status: domain.StatusClosed, Verdict: domain.VerdictMerged,
		Revision: 8, At: at,
	})
	first, err := svc.Wait(context.Background(), "sub-A",
		WaitResultInput{WatchID: "watch-1", StartKey: "start-1"}, CallerExit{})
	if err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Status: domain.StatusClosed, Verdict: domain.VerdictReady,
		Revision: 9, At: at,
	})
	second, err := svc.Wait(context.Background(), "sub-A",
		WaitResultInput{WatchID: "watch-1", StartKey: "start-2"}, CallerExit{})
	if err != nil {
		t.Fatalf("second Wait: %v", err)
	}
	if first.Verdict != string(domain.VerdictMerged) || second.Verdict != string(domain.VerdictReady) {
		t.Errorf("verdicts = %q/%q, want merged/ready", first.Verdict, second.Verdict)
	}
	if len(source.subscribed) != 2 {
		t.Errorf("subscriptions = %d, want 2", len(source.subscribed))
	}
	if len(writer.reads) != 2 {
		t.Errorf("readAt writes = %d, want 2", len(writer.reads))
	}
}

// TestWaitResultStartKeyDistinctFromClientRequestID показывает, что ключ
// старта работает отдельно от ключа команды из ADR 0028.
func TestWaitResultStartKeyDistinctFromClientRequestID(t *testing.T) {
	svc, _, writer, source, _ := newWaitTestService()
	at := time.Date(2026, 11, 20, 12, 25, 0, 0, time.UTC)
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Status: domain.StatusClosed, Verdict: domain.VerdictMerged,
		Revision: 8, At: at,
	})
	first, err := svc.Wait(context.Background(), "sub-A",
		WaitResultInput{WatchID: "watch-1", StartKey: "start-1", ClientRequestID: "req-1"}, CallerExit{})
	if err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	second, err := svc.Wait(context.Background(), "sub-A",
		WaitResultInput{WatchID: "watch-1", StartKey: "start-1", ClientRequestID: "req-2"}, CallerExit{})
	if err != nil {
		t.Fatalf("repeat Wait: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("repeat result = %+v, want %+v", second, first)
	}
	if len(source.subscribed) != 1 {
		t.Errorf("subscriptions = %d, want 1", len(source.subscribed))
	}
	if len(writer.reads) != 1 {
		t.Errorf("readAt writes = %d, want 1", len(writer.reads))
	}
}

// TestWaitResultFilterAndStartKeyCompose проверяет фильтр и ключ старта вместе.
func TestWaitResultFilterAndStartKeyCompose(t *testing.T) {
	svc, _, writer, source, timeout := newWaitTestService()
	at := time.Date(2026, 11, 20, 12, 30, 0, 0, time.UTC)
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Kind: WaitEventProgress, Status: domain.StatusWatching,
		Verdict: domain.VerdictWatching, Revision: 8, At: at,
	})
	source.publish(WaitVerdictUpdate{
		WatchID: "watch-1", Kind: WaitEventCheckpoint, Status: domain.StatusNotifying,
		Verdict: domain.VerdictWatching, Revision: 9, At: at,
	})
	in := WaitResultInput{WatchID: "watch-1", EventKinds: []string{"checkpoint"}, StartKey: "start-1"}
	first, err := svc.Wait(context.Background(), "sub-A", in, CallerExit{})
	if err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	second, err := svc.Wait(context.Background(), "sub-A", in, CallerExit{})
	if err != nil {
		t.Fatalf("repeat Wait: %v", err)
	}
	if first.Revision != 9 || !reflect.DeepEqual(first, second) {
		t.Errorf("results = %+v / %+v, want checkpoint/9 in both", first, second)
	}
	if len(writer.reads) != 0 {
		t.Errorf("readAt writes = %d, want 0", len(writer.reads))
	}
	select {
	case <-timeout:
		t.Fatal("poll timeout fired; the wait returned by timeout, not by the event")
	default:
	}
	if len(source.subscribed) != 1 {
		t.Errorf("subscriptions = %d, want 1", len(source.subscribed))
	}
}
