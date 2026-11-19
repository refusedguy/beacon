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
// It never changes the Watch verdict and returns no verdict. A client reads the
// terminal verdict separately with beacon_get_result or beacon_wait_result.
package mcp

import (
	"context"
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
}

// AckHandler implements beacon_ack over the read port and the transactional
// stamper. Writing claimedAt is the only effect of the tool. The handler never
// writes a Watch verdict or an obligation verdict pointer (ADR 0028).
type AckHandler struct {
	store   resultStoreReader
	stamper ObligationStamper
	clock   domain.Clock
}

// NewAckHandler builds a handler. A nil clock falls back to wall time.
func NewAckHandler(store resultStoreReader, stamper ObligationStamper, clock domain.Clock) *AckHandler {
	if clock == nil {
		clock = ackWallClock{}
	}
	return &AckHandler{store: store, stamper: stamper, clock: clock}
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
// Ack leaves the Watch verdict and the obligation verdict pointer untouched
// (ADR 0028).
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
	if _, _, err := h.stamper.StampClaimed(ctx, in.ObligationID, h.clock.Now()); err != nil {
		return err
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
