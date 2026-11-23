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
// ADR 0041 p5 adds wake_between: when the wait contract names a window, this
// tool staggers each waiter to a random point inside it before any delivery is
// attempted. Fifteen agents waiting on one event then wake at spread points
// instead of all at once. The window comes from the call, not from a per-agent
// setting; an absent window keeps the immediate wake of earlier callers. This
// is the wake-up point of a waiter, not the redelivery jitter of ADR 0017 and
// not the backpressure quota of ADR 0038 p2.
//
// ADR 0042 p3 addresses a notification to the worktree path, not to a session
// id and not to the PEER_NAME of ADR 0039. The environment advertises the
// worktree of each candidate session; when a caller names a target worktree,
// the core resolves the delivery key from the candidate running in it. The
// worktree path survives a session restart, so a restarted session in the same
// worktree receives the notification without reconfiguring the recipient, and
// a session in another worktree does not receive it. The explicit Target path
// of callers written before ADR 0042 is unchanged.
//
// ADR 0056 p3 (T3.124) adds active detection of a duplicate prompt and an
// instant operationId at wake entry. A command that starts long work returns
// an operationId immediately; an equal prompt already running returns that
// existing id instead of starting a second task. This deduplicates by prompt
// content, not by the clientRequestId request key of ADR 0028 and T2.22, and a
// duplicate consumes no delivery and creates no second follow-up.
//
// ADR 0058 p2 (T3.129) adds the addressed doorbell. An agent owns a private
// mailbox and a long-poll listener blocks on its own address. A message
// deposited in that mailbox is the only trigger that releases the listener;
// a broadcast message or a message for another address never does. This is a
// wake trigger, not a second delivery mode and not a step of the ADR 0017
// ladder: Delivery keeps its ADR 0026 closed set and no timer is restarted.
// The doorbell is attached as a narrow port, so callers written before this
// feature keep their exact behaviour.
//
// ADR 0059 p2 (T3.135) adds the guarded heartbeat. An agent wakes only when the
// observed state changed since the last heartbeat or the quota gate is open;
// a timer tick on its own never releases a wake. The guard is checked as a
// precondition before a wake is sent, so a held heartbeat registers no
// operation, rings no doorbell and touches no delivery channel. It is a wake
// precondition, not a new Delivery value and not a step of the ADR 0017 ladder.
// The guard is attached as a narrow port, so callers written before this
// feature keep their exact behaviour.
//
// T5.49 (ADR 0090 and ADR 0095) adds the registry status precondition. An
// executor whose registry row is stale, stopped or missing must not be woken;
// a heartbeat tick does not replace a successful sync. The environment owns
// the registry rule and reports it through RegistryWakeGuard, so this package
// reads the answer without importing a store or a registry implementation.
// The refusal stays inside the ADR 0026 closed set and names the stored status
// on the receipt.
//
// The tool is intentionally not registered in NewServer. Wiring it into a
// server is a separate integration task; AttachWake is the hook for it.
package mcp

import (
	"context"
	crand "crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

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

// DeliveryState is where one follow-up message sits in its lifecycle
// (ADR 0043 feature 4). It is a receipt state, not a delivery method: Delivery
// from ADR 0026 answers how a message was sent (steer, queue, unsupported),
// while DeliveryState answers at what point of its life the message is. The
// eight states stay a closed set and never include steer, queue or unsupported.
type DeliveryState string

const (
	// StateSpooled means the message is written to durable storage and not yet
	// handed to a channel.
	StateSpooled DeliveryState = "spooled"
	// StatePending means the message waits to be sent.
	StatePending DeliveryState = "pending"
	// StateHeld means the message is on hold and no send was attempted, for
	// example when a human interruption stopped the wait.
	StateHeld DeliveryState = "held"
	// StatePushed means the message was sent to a channel.
	StatePushed DeliveryState = "pushed"
	// StatePushUnreachable means the message was sent to a channel the
	// recipient cannot reach.
	StatePushUnreachable DeliveryState = "push-unreachable"
	// StateRead means the recipient read the message.
	StateRead DeliveryState = "read"
	// StateRefused means the recipient refused the message. It is never read.
	StateRefused DeliveryState = "refused"
	// StateExpired means the message expired before it was read.
	StateExpired DeliveryState = "expired"
)

// validDeliveryState reports whether state is one of the eight receipt states
// from ADR 0043. Delivery values from ADR 0026 are deliberately not states.
func validDeliveryState(state DeliveryState) bool {
	switch state {
	case StateSpooled, StatePending, StateHeld, StatePushed,
		StatePushUnreachable, StateRead, StateRefused, StateExpired:
		return true
	default:
		return false
	}
}

// IsRead reports whether the recipient actually read the message. Only the
// read state qualifies: a refused message is explicitly not read (ADR 0043).
func (s DeliveryState) IsRead() bool {
	return s == StateRead
}

// reachedRecipient reports whether the message reached the recipient. Pushed
// and read are the two states that count; refused, push-unreachable and expired
// do not, so a receipt in one of them is never a delivered follow-up.
func (s DeliveryState) reachedRecipient() bool {
	return s == StatePushed || s == StateRead
}

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
	// WakeBetweenMillis is the wait window, in milliseconds, from the wait
	// contract (ADR 0041 p5). When positive, this waiter wakes at one random
	// point inside the window instead of at the same instant as every other
	// waiter on the same event. Zero or negative keeps the immediate wake of
	// callers written before ADR 0041. The window is a property of the wait,
	// never a per-agent setting.
	WakeBetweenMillis int64 `json:"wakeBetweenMillis,omitempty" jsonschema:"wait window in milliseconds; wake at one random point inside it to spread waiters on the same event; zero means wake immediately"`
	// TargetWorktree is the worktree path the notification is addressed to
	// (ADR 0042 p3). When set, the core resolves the delivery key from the
	// candidate session running in that worktree instead of using a session id
	// or a PEER_NAME. The path is stable across a session restart, so a
	// restarted session in the same worktree still receives the wake and a
	// session in another worktree does not. An empty path keeps the explicit
	// Target or capability path used by earlier callers.
	TargetWorktree string `json:"targetWorktree,omitempty" jsonschema:"worktree path the notification is addressed to; resolves the recipient by worktree, stable across a session restart; empty keeps the explicit target or capability path"`
	// State is the receipt state the environment reports for this message
	// (ADR 0043 feature 4). It answers where in its life the message is, not
	// how it is sent: steer, queue and unsupported stay in Delivery (ADR 0026).
	// An empty value keeps earlier callers working; the core then derives a
	// state from the delivery outcome. An unknown value is rejected here.
	State DeliveryState `json:"state,omitempty" jsonschema:"receipt lifecycle state: spooled, pending, held, pushed, push-unreachable, read, refused or expired; empty derives one from the delivery outcome"`
	// Prompt is the content of the long-running job this wake starts (ADR 0056
	// p3, T3.124). When an operation registry is attached, an equal prompt is
	// detected at entry and returns the existing operationId instead of
	// starting a second task. This is content deduplication, distinct from the
	// clientRequestId request key of ADR 0028 and T2.22 that only collapses
	// repeats of one MCP command after a broken connection. Surrounding
	// whitespace is ignored when comparing prompts. An empty prompt keeps the
	// pointer-only wake message of ADR 0017 unchanged.
	Prompt string `json:"prompt,omitempty" jsonschema:"prompt content of the long-running job; an equal prompt returns the existing operationId and starts no second task"`
	// Mailbox is the address of the recipient's private doorbell (ADR 0058 p2,
	// T3.129). When set and a doorbell is attached, wake deposits a message
	// addressed to this mailbox, so a long-poll listener on the same address
	// wakes. An empty address keeps the pre-feature wake unchanged. The address
	// is the routing key; it is not a session name or a delivery mode.
	Mailbox string `json:"mailbox,omitempty" jsonschema:"address of the recipient's private doorbell; a long-poll listener on this address wakes only on a message addressed to it"`
	// Broadcast marks this follow-up as sent to everyone (ADR 0058 p2). A
	// broadcast message is addressed to no single mailbox: it rings no doorbell
	// and releases no listener, even when Mailbox names one.
	Broadcast bool `json:"broadcast,omitempty" jsonschema:"broadcast follow-up; reaches no single mailbox and never rings a doorbell"`
	// HeartbeatState is the environment's opaque state fingerprint observed at
	// this heartbeat (ADR 0059 p2, T3.135). The guard releases a wake when this
	// differs from the state seen at the previous guarded wake. An empty value
	// is a valid observation; it only compares equal to another empty value.
	HeartbeatState string `json:"heartbeatState,omitempty" jsonschema:"opaque state fingerprint observed at this heartbeat; a change since the last guarded wake releases the wake"`
	// QuotaShouldRun is the open state of the quota gate (ADR 0059 p2). When
	// true the gate is open and a wake is released even without a state change.
	QuotaShouldRun bool `json:"quotaShouldRun,omitempty" jsonschema:"quota should-run gate is open; releases the wake even when the state is unchanged"`
	// HeartbeatTick marks this wake attempt as a timer heartbeat tick (ADR 0059
	// p2). A tick on its own never releases a wake; it is recorded so the
	// decision can name the held tick. It does not change the guard rule.
	HeartbeatTick bool `json:"heartbeatTick,omitempty" jsonschema:"this attempt is a timer heartbeat tick; a tick alone never wakes, only state change or an open quota gate does"`
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
	// State is the eight-valued receipt lifecycle state (ADR 0043 feature 4).
	// It is independent of the ADR 0026 delivery method. A refused,
	// push-unreachable or expired message keeps OK false even when a channel
	// acknowledged the send, so a rejection is never read as a delivery.
	State DeliveryState `json:"state"`
	// OperationID is issued at wake entry when a prompt is supplied and an
	// operation registry is attached (ADR 0056 p3, T3.124). It names the
	// long-running operation this follow-up starts. On a duplicate it carries
	// the existing id. It stays empty when no prompt or no registry was given,
	// so earlier callers keep their exact output.
	OperationID string `json:"operationId,omitempty"`
	// Duplicate is true when an equal prompt was already running: the existing
	// OperationID is returned and no second task or delivery is started.
	Duplicate bool `json:"duplicate,omitempty"`
	// DoorbellRang reports that an addressed message was deposited in the
	// recipient's private mailbox and the doorbell rang (ADR 0058 p2, T3.129).
	// It is a separate trigger fact: not a new Delivery value and not a ladder
	// step. It stays false for a broadcast message, an empty Mailbox, and when
	// no doorbell is attached.
	DoorbellRang bool `json:"doorbellRang,omitempty"`
	// Guarded reports that a heartbeat guard was attached and evaluated this
	// wake (ADR 0059 p2, T3.135). It is false for callers with no guard, which
	// keep their exact output.
	Guarded bool `json:"guarded,omitempty"`
	// GuardReason names why the guard released or held the wake. A held reason
	// states that a timer tick alone does not wake.
	GuardReason string `json:"guardReason,omitempty"`
	// HeartbeatChanged reports that the observed state changed since the last
	// guarded wake. It is one of the two facts that release a wake.
	HeartbeatChanged bool `json:"heartbeatChanged,omitempty"`
	// QuotaGateOpen reports that the quota should-run gate was open. It is the
	// other fact that releases a wake.
	QuotaGateOpen bool `json:"quotaGateOpen,omitempty"`
	// HeartbeatTick echoes whether this attempt was a timer tick. It stays a
	// separate fact and never by itself makes Guarded or Delivery succeed.
	HeartbeatTick bool `json:"heartbeatTick,omitempty"`
	// RegistryStatus is the status the registry held when a guard was attached
	// (T5.49). It names the stored value on both a refusal and a released wake,
	// so a receipt can show that a stale row held the wake. It stays empty when
	// no guard is attached.
	RegistryStatus string `json:"registryStatus,omitempty"`
}

