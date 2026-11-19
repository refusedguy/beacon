// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements the wake tool (ADR 0026): it asks the environment how a
// follow-up was actually delivered and reports that honestly. The tool never
// claims a message reached the running turn. The wake ladder from ADR 0017 is
// unchanged; this tool only supplies the delivery value the ladder branches on.
//
// ADR 0028 adds an interruption path: when the agent needs a human (pending
// question or pending approval), wake returns before any delivery attempt with
// DeliveryInterrupted, Interrupted=true and a reason. A waiting manager stops
// early and hands control to the human; no ladder attempt is consumed.
//
// ADR 0030 p6 adds the message source. A diff comment is a review message and
// travels through the same steer channel ADR 0026 defines; review is the
// source, not a new delivery mode. The source is carried as metadata in the
// pointer, so the environment adapter can route it to steer and the plugin can
// record it on the receipt (ADR 0014).
//
// ADR 0031 p4 adds the current executor. The wake tool does not own the
// fallback chain; it only echoes the executor supplied by the CLI manager so a
// handover after a 429 stays visible in the wake receipt and in status.
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

// Delivery is the honest outcome of one wake attempt (ADR 0026 and 0028).
// steer means delivery into the running turn, queue means the message was
// queued, unsupported means the environment cannot deliver to an open session,
// and interrupted means waiting stopped because the agent needs a human.
type Delivery string

const (
	DeliverySteer       Delivery = "steer"
	DeliveryQueue       Delivery = "queue"
	DeliveryUnsupported Delivery = "unsupported"
	DeliveryInterrupted Delivery = "interrupted"
)

// WakeSource is where a follow-up message came from (ADR 0030 p6). SourceReview
// is a diff comment: it is delivered through the steer channel of ADR 0026 and
// differs from a manual nudge only in its source. SourceManual is the zero
// value's meaning, so older callers that omit source keep working.
type WakeSource string

const (
	SourceManual WakeSource = "manual"
	SourceReview WakeSource = "review"
)

// normalizeSource gives an absent source its documented default. Unknown
// values are not guessed; Wake rejects them at the boundary.
func normalizeSource(source WakeSource) WakeSource {
	if source == "" {
		return SourceManual
	}
	return source
}

// validSource reports whether source is one of the two defined values.
func validSource(source WakeSource) bool {
	switch normalizeSource(source) {
	case SourceManual, SourceReview:
		return true
	default:
		return false
	}
}

// WakeInput is the argument object of the wake tool. Target is the opaque
// identifier of an open session. Text is a pointer to content, not the content
// itself: ADR 0017 requires repeaters to carry a pointer, never the payload.
// HasPendingQuestion and PendingApproval (ADR 0028) report that the agent needs
// a human; when either is set wake interrupts the wait instead of delivering.
// Source (ADR 0030 p6) names the message origin; review means a diff comment.
type WakeInput struct {
	Target             string     `json:"target" jsonschema:"opaque identifier of the open session"`
	Text               string     `json:"text" jsonschema:"pointer-only wake message, not check contents"`
	HasPendingQuestion bool       `json:"hasPendingQuestion" jsonschema:"agent has a pending question that needs a human"`
	PendingApproval    bool       `json:"pendingApproval" jsonschema:"agent is waiting for human approval"`
	Source             WakeSource `json:"source" jsonschema:"message source: manual or review (diff comment)"`
	// Executor is the agent currently executing the job this wake follows up
	// on (ADR 0031 p4). It is metadata, not check contents. The CLI manager owns
	// the fallback chain; the core only echoes the value on the output.
	Executor string `json:"executor,omitempty" jsonschema:"agent currently executing the job (fallback chain)"`
}

// WakeOutput is the response of the wake tool. Delivery is mandatory and is
// one of steer, queue, unsupported or interrupted. OK is true only when a
// follow-up was actually delivered (steer or queue); it is false for unsupported
// and interrupted, so a refusal or an interruption is never mistaken for a
// delivered follow-up (ADR 0026). Reason is populated on refusal and
// interruption and empty on a successful delivery. Interrupted is true only
// when the wait was cut short because the agent needs a human (ADR 0028).
// Source echoes the normalized message source so a caller can put it on the
// ADR 0014 receipt: review means the follow-up was a diff comment. Executor
// echoes the current executor supplied by the caller, so a handover after a
// 429 is visible in the wake status (ADR 0031 p4).
type WakeOutput struct {
	Delivery    Delivery   `json:"delivery"`
	OK          bool       `json:"ok"`
	Source      WakeSource `json:"source"`
	Executor    string     `json:"executor,omitempty"`
	Interrupted bool       `json:"interrupted,omitempty"`
	Reason      string     `json:"reason,omitempty"`
}

