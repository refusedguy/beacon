// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements beacon_wait_result: a cancel-safe, bounded long-poll
// (WORKER-PROMPT §4.1 and §4.2, ADR 0003, ADR 0019). A terminal verdict marks
// the calling subscriber's obligation read inside one transaction before the
// response is returned. A timeout returns status=watching with no side effects.
// All three ways the caller can leave (notifications/cancelled, a closed
// session, a broken connection) are separate select cases and write nothing.
//
// A non-empty clientRequestId makes a call idempotent (ADR 0028): a repeat
// after a broken connection returns the first result without running the wait
// again or marking the obligation read a second time.
//
// A Watch parked in needs_input returns early with that status and marks no
// obligation read (ADR 0031 §3, ADR 0028 §3). After the human answers through
// ack(continueOf), the same Watch can be waited on again: continueOf names that
// Watch so the wait resumes the existing task instead of creating a new one.
//
// The tool is deliberately not registered in NewServer; wiring it into the
// server is a separate integration task. AttachWaitResult is the hook that task
// will call.
package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// waitResultDefaultTimeoutMs is the contract default when timeoutMs is
	// absent or not positive (WORKER-PROMPT §4.1).
	waitResultDefaultTimeoutMs int64 = 50000

	// waitResultMaxTimeoutMs caps a long-poll at nine minutes. The go-sdk has
	// no five-worker stdio pool, so a call may legitimately live this long
	// (ADR 0003).
	waitResultMaxTimeoutMs int64 = 540000

	// waitResultPollAfterMs is the hint returned to a client that must poll
	// again. It is not a timeout or a deadline.
	waitResultPollAfterMs int64 = 5000
)

// WaitResultInput is the argument object of beacon_wait_result. timeoutMs is
// optional; the service applies the default and the nine-minute cap.
// clientRequestId is the idempotency key (ADR 0028). When it is non-empty, a
// repeat with the same key returns the first result and performs no second
// effect. An empty key keeps the pre-idempotency behaviour for callers that do
// not send the field.
type WaitResultInput struct {
	WatchID         string `json:"watchID" jsonschema:"identifier of the Watch to wait on"`
	TimeoutMs       *int64 `json:"timeoutMs,omitempty" jsonschema:"wait budget in milliseconds; default 50000, capped at 540000"`
	ClientRequestID string `json:"clientRequestId,omitempty" jsonschema:"idempotency key; a repeat after a broken connection returns the first result without a second effect"`
	// ContinueOf names the same Watch after a human answer resumed a parked
	// needs_input task (ADR 0031 §3). The wait keeps polling that Watch; it
	// never creates a new task. An empty value keeps the original contract.
	ContinueOf string `json:"continueOf,omitempty" jsonschema:"watchID of the same task this wait continues after a human answer; must equal watchID and creates no new task"`
}

// WaitResultIdempotencyStore records the outcome of an idempotent wait_result
// call by clientRequestId. Lookup reports whether a result was already
// recorded; Record stores one. Implementations must be safe for concurrent use.
// The in-memory implementation below is the default. A composition root can
// replace it with a transactional store that writes the index in the same
// transaction as the obligation state (ADR 0028, ADR 0015).
type WaitResultIdempotencyStore interface {
	Lookup(clientRequestID string) (WaitResultOutput, bool)
	Record(clientRequestID string, out WaitResultOutput)
}

// memoryWaitResultIdempotency is the in-process default. It keeps one result
// per clientRequestId for the lifetime of the service.
type memoryWaitResultIdempotency struct {
	mu      sync.Mutex
	results map[string]WaitResultOutput
}

// NewMemoryWaitResultIdempotency builds the default in-process store.
func NewMemoryWaitResultIdempotency() WaitResultIdempotencyStore {
	return &memoryWaitResultIdempotency{results: make(map[string]WaitResultOutput)}
}

func (m *memoryWaitResultIdempotency) Lookup(clientRequestID string) (WaitResultOutput, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out, ok := m.results[clientRequestID]
	if !ok {
		return WaitResultOutput{}, false
	}
	return cloneWaitResultOutput(out), true
}

