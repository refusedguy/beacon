// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements the wake tool (ADR 0026): it asks the environment how a
// follow-up was actually delivered and reports that honestly. The tool never
// claims a message reached the running turn. The wake ladder from ADR 0017 is
// unchanged; this tool only supplies the delivery value the ladder branches on.
//
// The tool is intentionally not registered in NewServer. Wiring it into a
// server is a separate integration task; AttachWake is the hook for it.
package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// WakeToolName is the name the wake tool is registered under when the
// integration task attaches it. It is deliberately absent from ToolNames.
const WakeToolName = "beacon_wake"

// Delivery is the honest outcome of one wake attempt (ADR 0026). steer means
// delivery into the running turn, queue means the message was queued, and
// unsupported means the environment cannot deliver to an open session.
type Delivery string

const (
	DeliverySteer       Delivery = "steer"
	DeliveryQueue       Delivery = "queue"
	DeliveryUnsupported Delivery = "unsupported"
)

// WakeInput is the argument object of the wake tool. Target is the opaque
// identifier of an open session. Text is a pointer to content, not the content
// itself: ADR 0017 requires repeaters to carry a pointer, never the payload.
type WakeInput struct {
	Target string `json:"target" jsonschema:"opaque identifier of the open session"`
	Text   string `json:"text" jsonschema:"pointer-only wake message, not check contents"`
}

// WakeOutput is the response of the wake tool. Delivery is mandatory and is
// one of steer, queue or unsupported. OK is false only for unsupported, so a
// refusal is never mistaken for a delivered follow-up (ADR 0026). Reason is
// populated on refusal and empty on a successful delivery.
type WakeOutput struct {
	Delivery Delivery `json:"delivery"`
	OK       bool     `json:"ok"`
	Reason   string   `json:"reason,omitempty"`
}

// WakeSender is the narrow port implemented by the environment adapter. It
// attempts one delivery and reports the method that actually happened. An
// adapter that cannot deliver to an open session returns DeliveryUnsupported
// with a reason; it must never report steer or queue in that case. The core
// does not know which concrete client implements the port (ADR 0001).
type WakeSender interface {
	Deliver(ctx context.Context, target, text string) (Delivery, string)
}

// WakeHandler implements the wake tool over a WakeSender. It holds no state
// beyond the sender and performs no I/O of its own.
type WakeHandler struct {
	sender WakeSender
}

// NewWakeHandler builds a handler over sender. A nil sender is allowed here and
// is reported as unsupported at call time, so a misconfigured environment
// cannot fake a successful delivery.
func NewWakeHandler(sender WakeSender) *WakeHandler {
	return &WakeHandler{sender: sender}
}

// Handle adapts Wake to the go-sdk typed tool handler signature.
func (h *WakeHandler) Handle(ctx context.Context, _ *mcpsdk.CallToolRequest, in WakeInput) (*mcpsdk.CallToolResult, WakeOutput, error) {
	out, err := h.Wake(ctx, in)
	if err != nil {
		return nil, WakeOutput{}, err
	}
	return &mcpsdk.CallToolResult{}, out, nil
}

// Wake performs one delivery and returns an honest receipt. Input is validated
// at this boundary; steer and queue are success, while unsupported and any
// unknown delivery value are a refusal with a reason and OK=false (ADR 0026).
func (h *WakeHandler) Wake(ctx context.Context, in WakeInput) (WakeOutput, error) {
	if in.Target == "" || in.Text == "" {
		return WakeOutput{}, domain.NewAppError(domain.CodeInvalidInput, "wake: target and text are required")
	}
	if h == nil || h.sender == nil {
		return WakeOutput{
			Delivery: DeliveryUnsupported,
			OK:       false,
			Reason:   "wake: no delivery sender configured",
		}, nil
	}

	delivery, reason := h.sender.Deliver(ctx, in.Target, in.Text)
	switch delivery {
	case DeliverySteer, DeliveryQueue:
		return WakeOutput{Delivery: delivery, OK: true}, nil
	case DeliveryUnsupported:
		if reason == "" {
			reason = "wake: delivery to an open session is not supported"
		}
		return WakeOutput{Delivery: DeliveryUnsupported, OK: false, Reason: reason}, nil
	default:
		// An unknown value is never promoted to success. Report it as an
		// unsupported delivery with a reason so the caller can branch safely.
		return WakeOutput{
			Delivery: DeliveryUnsupported,
			OK:       false,
			Reason:   fmt.Sprintf("wake: unknown delivery value %q", string(delivery)),
		}, nil
	}
}

// AttachWake registers the real wake tool on a server. NewServer keeps the
// contracted stub set and does not call this function; registration is a
// separate integration task.
func AttachWake(server *mcpsdk.Server, handler *WakeHandler) {
	if server == nil || handler == nil {
		return
	}
	server.RemoveTools(WakeToolName)
	no := false
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        WakeToolName,
		Description: "Wake an open session and report the delivery actually used: steer, queue or unsupported.",
		Annotations: &mcpsdk.ToolAnnotations{
			DestructiveHint: &no,
			IdempotentHint:  false,
		},
	}, handler.Handle)
}