// T3.110 (ADR 0046 feature 2) makes a wake error repairable. ADR 0009 fixes the
// six MCP error fields ok, code, message, status, retryAfterMs and newWatchID;
// that envelope is not rewritten. Three recovery fields are added on top:
// retryable says whether a retry can succeed, recoveryHint names the one
// parameter the model should fix, and partial.cursor marks the position the
// call reached so work continues from that offset instead of restarting.

// PartialResult is the already-processed position of a repairable wake error.
// Cursor is opaque to the core. The model fixes the named parameter and passes
// the cursor back to resume at that position.
type PartialResult struct {
	Cursor string `json:"cursor"`
}

// RepairableErrorShape is the ADR 0046 repair contract: the six-field ADR 0009
// ErrorShape plus three recovery fields. It is not a second error format. Both
// shapes come from the one conversion core in schema.go, so the six original
// fields are built once and stay identical (BCX-69).
type RepairableErrorShape struct {
	ErrorShape
	Retryable    bool          `json:"retryable"`
	RecoveryHint string        `json:"recoveryHint,omitempty"`
	Partial      PartialResult `json:"partial,omitempty"`
}

// RecoveryError is a wake boundary error that carries repair metadata while
// remaining a domain.AppError. Unwrap keeps errors.As(err, *domain.AppError)
// working for callers written before ADR 0046.
type RecoveryError struct {
	*domain.AppError
	Retryable    bool
	RecoveryHint string
	Partial      PartialResult
}

func (e *RecoveryError) Error() string {
	if e == nil || e.AppError == nil {
		return ""
	}
	return e.AppError.Error()
}

func (e *RecoveryError) Unwrap() error {
	if e == nil || e.AppError == nil {
		return nil
	}
	return e.AppError
}

// RecoveryFields exposes the ADR 0046 repair metadata to the one conversion
// core in schema.go. The accessor keeps wake details in this file while the
// generic envelope stays free of wake-specific types.
func (e *RecoveryError) RecoveryFields() (bool, string, PartialResult) {
	if e == nil {
		return false, "", PartialResult{}
	}
	return e.Retryable, e.RecoveryHint, e.Partial
}

// Stable cursors name the input position a repairable wake error reached. The
// model fixes that one field and continues from its offset.
const (
	recoveryCursorText   = "input.text"
	recoveryCursorSource = "input.source"
	recoveryCursorState  = "input.state"
	recoveryCursorTarget = "input.target"
	recoveryCursorWindow = "input.wakeBetweenMillis"
)

// repairableError wraps a domain error with repair metadata. The hint names the
// single parameter to fix; the cursor marks where to resume.
func repairableError(base *domain.AppError, retryable bool, hint, cursor string) error {
	return &RecoveryError{AppError: base, Retryable: retryable, RecoveryHint: hint, Partial: PartialResult{Cursor: cursor}}
}

// ToRepairableErrorShape converts an error into the repairable envelope. It
// reads the same conversion core as ToErrorShape, so the six ADR 0009 fields
// are built once. The three ADR 0046 fields are a separate documented contract
// layered on that base. The bool is false when err carries no AppError,
// matching ToErrorShape.
func ToRepairableErrorShape(err error) (RepairableErrorShape, bool) {
	base, meta, ok := errorEnvelopeFrom(err)
	if !ok {
		return RepairableErrorShape{}, false
	}
	return RepairableErrorShape{
		ErrorShape:   base,
		Retryable:    meta.retryable,
		RecoveryHint: meta.recoveryHint,
		Partial:      meta.partial,
	}, true
}

// Interruption reasons are fixed strings so tests and callers can rely on the
// cause being named. They never name payload or check contents (ADR 0017).
const (
	interruptReasonQuestion = "wake: interrupted, agent has a pending question that needs a human"
	interruptReasonApproval = "wake: interrupted, agent is waiting for human approval"
	interruptReasonBoth     = "wake: interrupted, agent has a pending question and is waiting for human approval"
)