func (m *memoryWaitResultIdempotency) Record(clientRequestID string, out WaitResultOutput) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.results[clientRequestID] = cloneWaitResultOutput(out)
}

// cloneWaitResultOutput copies the reference fields so the store owns its
// result and each caller receives its own value. A caller mutating Checks or
// PollAfterMs cannot change what a later repeat returns.
func cloneWaitResultOutput(out WaitResultOutput) WaitResultOutput {
	clone := out
	if out.Checks != nil {
		clone.Checks = make(map[string]domain.Check, len(out.Checks))
		for name, check := range out.Checks {
			clone.Checks[name] = check
		}
	}
	if out.PollAfterMs != nil {
		ms := *out.PollAfterMs
		clone.PollAfterMs = &ms
	}
	return clone
}

// WaitVerdictUpdate is one verdict change delivered by a VerdictSource. It is
// the input to the long-poll loop; a watching update carries the current
// revision and checks but does not end the wait.
type WaitVerdictUpdate struct {
	WatchID  string
	Status   domain.Status
	Verdict  domain.Verdict
	Revision uint64
	Checks   map[string]domain.Check
	At       time.Time
}

// VerdictSubscription is one caller's stream of verdict updates. Close always
// releases the subscription, including when the wait ends on a timeout or a
// caller departure.
type VerdictSubscription interface {
	Updates() <-chan WaitVerdictUpdate
	Close()
}

// VerdictSource opens a subscription for one Watch. It is a port so the wait
// loop can be tested without the poller or the daemon. Production wiring
// implements it over verdict-engine events; that wiring is not part of this
// task.
type VerdictSource interface {
	Subscribe(watchID string) VerdictSubscription
}

// CallerExit holds the three independent ways the requesting party can leave.
// A nil channel is disabled in a select, so tests can arm exactly one channel.
// The go-sdk cancels the handler context on all three in production; a finer
// grained bridge can supply distinct channels through SetDepartureProvider.
type CallerExit struct {
	Cancelled        <-chan struct{} // notifications/cancelled
	SessionClosed    <-chan struct{} // the MCP session was closed
	ConnectionBroken <-chan struct{} // the transport connection broke
}

// callerExitFromContext is the default bridge: the go-sdk cancels ctx on a
// cancellation notification, on session close and on a broken reader. All
// three channels point at ctx.Done, so the wait still has one select case per
// departure and never mutates state on any of them.
func callerExitFromContext(ctx context.Context) CallerExit {
	done := ctx.Done()
	return CallerExit{Cancelled: done, SessionClosed: done, ConnectionBroken: done}
}

// WaitResultWriter marks one obligation read. The implementation must commit
// the mutation before returning so the response is never sent ahead of the
// disk write (WORKER-PROMPT §4.2 non-invariant 7).
type WaitResultWriter interface {
	MarkRead(ctx context.Context, obligationID string, at time.Time) error
}

// waitResultStore is the read side of the store needed by the wait. The write
// side goes through WaitResultWriter so readAt is one atomic transaction.
type waitResultStore interface {
	Watch(id string) (domain.Watch, error)
}

// waitTxWriter is the store's single-writer port. It matches *store.DB.Write
// without importing the store implementation.
type waitTxWriter interface {
	Write(ctx context.Context, fn func(*sql.Tx) error) error
}

// sqlWaitResultWriter is the production writer. It updates the obligation's
// payload inside one transaction and does nothing when readAt is already set,
// so repeated reads stay idempotent.
type sqlWaitResultWriter struct {
	w waitTxWriter
}

// NewSQLWaitResultWriter builds the production writer over the store's single
// writer connection.
func NewSQLWaitResultWriter(w waitTxWriter) WaitResultWriter {
	return &sqlWaitResultWriter{w: w}
}

