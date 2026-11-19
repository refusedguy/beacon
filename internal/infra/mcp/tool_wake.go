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
// ADR 0032 p2 adds a separate woke fact. Delivery (steer or queue) reports that
// a follow-up was sent; it never proves the agent started work. Woke is an
// independent confirmation supplied by the caller and echoed unchanged. It is
// not derived from delivery, OK, or the delivered/claimed/read timestamps of
// ADR 0014: a message can be delivered without starting a new agent turn.
//
// ADR 0037 p1 adds capability-based recipient selection. A session declares
// capabilities and the environment reports those declarations through a port.
// When a caller requests capabilities, the core picks the session that declares
// them all and uses its name only as the delivery key. The core keeps no list
// of agent names (ADR 0024).
//
// ADR 0038 p6 adds a fallback chain. The adapter first tries the running turn,
// then the next turn when the agent did not confirm receipt, and finally the
// task trace for a later retry. The chain is driven by the core over narrow
// ports; it reports the honest delivery value and keeps the trace as a separate
// fact. A trace is never a delivery into the running turn, and falling back
// never promotes unsupported into success. The ADR 0017 ladder is unchanged.
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

// WakeChannel is the channel the core asks the adapter to attempt for one step
// of the ADR 0038 p6 fallback chain. It is an input hint, never an outcome: the
// adapter still reports the Delivery that actually happened, so an environment
// that supports only queue may answer queue to a steer request. The two values
// share the steer/queue vocabulary but are not interchangeable.
type WakeChannel string

const (
	ChannelSteer WakeChannel = "steer"
	ChannelQueue WakeChannel = "queue"
)

// FallbackStage names the step at which a fallback wake ended. It is separate
// from Delivery so the honest delivery value is never widened with a new
// success meaning. FallbackTraced means the message was written to the task
// trace for a later retry; it is not a delivery.
type FallbackStage string