// WakeSender attempts one delivery and reports the method that actually
// happened. Source is passed through so a review (diff comment) is routed to
// the steer channel and can be recorded on the receipt; it is metadata, never
// check contents. Channel names the channel to try; the single-delivery path
// passes an empty channel and lets the adapter choose. An adapter that cannot
// deliver to an open session returns DeliveryUnsupported with a reason; it
// must never report steer or queue in that case. The core does not know which
// concrete client implements the port (ADR 0001, ADR 0038 p6).
type WakeSender func(ctx context.Context, target, text string, source WakeSource, channel WakeChannel) (Delivery, string)

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
// criterion. WorktreePath (ADR 0042 p3) is the worktree this session runs in;
// it is the stable routing key for a notification. The environment reports a
// fresh Target for a restarted session while the WorktreePath stays the same,
// so a wake addressed to the worktree reaches the restarted session.
type CandidateAgent struct {
	Target       string       `json:"target"`
	Capabilities []Capability `json:"capabilities"`
	WorktreePath string       `json:"worktreePath,omitempty"`
}

// AgentDirectory is the port the environment adapter implements so the core
// can match a follow-up to a session by declared capability (ADR 0037 p1) or
// by the worktree path the session runs in (ADR 0042 p3). The core does not
// know which concrete agents exist and hardcodes no names (ADR 0037 p1 and
// ADR 0024).
type AgentDirectory interface {
	CandidateAgents(ctx context.Context) []CandidateAgent
}

// RegistryWakeGuard reports the registry status of one executor and whether
// the registry permits a wake for it (T5.49, ADR 0090 and ADR 0095). The rule
// belongs to the environment that owns the registry, so the core only reads
// the answer. An empty status is an unknown row and never allows a wake.
// Status is copied to the receipt so a refusal names the stored value.
type RegistryWakeGuard interface {
	WakeStatus(executorID string) (status string, allowed bool)
}

// WakeSleeper waits until a chosen point inside the wake window (ADR 0041 p5).
// It is the narrow seam that lets a test observe the chosen wake points without
// sleeping. The real implementation uses a timer that honors context
// cancellation.
type WakeSleeper interface {
	Sleep(ctx context.Context, d time.Duration) error
}

// timerSleeper is the production WakeSleeper. time.NewTimer stops early when
// the caller's context is cancelled, so a cancelled wait never delivers after
// the caller has gone away.
type timerSleeper struct{}

func (timerSleeper) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// randomWakePoint picks one uniform point in [0, window) for a waiter. Each
// call is independent, so fifteen waiters on one event land at spread points.
// It is the real point picker; tests may call it directly to check the spread.
func randomWakePoint(window time.Duration) time.Duration {
	if window <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(window)))
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

// selectAgent picks the first candidate satisfying every requested routing
// constraint: all required capabilities and, when worktreePath is set, the
// worktree the session runs in. Input order breaks ties so the choice is
// repeatable. It never uses a session name as a criterion and returns false
// when nothing matches. With no constraints it returns false, so a caller
// that requests no routing key keeps its explicit Target.
func selectAgent(candidates []CandidateAgent, required []Capability, worktreePath string) (string, bool) {
	if len(required) == 0 && worktreePath == "" {
		return "", false
	}
	for _, candidate := range candidates {
		if candidate.Target == "" {
			continue
		}
		if len(required) > 0 && !declaresAll(candidate.Capabilities, required) {
			continue
		}
		if worktreePath != "" && candidate.WorktreePath != worktreePath {
			continue
		}
		return candidate.Target, true
	}
	return "", false
}

// selectAgentByCapabilities keeps the capability-only entry point used before
// ADR 0042: it resolves a target by declared capabilities alone.
func selectAgentByCapabilities(candidates []CandidateAgent, required []Capability) (string, bool) {
	return selectAgent(candidates, required, "")
}

// selectAgentByWorktree resolves a target by the worktree path a candidate
// runs in (ADR 0042 p3). The returned target is the session's current delivery
// key, which may change on a restart while the worktree path does not.
func selectAgentByWorktree(candidates []CandidateAgent, worktreePath string) (string, bool) {
	return selectAgent(candidates, nil, worktreePath)
}

// WakeHandler implements the wake tool over a WakeSender and, optionally, an
// AgentDirectory. A handler built with NewFallbackWakeHandler instead drives
// the ADR 0038 p6 chain over its own ports. In either case it holds no state
// beyond those ports and performs no I/O of its own.
type WakeHandler struct {
	sender        WakeSender
	directory     AgentDirectory
	confirmer     ReceiptConfirmer
	tracer        TaskTracer
	useFallback   bool
	sleeper       WakeSleeper
	point         func(time.Duration) time.Duration
	confirmations *ConfirmationLedger
	operations    OperationRegistry
	doorbell      DoorbellPort
	guard         *HeartbeatGuard
	registryGuard RegistryWakeGuard
}

// WithWakeSleeper replaces the timer used to stagger wake points (ADR 0041
// p5). A test injects a recorder to observe the points without sleeping; a nil
// argument restores the real timer. It does not change delivery ports.
func (h *WakeHandler) WithWakeSleeper(s WakeSleeper) *WakeHandler {
	if h != nil {
		h.sleeper = s
	}
	return h
}

// WithWakePoint replaces the random point picker. A nil argument restores the
// uniform picker. It exists so a test can pin the point while checking that a
// handler passes the window's point to the sleeper.
func (h *WakeHandler) WithWakePoint(fn func(time.Duration) time.Duration) *WakeHandler {
	if h != nil {
		h.point = fn
	}
	return h
}

// applyWakeWindow staggers this waiter to a random point inside the window
// named by the wait contract (ADR 0041 p5). It runs after the boundary checks,
// so a human interruption or an invalid call never waits. A missing window
// keeps the immediate wake used by callers before ADR 0041. A cancelled wait
// returns an error and no delivery is attempted.
func (h *WakeHandler) applyWakeWindow(ctx context.Context, in WakeInput) error {
	if in.WakeBetweenMillis <= 0 {
		return nil
	}
	window := time.Duration(in.WakeBetweenMillis) * time.Millisecond
	pick := randomWakePoint
	sleeper := WakeSleeper(timerSleeper{})
	if h != nil {
		if h.point != nil {
			pick = h.point
		}
		if h.sleeper != nil {
			sleeper = h.sleeper
		}
	}
	if err := sleeper.Sleep(ctx, pick(window)); err != nil {
		return repairableError(
			domain.NewAppError(domain.CodeCancelled, "wake: waiting inside wake window was cancelled: "+err.Error()),
			true,
			"wake: reissue the wake with a fresh wait window; the previous wait was cancelled",
			recoveryCursorWindow,
		)
	}
	return nil
}

// The wake handlers in this group stay unregistered by design. ADR 0263 fixes
// the MCP tool contract at seven names and forbids silent wake registration.
// Adding a tool name needs a separate ADR, not a later wave. These constructors
// are a permanent boundary, not work deferred to Waves 6-8.
//
// NewWakeHandler builds a handler over sender. A nil sender is allowed here and
// is reported as unsupported at call time, so a misconfigured environment
// cannot fake a successful delivery. Callers that only have a sender keep the
// explicit-target path from before ADR 0037 p1.
func NewWakeHandler(sender WakeSender) *WakeHandler {
	return &WakeHandler{sender: sender}
}

// NewCapabilityWakeHandler builds a handler that can resolve a delivery target
// from declared capabilities. A nil directory is allowed and makes a capability
// request an honest refusal instead of a guess. The handler also resolves
// worktree-addressed wakes (ADR 0042 p3) through the same directory.
func NewCapabilityWakeHandler(sender WakeSender, directory AgentDirectory) *WakeHandler {
	return &WakeHandler{sender: sender, directory: directory}
}