func (w *sqlWaitResultWriter) MarkRead(ctx context.Context, obligationID string, at time.Time) error {
	if w == nil || w.w == nil {
		return domain.NewAppError(domain.CodePersistFailed, "wait_result: obligation writer is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return w.w.Write(ctx, func(tx *sql.Tx) error {
		var payload []byte
		err := tx.QueryRow(`SELECT payload FROM obligations WHERE id = ?`, obligationID).Scan(&payload)
		if err != nil {
			return err
		}
		var o domain.Obligation
		if err := json.Unmarshal(payload, &o); err != nil {
			return err
		}
		if !o.ReadAt.IsZero() {
			return nil
		}
		o.ReadAt = at
		updated, err := json.Marshal(o)
		if err != nil {
			return err
		}
		result, err := tx.Exec(`UPDATE obligations SET payload = ? WHERE id = ?`, updated, obligationID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.NewAppError(domain.CodePersistFailed, "wait_result: obligation "+obligationID+" was not found")
		}
		return nil
	})
}

// WaitResultService is the cancel-safe long-poll. It is transport agnostic:
// the caller supplies the departure signals, the verdict subscription and the
// transactional writer through ports.
type WaitResultService struct {
	store       waitResultStore
	writer      WaitResultWriter
	source      VerdictSource
	clock       domain.Clock
	after       func(time.Duration) <-chan time.Time
	departures  func(*mcpsdk.CallToolRequest) CallerExit
	idempotency WaitResultIdempotencyStore
}

// NewWaitResultService builds a service. A nil clock falls back to the wall
// clock. Tests may replace after to drive or observe the timeout precisely.
// The default idempotency store is in memory; use SetIdempotencyStore to swap
// in a transactional one.
func NewWaitResultService(store waitResultStore, writer WaitResultWriter, source VerdictSource, clock domain.Clock) *WaitResultService {
	if clock == nil {
		clock = waitResultWallClock{}
	}
	return &WaitResultService{
		store:       store,
		writer:      writer,
		source:      source,
		clock:       clock,
		after:       time.After,
		idempotency: NewMemoryWaitResultIdempotency(),
	}
}

// SetIdempotencyStore replaces the result store. A nil argument disables
// idempotency, which is useful for a caller that manages keys itself.
func (s *WaitResultService) SetIdempotencyStore(store WaitResultIdempotencyStore) {
	if s != nil {
		s.idempotency = store
	}
}

// SetDepartureProvider lets integration supply distinct departure channels
// (for example a bridge that watches the Session and the transport). Without
// one, the handler derives all three from the handler context.
func (s *WaitResultService) SetDepartureProvider(fn func(*mcpsdk.CallToolRequest) CallerExit) {
	if s != nil {
		s.departures = fn
	}
}

type waitResultWallClock struct{}

func (waitResultWallClock) Now() time.Time { return time.Now() }

// effectiveTimeoutMs applies the contract default and the nine-minute cap.
// A non-positive request falls back to the default rather than busy-waiting.
func effectiveTimeoutMs(requested *int64) int64 {
	if requested == nil || *requested <= 0 {
		return waitResultDefaultTimeoutMs
	}
	if *requested > waitResultMaxTimeoutMs {
		return waitResultMaxTimeoutMs
	}
	return *requested
}

// isWaitResultVerdict reports whether a verdict ends the wait. Only watching
// keeps the caller parked; every other verdict is a result that delivers
// content and therefore marks the obligation read. Cancelled stays closed with
// a watching verdict and is handled through CloseReason before this check.
func isWaitResultVerdict(v domain.Verdict) bool {
	switch v {
	case domain.VerdictReady, domain.VerdictRebase, domain.VerdictRetry,
		domain.VerdictKeepWorking, domain.VerdictTimeout,
		domain.VerdictMerged, domain.VerdictClosed:
		return true
	default:
		return false
	}
}

// Wait runs the bounded long-poll with clientRequestId idempotency. A non-empty
// key whose result was already recorded returns that result immediately without
// running the wait, so no obligation is marked read a second time (ADR 0028).
// Only successful outcomes are recorded: a departure or an error leaves no key
// behind, so a later retry can still run the wait and produce a result.
func (s *WaitResultService) Wait(ctx context.Context, subscriberID string, in WaitResultInput, exits CallerExit) (WaitResultOutput, error) {
	if s == nil || in.ClientRequestID == "" || s.idempotency == nil {
		return s.wait(ctx, subscriberID, in, exits)
	}
	if out, ok := s.idempotency.Lookup(in.ClientRequestID); ok {
		return out, nil
	}
	out, err := s.wait(ctx, subscriberID, in, exits)
	if err == nil {
		s.idempotency.Record(in.ClientRequestID, out)
	}
	return out, err
}

