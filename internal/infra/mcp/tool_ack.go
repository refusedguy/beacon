// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements beacon_ack (WORKER-PROMPT §4.1): a single additive tool
// that writes claimedAt on one obligation. It is idempotent: a repeat leaves the
// original timestamp untouched. There is no second ack tool. readAt is set by
// the automatic issuing of content (wait_result / get_result), never by ack
// (WORKER-PROMPT §4.3), and either fact stops the wake ladder (ADR 0017).
//
// ADR 0028 fixes the meaning of the acknowledgement: an ack records a written
// intention, not a finished result. The tool confirms that the claim was stored.
// It returns no verdict. A client reads the terminal verdict separately with
// beacon_get_result or beacon_wait_result.
//
// A plain ack never changes the Watch. An ack that carries continueOf resumes a
// parked needs_input Watch at the next answer round, capped at five rounds by
// ADR 0031 §3. The answer goes back into the same Watch; no new task is made.
package mcp

import (
	"context"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// AckInput is the argument object of beacon_ack. The obligationID is the
// capability being acknowledged; watchID must name the same Watch. Neither
// field asks for a verdict: the ack only confirms that the claim was stored
// (ADR 0028).
type AckInput struct {
	WatchID      string `json:"watchID" jsonschema:"identifier of the Watch whose intention is being recorded"`
	ObligationID string `json:"obligationID" jsonschema:"identifier of the obligation to claim; claiming records an intention and does not return a verdict"`
	// ContinueOf names the same Watch when the ack carries a human answer to a
	// parked needs_input task (ADR 0031 §3). It must equal WatchID: answering
	// resumes that task and never creates a new one. Empty keeps the plain ack
	// contract from ADR 0028.
	ContinueOf string `json:"continueOf,omitempty" jsonschema:"watchID of the same parked task whose human answer resumes it; must equal watchID and creates no new task"`
}

// ContinuationRegistry counts human-answer rounds per Watch. A round is
// consumed only when an ack with continueOf is accepted, and the count never
// passes domain.MaxContinueRounds (ADR 0031 §3). It is safe for concurrent use.
type ContinuationRegistry struct {
	mu     sync.Mutex
	rounds map[string]uint64
}

// NewContinuationRegistry builds an empty round counter.
func NewContinuationRegistry() *ContinuationRegistry {
	return &ContinuationRegistry{rounds: make(map[string]uint64)}
}

// Next reserves and returns the next answer round for watchID. A sixth answer
// is refused with CodeLimitExceeded and consumes no round, so a later retry
// cannot slip past the cap.
func (r *ContinuationRegistry) Next(watchID string) (uint64, error) {
	if r == nil {
		return 0, domain.NewAppError(domain.CodePersistFailed, "ack: continuation registry is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rounds == nil {
		r.rounds = make(map[string]uint64)
	}
	next := r.rounds[watchID] + 1
	if next > domain.MaxContinueRounds {
		return 0, domain.NewAppError(domain.CodeLimitExceeded, "ack: continuation round limit reached for Watch "+watchID)
	}
	r.rounds[watchID] = next
	return next, nil
}

// Rounds reports the number of accepted answer rounds for watchID.
func (r *ContinuationRegistry) Rounds(watchID string) uint64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rounds[watchID]
}

// ackWatchWriter is the optional write side a composition root can supply to
// persist the resumed Watch. The read port alone stays unchanged: a store that
// cannot write a Watch still gets the idempotency and round checks, but the
// transition is not persisted.
type ackWatchWriter interface {
	SaveWatch(domain.Watch) error
}

// AckHandler implements beacon_ack over the read port and the transactional
// stamper. Writing claimedAt is the only effect of a plain ack. With continueOf
// it also applies the domain continue transition to the same parked Watch. The
// handler never writes an obligation verdict pointer (ADR 0028).
type AckHandler struct {
	store         resultStoreReader
	stamper       ObligationStamper
	clock         domain.Clock
	continuations *ContinuationRegistry
}

// NewAckHandler builds a handler. A nil clock falls back to wall time.
func NewAckHandler(store resultStoreReader, stamper ObligationStamper, clock domain.Clock) *AckHandler {
	if clock == nil {
		clock = ackWallClock{}
	}
	return &AckHandler{
		store:         store,
		stamper:       stamper,
		clock:         clock,
		continuations: NewContinuationRegistry(),
	}
}

// SetContinuationRegistry shares the round counter with a composition root, so
// other tools can observe continuation rounds. A nil argument leaves the
// handler's own counter in place.
func (h *AckHandler) SetContinuationRegistry(r *ContinuationRegistry) {
	if h != nil && r != nil {
		h.continuations = r
	}
}

type ackWallClock struct{}

func (ackWallClock) Now() time.Time { return time.Now() }

// Handle adapts Ack to the go-sdk typed tool handler signature and returns the
// shared ok-only response. The response carries no verdict: a caller still has
// to call beacon_get_result or beacon_wait_result to read one (ADR 0028).
func (h *AckHandler) Handle(ctx context.Context, _ *mcpsdk.CallToolRequest, in AckInput) (*mcpsdk.CallToolResult, OKOutput, error) {
	if err := h.Ack(ctx, in); err != nil {
		return nil, OKOutput{}, err
	}
	return nil, OKOutput{OK: true}, nil
}

// Ack validates that the obligation belongs to the Watch, then stamps
// claimedAt once. A repeated call observes the existing timestamp and returns
// success without writing again. Both the read and the write happen before the
// ok response is returned (ADR 0015, ADR 0022). The stamp records an intention;
// Ack leaves the obligation verdict pointer untouched (ADR 0028).
//
// A non-empty ContinueOf additionally resumes a parked needs_input Watch at
// the next answer round. It must equal WatchID, so the human answer goes back
// into the same task and no new Watch is created (ADR 0031 §3). A plain ack
// without ContinueOf never changes the Watch verdict or status.
func (h *AckHandler) Ack(ctx context.Context, in AckInput) error {
	if in.WatchID == "" || in.ObligationID == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "ack: watchID and obligationID are required")
	}
	if h == nil || h.store == nil || h.stamper == nil {
		return domain.NewAppError(domain.CodePersistFailed, "ack: store or stamper is nil")
	}

	o, err := h.store.Obligation(in.ObligationID)
	if err != nil {
		return mapResultReadError(err, "ack: obligation")
	}
	if o.WatchID != in.WatchID {
		return domain.NewAppError(domain.CodeInvalidInput, "ack: obligation does not belong to the Watch")
	}
	if in.ContinueOf != "" {
		if err := h.continueWatch(in); err != nil {
			return err
		}
	}
	if _, _, err := h.stamper.StampClaimed(ctx, in.ObligationID, h.clock.Now()); err != nil {
		return err
	}
	return nil
}