// NewWorktreeWakeHandler builds a handler that resolves the delivery target
// from the worktree path a candidate session runs in (ADR 0042 p3). The path
// is stable across a session restart, so a restarted session in the same
// worktree is reached without reconfiguring the recipient. It is the same
// wiring as NewCapabilityWakeHandler; the name states the addressing key this
// caller uses. A nil directory makes a worktree request an honest refusal.
func NewWorktreeWakeHandler(sender WakeSender, directory AgentDirectory) *WakeHandler {
	return &WakeHandler{sender: sender, directory: directory}
}

// NewFallbackWakeHandler builds a handler that runs the ADR 0038 p6 chain:
// steer, then queue when the agent did not confirm, then the task trace when
// confirmation failed again. Nil ports are honest refusals, never guesses: a
// nil sender reports unsupported on every channel, a nil confirmer counts as
// unconfirmed, and a nil tracer records Traced=false with a reason.
func NewFallbackWakeHandler(sender WakeSender, confirmer ReceiptConfirmer, tracer TaskTracer) *WakeHandler {
	return &WakeHandler{sender: sender, confirmer: confirmer, tracer: tracer, useFallback: true}
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
	target          string
	source          WakeSource
	refusal         *WakeOutput
	err             error
	registryStatus  string
	registryChecked bool
}

// prepareWake validates input and resolves the target without attempting any
// delivery. An interruption or an honest refusal from a routing-key lookup
// (capability or worktree) arrives as a refusal; a malformed call arrives as
// an error. Routing keys are resolved in one pass so a caller may request both
// a capability set and a worktree path. With a registry guard attached, the
// stored status is checked before the setup is returned, so a stale row never
// reaches an operation, a wake window, a doorbell or a delivery channel.
func (h *WakeHandler) prepareWake(ctx context.Context, in WakeInput) wakeSetup {
	if in.HasPendingQuestion || in.PendingApproval {
		out := interruptedWake(in)
		return wakeSetup{refusal: &out}
	}
	if in.Text == "" {
		return wakeSetup{err: repairableError(
			domain.NewAppError(domain.CodeInvalidInput, "wake: text is required"),
			true,
			"wake: set the text field to a pointer-only wake message",
			recoveryCursorText,
		)}
	}
	source := normalizeSource(in.Source)
	if !validSource(source) {
		return wakeSetup{err: repairableError(
			domain.NewAppError(domain.CodeInvalidInput, fmt.Sprintf("wake: unknown message source %q", string(in.Source))),
			true,
			"wake: set the source field to manual or review",
			recoveryCursorSource,
		)}
	}
	if in.State != "" && !validDeliveryState(in.State) {
		return wakeSetup{err: repairableError(
			domain.NewAppError(domain.CodeInvalidInput, fmt.Sprintf("wake: unknown receipt state %q", string(in.State))),
			true,
			"wake: set the state field to one of the eight receipt lifecycle states",
			recoveryCursorState,
		)}
	}

	hasCapabilities := len(in.RequiredCapabilities) > 0
	hasWorktree := in.TargetWorktree != ""
	target := in.Target
	if hasCapabilities || hasWorktree {
		if h == nil || h.directory == nil {
			reason := "wake: no agent directory configured for "
			switch {
			case hasCapabilities && hasWorktree:
				reason += "capability or worktree selection"
			case hasWorktree:
				reason += "worktree addressing"
			default:
				reason += "capability selection"
			}
			return wakeSetup{refusal: &WakeOutput{
				Delivery: DeliveryUnsupported,
				OK:       false,
				Source:   source,
				Executor: in.Executor,
				Reason:   reason,
				Woke:     in.Woke,
			}}
		}
		selected, ok := selectAgent(h.directory.CandidateAgents(ctx), in.RequiredCapabilities, in.TargetWorktree)
		if !ok {
			reason := "wake: no session "
			switch {
			case hasCapabilities && hasWorktree:
				reason += "declares the required capabilities in the requested worktree"
			case hasWorktree:
				reason += "runs in the requested worktree"
			default:
				reason += "declares the required capabilities"
			}
			return wakeSetup{refusal: &WakeOutput{
				Delivery: DeliveryUnsupported,
				OK:       false,
				Source:   source,
				Executor: in.Executor,
				Reason:   reason,
				Woke:     in.Woke,
			}}
		}
		target = selected
	}
	if target == "" {
		return wakeSetup{err: repairableError(
			domain.NewAppError(domain.CodeInvalidInput, "wake: target is required when no routing key is requested"),
			true,
			"wake: set the target field or a routing key (requiredCapabilities or targetWorktree)",
			recoveryCursorTarget,
		)}
	}
	if h == nil || h.registryGuard == nil {
		return wakeSetup{target: target, source: source}
	}
	executorID := strings.TrimSpace(in.Executor)
	if executorID == "" {
		executorID = target
	}
	status, allowed := h.registryGuard.WakeStatus(executorID)
	setup := wakeSetup{target: target, source: source, registryStatus: status, registryChecked: true}
	if !allowed {
		out := WakeOutput{
			Delivery:       DeliveryUnsupported,
			OK:             false,
			Source:         source,
			Executor:       in.Executor,
			Reason:         registryHeldReason(executorID, status),
			Woke:           in.Woke,
			RegistryStatus: status,
		}
		setup.refusal = &out
	}
	return setup
}

// registryHeldReason names the stored status in a registry refusal. An unknown
// row has no status string, so the reason says unknown instead of quoting an
// empty value. The status is also reported in the RegistryStatus receipt field.
func registryHeldReason(executorID, status string) string {
	if status == "" {
		status = "unknown"
	}
	return fmt.Sprintf("wake: registry guard held; executor %q has status %q, which does not allow wake", executorID, status)
}

// withRegistryReceipt copies the checked registry status onto a receipt. It is
// a no-op for callers with no guard, so their output is byte-for-byte unchanged.
func withRegistryReceipt(out WakeOutput, setup wakeSetup) WakeOutput {
	if !setup.registryChecked {
		return out
	}
	out.RegistryStatus = setup.registryStatus
	return out
}

// isSuccessDelivery reports whether a delivery value is one of the two honest
// success modes of ADR 0026. Any other value, including interrupted, is not a
// delivery the fallback chain may treat as done.
func isSuccessDelivery(delivery Delivery) bool {
	return delivery == DeliverySteer || delivery == DeliveryQueue
}

// defaultDeliveryState picks a receipt state for callers that do not report
// one. It claims only what the delivery outcome proves: a delivered follow-up
// was pushed, a traced message was spooled for later, and everything else was
// held. An explicit State from the caller narrows this default.
func defaultDeliveryState(out WakeOutput) DeliveryState {
	switch {
	case out.Delivery == DeliveryInterrupted:
		return StateHeld
	case out.Traced:
		return StateSpooled
	case out.OK:
		return StatePushed
	default:
		return StateHeld
	}
}

// withReceiptState attaches the receipt lifecycle state to a wake result. An
// explicit State from the caller wins; otherwise the state follows the
// delivery outcome. A state that does not reach the recipient forces OK false,
// so a refused or expired message is never reported as a delivered follow-up.
func withReceiptState(out WakeOutput, in WakeInput) WakeOutput {
	state := in.State
	if state == "" {
		state = defaultDeliveryState(out)
	}
	out.State = state
	if !state.reachedRecipient() && out.OK {
		out.OK = false
		out.Reason = "wake: receipt state " + string(state) + " did not reach the recipient"
	}
	return out
}