const (
	FallbackSteer  FallbackStage = "steer"
	FallbackQueue  FallbackStage = "queue"
	FallbackTraced FallbackStage = "traced"
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
	// Woke is the caller's separate confirmation that the agent actually began
	// work on the follow-up (ADR 0032 p2). It is not a delivery outcome: the
	// caller reports it when it observes the new turn, never from a steer or
	// queue receipt. The tool echoes it without interpreting it.
	Woke bool `json:"woke,omitempty" jsonschema:"separate confirmation that the agent began work; not derived from delivery or delivery timestamps"`
	// RequiredCapabilities are the abilities the recipient must declare
	// (ADR 0037 p1). When set, the core selects the delivery target through the
	// AgentDirectory instead of using a name from the call. An empty list keeps
	// the explicit Target path used by earlier callers.
	RequiredCapabilities []Capability `json:"requiredCapabilities,omitempty" jsonschema:"capabilities the recipient session must declare; selection is by capability, not by session name"`
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
// 429 is visible in the wake status (ADR 0031 p4). Woke echoes the caller's
// separate confirmation that the agent began work (ADR 0032 p2); it stays
// false on every dispatch path unless the caller reports it, so a delivered
// follow-up is never mistaken for a started agent turn. Fallback names the
// last step of the ADR 0038 p6 chain and is empty when no chain ran; Traced is
// a separate fact that stays true only when the message was written to the
// task trace. Neither widens the honest Delivery value.
type WakeOutput struct {
	Delivery    Delivery      `json:"delivery"`
	OK          bool          `json:"ok"`
	Source      WakeSource    `json:"source"`
	Executor    string        `json:"executor,omitempty"`
	Interrupted bool          `json:"interrupted,omitempty"`
	Reason      string        `json:"reason,omitempty"`
	Woke        bool          `json:"woke,omitempty"`
	Fallback    FallbackStage `json:"fallback,omitempty"`
	Traced      bool          `json:"traced,omitempty"`
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

// FallbackWakeSender attempts one delivery on a requested channel (ADR 0038
// p6). The channel is only a hint: the adapter reports the Delivery that
// actually happened and an honest reason. It must never report steer or queue
// for an attempt it could not make. A handler built with
// NewFallbackWakeHandler uses this port instead of WakeSender.
type FallbackWakeSender interface {
	Deliver(ctx context.Context, target, text string, source WakeSource, channel WakeChannel) (Delivery, string)
}

// ReceiptConfirmer reports whether the agent confirmed receipt of a follow-up
// sent on a channel (ADR 0038 p6). It is consulted after each successful
// channel attempt. A nil confirmer means unconfirmed, so the chain continues
// to the next step instead of guessing a receipt.
type ReceiptConfirmer interface {
	Confirmed(ctx context.Context, target, text string, channel WakeChannel) bool
}

// TaskTracer records a follow-up in the task trace for a later retry (ADR 0038
// p6). This is the last fallback step and is explicitly not a delivery: a
// written trace never means the message reached the running turn and never
// turns an unsupported delivery into success.
type TaskTracer interface {
	Trace(ctx context.Context, target, text string, source WakeSource) error
}

// Capability is one declared ability of an agent session (ADR 0037 p1). The
// core treats it as an opaque string. The environment owns the vocabulary and
// the core only compares values.
type Capability string

// CandidateAgent is one session the environment offers as a possible
// recipient. Target is the opaque delivery key and Capabilities are the
// abilities that session declares. The name in Target is never a selection
// criterion.
type CandidateAgent struct {
	Target       string       `json:"target"`
	Capabilities []Capability `json:"capabilities"`
}

// AgentDirectory is the port the environment adapter implements so the core
// can match a follow-up to a session by declared capability. The core does not
// know which concrete agents exist and hardcodes no names (ADR 0037 p1 and
// ADR 0024).
type AgentDirectory interface {
	CandidateAgents(ctx context.Context) []CandidateAgent
}

// declaresAll reports whether declared covers every required capability. A
// session may declare more capabilities than the caller needs.
func declaresAll(declared, required []Capability) bool {
	set := make(map[Capability]struct{}, len(declared))
	for _, c := range declared {
		set[c] = struct{}{}
	}
	for _, c := range required {
		if _, ok := set[c]; !ok {
			return false
		}
	}
	return true
}

// selectAgentByCapabilities picks the first candidate that declares all
// required capabilities. Input order breaks ties so the choice is repeatable.
// It never compares session names and returns false when nothing matches.
func selectAgentByCapabilities(candidates []CandidateAgent, required []Capability) (string, bool) {
	if len(required) == 0 {
		return "", false
	}
	for _, candidate := range candidates {
		if candidate.Target == "" {
			continue
		}
		if declaresAll(candidate.Capabilities, required) {
			return candidate.Target, true
		}
	}
	return "", false
}

// WakeHandler implements the wake tool over a WakeSender and, optionally, an
// AgentDirectory. A handler built with NewFallbackWakeHandler instead drives
// the ADR 0038 p6 chain over its own ports. In either case it holds no state
// beyond those ports and performs no I/O of its own.
type WakeHandler struct {
	sender         WakeSender
	directory      AgentDirectory
	fallbackSender FallbackWakeSender
	confirmer      ReceiptConfirmer
	tracer         TaskTracer
	useFallback    bool
}

// NewWakeHandler builds a handler over sender. A nil sender is allowed here and
// is reported as unsupported at call time, so a misconfigured environment
// cannot fake a successful delivery. Callers that only have a sender keep the
// explicit-target path from before ADR 0037 p1.
func NewWakeHandler(sender WakeSender) *WakeHandler {
	return &WakeHandler{sender: sender}
}

// NewCapabilityWakeHandler builds a handler that can resolve a delivery target
// from declared capabilities. A nil directory is allowed and makes a capability
// request an honest refusal instead of a guess.
func NewCapabilityWakeHandler(sender WakeSender, directory AgentDirectory) *WakeHandler {
	return &WakeHandler{sender: sender, directory: directory}
}

// NewFallbackWakeHandler builds a handler that runs the ADR 0038 p6 chain:
// steer, then queue when the agent did not confirm, then the task trace when
// confirmation failed again. Nil ports are honest refusals, never guesses: a
// nil sender reports unsupported on every channel, a nil confirmer counts as
// unconfirmed, and a nil tracer records Traced=false with a reason.
func NewFallbackWakeHandler(sender FallbackWakeSender, confirmer ReceiptConfirmer, tracer TaskTracer) *WakeHandler {
	return &WakeHandler{fallbackSender: sender, confirmer: confirmer, tracer: tracer, useFallback: true}
}

// NewCapabilityFallbackWakeHandler adds capability-based target selection to
// the fallback chain. It keeps the ADR 0037 p1 path intact while the chain
// drives delivery.
func NewCapabilityFallbackWakeHandler(sender FallbackWakeSender, confirmer ReceiptConfirmer, tracer TaskTracer, directory AgentDirectory) *WakeHandler {
	return &WakeHandler{
		fallbackSender: sender,
		confirmer:      confirmer,
		tracer:         tracer,
		directory:      directory,
		useFallback:    true,
	}
}

// Handle adapts Wake to the go-sdk typed tool handler signature. A fallback
// handler dispatches to WakeWithFallback so the chain is reachable over MCP;
// all earlier handlers keep the single-delivery Wake path.
func (h *WakeHandler) Handle(ctx context.Context, _ *mcpsdk.CallToolRequest, in WakeInput) (*mcpsdk.CallToolResult, WakeOutput, error) {
	var out WakeOutput
	var err error
	if h != nil && h.useFallback {
		out, err = h.WakeWithFallback(ctx, in)
	} else {
		out, err = h.Wake(ctx, in)
	}
	if err != nil {
		return nil, WakeOutput{}, err
	}
	return &mcpsdk.CallToolResult{}, out, nil
}

// wakeSetup is the shared boundary result of validating a wake call and
// resolving its delivery target. Both the single-delivery and the fallback
// paths use it so the human-first rule and capability selection stay identical.
type wakeSetup struct {
	target  string
	source  WakeSource
	refusal *WakeOutput
	err     error
}

// prepareWake validates input and resolves the target without attempting any
// delivery. An interruption or an honest capability refusal arrives as a
// refusal; a malformed call arrives as an error.
func (h *WakeHandler) prepareWake(ctx context.Context, in WakeInput) wakeSetup {
	if in.HasPendingQuestion || in.PendingApproval {
		out := interruptedWake(in)
		return wakeSetup{refusal: &out}
	}
	if in.Text == "" {
		return wakeSetup{err: domain.NewAppError(domain.CodeInvalidInput, "wake: text is required")}
	}
	source := normalizeSource(in.Source)
	if !validSource(source) {
		return wakeSetup{err: domain.NewAppError(domain.CodeInvalidInput, fmt.Sprintf("wake: unknown message source %q", string(in.Source)))}
	}

	target := in.Target
	if len(in.RequiredCapabilities) > 0 {
		if h == nil || h.directory == nil {
			return wakeSetup{refusal: &WakeOutput{
				Delivery: DeliveryUnsupported,
				OK:       false,
				Source:   source,
				Executor: in.Executor,
				Reason:   "wake: no agent directory configured for capability selection",
				Woke:     in.Woke,
			}}
		}
		selected, ok := selectAgentByCapabilities(h.directory.CandidateAgents(ctx), in.RequiredCapabilities)
		if !ok {
			return wakeSetup{refusal: &WakeOutput{
				Delivery: DeliveryUnsupported,
				OK:       false,
				Source:   source,
				Executor: in.Executor,
				Reason:   "wake: no session declares the required capabilities",
				Woke:     in.Woke,
			}}
		}
		target = selected
	}
	if target == "" {
		return wakeSetup{err: domain.NewAppError(domain.CodeInvalidInput, "wake: target is required when no capabilities are requested")}
	}
	return wakeSetup{target: target, source: source}
}

// isSuccessDelivery reports whether a delivery value is one of the two honest
// success modes of ADR 0026. Any other value, including interrupted, is not a
// delivery the fallback chain may treat as done.
func isSuccessDelivery(delivery Delivery) bool {
	return delivery == DeliverySteer || delivery == DeliveryQueue
}

// Wake performs one delivery and returns an honest receipt. Input is validated
// at this boundary; steer and queue are success, while unsupported and any
// unknown delivery value are a refusal with a reason and OK=false (ADR 0026).
// The ADR 0028 human flags are checked first: when the agent needs a human,
// the wait is interrupted before any delivery attempt and the reason is returned.
// Source is normalized and passed to the sender, then echoed on the output so
// a review (diff comment) can be traced onto its ADR 0014 receipt. Woke is
// only echoed from the input; no delivery outcome sets it (ADR 0032 p2).
// Required capabilities are resolved through the directory into a delivery
// target (ADR 0037 p1); without them the explicit Target still applies.
func (h *WakeHandler) Wake(ctx context.Context, in WakeInput) (WakeOutput, error) {
	setup := h.prepareWake(ctx, in)
	if setup.err != nil {
		return WakeOutput{}, setup.err
	}
	if setup.refusal != nil {
		return *setup.refusal, nil
	}

	if h == nil || h.sender == nil {
		return WakeOutput{
			Delivery: DeliveryUnsupported,
			OK:       false,
			Source:   setup.source,
			Executor: in.Executor,
			Reason:   "wake: no delivery sender configured",
			Woke:     in.Woke,
		}, nil
	}

	delivery, reason := h.sender.Deliver(ctx, setup.target, in.Text, setup.source)
	switch delivery {
	case DeliverySteer, DeliveryQueue:
		return WakeOutput{Delivery: delivery, OK: true, Source: setup.source, Executor: in.Executor, Woke: in.Woke}, nil
	case DeliveryUnsupported:
		if reason == "" {
			reason = "wake: delivery to an open session is not supported"
		}
		return WakeOutput{Delivery: DeliveryUnsupported, OK: false, Source: setup.source, Executor: in.Executor, Reason: reason, Woke: in.Woke}, nil
	default:
		// An unknown value is never promoted to success. Report it as an
		// unsupported delivery with a reason so the caller can branch safely.
		return WakeOutput{
			Delivery: DeliveryUnsupported,
			OK:       false,
			Source:   setup.source,
			Executor: in.Executor,
			Reason:   fmt.Sprintf("wake: unknown delivery value %q", string(delivery)),
			Woke:     in.Woke,
		}, nil
	}
}

// WakeWithFallback runs the ADR 0038 p6 chain after the same boundary checks as
// Wake. The first confirmed channel wins. When no channel is confirmed the
// message is written to the task trace and the honest delivery stays
// unsupported; a trace is never reported as a delivery into the running turn.
func (h *WakeHandler) WakeWithFallback(ctx context.Context, in WakeInput) (WakeOutput, error) {
	setup := h.prepareWake(ctx, in)
	if setup.err != nil {
		return WakeOutput{}, setup.err
	}
	if setup.refusal != nil {
		return *setup.refusal, nil
	}
	return h.runFallback(ctx, in, setup.target, setup.source), nil
}

// fallbackStage maps a requested channel to the stage recorded on the receipt.
func fallbackStage(channel WakeChannel) FallbackStage {
	if channel == ChannelSteer {
		return FallbackSteer
	}
	return FallbackQueue
}

// runFallback walks steer then queue. A channel that is unsupported or that
// delivered without a confirmation moves the chain forward. Only a confirmed
// delivery returns OK=true; exhausting both channels writes the task trace and
// returns DeliveryUnsupported with Traced reporting the separate fact.
func (h *WakeHandler) runFallback(ctx context.Context, in WakeInput, target string, source WakeSource) WakeOutput {
	base := WakeOutput{Source: source, Executor: in.Executor, Woke: in.Woke}
	outcomes := ""
	record := func(outcome string) {
		if outcomes != "" {
			outcomes += "; "
		}
		outcomes += outcome
	}
	for _, channel := range []WakeChannel{ChannelSteer, ChannelQueue} {
		delivery, reason := DeliveryUnsupported, "wake: no fallback sender configured"
		if h != nil && h.fallbackSender != nil {
			delivery, reason = h.fallbackSender.Deliver(ctx, target, in.Text, source, channel)
		}
		if !isSuccessDelivery(delivery) {
			if reason == "" {
				reason = "wake: channel " + string(channel) + " is not supported by the environment"
			}
			record(string(channel) + " unsupported: " + reason)
			continue
		}
		confirmed := h != nil && h.confirmer != nil && h.confirmer.Confirmed(ctx, target, in.Text, channel)
		if confirmed {
			out := base
			out.Delivery = delivery
			out.OK = true
			out.Fallback = fallbackStage(channel)
			return out
		}
		record(string(channel) + " delivered but not confirmed")
	}

	out := base
	out.Delivery = DeliveryUnsupported
	out.OK = false
	out.Fallback = FallbackTraced
	reason := "wake: fallback exhausted (" + outcomes + "); follow-up written to task trace for retry"
	if h == nil || h.tracer == nil {
		out.Reason = reason + ", but no task tracer is configured"
		return out
	}
	if err := h.tracer.Trace(ctx, target, in.Text, source); err != nil {
		out.Reason = reason + ", task trace failed: " + err.Error()
		return out
	}
	out.Traced = true
	out.Reason = reason
	return out
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
		Woke:        in.Woke,
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
		Description: "Wake an open session and report the delivery actually used: steer, queue, unsupported, or interrupted when a human is needed. Delivery says the follow-up was sent, not that the agent started; woke is a separate caller-supplied confirmation. Source review marks a diff comment delivered as steer. A fallback handler tries the running turn, then the next turn, then the task trace; a traced follow-up stays unsupported and is not a delivery.",
		Annotations: &mcpsdk.ToolAnnotations{
			DestructiveHint: &no,
			IdempotentHint:  false,
		},
	}, handler.Handle)
}