// Interruption reasons are fixed strings so tests and callers can rely on the
// cause being named. They never name payload or check contents (ADR 0017).
const (
	interruptReasonQuestion = "wake: interrupted, agent has a pending question that needs a human"
	interruptReasonApproval = "wake: interrupted, agent is waiting for human approval"
	interruptReasonBoth     = "wake: interrupted, agent has a pending question and is waiting for human approval"
)

// WakeSender is the narrow port implemented by the environment adapter. It
// attempts one delivery and reports the method that actually happened. Source
// is passed through so a review (diff comment) is routed to the steer channel
// and can be recorded on the receipt; it is metadata, never check contents.
// An adapter that cannot deliver to an open session returns DeliveryUnsupported
// with a reason; it must never report steer or queue in that case. The core
// does not know which concrete client implements the port (ADR 0001).
type WakeSender interface {
	Deliver(ctx context.Context, target, text string, source WakeSource) (Delivery, string)
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
// The ADR 0028 human flags are checked first: when the agent needs a human,
// the wait is interrupted before any delivery attempt and the reason is returned.
// Source is normalized and passed to the sender, then echoed on the output so
// a review (diff comment) can be traced onto its ADR 0014 receipt.
func (h *WakeHandler) Wake(ctx context.Context, in WakeInput) (WakeOutput, error) {
	if in.HasPendingQuestion || in.PendingApproval {
		return interruptedWake(in), nil
	}
	if in.Target == "" || in.Text == "" {
		return WakeOutput{}, domain.NewAppError(domain.CodeInvalidInput, "wake: target and text are required")
	}
	source := normalizeSource(in.Source)
	if !validSource(source) {
		return WakeOutput{}, domain.NewAppError(domain.CodeInvalidInput, fmt.Sprintf("wake: unknown message source %q", string(in.Source)))
	}
	if h == nil || h.sender == nil {
		return WakeOutput{
			Delivery: DeliveryUnsupported,
			OK:       false,
			Source:   source,
			Executor: in.Executor,
			Reason:   "wake: no delivery sender configured",
		}, nil
	}

	delivery, reason := h.sender.Deliver(ctx, in.Target, in.Text, source)
	switch delivery {
	case DeliverySteer, DeliveryQueue:
		return WakeOutput{Delivery: delivery, OK: true, Source: source, Executor: in.Executor}, nil
	case DeliveryUnsupported:
		if reason == "" {
			reason = "wake: delivery to an open session is not supported"
		}
		return WakeOutput{Delivery: DeliveryUnsupported, OK: false, Source: source, Executor: in.Executor, Reason: reason}, nil
	default:
		// An unknown value is never promoted to success. Report it as an
		// unsupported delivery with a reason so the caller can branch safely.
		return WakeOutput{
			Delivery: DeliveryUnsupported,
			OK:       false,
			Source:   source,
			Executor: in.Executor,
			Reason:   fmt.Sprintf("wake: unknown delivery value %q", string(delivery)),
		}, nil
	}
}

// interruptedWake builds the ADR 0028 interruption receipt. It never touches
// the sender, so no follow-up is delivered and no ladder attempt is consumed.
// The reason names whichever human flag fired, or both when they coincide.
func interruptedWake(in WakeInput) WakeOutput {
	reason := interruptReasonApproval
	switch {
	case in.HasPendingQuestion && in.PendingApproval:
		reason = interruptReasonBoth
	case in.HasPendingQuestion:
		reason = interruptReasonQuestion
	}
	return WakeOutput{
		Delivery:    DeliveryInterrupted,
		OK:          false,
		Source:      normalizeSource(in.Source),
		Executor:    in.Executor,
		Interrupted: true,
		Reason:      reason,
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
		Description: "Wake an open session and report the delivery actually used: steer, queue, unsupported, or interrupted when a human is needed. Source review marks a diff comment delivered as steer.",
		Annotations: &mcpsdk.ToolAnnotations{
			DestructiveHint: &no,
			IdempotentHint:  false,
		},
	}, handler.Handle)
}