// Wake performs one delivery and returns an honest receipt. Input is validated
// at this boundary; steer and queue are success, while unsupported and any
// unknown delivery value are a refusal with a reason and OK=false (ADR 0026).
// The ADR 0028 human flags are checked first: when the agent needs a human,
// the wait is interrupted before any delivery attempt and the reason is returned.
// Source is normalized and passed to the sender, then echoed on the output so
// a review (diff comment) can be traced onto its ADR 0014 receipt. Woke is
// only echoed from the input; no delivery outcome sets it (ADR 0032 p2).
// Required capabilities and the target worktree are resolved through the
// directory into a delivery target (ADR 0037 p1 and ADR 0042 p3); without
// either routing key the explicit Target still applies. A worktree path is a
// stable key: a restarted session in the same worktree is reached, and a
// session in another worktree is not. When the wait contract names a window,
// the wake is staggered to one random point inside it before any sender is
// touched (ADR 0041 p5). With a prompt and an operation registry attached, the
// entry check of ADR 0056 p3 issues the operationId before any delivery and a
// repeated prompt returns the existing id with Duplicate=true and no sender
// call. When a mailbox is named, ADR 0058 p2 rings that addressed doorbell
// after the entry check, so a duplicate prompt rings nothing and a broadcast
// message rings no mailbox. When a guard is attached, ADR 0059 p2 checks it
// before any of that work: a tick with no state change and a closed quota gate
// returns a held receipt with OK=false and touches no operation, window,
// doorbell or delivery channel. With a registry guard attached, T5.49 checks
// the stored executor status before any of that work: a stale or unknown row
// returns a held receipt with OK=false and the status on the receipt, and no
// operation, window, doorbell or delivery channel is touched.
func (h *WakeHandler) Wake(ctx context.Context, in WakeInput) (out WakeOutput, err error) {
	setup := h.prepareWake(ctx, in)
	defer func() { out = withRegistryReceipt(out, setup) }()
	if setup.err != nil {
		return WakeOutput{}, setup.err
	}
	if setup.refusal != nil {
		return withReceiptState(*setup.refusal, in), nil
	}
	// ADR 0059 p2: the guard is a precondition before any wake is sent. A held
	// heartbeat registers no operation, sleeps no window, rings no doorbell and
	// touches no delivery channel.
	decision, guarded := h.evaluateHeartbeat(in)
	if guarded && !decision.Wake {
		return withReceiptState(guardedWake(in, decision), in), nil
	}
	op, err := h.beginOperation(in)
	if err != nil {
		return WakeOutput{}, err
	}
	if op.Duplicate {
		return withReceiptState(withGuard(duplicateWake(in, op), decision, guarded), in), nil
	}
	if err := h.applyWakeWindow(ctx, in); err != nil {
		return WakeOutput{}, err
	}
	rang := h.ringDoorbell(in)

	finish := func(out WakeOutput) WakeOutput {
		out.DoorbellRang = rang
		return withReceiptState(withOperation(withGuard(out, decision, guarded), op), in)
	}

	if h == nil || h.sender == nil {
		return finish(WakeOutput{
			Delivery: DeliveryUnsupported,
			OK:       false,
			Source:   setup.source,
			Executor: in.Executor,
			Reason:   "wake: no delivery sender configured",
			Woke:     in.Woke,
		}), nil
	}

	delivery, reason := h.sender(ctx, setup.target, in.Text, setup.source, "")
	switch delivery {
	case DeliverySteer, DeliveryQueue:
		return finish(WakeOutput{Delivery: delivery, OK: true, Source: setup.source, Executor: in.Executor, Woke: in.Woke}), nil
	case DeliveryUnsupported:
		if reason == "" {
			reason = "wake: delivery to an open session is not supported"
		}
		return finish(WakeOutput{Delivery: DeliveryUnsupported, OK: false, Source: setup.source, Executor: in.Executor, Reason: reason, Woke: in.Woke}), nil
	default:
		// An unknown value is never promoted to success. Report it as an
		// unsupported delivery with a reason so the caller can branch safely.
		return finish(WakeOutput{
			Delivery: DeliveryUnsupported,
			OK:       false,
			Source:   setup.source,
			Executor: in.Executor,
			Reason:   fmt.Sprintf("wake: unknown delivery value %q", string(delivery)),
			Woke:     in.Woke,
		}), nil
	}
}