// continueWatch applies the domain continue transition to the same Watch. It
// consumes one answer round before touching the Watch, so a refused round
// leaves both the Watch and the obligation unchanged. The domain transition
// never emits EffectCreateWatch; that invariant is checked here as well.
func (h *AckHandler) continueWatch(in AckInput) error {
	if in.ContinueOf != in.WatchID {
		return domain.NewAppError(domain.CodeInvalidInput, "ack: continueOf must name the same Watch")
	}
	round, err := h.continuations.Next(in.WatchID)
	if err != nil {
		return err
	}
	w, err := h.store.Watch(in.WatchID)
	if err != nil {
		return mapResultReadError(err, "ack: watch")
	}
	next, effects := domain.Transition(w, domain.ContinueEvent(in.ContinueOf, round, h.clock.Now()))
	if next.Status != domain.StatusWatching || next.Revision == w.Revision {
		return domain.NewAppError(domain.CodeLimitExceeded, "ack: Watch is not parked for input or the answer round is invalid")
	}
	for _, effect := range effects {
		if effect.Kind == domain.EffectCreateWatch {
			return domain.NewAppError(domain.CodeInvalidInput, "ack: continueOf must not create a new Watch")
		}
	}
	if writer, ok := h.store.(ackWatchWriter); ok {
		if err := writer.SaveWatch(next); err != nil {
			return domain.NewAppError(domain.CodePersistFailed, err.Error())
		}
	}
	return nil
}

// LadderStopped reports whether a delivery fact already ends the wake ladder
// for an obligation. ADR 0017 says read stops everything; the T1.4 wake-ledger
// model likewise gives no further attempts once the content was issued or
// claimed. Only a transport-failed obligation with neither fact may retry, and
// only within its three-attempt cap.
func LadderStopped(o domain.Obligation) bool {
	return !o.ReadAt.IsZero() || !o.ClaimedAt.IsZero()
}

// AttachAck replaces the startup stub with the real tool. It registers exactly
// one ack tool; wiring it into NewServer is a separate integration task.
func AttachAck(server *mcpsdk.Server, handler *AckHandler) {
	if server == nil || handler == nil {
		return
	}
	server.RemoveTools("beacon_ack")
	no := false
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "beacon_ack",
		Description: "Record the intention to claim one obligation by stamping claimedAt; idempotent. This is an acknowledgement, not a result: read the verdict with beacon_get_result or beacon_wait_result.",
		Annotations: &mcpsdk.ToolAnnotations{
			DestructiveHint: &no,
			IdempotentHint:  true,
		},
	}, handler.Handle)
}