// wait runs the bounded long-poll. A result verdict writes readAt through the
// writer and returns; a timeout or any caller departure returns without a
// write, so no obligation is created and no delivery is marked.
func (s *WaitResultService) wait(ctx context.Context, subscriberID string, in WaitResultInput, exits CallerExit) (WaitResultOutput, error) {
	if in.WatchID == "" {
		return WaitResultOutput{}, domain.NewAppError(domain.CodeInvalidInput, "wait_result: watchID is required")
	}
	if in.ContinueOf != "" && in.ContinueOf != in.WatchID {
		return WaitResultOutput{}, domain.NewAppError(domain.CodeInvalidInput, "wait_result: continueOf must name the same Watch")
	}
	if s == nil || s.store == nil {
		return WaitResultOutput{}, domain.NewAppError(domain.CodePersistFailed, "wait_result: store is nil")
	}

	w, err := s.store.Watch(in.WatchID)
	if err != nil {
		return WaitResultOutput{}, domain.NewAppError(domain.CodeWatchNotFound, "wait_result: watch "+in.WatchID+" not found")
	}

	// A parked Watch needs an answer, not a result. Return before any readAt
	// write so the caller can answer through ack(continueOf) and wait again on
	// this same Watch. An empty ContinueOf keeps the original contract.
	if isNeedsInput(w.Status, w.Verdict) {
		return needsInputOutput(w.Revision, w.Checks), nil
	}

	// A result that landed before the call is delivered immediately. A Watch
	// closed by cancel or head change is not a result and writes nothing.
	if isWaitResultVerdict(w.Verdict) {
		return s.deliver(ctx, in.WatchID, w.Status, w.Verdict, w.Revision, w.Checks, subscriberID, w.ClosedAt)
	}
	if w.Status == domain.StatusClosed {
		if w.CloseReason == string(domain.CloseReasonCancelled) {
			return WaitResultOutput{}, domain.NewAppError(domain.CodeCancelled, "wait_result: watch was cancelled")
		}
		return WaitResultOutput{}, domain.NewAppError(domain.CodeGone, "wait_result: watch is closed")
	}

	var sub VerdictSubscription
	if s.source != nil {
		sub = s.source.Subscribe(in.WatchID)
	}
	if sub == nil {
		return WaitResultOutput{}, domain.NewAppError(domain.CodePersistFailed, "wait_result: verdict source is nil")
	}
	defer sub.Close()

	updates := sub.Updates()
	revision := w.Revision
	checks := w.Checks
	after := s.after
	if after == nil {
		after = time.After
	}
	timeoutCh := after(time.Duration(effectiveTimeoutMs(in.TimeoutMs)) * time.Millisecond)

	for {
		select {
		case <-ctx.Done():
			return WaitResultOutput{}, domain.NewAppError(domain.CodeCancelled, "wait_result: caller context ended")
		case <-exits.Cancelled:
			return WaitResultOutput{}, domain.NewAppError(domain.CodeCancelled, "wait_result: caller cancelled")
		case <-exits.SessionClosed:
			return WaitResultOutput{}, domain.NewAppError(domain.CodeGone, "wait_result: session closed")
		case <-exits.ConnectionBroken:
			return WaitResultOutput{}, domain.NewAppError(domain.CodeGone, "wait_result: connection broken")
		case <-timeoutCh:
			return watchingOutput(revision, checks), nil
		case update, ok := <-updates:
			if !ok {
				// The source ended; do not invent a result or a write.
				return watchingOutput(revision, checks), nil
			}
			if update.WatchID != in.WatchID {
				continue
			}
			revision = update.Revision
			checks = update.Checks
			if isNeedsInput(update.Status, update.Verdict) {
				// The agent just asked a human a question. Park here without
				// marking the obligation read; an ack(continueOf) resumes this
				// same Watch and the next wait carries on from its new revision.
				return needsInputOutput(update.Revision, update.Checks), nil
			}
			if !isWaitResultVerdict(update.Verdict) {
				// A watching tick moves no revision and wakes nobody.
				continue
			}
			status := update.Status
			if status == "" {
				status = domain.StatusNotifying
			}
			return s.deliver(ctx, in.WatchID, status, update.Verdict, update.Revision, update.Checks, subscriberID, update.At)
		}
	}
}