// WakeWithFallback runs the ADR 0038 p6 chain after the same boundary checks as
// Wake. The first confirmed channel wins. When no channel is confirmed the
// message is written to the task trace and the honest delivery stays
// unsupported; a trace is never reported as a delivery into the running turn.
// The ADR 0041 p5 stagger runs before the chain so all channels of one waiter
// share the same wake point. A target worktree (ADR 0042 p3) is resolved once
// by prepareWake, so every channel of the chain addresses the same session.
// The duplicate-prompt entry check of ADR 0056 p3 runs before the chain, so a
// repeated prompt never walks a channel or writes a trace. A named mailbox
// rings its addressed doorbell (ADR 0058 p2) as a separate trigger fact; it
// does not change the honest delivery the chain reports. The ADR 0059 p2 guard
// is checked before the chain, so a held heartbeat walks no channel and writes
// no trace. The registry guard of T5.49 runs at the same boundary: a stale or
// unknown executor status holds the wake before any channel or trace is used.
func (h *WakeHandler) WakeWithFallback(ctx context.Context, in WakeInput) (out WakeOutput, err error) {
	setup := h.prepareWake(ctx, in)
	defer func() { out = withRegistryReceipt(out, setup) }()
	if setup.err != nil {
		return WakeOutput{}, setup.err
	}
	if setup.refusal != nil {
		return withReceiptState(*setup.refusal, in), nil
	}
	// ADR 0059 p2: the same guard precondition runs before the fallback chain.
	decision, guarded := h.evaluateHeartbeat(in)
	if guarded && !decision.Wake {
		return withReceiptState(guardedWake(in, decision), in), nil
	}
	op, err := h.beginOperation(in)
	if err != nil {
		return WakeOutput{}, err
	}
	if op.Duplicate {
		return withReceiptState(withGuard(duplicateWake(in, op), decision, guarded), in), nil
	}
	if err := h.applyWakeWindow(ctx, in); err != nil {
		return WakeOutput{}, err
	}
	rang := h.ringDoorbell(in)
	out = withOperation(h.runFallback(ctx, in, setup.target, setup.source), op)
	out.DoorbellRang = rang
	out = withGuard(out, decision, guarded)
	return withReceiptState(out, in), nil
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
		if h != nil && h.sender != nil {
			delivery, reason = h.sender(ctx, target, in.Text, source, channel)
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

// ---------------------------------------------------------------------------
// ADR 0051 p5: transactional verification of token and deadline in the
// confirmation of a wake delivery.
//
// A delivery carries a random confirmation token and a deadline. Ack and Fail
// take a write lock and verify, in one critical section, that the stored token
// matches, the confirmation is still pending, and the deadline has not passed.
// A lagging worker holding an old token cannot confirm another task, and
// recovery needs no reaper goroutine: an expired deadline is resolved inside
// the same transaction. This is a confirmation-time check with a random token;
// it is not the monotonic fencing token of T3.51 (tool_claim.go), which guards
// lease reassignment at claim time.
// ---------------------------------------------------------------------------

// ConfirmationState is the lifecycle of one wake confirmation.
type ConfirmationState string

const (
	ConfirmationPending ConfirmationState = "pending"
	ConfirmationAcked   ConfirmationState = "acked"
	ConfirmationFailed  ConfirmationState = "failed"
	ConfirmationExpired ConfirmationState = "expired"
)

// Confirmation is the stored state of one wake delivery's confirmation. Token
// comes from crypto/rand, so a lagging worker cannot guess or derive it. A zero
// Deadline means the confirmation never expires.
type Confirmation struct {
	ID       string
	Token    string
	State    ConfirmationState
	Deadline time.Time
}

// ConfirmationError is a transactional confirmation rejection. Reason names
// which of the three checked facts failed: token, state or deadline.
type ConfirmationError struct {
	ID     string
	Reason string
}

func (e *ConfirmationError) Error() string {
	return "wake confirmation " + e.ID + ": " + e.Reason
}

// Fixed reasons make the rejection branch testable without parsing strings.
const (
	confirmationReasonNotFound = "unknown confirmation"
	confirmationReasonToken    = "token mismatch, presented by a stale worker"
	confirmationReasonState    = "confirmation is not pending"
	confirmationReasonExpired  = "deadline expired"
	confirmationReasonIssued   = "confirmation already pending"
)

// IsConfirmationRejected reports whether err is any transactional
// confirmation rejection.
func IsConfirmationRejected(err error) bool { return domain.IsErr[*ConfirmationError](err) }

// IsConfirmationExpired reports whether err is the deadline rejection. The
// state is set to expired by the same transaction that returns this error.
func IsConfirmationExpired(err error) bool {
	var rejection *ConfirmationError
	return errors.As(err, &rejection) && rejection.Reason == confirmationReasonExpired
}

// ConfirmationLedger holds wake confirmations. Every mutation holds one write
// lock and does the token, state and deadline checks before touching state, so
// the verification is linearizable and needs no background reaper.
type ConfirmationLedger struct {
	mu       sync.Mutex
	records  map[string]Confirmation
	now      func() time.Time
	newToken func() (string, error)
}

// NewConfirmationLedger builds an empty ledger with a random token source and
// the wall clock. Tests replace now and newToken on the same package instance.
func NewConfirmationLedger() *ConfirmationLedger {
	return &ConfirmationLedger{
		records:  make(map[string]Confirmation),
		now:      time.Now,
		newToken: newConfirmationToken,
	}
}

// newConfirmationToken is a random token, deliberately not a monotonic counter
// like the T3.51 fencing token.
func newConfirmationToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := crand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// Issue records a pending confirmation for id and returns its random token.
// ttl <= 0 means no deadline. A still-pending, unexpired id is not reissued, so
// a second call cannot silently replace the token a caller is holding. An
// expired id may be reissued; that is the recovery path in place of a reaper.
func (l *ConfirmationLedger) Issue(id string, ttl time.Duration) (string, error) {
	if id == "" {
		return "", &ConfirmationError{ID: id, Reason: confirmationReasonNotFound}
	}
	token, err := l.newToken()
	if err != nil {
		return "", fmt.Errorf("wake confirmation: generate token: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if rec, ok := l.records[id]; ok && rec.State == ConfirmationPending &&
		(rec.Deadline.IsZero() || now.Before(rec.Deadline)) {
		return "", &ConfirmationError{ID: id, Reason: confirmationReasonIssued}
	}
	rec := Confirmation{ID: id, Token: token, State: ConfirmationPending}
	if ttl > 0 {
		rec.Deadline = now.Add(ttl)
	}
	l.records[id] = rec
	return token, nil
}

// decide is the single transaction shared by Ack and Fail. Token, pending
// state and unexpired deadline are all checked under one write lock before any
// state is written. An expired deadline is recorded inside that transaction,
// so no reaper goroutine is needed to observe it.
func (l *ConfirmationLedger) decide(id, token string, final ConfirmationState) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	rec, ok := l.records[id]
	if !ok {
		return &ConfirmationError{ID: id, Reason: confirmationReasonNotFound}
	}
	if !rec.Deadline.IsZero() && !l.now().Before(rec.Deadline) {
		rec.State = ConfirmationExpired
		l.records[id] = rec
		return &ConfirmationError{ID: id, Reason: confirmationReasonExpired}
	}
	if rec.State != ConfirmationPending {
		return &ConfirmationError{ID: id, Reason: confirmationReasonState}
	}
	if subtle.ConstantTimeCompare([]byte(rec.Token), []byte(token)) != 1 {
		return &ConfirmationError{ID: id, Reason: confirmationReasonToken}
	}
	rec.State = final
	l.records[id] = rec
	return nil
}

// Ack confirms a delivery in one transaction. It rejects a stale token, a
// confirmation that is no longer pending, and an expired deadline.
func (l *ConfirmationLedger) Ack(id, token string) error {
	return l.decide(id, token, ConfirmationAcked)
}

// Fail marks a delivery as failed under the same three checks as Ack.
func (l *ConfirmationLedger) Fail(id, token string) error {
	return l.decide(id, token, ConfirmationFailed)
}

// State reports the current state of a confirmation. It exists so a test can
// observe the result of a transaction without reaching into the private map.
func (l *ConfirmationLedger) State(id string) (ConfirmationState, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.records[id]
	return rec.State, ok
}

// WithConfirmations attaches a transactional confirmation ledger to a wake
// handler. A nil ledger disables confirmation, keeping every earlier caller
// unchanged.
func (h *WakeHandler) WithConfirmations(ledger *ConfirmationLedger) *WakeHandler {
	if h != nil {
		h.confirmations = ledger
	}
	return h
}

// ConfirmDelivery acks the named confirmation through the handler's ledger.
// It returns a rejection error when no ledger is configured, mirroring an
// honest refusal instead of pretending the delivery was confirmed.
func (h *WakeHandler) ConfirmDelivery(id, token string) error {
	if h == nil || h.confirmations == nil {
		return &ConfirmationError{ID: id, Reason: confirmationReasonNotFound}
	}
	return h.confirmations.Ack(id, token)
}

// FailDelivery fails the named confirmation through the handler's ledger.
func (h *WakeHandler) FailDelivery(id, token string) error {
	if h == nil || h.confirmations == nil {
		return &ConfirmationError{ID: id, Reason: confirmationReasonNotFound}
	}
	return h.confirmations.Fail(id, token)
}

// ---------------------------------------------------------------------------
// ADR 0056 p3 / T3.124: active duplicate-prompt detection and instant
// operationId at wake entry.
//
// A command that starts long work must hand back an operationId immediately,
// before it does any delivery. If an equal prompt is already running, the
// command returns that existing id instead of starting a second task. This is
// detection by prompt content. It is not the clientRequestId request key of
// ADR 0028 and T2.22, which suppresses repeats of one MCP command after a
// broken connection and never compares what the two commands asked for.
// ---------------------------------------------------------------------------

// operationIDPrefix marks an id issued at wake entry so a caller can tell it
// apart from a session id or a clientRequestId.
const operationIDPrefix = "op_"

// newOperationID issues a fresh random id. It uses crypto/rand, like the wake
// confirmation token of ADR 0051, so a caller cannot derive another operation's
// id from this one.
func newOperationID() (string, error) {
	buf := make([]byte, 16)
	if _, err := crand.Read(buf); err != nil {
		return "", err
	}
	return operationIDPrefix + hex.EncodeToString(buf), nil
}

// normalizePrompt gives two prompts one comparison key. Only surrounding
// whitespace is ignored; the content itself is compared byte for byte. This
// keeps a trailing newline from starting a second task without guessing that
// two different prompts mean the same thing.
func normalizePrompt(prompt string) string {
	return strings.TrimSpace(prompt)
}

// OperationRegistry is the dedup index of running long operations. Register
// issues an operationId for prompt and reports whether an equal prompt was
// already registered. The lookup and the insert happen in one call, so two
// concurrent equal prompts cannot both start a task. A composition root can
// replace the in-memory default with a scoped or durable implementation; the
// registry owns when a finished operation leaves the "running" set.
type OperationRegistry interface {
	Register(prompt string) (operationID string, duplicate bool, err error)
}

// memoryOperationRegistry is the in-process default. It keeps one id per
// normalized prompt for the lifetime of the registry and is safe for
// concurrent use.
type memoryOperationRegistry struct {
	mu       sync.Mutex
	byPrompt map[string]string
}

// NewMemoryOperationRegistry builds the default in-process registry.
func NewMemoryOperationRegistry() OperationRegistry {
	return &memoryOperationRegistry{byPrompt: make(map[string]string)}
}

func (r *memoryOperationRegistry) Register(prompt string) (string, bool, error) {
	key := normalizePrompt(prompt)
	if key == "" {
		return "", false, domain.NewAppError(domain.CodeInvalidInput, "wake: prompt is required to register an operation")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.byPrompt[key]; ok {
		return id, true, nil
	}
	id, err := newOperationID()
	if err != nil {
		return "", false, fmt.Errorf("wake: issue operation id: %w", err)
	}
	r.byPrompt[key] = id
	return id, false, nil
}

// OperationResult is the instant outcome of the entry check: the id for this
// prompt and whether the prompt was already running.
type OperationResult struct {
	OperationID string
	Duplicate   bool
}

// WithOperationRegistry attaches the duplicate-prompt index to a handler. A
// nil registry disables the feature, so callers written before ADR 0056 p3
// keep their exact behaviour and output.
func (h *WakeHandler) WithOperationRegistry(registry OperationRegistry) *WakeHandler {
	if h != nil {
		h.operations = registry
	}
	return h
}

// beginOperation runs the active entry check before any delivery. A missing
// prompt or registry keeps the call on the pre-feature path. The id exists
// before a sender, a window or a fallback chain is touched, which is what
// makes it instant.
func (h *WakeHandler) beginOperation(in WakeInput) (OperationResult, error) {
	if h == nil || h.operations == nil || normalizePrompt(in.Prompt) == "" {
		return OperationResult{}, nil
	}
	id, duplicate, err := h.operations.Register(in.Prompt)
	if err != nil {
		return OperationResult{}, err
	}
	return OperationResult{OperationID: id, Duplicate: duplicate}, nil
}

// duplicateWake is the honest receipt for a repeated prompt. No sender is
// touched, so OK stays false and the receipt is a refusal with a reason. The
// two useful facts ride in their own fields: the existing OperationID and
// Duplicate. Delivery stays inside the ADR 0026 closed set; a duplicate is not
// a new success mode and never reports steer or queue.
func duplicateWake(in WakeInput, op OperationResult) WakeOutput {
	return WakeOutput{
		Delivery:    DeliveryUnsupported,
		OK:          false,
		Source:      normalizeSource(in.Source),
		Executor:    in.Executor,
		Reason:      "wake: prompt duplicates a running operation; existing operationId " + op.OperationID + " returned, no second task started",
		Woke:        in.Woke,
		OperationID: op.OperationID,
		Duplicate:   true,
	}
}

// withOperation stamps the entry result onto a delivery receipt.
func withOperation(out WakeOutput, op OperationResult) WakeOutput {
	out.OperationID = op.OperationID
	out.Duplicate = op.Duplicate
	return out
}

// ---------------------------------------------------------------------------
// ADR 0058 p2 / T3.129: addressed doorbell wake.
//
// An agent owns a private mailbox and waits on its own address. The message in
// that mailbox is the trigger: a long-poll listener returns only when a message
// addressed to it arrives. A broadcast message or a message for another
// address never releases a listener, and no ladder timer (ADR 0017) is started.
// This is a wake trigger, not a new Delivery value and not a ladder step.
// ---------------------------------------------------------------------------

// DoorbellMessage is one message deposited in a private mailbox. Address names
// the single mailbox it is for; Broadcast marks a message sent to everyone.
// A broadcast or unaddressed message rings nothing. Text is pointer-only
// content (ADR 0017): the doorbell wakes, it does not carry check contents.
type DoorbellMessage struct {
	Address   string `json:"address"`
	Text      string `json:"text"`
	Broadcast bool   `json:"broadcast,omitempty"`
}

// DoorbellPort is the narrow port the wake handler uses to ring an addressed
// doorbell. The environment adapter supplies the real long-poll mailbox; the
// in-memory Doorbell below is the default for tests and single-process use.
type DoorbellPort interface {
	Ring(msg DoorbellMessage) bool
}

// doorbellMailbox is one address's queue and waiting listeners.
type doorbellMailbox struct {
	pending []DoorbellMessage
	signals []chan struct{}
}

// Doorbell is an in-memory switchboard of addressed private mailboxes. Each
// address has its own queue, so a message for one address can never release a
// listener on another. A broadcast or unaddressed message is deposited
// nowhere, so it cannot wake any agent.
type Doorbell struct {
	mu        sync.Mutex
	mailboxes map[string]*doorbellMailbox
}

// NewDoorbell builds an empty addressed-doorbell switchboard.
func NewDoorbell() *Doorbell {
	return &Doorbell{mailboxes: make(map[string]*doorbellMailbox)}
}

func (d *Doorbell) mailbox(address string) *doorbellMailbox {
	mb := d.mailboxes[address]
	if mb == nil {
		mb = &doorbellMailbox{}
		d.mailboxes[address] = mb
	}
	return mb
}

// removeSignal unregisters a listener's wake signal. It is called under the
// switchboard lock on every Listen exit, so a finished waiter never keeps a
// stale channel in the mailbox.
func removeSignal(mb *doorbellMailbox, sig chan struct{}) {
	for i, s := range mb.signals {
		if s == sig {
			mb.signals = append(mb.signals[:i], mb.signals[i+1:]...)
			return
		}
	}
}

// Ring deposits msg in the mailbox named by its address and notifies that
// mailbox's listeners. A broadcast message or an empty address is addressed to
// no single mailbox: Ring returns false and releases no listener. The return
// value is the separate trigger fact recorded on the wake receipt.
func (d *Doorbell) Ring(msg DoorbellMessage) bool {
	if d == nil || msg.Broadcast || msg.Address == "" {
		return false
	}
	d.mu.Lock()
	mb := d.mailbox(msg.Address)
	mb.pending = append(mb.pending, msg)
	signals := append([]chan struct{}(nil), mb.signals...)
	d.mu.Unlock()
	for _, sig := range signals {
		select {
		case sig <- struct{}{}:
		default:
		}
	}
	return true
}

// Listen is the long-poll entry point of the addressed doorbell. It blocks
// until a message addressed to address arrives, then returns that message. It
// never returns on a timer and never on a broadcast or a message for another
// address: the addressed message is the only trigger (ADR 0058 p2). An empty
// address is rejected. Cancelling ctx releases the waiter with an error and
// leaves any pending message for another listener intact.
func (d *Doorbell) Listen(ctx context.Context, address string) (DoorbellMessage, error) {
	if d == nil || address == "" {
		return DoorbellMessage{}, domain.NewAppError(domain.CodeInvalidInput, "doorbell: an address is required to listen")
	}
	sig := make(chan struct{}, 1)
	d.mu.Lock()
	mb := d.mailbox(address)
	mb.signals = append(mb.signals, sig)
	for {
		if len(mb.pending) > 0 {
			msg := mb.pending[0]
			mb.pending = mb.pending[1:]
			removeSignal(mb, sig)
			d.mu.Unlock()
			return msg, nil
		}
		d.mu.Unlock()
		select {
		case <-sig:
			d.mu.Lock()
		case <-ctx.Done():
			d.mu.Lock()
			removeSignal(mb, sig)
			d.mu.Unlock()
			return DoorbellMessage{}, ctx.Err()
		}
	}
}

// WithDoorbell attaches the addressed doorbell used to ring a listener when a
// wake names a mailbox. A nil port disables the feature, so callers written
// before ADR 0058 p2 keep their exact behaviour.
func (h *WakeHandler) WithDoorbell(port DoorbellPort) *WakeHandler {
	if h != nil {
		h.doorbell = port
	}
	return h
}

// ringDoorbell deposits an addressed message in the recipient's private
// mailbox (ADR 0058 p2). A broadcast message, an empty Mailbox or a missing
// port rings nothing. It runs after the operation entry check, so a duplicate
// prompt never rings a second time.
func (h *WakeHandler) ringDoorbell(in WakeInput) bool {
	if h == nil || h.doorbell == nil || in.Mailbox == "" {
		return false
	}
	return h.doorbell.Ring(DoorbellMessage{Address: in.Mailbox, Text: in.Text, Broadcast: in.Broadcast})
}

// ---------------------------------------------------------------------------
// ADR 0059 p2 / T3.135: guarded heartbeat.
//
// A timer tick is not a reason to wake. An agent wakes only when the observed
// state changed since the last guarded wake or the quota should-run gate is
// open. The guard runs as a precondition before a wake is sent: a held tick
// registers no operation, rings no doorbell and reaches no delivery channel.
// This is a wake precondition, not a new Delivery value and not a step of the
// ADR 0017 ladder.
// ---------------------------------------------------------------------------

// HeartbeatObservation is the environment's report at one heartbeat. State is
// an opaque fingerprint of the observed state; the core never interprets it.
// QuotaShouldRun is the open state of the quota gate. Tick says this attempt
// is a timer tick and is recorded only so a held decision can name it; a tick
// never by itself releases a wake.
type HeartbeatObservation struct {
	State          string `json:"state"`
	QuotaShouldRun bool   `json:"quotaShouldRun"`
	Tick           bool   `json:"tick"`
}

// HeartbeatDecision is the result of evaluating one observation against the
// guard. Wake is released only by Changed or GateOpen. Reason names which
// facts fired. Changed and GateOpen are separate facts, not delivery modes.
type HeartbeatDecision struct {
	Wake     bool
	Changed  bool
	GateOpen bool
	Tick     bool
	Reason   string
}

// Fixed guard reasons name the decision branch so tests and callers do not
// parse strings. The held reasons state that a tick alone does not wake.
const (
	guardReasonHeld           = "wake: guarded heartbeat held; no state change and quota gate closed"
	guardReasonTickHeld       = "wake: guarded heartbeat held; a timer tick alone does not wake"
	guardReasonChanged        = "wake: guarded heartbeat released; state changed"
	guardReasonGate           = "wake: guarded heartbeat released; quota should-run gate is open"
	guardReasonChangedAndGate = "wake: guarded heartbeat released; state changed and quota should-run gate is open"
)

// HeartbeatGuard remembers the last observed state so a change is measured
// against the most recent guarded wake. The first observation establishes the
// baseline and is not itself a change: a fresh agent does not wake just from
// having an initial state. Every observation updates the baseline, including a
// held one, so the next change is relative to the latest heartbeat.
type HeartbeatGuard struct {
	mu        sync.Mutex
	lastState string
	seen      bool
}

// NewHeartbeatGuard builds an empty guard with no baseline state.
func NewHeartbeatGuard() *HeartbeatGuard {
	return &HeartbeatGuard{}
}

// Evaluate decides one observation under one lock. The lock makes the compare
// and the baseline update a single step, so concurrent heartbeats cannot both
// read an old baseline and both fire on the same change. A timer tick is never
// a trigger: Wake is set only by a state change or an open quota gate.
func (g *HeartbeatGuard) Evaluate(obs HeartbeatObservation) HeartbeatDecision {
	if g == nil {
		return HeartbeatDecision{Reason: guardReasonHeld, Tick: obs.Tick}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	changed := g.seen && obs.State != g.lastState
	gate := obs.QuotaShouldRun
	g.lastState = obs.State
	g.seen = true

	d := HeartbeatDecision{Changed: changed, GateOpen: gate, Tick: obs.Tick}
	switch {
	case changed && gate:
		d.Wake, d.Reason = true, guardReasonChangedAndGate
	case changed:
		d.Wake, d.Reason = true, guardReasonChanged
	case gate:
		d.Wake, d.Reason = true, guardReasonGate
	case obs.Tick:
		d.Reason = guardReasonTickHeld
	default:
		d.Reason = guardReasonHeld
	}
	return d
}

// WithHeartbeatGuard attaches the guarded-heartbeat precondition to a wake
// handler. A nil guard disables the feature, so callers written before ADR 0059
// p2 keep their exact behaviour.
func (h *WakeHandler) WithHeartbeatGuard(g *HeartbeatGuard) *WakeHandler {
	if h != nil {
		h.guard = g
	}
	return h
}

// WithRegistryGuard attaches the registry status precondition to a wake
// handler (T5.49). A nil guard keeps the pre-feature path, so callers that own
// no registry still wake through their delivery port. With a guard attached,
// every wake asks for the executor status before any delivery or operation is
// started; an unknown or stale row is an honest refusal.
func (h *WakeHandler) WithRegistryGuard(g RegistryWakeGuard) *WakeHandler {
	if h != nil {
		h.registryGuard = g
	}
	return h
}

// evaluateHeartbeat runs the guard when one is attached. The bool reports
// whether a guard evaluated this wake, so callers with no guard keep the
// pre-feature path and emit no guard facts on their receipt.
func (h *WakeHandler) evaluateHeartbeat(in WakeInput) (HeartbeatDecision, bool) {
	if h == nil || h.guard == nil {
		return HeartbeatDecision{}, false
	}
	return h.guard.Evaluate(HeartbeatObservation{
		State:          in.HeartbeatState,
		QuotaShouldRun: in.QuotaShouldRun,
		Tick:           in.HeartbeatTick,
	}), true
}

// withGuard stamps guard facts on a receipt when a guard evaluated the wake.
func withGuard(out WakeOutput, d HeartbeatDecision, guarded bool) WakeOutput {
	if !guarded {
		return out
	}
	out.Guarded = true
	out.GuardReason = d.Reason
	out.HeartbeatChanged = d.Changed
	out.QuotaGateOpen = d.GateOpen
	out.HeartbeatTick = d.Tick
	return out
}

// guardedWake is the honest receipt for a held heartbeat. No sender, operation,
// window or doorbell is touched, so OK stays false and Delivery stays inside
// the ADR 0026 closed set. A held wake is not a new success mode.
func guardedWake(in WakeInput, d HeartbeatDecision) WakeOutput {
	return withGuard(WakeOutput{
		Delivery: DeliveryUnsupported,
		OK:       false,
		Source:   normalizeSource(in.Source),
		Executor: in.Executor,
		Reason:   d.Reason,
		Woke:     in.Woke,
	}, d, true)
}

// AttachWake registers the real wake tool on a server. NewServer does not call
// it: ADR 0263 fixes the MCP contract at seven names and forbids silent wake
// registration. The constructor stays for callers that hold an explicit
// WakeHandler; adding a tool name still needs a separate ADR. This is a
// permanent boundary, not a deferred Wave 6-8 task.
func AttachWake(server *mcpsdk.Server, handler *WakeHandler) {
	if server == nil || handler == nil {
		return
	}
	server.RemoveTools(WakeToolName)
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        WakeToolName,
		Description: "Wake an open session and report the delivery actually used: steer, queue, unsupported, or interrupted when a human is needed. Delivery says the follow-up was sent, not that the agent started; woke is a separate caller-supplied confirmation. Source review marks a diff comment delivered as steer. A fallback handler tries the running turn, then the next turn, then the task trace; a traced follow-up stays unsupported and is not a delivery. wakeBetweenMillis in the wait contract staggers each waiter to one random point in that window so waiters on the same event do not wake all at once. targetWorktree addresses the notification to the worktree path, not a session id: a restarted session in that worktree receives it and a session in another worktree does not. The state field reports the receipt lifecycle state: spooled, pending, held, pushed, push-unreachable, read, refused or expired. State answers where the message is, delivery answers how it was sent; refused, push-unreachable and expired are never a delivered follow-up. prompt names the content of the long job: an operationId is issued at entry, and an equal prompt already running returns that existing id with duplicate=true and starts no second task. mailbox names the recipient's private doorbell: a message addressed to it rings the bell so a long-poll listener on that address wakes, and doorbellRang reports it. A broadcast message reaches no single mailbox and rings nothing; the doorbell is a wake trigger, not a new delivery value or a ladder step. heartbeatState, quotaShouldRun and heartbeatTick feed the guarded heartbeat: when a guard is attached the wake is released only if the state changed or the quota gate is open, so a timer tick alone holds the wake with ok=false and guarded=true. The guard is a precondition checked before any delivery, not a new delivery value or a ladder step. registryStatus reports the executor status held by the registry guard; a stale or missing row holds the wake with ok=false and no delivery, because a heartbeat tick does not replace a successful sync.",
		Annotations: attachToolAnnotations(false),
	}, handler.Handle)
}