// watchingOutput is the side-effect-free response for a timeout or an ended
// source. pollAfterMs tells the caller when to ask again.
func watchingOutput(revision uint64, checks map[string]domain.Check) WaitResultOutput {
	ms := waitResultPollAfterMs
	return WaitResultOutput{
		Status:      string(domain.StatusWatching),
		Verdict:     string(domain.VerdictWatching),
		Revision:    revision,
		Checks:      checks,
		PollAfterMs: &ms,
	}
}

// needsInputOutput is the side-effect-free response for a Watch parked while
// the agent waits for a human. It is not a verdict: no obligation is marked read,
// so the caller can answer and come back to the same Watch (ADR 0031 §3).
func needsInputOutput(revision uint64, checks map[string]domain.Check) WaitResultOutput {
	ms := waitResultPollAfterMs
	return WaitResultOutput{
		Status:      string(domain.StatusNeedsInput),
		Verdict:     string(domain.VerdictNeedsInput),
		Revision:    revision,
		Checks:      checks,
		PollAfterMs: &ms,
	}
}

// isNeedsInput reports whether the Watch is parked for a human answer. The
// status is authoritative; the verdict is also checked for a source that only
// stamps the verdict on an update.
func isNeedsInput(status domain.Status, verdict domain.Verdict) bool {
	return status == domain.StatusNeedsInput || verdict == domain.VerdictNeedsInput
}

// deliver writes readAt on the caller's obligation at the result revision, then
// builds the response. The writer is synchronous, so the disk mutation always
// precedes the returned result (WORKER-PROMPT §4.2 non-invariant 7).
func (s *WaitResultService) deliver(ctx context.Context, watchID string, status domain.Status, verdict domain.Verdict, revision uint64, checks map[string]domain.Check, subscriberID string, at time.Time) (WaitResultOutput, error) {
	if s.writer == nil {
		return WaitResultOutput{}, domain.NewAppError(domain.CodePersistFailed, "wait_result: obligation writer is nil")
	}
	if at.IsZero() {
		at = s.now()
	}
	obligationID := domain.ObligationKey(watchID, revision, subscriberID)
	if err := s.writer.MarkRead(ctx, obligationID, at); err != nil {
		return WaitResultOutput{}, domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	ms := waitResultPollAfterMs
	return WaitResultOutput{
		Status:      string(status),
		Verdict:     string(verdict),
		Revision:    revision,
		Checks:      checks,
		PollAfterMs: &ms,
	}, nil
}

func (s *WaitResultService) now() time.Time {
	if s.clock == nil {
		return time.Now()
	}
	return s.clock.Now()
}

// Handle adapts the service to the go-sdk typed tool handler signature. The
// caller id is the opaque sessionID from _meta and is never interpreted.
func (s *WaitResultService) Handle(ctx context.Context, req *mcpsdk.CallToolRequest, in WaitResultInput) (*mcpsdk.CallToolResult, WaitResultOutput, error) {
	subscriberID := ""
	if id := SessionIDFromRequest(req); id != nil {
		subscriberID = *id
	}
	exits := callerExitFromContext(ctx)
	if s != nil && s.departures != nil {
		exits = s.departures(req)
	}
	out, err := s.Wait(ctx, subscriberID, in, exits)
	return &mcpsdk.CallToolResult{}, out, err
}

// AttachWaitResult registers the real tool on a server. NewServer keeps the
// stub; integration chooses when to call this helper.
func AttachWaitResult(server *mcpsdk.Server, svc *WaitResultService) {
	if server == nil || svc == nil {
		return
	}
	server.RemoveTools("beacon_wait_result")
	no := false
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "beacon_wait_result",
		Description: "Bounded long-poll for a Watch result; a terminal verdict marks the caller's obligation read.",
		Annotations: &mcpsdk.ToolAnnotations{
			DestructiveHint: &no,
			IdempotentHint:  true,
		},
	}, svc.Handle)
}
