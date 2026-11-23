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
// A wait bound to a CapabilityContext (ADR 0040 §1) is cancelled as a whole
// tree when its calling agent disappears. The caller's departure cancels the
// context, which delivers one reason to every task and writes it to the cancel
// journal. The long-poll timeout stays a private case and never cancels a tree.
// Capability matching from T3.47 is unchanged.
//
// On entry to the long-poll a wait passes through the shared CI gate port
// (ADR 0037 p3), keyed by the Watch's repo/PR/headSHA. The composition root
// injects the concrete gate; with no gate wired, each caller runs its own
// operation as before.
//
// ADR 0045 p1 adds an event filter and a start key (T3.102). With eventKinds
// set, the wait returns on the first update of a listed kind. The filter adds
// kind selection on top of the T3.48 event return; it does not replace it. A
// startKey makes a repeat join the first wait, so a lost response cannot open
// a second task. That key gauges the start of waiting, unlike clientRequestId
// (ADR 0028), which deduplicates repeats of a command.
//
// ADR 0046 p3 adds the stop reason to the same response. A wait now names why
// it ended: terminal, input_required, unknown_submission, timeout or
// interrupted. The reason is a new field appended after the byte-stable fields
// of ADR 0036 §2, so an output without a reason keeps its exact old bytes. It
// only names the return that already happened; the event and approval boundary
// of ADR 0037 is unchanged, and needs_input still parks without a read write.
//
// SB-16 adds a bounded requery for three fixable agent action errors: a
// malformed action, a blocked action and a shell syntax error. Each error has
// its own response template and all three share a budget of three attempts.
// Corrected attempts go to a trajectory sink, never to the visible wait result
// or its stop reasons. An exhausted budget ends the run with exit_format; an
// API or environment error ends it at once. The wake rule and the red-check
// retry budget stay unchanged.
//
// The tool is deliberately not registered in NewServer; wiring it into the
// server is a separate integration task. AttachWaitResult is the hook that task
// will call.
package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
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

// Top-level field names of a wait_result response. ADR 0036 §2 fixes the
// field order with constants so the same state always serialises to the same
// bytes and a prefix-cache keyed by the response prefix does not burn. The
// canonical order is status, verdict, revision, checks, pollAfterMs; the
// MarshalJSON method below is the single place that emits them. ADR 0046 p3
// appends one more field, reason, after the five fixed ones. SB-03 appends two
// advisory fields, infra_tier and infra_reason, after reason. SB-05 appends
// exit_cause and partial_diff after those, so a task that died before PR
// creation can hand its saved work to one following turn. Nothing is reordered,
// so an output that carries none of the appended fields still serialises to the
// exact bytes the ADR 0036 §2 contract locks.
const (
	waitResultFieldStatus      = "status"
	waitResultFieldVerdict     = "verdict"
	waitResultFieldRevision    = "revision"
	waitResultFieldChecks      = "checks"
	waitResultFieldPollAfterMs = "pollAfterMs"
	waitResultFieldReason      = "reason"
	waitResultFieldInfraTier   = "infra_tier"
	waitResultFieldInfraReason = "infra_reason"
	waitResultFieldExitCause   = "exit_cause"
	waitResultFieldPartialDiff = "partial_diff"
)

// WaitStopReason is why a wait ended (ADR 0046 p3). The five values are
// distinct: a script must not mistake a timeout for a terminal result, and it
// must not resend a submission the server never accepted. This reason names
// the stop only; it does not move the event or approval return of ADR 0037 and
// does not replace the ADR 0026 delivery field.
type WaitStopReason string

// The long-poll timeout has its own wire value. A terminal domain verdict also
// serialises as "timeout", and get_result maps that verdict to nextAction=wait
// with "the Watch is finished". Both strings reach the same agent, but this
// reason is retryable and exits 143. A client branching on one string would
// finish the turn on a wait it should retry (BCX-11). The two contracts keep
// separate values so the string alone decides whether to wait or to end.
const (
	WaitStopTerminal          WaitStopReason = "terminal"
	WaitStopInputRequired     WaitStopReason = "input_required"
	WaitStopUnknownSubmission WaitStopReason = "unknown_submission"
	WaitStopTimeout           WaitStopReason = "wait_timeout"
	WaitStopInterrupted       WaitStopReason = "interrupted"
)

// valid reports whether r is one of the five contract reasons.
func (r WaitStopReason) valid() bool {
	switch r {
	case WaitStopTerminal, WaitStopInputRequired, WaitStopUnknownSubmission,
		WaitStopTimeout, WaitStopInterrupted:
		return true
	default:
		return false
	}
}

// ExitCode is the operation's exit code for this reason (ADR 0046 p3). A
// terminal result and a parked input request are successes. The three failure
// reasons get distinct codes so a script can tell them apart: an unknown
// submission is a usage error, an interruption is a signal, a timeout is an
// expired budget. The CLI owns the actual process exit; this mapping is the
// contract value it reads from the reason. Code 143 belongs only to the
// "wait_timeout" reason, so an expired budget never reads as nextAction=wait
// borrowed from the terminal "timeout" verdict (BCX-11).
func (r WaitStopReason) ExitCode() int {
	switch r {
	case WaitStopUnknownSubmission:
		return 2
	case WaitStopInterrupted:
		return 130
	case WaitStopTimeout:
		return 143
	default:
		return 0
	}
}

// Retryable reports whether a caller may resend the wait. Only an unknown
// submission is refused: the server never registered it, so a repeat cannot
// succeed. The other four reasons describe a wait that ran and may be tried
// again (after an answer, a fresh budget or a new submission).
func (r WaitStopReason) Retryable() bool {
	return r.valid() && r != WaitStopUnknownSubmission
}

// WaitReasonOutput is the named response type of the typed wait_result handler.
// WaitResultOutput declares reason (BCX-43), so this type adds no second copy of
// the field. Its derived schema matches the advertised OutputSchema.
type WaitReasonOutput struct {
	WaitResultOutput
}

// MarshalJSON delegates to WaitResultOutput so the byte-stable field order of
// ADR 0036 §2 lives in one place.
func (o WaitReasonOutput) MarshalJSON() ([]byte, error) {
	return o.WaitResultOutput.MarshalJSON()
}

// waitReasonOutput lifts a reason stamped on a WaitResultOutput into the real
// Reason field. The reserved Checks key is removed so a client sees the reason
// once, as a top-level field.
func waitReasonOutput(out WaitResultOutput) WaitReasonOutput {
	reason := waitStopReasonOf(out)
	if reason != "" {
		checks := make(map[string]domain.Check, len(out.Checks))
		for name, check := range out.Checks {
			if name != waitStopReasonCheckName {
				checks[name] = check
			}
		}
		out.Checks = checks
		out.Reason = reason
	}
	return WaitReasonOutput{WaitResultOutput: out}
}

// waitStopReasonCheckName is the reserved Checks key that carries the reason
// through service paths returning WaitResultOutput before the handler lifts it
// into the declared Reason field. MarshalJSON reads either source and hides the
// key from checks. waitStopReasonOf validates the value, so a user check that
// reuses the key without a valid reason is emitted as an ordinary check.
const waitStopReasonCheckName = "_beaconWaitStopReason"

// SB-03 uses the same carry-through pattern for the two advisory fields. A
// classification stamped on a WaitResultOutput rides in Checks until the
// handler marshals it into the top-level infra_tier and infra_reason fields.
// SB-05 carries exit_cause and partial_diff the same way: the adapter stamps
// them from the last trajectory step on a terminal error, and MarshalJSON
// lifts them into top-level fields. The keys are reserved; a user check that
// reuses one with an invalid value stays an ordinary check.
const (
	waitInfraTierCheckName   = "_beaconInfraTier"
	waitInfraReasonCheckName = "_beaconInfraReason"
	waitExitCauseCheckName   = "_beaconExitCause"
	waitPartialDiffCheckName = "_beaconPartialDiff"
)

// withWaitStopReason stamps reason onto out without mutating the caller's
// Checks map. An empty or unknown reason leaves out untouched.
func withWaitStopReason(out WaitResultOutput, reason WaitStopReason) WaitResultOutput {
	if !reason.valid() {
		return out
	}
	checks := make(map[string]domain.Check, len(out.Checks)+1)
	for name, check := range out.Checks {
		checks[name] = check
	}
	checks[waitStopReasonCheckName] = domain.Check{State: string(reason), Conclusion: string(reason)}
	out.Checks = checks
	return out
}

// StopReasonOf reads the stop reason from the reserved Checks key. Service
// paths stamp that key before the handler lifts the value into the declared
// Reason field. An empty result means the output carries no reason, for example
// a precheck failure or an event tick.
func StopReasonOf(out WaitResultOutput) WaitStopReason {
	return waitStopReasonOf(out)
}

// waitStopReasonOf reads the reason stamped on out. It returns empty when no
// reason was stamped or the reserved key holds something other than a valid
// reason.
func waitStopReasonOf(out WaitResultOutput) WaitStopReason {
	check, ok := out.Checks[waitStopReasonCheckName]
	if !ok {
		return ""
	}
	reason := WaitStopReason(check.State)
	if !reason.valid() {
		return ""
	}
	return reason
}

// WithInfraClassification stamps tier and reason onto out without mutating the
// caller's Checks map. Invalid values leave out untouched, so a caller cannot
// inject an arbitrary tier or reason through the reserved keys. The stamp is
// advisory: it changes neither out.Verdict nor the wait stop reason.
func WithInfraClassification(out WaitResultOutput, tier domain.InfraTier, reason domain.InfraReason) WaitResultOutput {
	if !tier.Valid() || !reason.Valid() {
		return out
	}
	checks := make(map[string]domain.Check, len(out.Checks)+2)
	for name, check := range out.Checks {
		checks[name] = check
	}
	checks[waitInfraTierCheckName] = domain.Check{State: string(tier), Conclusion: string(tier)}
	checks[waitInfraReasonCheckName] = domain.Check{State: string(reason), Conclusion: string(reason)}
	out.Checks = checks
	return out
}

// InfraClassificationOf reads the advisory tier and reason stamped on out.
// Empty values mean the output carries no classification.
func InfraClassificationOf(out WaitResultOutput) (domain.InfraTier, domain.InfraReason) {
	return infraTierOf(out), infraReasonOf(out)
}

func infraTierOf(out WaitResultOutput) domain.InfraTier {
	tier := domain.InfraTier(out.Checks[waitInfraTierCheckName].State)
	if !tier.Valid() {
		return ""
	}
	return tier
}

func infraReasonOf(out WaitResultOutput) domain.InfraReason {
	reason := domain.InfraReason(out.Checks[waitInfraReasonCheckName].State)
	if !reason.Valid() {
		return ""
	}
	return reason
}

// ExitStatusCause names why a task died before it could open a PR. The values
// mirror the terminal causes of the task's exit_status table: context, cost,
// timeout, api, environment and unknown. An empty cause marks a trajectory
// that ended with no exit_status. The cause is advisory: it never replaces a
// WaitStopReason or its exit code, so all five reasons and codes stay intact
// (BCX-11).
type ExitStatusCause string

const (
	ExitStatusContext     ExitStatusCause = "context"
	ExitStatusCost        ExitStatusCause = "cost"
	ExitStatusTimeout     ExitStatusCause = "timeout"
	ExitStatusAPI         ExitStatusCause = "api"
	ExitStatusEnvironment ExitStatusCause = "environment"
	ExitStatusUnknown     ExitStatusCause = "unknown"
)

func (c ExitStatusCause) valid() bool {
	switch c {
	case ExitStatusContext, ExitStatusCost, ExitStatusTimeout,
		ExitStatusAPI, ExitStatusEnvironment, ExitStatusUnknown:
		return true
	default:
		return false
	}
}

// NeedsRetry reports whether a trajectory with this cause must run again. An
// empty cause means the task ended with no exit_status. Such a trajectory is
// incomplete: the batch removes it before a repeat instead of counting it as
// a finished submission. A recorded cause is terminal and is not retried
// blindly. Source: comp-swebench, run_batch.py:376-409.
func NeedsRetry(cause ExitStatusCause) bool {
	return cause == ""
}

// WithTerminalFailure stamps the terminal cause and the saved partial diff on
// out without mutating the caller's Checks map. The adapter calls it with the
// diff from the last trajectory step when the runtime dies before PR creation.
// An invalid cause or an empty diff leaves that stamp unset. The stamp is
// advisory: it changes neither out.Verdict nor the wait stop reason.
func WithTerminalFailure(out WaitResultOutput, cause ExitStatusCause, partialDiff string) WaitResultOutput {
	if !cause.valid() && partialDiff == "" {
		return out
	}
	checks := make(map[string]domain.Check, len(out.Checks)+2)
	for name, check := range out.Checks {
		checks[name] = check
	}
	if cause.valid() {
		checks[waitExitCauseCheckName] = domain.Check{State: string(cause), Conclusion: string(cause)}
	}
	if partialDiff != "" {
		checks[waitPartialDiffCheckName] = domain.Check{State: partialDiff, Conclusion: partialDiff}
	}
	out.Checks = checks
	return out
}

// FailureCauseOf reads the terminal cause stamped on out. An empty result
// means the output carries no cause, for example a wait that ended without a
// terminal task failure.
func FailureCauseOf(out WaitResultOutput) ExitStatusCause {
	cause := ExitStatusCause(out.Checks[waitExitCauseCheckName].State)
	if !cause.valid() {
		return ""
	}
	return cause
}

// PartialDiffOf reads the saved partial diff stamped on out. An empty result
// means the output carries no saved work. A restarted session reads this value
// from the wait journal and opens the PR from it in one following turn.
func PartialDiffOf(out WaitResultOutput) string {
	return out.Checks[waitPartialDiffCheckName].State
}

// ---- SB-16: limited requery after a fixable agent action error ----
//
// SWE-agent gives a model up to max_requeries attempts to correct three action
// errors: a malformed action, a blocked action and a shell syntax error
// (sweagent/agent/agents.py:149-161, :1062-1125). Each error gets its own
// response template. The corrected attempt goes to the trajectory, not to the
// visible message history. After the budget is spent the run ends with
// exit_format (:1211-1218). An API or environment error is not a fixable action
// error: it ends the run at once with its own exit status.
//
// This budget is separate from the red-check retry budget of
// domain.FlakyConfig and from every WaitStopReason value. It changes no verdict
// and no wake rule. A format error the agent fixes itself costs no orchestrator
// slot.

// ActionErrorKind names an agent action error the model can correct after a
// requery prompt. The three values mirror the three retry branches of
// forward_with_handling in sweagent. API, environment and runtime errors are
// not action errors and never enter this loop.
type ActionErrorKind string

const (
	ActionFormatError   ActionErrorKind = "action_format"
	ActionBlockedAction ActionErrorKind = "blocked_action"
	ActionCommandSyntax ActionErrorKind = "command_syntax"
)

func (k ActionErrorKind) valid() bool {
	switch k {
	case ActionFormatError, ActionBlockedAction, ActionCommandSyntax:
		return true
	default:
		return false
	}
}

// Fixable reports whether the model can correct this error by answering a
// requery prompt. Only the three action errors are fixable.
func (k ActionErrorKind) Fixable() bool { return k.valid() }

// MaxActionRequeries is the shared requery budget for one run. The same counter
// covers all three action errors (sweagent max_requeries = 3). It is not
// domain.FlakyConfig.MaxRetries, which counts red CI checks.
const MaxActionRequeries = 3

// ActionFormatExitStatus is the terminal outcome after the requery budget is
// exhausted. It is an exit status of the agent run, not a WaitStopReason.
const ActionFormatExitStatus = "exit_format"

// ActionRequeryTemplate returns the separate response prompt for one fixable
// action error. The prompt names the error, includes the rejected output and
// the error message, and asks for one corrected action. It carries no verdict
// and no wake instruction. An error outside the three kinds returns an empty
// template, so the caller ends the run instead of requerying.
func ActionRequeryTemplate(kind ActionErrorKind, output, message string) string {
	const head = "The last action was rejected. Send one corrected action only."
	switch kind {
	case ActionFormatError:
		return head + " The action could not be parsed as a tool call.\nRejected output:\n" + output + "\nError: " + message
	case ActionBlockedAction:
		return head + " The action used a command on the blocklist. Replace the blocked command.\nRejected output:\n" + output + "\nError: " + message
	case ActionCommandSyntax:
		return head + " The shell command has a syntax error. Fix the syntax.\nRejected output:\n" + output + "\nError: " + message
	default:
		return ""
	}
}

// ActionRequeryOutcome is the decision for one failed agent action. Requery is
// true only for a fixable error inside the budget. ExitStatus is set when the
// run ends: exit_format after the budget is spent, or the caller's own status
// for an error that cannot be fixed by a requery. Attempt is the 1-based
// requery number, or the number of requeries already spent at exit.
type ActionRequeryOutcome struct {
	Requery    bool
	Attempt    int
	Template   string
	ExitStatus string
}

// DecideActionRequery spends one step of the action error budget. A fixable
// error inside the budget returns a requery with its own template. The next
// fixable error after the budget returns exit_format. Any other error returns
// the supplied exit status and no requery, so an API or environment failure
// ends the run at once.
func DecideActionRequery(kind ActionErrorKind, priorRequeries int, output, message, exitStatus string) ActionRequeryOutcome {
	if !kind.Fixable() {
		return ActionRequeryOutcome{ExitStatus: exitStatus}
	}
	if priorRequeries >= MaxActionRequeries {
		return ActionRequeryOutcome{ExitStatus: ActionFormatExitStatus, Attempt: priorRequeries}
	}
	return ActionRequeryOutcome{
		Requery:  true,
		Attempt:  priorRequeries + 1,
		Template: ActionRequeryTemplate(kind, output, message),
	}
}

// ActionTrajectoryRecord is one corrected attempt kept outside the visible
// message history. A requery writes here only: the attempt never reaches a
// WaitResultOutput field, a Check, a verdict or a WaitStopReason.
type ActionTrajectoryRecord struct {
	Kind     ActionErrorKind
	Attempt  int
	Output   string
	Message  string
	Template string
	At       time.Time
}

// ActionTrajectory is the sink for corrected attempts. It adds a read-back to
// the write port so tests and diagnostics can inspect what was saved. The app
// layer supplies durable storage; the in-memory sink below is the default.
type ActionTrajectory interface {
	RecordActionAttempt(ActionTrajectoryRecord)
	Records() []ActionTrajectoryRecord
}

// NewMemoryActionTrajectory builds the in-process trajectory sink.
func NewMemoryActionTrajectory() ActionTrajectory {
	return &memoryActionTrajectory{}
}

type memoryActionTrajectory struct {
	mu      sync.Mutex
	records []ActionTrajectoryRecord
}

func (t *memoryActionTrajectory) RecordActionAttempt(rec ActionTrajectoryRecord) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.records = append(t.records, rec)
}

// Records returns the corrected attempts in insertion order.
func (t *memoryActionTrajectory) Records() []ActionTrajectoryRecord {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]ActionTrajectoryRecord, len(t.records))
	copy(out, t.records)
	return out
}

// TerminalOutcomeRecord is one journal line: which Watch ended with which
// cause. The cause comes from the SB-05 table, so this adds no second enum.
type TerminalOutcomeRecord struct {
	WatchID string
	Cause   ExitStatusCause
	At      time.Time
}

// TerminalOutcomeJournal is the sink for terminal causes. Counts groups the
// recorded lines by cause, so stats reads the journal and keeps no second
// counter. The in-memory implementation below is the default.
type TerminalOutcomeJournal interface {
	RecordTerminalOutcome(TerminalOutcomeRecord)
	Records() []TerminalOutcomeRecord
	Counts() map[ExitStatusCause]int
}

type memoryTerminalOutcomeJournal struct {
	mu      sync.Mutex
	records []TerminalOutcomeRecord
}

// NewMemoryTerminalOutcomeJournal builds the in-process outcome journal.
func NewMemoryTerminalOutcomeJournal() TerminalOutcomeJournal {
	return &memoryTerminalOutcomeJournal{}
}

func (j *memoryTerminalOutcomeJournal) RecordTerminalOutcome(record TerminalOutcomeRecord) {
	if j == nil || !record.Cause.valid() {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.records = append(j.records, record)
}

// Records returns a snapshot of the journal in insertion order.
func (j *memoryTerminalOutcomeJournal) Records() []TerminalOutcomeRecord {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]TerminalOutcomeRecord, len(j.records))
	copy(out, j.records)
	return out
}

// Counts groups the recorded causes. A cause with no Watch is absent from the
// map, so a reader never reports a group that was not observed.
func (j *memoryTerminalOutcomeJournal) Counts() map[ExitStatusCause]int {
	records := j.Records()
	out := make(map[ExitStatusCause]int, len(records))
	for _, record := range records {
		out[record.Cause]++
	}
	return out
}

// MarshalJSON writes a WaitResultOutput in the fixed order above. encoding/json
// marshals struct fields in declaration order and map keys in sorted order,
// but spelling the order out here keeps it a deliberate, testable contract
// instead of an accident of the struct layout (ADR 0036 §2). Omitted fields
// follow the same omitempty rules as the type in schema.go.
func (o WaitResultOutput) MarshalJSON() ([]byte, error) {
	type field struct {
		name  string
		value any
	}
	// Reason is declared on WaitResultOutput (BCX-43). Service paths stamp the
	// reserved Checks key before the handler lifts it into that field, so read
	// both sources: the real field wins, the key keeps older paths working.
	// Appending after pollAfterMs keeps the five ADR 0036 §2 fields byte-stable
	// when no reason is present. SB-03 appends the advisory tier and reason
	// after reason, on the same omitempty rule. SB-05 appends the terminal
	// exit cause and the saved partial diff after those, also omitempty.
	reason := o.Reason
	if reason == "" {
		reason = waitStopReasonOf(o)
	}
	infraTier := infraTierOf(o)
	infraReason := infraReasonOf(o)
	exitCause := FailureCauseOf(o)
	partialDiff := PartialDiffOf(o)
	checks := o.Checks
	if reason != "" || infraTier != "" || infraReason != "" || exitCause != "" || partialDiff != "" {
		checks = make(map[string]domain.Check, len(o.Checks))
		for name, check := range o.Checks {
			switch name {
			case waitStopReasonCheckName, waitInfraTierCheckName, waitInfraReasonCheckName,
				waitExitCauseCheckName, waitPartialDiffCheckName:
				continue
			}
			checks[name] = check
		}
	}

	fields := []field{{name: waitResultFieldStatus, value: o.Status}}
	if o.Verdict != "" {
		fields = append(fields, field{name: waitResultFieldVerdict, value: o.Verdict})
	}
	fields = append(fields, field{name: waitResultFieldRevision, value: o.Revision})
	if len(checks) > 0 {
		fields = append(fields, field{name: waitResultFieldChecks, value: checks})
	}
	if o.PollAfterMs != nil {
		fields = append(fields, field{name: waitResultFieldPollAfterMs, value: o.PollAfterMs})
	}
	if reason != "" {
		fields = append(fields, field{name: waitResultFieldReason, value: reason})
	}
	if infraTier != "" {
		fields = append(fields, field{name: waitResultFieldInfraTier, value: infraTier})
	}
	if infraReason != "" {
		fields = append(fields, field{name: waitResultFieldInfraReason, value: infraReason})
	}
	if exitCause != "" {
		fields = append(fields, field{name: waitResultFieldExitCause, value: exitCause})
	}
	if partialDiff != "" {
		fields = append(fields, field{name: waitResultFieldPartialDiff, value: partialDiff})
	}

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(f.name)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(f.value)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// WaitResultInput is the argument object of beacon_wait_result. timeoutMs is
// optional; the service applies the default and the nine-minute cap.
// clientRequestId is the idempotency key (ADR 0028). When it is non-empty, a
// repeat with the same key returns the first result and performs no second
// effect. An empty key keeps the pre-idempotency behaviour for callers that do
// not send the field.
type WaitResultInput struct {
	WatchID         string `json:"watchID" jsonschema:"identifier of the Watch to wait on; a handle leased for 30 minutes from issue; after that wait_result returns WATCH_EXPIRED"`
	TimeoutMs       *int64 `json:"timeoutMs,omitempty" jsonschema:"wait budget in milliseconds; default 50000, capped at 540000"`
	ClientRequestID string `json:"clientRequestId,omitempty" jsonschema:"idempotency key; a repeat after a broken connection returns the first result without a second effect"`
	// ContinueOf names the same Watch after a human answer resumed a parked
	// needs_input task (ADR 0031 §3). The wait keeps polling that Watch; it
	// never creates a new task. An empty value keeps the original contract.
	ContinueOf string `json:"continueOf,omitempty" jsonschema:"watchID of the same task this wait continues after a human answer; must equal watchID and creates no new task"`
	// EventKinds selects which job events end this wait (ADR 0045 p1). Each
	// value must be progress, checkpoint or completed. With a non-empty list,
	// the first update whose kind is listed ends the wait, including when an
	// unlisted kind carries a terminal verdict. An empty list keeps the T3.48
	// behaviour: any terminal verdict ends the wait.
	EventKinds []string `json:"eventKinds,omitempty" jsonschema:"event kinds that end the wait; each is progress, checkpoint or completed; empty keeps the terminal-verdict default"`
	// StartKey is the idempotency key at the start of waiting (ADR 0045 p1).
	// A repeat with the same key returns the first outcome and opens no second
	// wait or task. It is distinct from clientRequestId, which deduplicates
	// repeats of a command rather than the start of a wait.
	StartKey string `json:"startKey,omitempty" jsonschema:"idempotency key at the start of waiting; a repeat with the same key returns the first outcome and creates no second task"`
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

// waitStart is one registered start of a wait, keyed by StartKey (ADR 0045 p1).
// The owner of the entry runs the wait and publishes the outcome; a repeat with
// the same key waits on done and returns that outcome without running a second
// wait, even when the first response was lost. The registry evicts the oldest
// start when it fills, so the index cannot grow without bound.
type waitStart struct {
	done chan struct{}
	out  WaitResultOutput
	err  error
}

// finish publishes the outcome and releases every caller joined on this start.
func (e *waitStart) finish(out WaitResultOutput, err error) {
	e.out = out
	e.err = err
	close(e.done)
}

// await returns the outcome of the first wait with this key. A caller whose own
// context ends stops waiting without writing anything.
func (e *waitStart) await(ctx context.Context) (WaitResultOutput, error) {
	select {
	case <-e.done:
		return cloneWaitResultOutput(e.out), e.err
	case <-ctx.Done():
		return WaitResultOutput{}, domain.NewAppError(domain.CodeCancelled, "wait_result: wait start cancelled")
	}
}

// maxWaitStarts bounds the number of live start keys. Without a cap a caller
// that keeps sending fresh start keys would grow the registry for the lifetime
// of the service. Eviction drops the oldest start first; a caller already
// holding an entry is unaffected because finish still releases its joiners.
const maxWaitStarts = 4096

// waitStartRegistry maps a StartKey to the wait it first started, up to
// maxWaitStarts live starts. begin returns the entry and true for the caller
// that must run the wait, false for a repeat that only joins the existing one.
// A start stays available for replay until the registry evicts it.
type waitStartRegistry struct {
	mu     sync.Mutex
	starts map[string]*waitStart
	order  []string // start keys in insertion order, for oldest-first eviction
}

func newWaitStartRegistry() *waitStartRegistry {
	return &waitStartRegistry{starts: make(map[string]*waitStart)}
}

// forgetLocked removes a key from the eviction order. The caller holds r.mu.
func (r *waitStartRegistry) forgetLocked(key string) {
	for i, k := range r.order {
		if k == key {
			copy(r.order[i:], r.order[i+1:])
			r.order[len(r.order)-1] = ""
			r.order = r.order[:len(r.order)-1]
			return
		}
	}
}

func (r *waitStartRegistry) begin(key string) (*waitStart, bool) {
	if key == "" || r == nil {
		return &waitStart{done: make(chan struct{})}, true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.starts[key]; ok {
		return existing, false
	}
	if len(r.starts) >= maxWaitStarts {
		oldest := r.order[0]
		delete(r.starts, oldest)
		r.forgetLocked(oldest)
	}
	entry := &waitStart{done: make(chan struct{})}
	r.starts[key] = entry
	r.order = append(r.order, key)
	return entry, true
}

// WaitEventKind is a job event kind selected by an event filter (ADR 0045 p1).
// The three values are the only kinds a caller may put in EventKinds.
type WaitEventKind string

const (
	WaitEventProgress   WaitEventKind = "progress"
	WaitEventCheckpoint WaitEventKind = "checkpoint"
	WaitEventCompleted  WaitEventKind = "completed"
)

// WaitVerdictUpdate is one verdict change delivered by a VerdictSource. It is
// the input to the long-poll loop; a watching update carries the current
// revision and checks but does not end the wait. Kind names the job event the
// source observed. An empty Kind keeps the pre-filter behaviour, so existing
// sources need no change.
type WaitVerdictUpdate struct {
	WatchID  string
	Status   domain.Status
	Verdict  domain.Verdict
	Revision uint64
	Checks   map[string]domain.Check
	At       time.Time
	Kind     WaitEventKind
}

// parseWaitEventFilter validates EventKinds into a lookup set. A nil set means
// no filter, which keeps the terminal-verdict return of T3.48. An unknown kind
// is refused before the service subscribes or writes anything.
func parseWaitEventFilter(kinds []string) (map[WaitEventKind]struct{}, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	set := make(map[WaitEventKind]struct{}, len(kinds))
	for _, raw := range kinds {
		kind := WaitEventKind(raw)
		switch kind {
		case WaitEventProgress, WaitEventCheckpoint, WaitEventCompleted:
			set[kind] = struct{}{}
		default:
			return nil, domain.NewAppError(domain.CodeInvalidInput, "wait_result: unknown event kind: "+raw)
		}
	}
	return set, nil
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

// CapabilityID identifies the calling agent's capability context. It is an
// opaque label supplied by the composition root; wait_result never interprets
// it (ADR 0040 §1). Selecting which worker takes a task stays with the
// capability matching of T3.47.
type CapabilityID string

// CancelReason is the journal-ready cause of a capability-context cancellation.
// A caller walking away is distinct from the private long-poll timeout, which
// never cancels a tree (ADR 0040 §1).
type CancelReason string

const (
	CancelCallerCancelled  CancelReason = "caller_cancelled"
	CancelSessionClosed    CancelReason = "session_closed"
	CancelConnectionBroken CancelReason = "connection_broken"
	CancelContextEnded     CancelReason = "caller_context_ended"
)

// reasonErrorCode maps a disappearance reason to the contract error code the
// cancelled wait reports. It mirrors the three departure channels of CallerExit.
func reasonErrorCode(reason CancelReason) domain.ErrorCode {
	switch reason {
	case CancelSessionClosed, CancelConnectionBroken:
		return domain.CodeGone
	default:
		return domain.CodeCancelled
	}
}

// CancellationRecord is one journal line: a task, why it was cancelled and when.
type CancellationRecord struct {
	Owner  CapabilityID
	TaskID string
	Reason CancelReason
	At     time.Time
}

// CancelledTask is one node of a cancelled tree with the reason it received.
type CancelledTask struct {
	TaskID string
	Reason CancelReason
}

// CancelJournal is the sink for cancellation reasons (ADR 0040 §1). The
// composition root supplies durable persistence; the in-memory sink below is
// the default so a bound wait always has somewhere to record the reason.
type CancelJournal interface {
	RecordCancellation(CancellationRecord)
}

type memoryCancelJournal struct {
	mu      sync.Mutex
	records []CancellationRecord
}

// NewMemoryCancelJournal builds the in-process cancellation journal.
func NewMemoryCancelJournal() CancelJournal { return &memoryCancelJournal{} }

func (j *memoryCancelJournal) RecordCancellation(r CancellationRecord) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.records = append(j.records, r)
}

// Records returns a snapshot of the journal in insertion order.
func (j *memoryCancelJournal) Records() []CancellationRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]CancellationRecord, len(j.records))
	copy(out, j.records)
	return out
}

// ---- ADR 0041 §6 / T3.82: причина precheck в wait_result и журнале ----
//
// T3.81 останавливает run до входа в ожидание и печатает причину отказа в
// stderr. ADR 0041 п6 требует, чтобы та же причина была видна в ответе
// wait_result и в журнале. WaitResultService держит причину сконфигурированного
// отказа precheck и пишет её в журнал через порт PrecheckJournal. Порт
// реализован in-memory по умолчанию; слой app подставляет долговременный
// журнал, не зависящий от этого пакета.

// PrecheckFailureOutcome — исход run из ADR 0041 п6.
const PrecheckFailureOutcome = "precheck_failure"

// precheckCheckName — имя записи в Checks, которой доставляется причина.
const precheckCheckName = "precheck"

// PrecheckFailureRecord — одна запись журнала причин отказа precheck.
type PrecheckFailureRecord struct {
	Outcome string
	Reason  string
	At      time.Time
}

// PrecheckJournal — приёмник причин отказа precheck. Сигнатура на примитивах,
// чтобы реализация в слое app не импортировала этот пакет.
type PrecheckJournal interface {
	RecordPrecheckFailure(reason string, at time.Time) error
}

// NewMemoryPrecheckJournal builds the in-process precheck reason journal.
func NewMemoryPrecheckJournal() PrecheckJournal { return &memoryPrecheckJournal{} }

type memoryPrecheckJournal struct {
	mu      sync.Mutex
	records []PrecheckFailureRecord
}

func (j *memoryPrecheckJournal) RecordPrecheckFailure(reason string, at time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.records = append(j.records, PrecheckFailureRecord{
		Outcome: PrecheckFailureOutcome, Reason: reason, At: at,
	})
	return nil
}

// Records returns the journal in insertion order.
func (j *memoryPrecheckJournal) Records() []PrecheckFailureRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]PrecheckFailureRecord, len(j.records))
	copy(out, j.records)
	return out
}

// InfraFailureJournalReader is the in-process advisory journal with read-back
// for diagnostics and tests. The domain port stays write-only so a durable
// app-layer adapter has no extra methods to implement.
type InfraFailureJournalReader interface {
	domain.InfraFailureJournal
	Records() []domain.InfraFailureRecord
}

// NewMemoryInfraFailureJournal builds the in-process advisory journal. A
// service built by NewWaitResultService already has one.
func NewMemoryInfraFailureJournal() InfraFailureJournalReader {
	return &memoryInfraFailureJournal{}
}

type memoryInfraFailureJournal struct {
	mu      sync.Mutex
	records []domain.InfraFailureRecord
}

func (j *memoryInfraFailureJournal) RecordInfraFailure(record domain.InfraFailureRecord) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.records = append(j.records, record)
}

// Records returns the classifications in insertion order.
func (j *memoryInfraFailureJournal) Records() []domain.InfraFailureRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]domain.InfraFailureRecord, len(j.records))
	copy(out, j.records)
	return out
}

// capabilityTask is one node of the cancellation tree. reason is set when the
// context is cancelled and is delivered to the node's wait.
type capabilityTask struct {
	id       string
	parentID string
	reason   CancelReason
}

// CapabilityContext is the cancellation scope of one calling agent's capability
// (ADR 0040 §1). It owns a tree of tasks: a root wait and its descendants.
// Cancel cancels the entire tree, delivers one reason to every node and writes
// each node to the journal. A cancelled context refuses new tasks, so a
// descendant cannot resume work after the initiator disappears. It is safe for
// concurrent use.
type CapabilityContext struct {
	owner     CapabilityID
	journal   CancelJournal
	clock     domain.Clock
	mu        sync.Mutex
	tasks     map[string]*capabilityTask
	order     []string
	done      chan struct{}
	reason    CancelReason
	cancelled bool
}

// NewCapabilityContext builds a scope. A nil journal falls back to an in-memory
// sink and a nil clock to the wall clock.
func NewCapabilityContext(owner CapabilityID, journal CancelJournal, clock domain.Clock) *CapabilityContext {
	if journal == nil {
		journal = NewMemoryCancelJournal()
	}
	if clock == nil {
		clock = waitResultWallClock{}
	}
	return &CapabilityContext{
		owner:   owner,
		journal: journal,
		clock:   clock,
		tasks:   make(map[string]*capabilityTask),
		done:    make(chan struct{}),
	}
}

// Owner returns the capability this context belongs to.
func (c *CapabilityContext) Owner() CapabilityID { return c.owner }

// AddTask registers a task in the tree. An empty parentID registers a root;
// otherwise the parent must already exist. Adding to a cancelled context is
// refused so a descendant cannot restart work after the initiator disappears.
func (c *CapabilityContext) AddTask(id, parentID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancelled {
		return domain.NewAppError(domain.CodeCancelled, "capability context is cancelled")
	}
	if id == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "capability task id is required")
	}
	if _, exists := c.tasks[id]; exists {
		return domain.NewAppError(domain.CodeInvalidInput, "capability task already registered: "+id)
	}
	if parentID != "" {
		if _, ok := c.tasks[parentID]; !ok {
			return domain.NewAppError(domain.CodeInvalidInput, "capability parent task not registered: "+parentID)
		}
	}
	c.tasks[id] = &capabilityTask{id: id, parentID: parentID}
	c.order = append(c.order, id)
	return nil
}

// Cancel cancels the whole tree with one reason. It is idempotent: the first
// call delivers the reason to every node, writes one journal record per node
// and closes Done; later calls do nothing.
func (c *CapabilityContext) Cancel(reason CancelReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancelled {
		return
	}
	c.cancelled = true
	c.reason = reason
	at := c.clock.Now()
	for _, id := range c.order {
		t := c.tasks[id]
		t.reason = reason
		c.journal.RecordCancellation(CancellationRecord{
			Owner: c.owner, TaskID: t.id, Reason: reason, At: at,
		})
	}
	close(c.done)
}

// Done is closed when the context is cancelled. A wait bound to this context
// selects on it and stops when the calling agent disappears.
func (c *CapabilityContext) Done() <-chan struct{} { return c.done }

// IsCancelled reports whether Cancel has run.
func (c *CapabilityContext) IsCancelled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancelled
}

// Reason returns the cancellation reason, or empty before cancellation.
func (c *CapabilityContext) Reason() CancelReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

// CancelledTasks returns a snapshot of every node with the reason it received.
func (c *CapabilityContext) CancelledTasks() []CancelledTask {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]CancelledTask, 0, len(c.order))
	for _, id := range c.order {
		out = append(out, CancelledTask{TaskID: id, Reason: c.tasks[id].reason})
	}
	return out
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

// SharedCIWaiter is the port for the expensive CI wait (ADR 0037 p3). It runs
// fn once for the active wait round on key and shares the outcome with callers
// that join it. The concrete implementation lives in the store package and is
// supplied by the composition root; this package declares only the port so no
// sibling infra edge is introduced (ADR 0005).
//
// The signature matches store.SharedCIWait, so that function can be passed in
// directly by a caller that owns both packages.
type SharedCIWaiter func(ctx context.Context, key string, fn func(context.Context) error) error

// directCIWait is the default port: it runs fn without coalescing. A service
// with no shared gate wired keeps the original per-caller behaviour.
func directCIWait(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

// WaitResultService is the cancel-safe long-poll. It is transport agnostic:
// the caller supplies the departure signals, the verdict subscription and the
// transactional writer through ports.
type WaitResultService struct {
	store            waitResultStore
	writer           WaitResultWriter
	source           VerdictSource
	clock            domain.Clock
	after            func(time.Duration) <-chan time.Time
	departures       func(*mcpsdk.CallToolRequest) CallerExit
	idempotency      WaitResultIdempotencyStore
	starts           *waitStartRegistry
	capability       func(*mcpsdk.CallToolRequest) *CapabilityContext
	ciWait           func(context.Context) error
	ciGate           SharedCIWaiter
	precheckReason   string
	precheckJournal  PrecheckJournal
	infraJournal     domain.InfraFailureJournal
	actionTrajectory ActionTrajectory
	terminalJournal  TerminalOutcomeJournal
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
		store:            store,
		writer:           writer,
		source:           source,
		clock:            clock,
		after:            time.After,
		idempotency:      NewMemoryWaitResultIdempotency(),
		starts:           newWaitStartRegistry(),
		ciWait:           sharedCIWaitNoop,
		ciGate:           directCIWait,
		precheckJournal:  NewMemoryPrecheckJournal(),
		infraJournal:     NewMemoryInfraFailureJournal(),
		actionTrajectory: NewMemoryActionTrajectory(),
		terminalJournal:  NewMemoryTerminalOutcomeJournal(),
	}
}

// sharedCIWaitNoop is the default expensive CI wait. A composition root can
// replace it with the operation that should run once per CI head.
func sharedCIWaitNoop(context.Context) error { return nil }

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

// SetCapabilityScope sets the provider that binds a request to the calling
// agent's capability context (ADR 0040 §1). A disappearance then cancels the
// context and its whole task tree. Without a provider a wait is stopped only by
// its own departure channels and is not tree-cancellable.
func (s *WaitResultService) SetCapabilityScope(fn func(*mcpsdk.CallToolRequest) *CapabilityContext) {
	if s != nil {
		s.capability = fn
	}
}

// SetCIWait replaces the expensive operation that wait runs through the shared
// CI gate (ADR 0037 p3). A nil argument restores the no-op default.
func (s *WaitResultService) SetCIWait(fn func(context.Context) error) {
	if s == nil {
		return
	}
	if fn == nil {
		fn = sharedCIWaitNoop
	}
	s.ciWait = fn
}

// SetSharedCIWaiter installs the gate used at the long-poll entry. The real
// implementation is store.SharedCIWait, injected by the composition root that
// owns both packages; a nil argument restores directCIWait, which runs every
// caller's operation instead of coalescing them.
func (s *WaitResultService) SetSharedCIWaiter(fn SharedCIWaiter) {
	if s == nil {
		return
	}
	if fn == nil {
		fn = directCIWait
	}
	s.ciGate = fn
}

// SetPrecheckFailure installs the reason of a failed startup precheck (ADR 0041
// §6). With a reason set, wait_result returns precheck_failure immediately and
// journals the reason instead of entering the wait loop. An empty reason clears
// the failure and restores the normal wait.
func (s *WaitResultService) SetPrecheckFailure(reason string) {
	if s != nil {
		s.precheckReason = reason
	}
}

// SetPrecheckJournal installs the sink for precheck reasons. A nil argument
// restores the in-memory journal. The app layer supplies the durable sink.
func (s *WaitResultService) SetPrecheckJournal(j PrecheckJournal) {
	if s == nil {
		return
	}
	if j == nil {
		j = NewMemoryPrecheckJournal()
	}
	s.precheckJournal = j
}

// PrecheckJournal returns the configured sink, mostly for callers that want to
// read a memory journal back. It is never nil for a service built by
// NewWaitResultService.
func (s *WaitResultService) PrecheckJournal() PrecheckJournal {
	if s == nil || s.precheckJournal == nil {
		return NewMemoryPrecheckJournal()
	}
	return s.precheckJournal
}

// InfraFailureJournal returns the configured advisory sink. It is never nil for
// a service built by NewWaitResultService.
func (s *WaitResultService) InfraFailureJournal() domain.InfraFailureJournal {
	if s == nil || s.infraJournal == nil {
		return NewMemoryInfraFailureJournal()
	}
	return s.infraJournal
}

// ActionTrajectory returns the configured sink. It is never nil for a service
// built by NewWaitResultService.
func (s *WaitResultService) ActionTrajectory() ActionTrajectory {
	if s == nil || s.actionTrajectory == nil {
		return NewMemoryActionTrajectory()
	}
	return s.actionTrajectory
}

// RecordActionAttempt writes a corrected attempt to the trajectory. It stamps
// the time when the record has none. It never touches the visible wait result,
// so a requery costs no orchestrator slot and opens no new obligation.
func (s *WaitResultService) RecordActionAttempt(rec ActionTrajectoryRecord) {
	if s == nil {
		return
	}
	if rec.At.IsZero() {
		rec.At = s.now()
	}
	s.ActionTrajectory().RecordActionAttempt(rec)
}

// TerminalOutcomeJournal returns the configured sink. It is never nil for a
// service built by NewWaitResultService.
func (s *WaitResultService) TerminalOutcomeJournal() TerminalOutcomeJournal {
	if s == nil || s.terminalJournal == nil {
		return NewMemoryTerminalOutcomeJournal()
	}
	return s.terminalJournal
}

// RecordTerminalOutcome writes one cause to the outcome journal and stamps the
// same cause on out. It leaves partial diff, WaitStopReason, verdict, status
// and exit code untouched, so the SB-05 rescue path still reads its own fields
// and the orchestrator wake rules stay the same.
func (s *WaitResultService) RecordTerminalOutcome(watchID string, cause ExitStatusCause, at time.Time, out WaitResultOutput) WaitResultOutput {
	if !cause.valid() {
		return out
	}
	if s != nil {
		if at.IsZero() {
			at = s.now()
		}
		s.TerminalOutcomeJournal().RecordTerminalOutcome(TerminalOutcomeRecord{
			WatchID: watchID, Cause: cause, At: at,
		})
	}
	// Reuse the SB-05 carry-through with an empty diff. The cause reaches the
	// wire as exit_cause without a second field in the advertised schema.
	return WithTerminalFailure(out, cause, "")
}

// TerminalOutcomeCounts groups the journal by cause. A stats reader calls this
// instead of maintaining its own counter.
func (s *WaitResultService) TerminalOutcomeCounts() map[ExitStatusCause]int {
	return s.TerminalOutcomeJournal().Counts()
}

// ClassifyInfraText runs the advisory classifier over text the harness already
// wrote, records the result in the Watch journal and stamps the tier and reason
// on out. It never changes out.Status, out.Verdict or the wait stop reason, and
// it never removes a Watch from a scoring list. Unmatched text returns out
// untouched.
func (s *WaitResultService) ClassifyInfraText(watchID, text string, at time.Time, out WaitResultOutput) WaitResultOutput {
	tier, reason, ok := domain.ClassifyInfra(text)
	if !ok {
		return out
	}
	if s != nil {
		if at.IsZero() {
			at = s.now()
		}
		s.InfraFailureJournal().RecordInfraFailure(domain.InfraFailureRecord{
			WatchID: watchID, Tier: tier, Reason: reason, At: at,
		})
	}
	return WithInfraClassification(out, tier, reason)
}

// classifyChecks joins the conclusions of the observed checks in name order and
// classifies them. Sorting makes the first-hit-wins rule deterministic when
// more than one check carries a signature. No obligation write or verdict
// decision depends on this result.
func (s *WaitResultService) classifyChecks(watchID string, checks map[string]domain.Check, out WaitResultOutput, at time.Time) WaitResultOutput {
	if len(checks) == 0 {
		return out
	}
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(checks[name].Conclusion)
		b.WriteByte('\n')
	}
	return s.ClassifyInfraText(watchID, b.String(), at, out)
}

// precheckFailureOutput is the wait_result response that carries the precheck
// reason (ADR 0041 §6). The reason lives in Checks so the fixed field order of
// WaitResultOutput from ADR 0036 §2 does not change.
func precheckFailureOutput(reason string) WaitResultOutput {
	return WaitResultOutput{
		Status:  PrecheckFailureOutcome,
		Verdict: PrecheckFailureOutcome,
		Checks: map[string]domain.Check{
			precheckCheckName: {State: "FAILED", Conclusion: reason, Required: true},
		},
	}
}

func (s *WaitResultService) recordPrecheckFailure(reason string) {
	j := s.PrecheckJournal()
	_ = j.RecordPrecheckFailure(reason, s.now())
}

// sharedCIWaitKey is the gate key for one CI head. The subscriber's identity is
// deliberately absent: renaming a session must not split one CI into several
// gates (ADR 0037 p3).
func sharedCIWaitKey(w domain.Watch) string {
	return fmt.Sprintf("ci:%s/%d/%s", w.Repo, w.PR, w.HeadSHA)
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
	return s.waitScoped(ctx, subscriberID, in, exits, nil)
}

// WaitInCapability runs the wait inside the calling agent's capability context
// (ADR 0040 §1). A caller disappearance cancels the context and its whole task
// tree; the long-poll timeout does not. The plain Wait keeps the pre-capability
// behaviour for callers that bind no context.
func (s *WaitResultService) WaitInCapability(ctx context.Context, subscriberID string, in WaitResultInput, exits CallerExit, capCtx *CapabilityContext) (WaitResultOutput, error) {
	return s.waitScoped(ctx, subscriberID, in, exits, capCtx)
}

// waitScoped applies clientRequestId idempotency around the long-poll. A
// departure or an error leaves no key behind, including a capability timeout.
// The start key is applied inside this scope so both keys can be present on one
// call without either replacing the other (ADR 0028, ADR 0045 p1).
func (s *WaitResultService) waitScoped(ctx context.Context, subscriberID string, in WaitResultInput, exits CallerExit, capCtx *CapabilityContext) (WaitResultOutput, error) {
	if s == nil || in.ClientRequestID == "" || s.idempotency == nil {
		return s.waitStarted(ctx, subscriberID, in, exits, capCtx)
	}
	if out, ok := s.idempotency.Lookup(in.ClientRequestID); ok {
		return out, nil
	}
	out, err := s.waitStarted(ctx, subscriberID, in, exits, capCtx)
	if err == nil {
		s.idempotency.Record(in.ClientRequestID, out)
	}
	return out, err
}

// waitStarted applies the start-key idempotency of ADR 0045 p1. The first call
// with a key owns the wait and publishes its outcome. A repeat with the same
// key joins that entry: it opens no subscription and creates no second task,
// even when the first response was lost. Unlike clientRequestId, a departure
// does not clear the key, because the join must survive a broken connection.
func (s *WaitResultService) waitStarted(ctx context.Context, subscriberID string, in WaitResultInput, exits CallerExit, capCtx *CapabilityContext) (out WaitResultOutput, err error) {
	if s == nil || in.StartKey == "" || s.starts == nil {
		return s.wait(ctx, subscriberID, in, exits, capCtx)
	}
	entry, owner := s.starts.begin(in.StartKey)
	if !owner {
		return entry.await(ctx)
	}
	// The owner always publishes, even if wait panics or a future edit adds an
	// early return: a joiner must never block on done with a long context.
	defer func() { entry.finish(out, err) }()
	return s.wait(ctx, subscriberID, in, exits, capCtx)
}

// cancelledByCapability builds the error for a wait whose capability context is
// already cancelled. The reason travels with the error so a caller can see why.
func cancelledByCapability(capCtx *CapabilityContext) (WaitResultOutput, error) {
	reason := capCtx.Reason()
	if reason == "" {
		reason = CancelContextEnded
	}
	return WaitResultOutput{}, domain.NewAppError(reasonErrorCode(reason), "wait_result: capability context cancelled: "+string(reason))
}

// depart cancels the whole capability tree before returning a departure error.
// A nil context keeps the original departure behaviour: no write, no key.
func (s *WaitResultService) depart(capCtx *CapabilityContext, reason CancelReason, code domain.ErrorCode, msg string) (WaitResultOutput, error) {
	if capCtx != nil {
		capCtx.Cancel(reason)
	}
	return WaitResultOutput{}, domain.NewAppError(code, msg)
}

// interruptedResult names a departure as an interrupted wait (ADR 0046 p3).
// The error code, cancellation reason and journal record are unchanged; only
// the returned output gains the reason so a caller can tell a departure from a
// timeout or a terminal result.
func interruptedResult(out WaitResultOutput, err error) (WaitResultOutput, error) {
	return withWaitStopReason(out, WaitStopInterrupted), err
}

// wait runs the bounded long-poll. A result verdict writes readAt through the
// writer and returns; a timeout or any caller departure returns without a
// write, so no obligation is created and no delivery is marked. When capCtx is
// set, a departure also cancels that context's entire task tree and journals
// the reason (ADR 0040 §1).
func (s *WaitResultService) wait(ctx context.Context, subscriberID string, in WaitResultInput, exits CallerExit, capCtx *CapabilityContext) (WaitResultOutput, error) {
	// ADR 0041 §6: a failed startup precheck ends the run before the wait. The
	// reason reaches the caller in the wait_result response and the journal,
	// not only stderr. No store is read and no obligation is written.
	if s != nil && s.precheckReason != "" {
		s.recordPrecheckFailure(s.precheckReason)
		return precheckFailureOutput(s.precheckReason), nil
	}
	if in.WatchID == "" {
		return WaitResultOutput{}, domain.NewAppError(domain.CodeInvalidInput, "wait_result: watchID is required")
	}
	// A handle past its lease is an error, not a watching result. This runs
	// before the store read so a stale handle never reaches the unknown-watch
	// branch; that branch keeps its unchanged CodeWatchNotFound contract.
	if s != nil && domain.WatchHandleExpired(in.WatchID, s.now()) {
		return WaitResultOutput{}, domain.NewAppError(domain.CodeWatchExpired,
			"wait_result: watchID handle expired; register again to obtain a fresh handle")
	}
	if in.ContinueOf != "" && in.ContinueOf != in.WatchID {
		return WaitResultOutput{}, domain.NewAppError(domain.CodeInvalidInput, "wait_result: continueOf must name the same Watch")
	}
	// ADR 0045 p1: refuse an unknown kind before any subscription, store read
	// or obligation write. A nil filter keeps the T3.48 return unchanged.
	filter, err := parseWaitEventFilter(in.EventKinds)
	if err != nil {
		return WaitResultOutput{}, err
	}
	if s == nil || s.store == nil {
		return WaitResultOutput{}, domain.NewAppError(domain.CodePersistFailed, "wait_result: store is nil")
	}
	if capCtx != nil && capCtx.IsCancelled() {
		out, err := cancelledByCapability(capCtx)
		return interruptedResult(out, err)
	}

	w, err := s.store.Watch(in.WatchID)
	if err != nil {
		// ADR 0046 p3: the submission was never registered here, so the caller
		// must not resend it. The reason rides on the output next to the
		// unchanged CodeWatchNotFound error.
		return withWaitStopReason(WaitResultOutput{}, WaitStopUnknownSubmission),
			domain.NewAppError(domain.CodeWatchNotFound, "wait_result: watch "+in.WatchID+" not found")
	}

	// A parked Watch needs an answer, not a result. Return before any readAt
	// write so the caller can answer through ack(continueOf) and wait again on
	// this same Watch. An empty ContinueOf keeps the original contract. The
	// reason only names this existing return; ADR 0037's approval boundary is
	// unchanged (ADR 0046 p3).
	if isNeedsInput(w.Status, w.Verdict) {
		return withWaitStopReason(needsInputOutput(w.Revision, w.Checks), WaitStopInputRequired), nil
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

	// ADR 0037 p3: the expensive CI wait runs once for every caller waiting on
	// this repo/PR/headSHA. The gate shares the outcome, and each caller then
	// runs its own long-poll loop and writes only its own obligation.
	fn := s.ciWait
	if fn == nil {
		fn = sharedCIWaitNoop
	}
	if err := s.ciGate(ctx, sharedCIWaitKey(w), fn); err != nil {
		return WaitResultOutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, "wait_result: shared CI wait failed: "+err.Error(), err)
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

	// A nil channel disables the case, so an unbound wait keeps its original
	// select set. A bound wait also stops when its capability context is
	// cancelled, for example when another wait on the same tree sees the caller
	// disappear and tears the tree down.
	var capDone <-chan struct{}
	if capCtx != nil {
		capDone = capCtx.Done()
	}

	for {
		select {
		case <-ctx.Done():
			return interruptedResult(s.depart(capCtx, CancelContextEnded, domain.CodeCancelled, "wait_result: caller context ended"))
		case <-exits.Cancelled:
			return interruptedResult(s.depart(capCtx, CancelCallerCancelled, domain.CodeCancelled, "wait_result: caller cancelled"))
		case <-exits.SessionClosed:
			return interruptedResult(s.depart(capCtx, CancelSessionClosed, domain.CodeGone, "wait_result: session closed"))
		case <-exits.ConnectionBroken:
			return interruptedResult(s.depart(capCtx, CancelConnectionBroken, domain.CodeGone, "wait_result: connection broken"))
		case <-capDone:
			out, err := cancelledByCapability(capCtx)
			return interruptedResult(out, err)
		case <-timeoutCh:
			return withWaitStopReason(watchingOutput(revision, checks), WaitStopTimeout), nil
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
				return withWaitStopReason(needsInputOutput(update.Revision, update.Checks), WaitStopInputRequired), nil
			}
			if filter != nil {
				// ADR 0045 p1: the filter, not the verdict, decides whether an
				// update ends the wait. An unlisted kind is a state tick even
				// when it carries a terminal verdict, so a progress report
				// cannot be mistaken for a completed event.
				if _, listed := filter[update.Kind]; !listed {
					continue
				}
				if !isWaitResultVerdict(update.Verdict) {
					// progress and checkpoint carry no terminal verdict. They
					// wake the caller with the current state and mark no read.
					return eventOutput(update.Status, update.Verdict, revision, checks), nil
				}
			} else if !isWaitResultVerdict(update.Verdict) {
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

// eventOutput is the side-effect-free response for a listed progress or
// checkpoint event. It carries the state the event observed and a poll hint.
// No obligation is marked read, because the job has not completed.
func eventOutput(status domain.Status, verdict domain.Verdict, revision uint64, checks map[string]domain.Check) WaitResultOutput {
	if status == "" {
		status = domain.StatusNotifying
	}
	if verdict == "" {
		verdict = domain.VerdictWatching
	}
	ms := waitResultPollAfterMs
	return WaitResultOutput{
		Status:      string(status),
		Verdict:     string(verdict),
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
		return WaitResultOutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
	}
	ms := waitResultPollAfterMs
	out := WaitResultOutput{
		Status:      string(status),
		Verdict:     string(verdict),
		Revision:    revision,
		Checks:      checks,
		PollAfterMs: &ms,
	}
	// SB-03: a failed check can carry an infrastructure signature. Classify it
	// over the text already in Checks, journal it and add the advisory fields.
	// The verdict, the readAt write and the stop reason are already fixed above.
	out = s.classifyChecks(watchID, checks, out, at)
	// A delivered verdict is the terminal stop (ADR 0046 p3). The reason rides
	// on the response without changing the readAt write or the field order.
	return withWaitStopReason(out, WaitStopTerminal), nil
}

func (s *WaitResultService) now() time.Time {
	if s.clock == nil {
		return time.Now()
	}
	return s.clock.Now()
}

// Handle adapts the service to the go-sdk typed tool handler signature. The
// caller id is the opaque sessionID from _meta and is never interpreted. It
// returns WaitReasonOutput, whose schema matches the advertised one (BCX-43).
func (s *WaitResultService) Handle(ctx context.Context, req *mcpsdk.CallToolRequest, in WaitResultInput) (*mcpsdk.CallToolResult, WaitReasonOutput, error) {
	subscriberID := ""
	if id := SessionIDFromRequest(req); id != nil {
		subscriberID = *id
	}
	exits := callerExitFromContext(ctx)
	if s != nil && s.departures != nil {
		exits = s.departures(req)
	}
	var capCtx *CapabilityContext
	if s != nil && s.capability != nil {
		capCtx = s.capability(req)
	}
	out, err := s.WaitInCapability(ctx, subscriberID, in, exits, capCtx)
	return &mcpsdk.CallToolResult{}, waitReasonOutput(out), err
}

// AttachWaitResult registers the real tool on a server. NewServer keeps the
// stub; integration chooses when to call this helper.
func AttachWaitResult(server *mcpsdk.Server, svc *WaitResultService) {
	if server == nil || svc == nil {
		return
	}
	server.RemoveTools("beacon_wait_result")
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "beacon_wait_result",
		Description: "Bounded long-poll for a Watch result; a terminal verdict marks the caller's obligation read. The watchID is a leased handle valid for 30 minutes; an expired handle returns WATCH_EXPIRED.",
		Annotations: attachToolAnnotations(true),
	}, svc.Handle)
}

// ---- CBX-11: CAS-based concurrent gate waiter registration ----
//
// When a phase closes, several callers can register as waiters on the next
// gate at the same time. A plain read-modify-write loses registrations, and a
// retry without a pause can starve every caller but one. The registrar below
// reads a versioned gate snapshot, writes with compare-and-swap, and on a
// version miss waits with a doubling backoff jittered over [d/2, d). The retry
// budget is a number of attempts, not wall-clock time, so a slow server does
// not shorten it. A busy answer with Retry-After is distinguished from an
// ordinary version miss and honours the server hint. Re-registering the same
// waiter leaves a single record.

// GateWaiterSnapshot is one versioned gate. Version changes only on a
// successful write, so a reader holding version v can commit while the gate
// still has version v and must retry otherwise.
type GateWaiterSnapshot struct {
	Version uint64
	Waiters map[string]struct{}
}

// Clone returns a deep copy. Callers never mutate the stored waiter set in
// place, so a failed CAS cannot leave a partial record behind.
func (g GateWaiterSnapshot) Clone() GateWaiterSnapshot {
	out := GateWaiterSnapshot{Version: g.Version, Waiters: make(map[string]struct{}, len(g.Waiters))}
	for id := range g.Waiters {
		out.Waiters[id] = struct{}{}
	}
	return out
}

// GateBusyError reports a busy gate. RetryAfter is the server hint; zero means
// the server did not supply one and the caller falls back to its own backoff.
type GateBusyError struct {
	RetryAfter time.Duration
}

func (e *GateBusyError) Error() string { return "gate busy" }

// GateWaiterStore is the storage port used by the registrar. Both methods may
// return a *GateBusyError; the registrar treats that separately from a version
// miss.
type GateWaiterStore interface {
	Gate(id string) (GateWaiterSnapshot, error)
	CompareAndSwapGate(id string, expected uint64, next GateWaiterSnapshot) (bool, error)
}

// MemoryGateWaiterStore is an in-memory CAS store safe for concurrent use. It
// is the default store when the composition root has no durable gate. The
// optional delay and sleep let a test inject server latency without blocking
// the test process.
type MemoryGateWaiterStore struct {
	mu      sync.Mutex
	gates   map[string]GateWaiterSnapshot
	delay   time.Duration
	sleepFn func(context.Context, time.Duration) error
}

// NewMemoryGateWaiterStore builds an empty store.
func NewMemoryGateWaiterStore() *MemoryGateWaiterStore {
	return &MemoryGateWaiterStore{gates: make(map[string]GateWaiterSnapshot)}
}

// SetDelay injects per-operation server latency. A nil sleep means the delay
// is applied with time.Sleep; a test can inject a virtual clock instead.
func (m *MemoryGateWaiterStore) SetDelay(d time.Duration, sleep func(context.Context, time.Duration) error) {
	if m == nil {
		return
	}
	m.delay = d
	m.sleepFn = sleep
}

func (m *MemoryGateWaiterStore) pause(ctx context.Context) error {
	if m == nil || m.delay <= 0 {
		return nil
	}
	if m.sleepFn != nil {
		return m.sleepFn(ctx, m.delay)
	}
	select {
	case <-time.After(m.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Gate returns a deep copy of the gate. An unknown gate has version zero and
// an empty waiter set.
func (m *MemoryGateWaiterStore) Gate(id string) (GateWaiterSnapshot, error) {
	if m == nil {
		return GateWaiterSnapshot{}, domain.NewAppError(domain.CodePersistFailed, "gate store is nil")
	}
	if err := m.pause(context.Background()); err != nil {
		return GateWaiterSnapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.gates[id]
	if !ok {
		return GateWaiterSnapshot{Waiters: make(map[string]struct{})}, nil
	}
	return g.Clone(), nil
}

// CompareAndSwapGate writes next only while the stored version still matches
// expected. On success it advances the version by one.
func (m *MemoryGateWaiterStore) CompareAndSwapGate(id string, expected uint64, next GateWaiterSnapshot) (bool, error) {
	if m == nil {
		return false, domain.NewAppError(domain.CodePersistFailed, "gate store is nil")
	}
	if err := m.pause(context.Background()); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.gates[id]
	if !ok {
		cur = GateWaiterSnapshot{Waiters: make(map[string]struct{})}
	}
	if cur.Version != expected {
		return false, nil
	}
	next.Version = expected + 1
	m.gates[id] = next.Clone()
	return true, nil
}

// Snapshot returns a deep copy of a gate for inspection. An unknown gate has
// version zero and an empty waiter set.
func (m *MemoryGateWaiterStore) Snapshot(id string) GateWaiterSnapshot {
	if m == nil {
		return GateWaiterSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.gates[id]
	if !ok {
		return GateWaiterSnapshot{Waiters: make(map[string]struct{})}
	}
	return g.Clone()
}

// Defaults for the registrar. They mirror the beads gate registration: twenty
// attempts, a 10 ms base and a 400 ms cap.
const (
	DefaultGateMaxAttempts = 20
	DefaultGateBaseDelay   = 10 * time.Millisecond
	DefaultGateMaxDelay    = 400 * time.Millisecond
)

// GateRegisterResult reports what one registration did. Attempts counts the
// rounds actually spent, including busy waits and version misses.
type GateRegisterResult struct {
	Registered bool
	Attempts   int
}

// GateWaiterRegistrar registers waiters on a versioned gate with CAS.
type GateWaiterRegistrar struct {
	Store       GateWaiterStore
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	sleep       func(context.Context, time.Duration) error
	jitter      func() float64
}

// NewGateWaiterRegistrar builds a registrar over store with the default retry
// budget and backoff. Callers may override the exported fields before use.
func NewGateWaiterRegistrar(store GateWaiterStore) *GateWaiterRegistrar {
	return &GateWaiterRegistrar{
		Store:       store,
		MaxAttempts: DefaultGateMaxAttempts,
		BaseDelay:   DefaultGateBaseDelay,
		MaxDelay:    DefaultGateMaxDelay,
		sleep:       sleepWithContext,
		jitter:      rand.Float64,
	}
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetSleep replaces the wait function. Tests inject a recorder to observe the
// backoff and the Retry-After without sleeping.
func (r *GateWaiterRegistrar) SetSleep(fn func(context.Context, time.Duration) error) {
	if r != nil && fn != nil {
		r.sleep = fn
	}
}

// SetJitter replaces the jitter source. It must return a value in [0, 1).
func (r *GateWaiterRegistrar) SetJitter(fn func() float64) {
	if r != nil && fn != nil {
		r.jitter = fn
	}
}

// busyDelay distinguishes a busy answer from any other error.
func busyDelay(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	var busy *GateBusyError
	if errors.As(err, &busy) {
		return busy.RetryAfter, true
	}
	return 0, false
}

// jitteredDelay maps d to [d/2, d) using a jitter value in [0, 1).
func jitteredDelay(d time.Duration, jitter func() float64) time.Duration {
	v := jitter()
	if v < 0 {
		v = 0
	}
	if v >= 1 {
		v = 0
	}
	return d/2 + time.Duration(float64(d)/2*v)
}

// Register adds waiterID to gateID. A waiter already present is a no-op that
// reports one attempt and keeps one record. A version miss costs one attempt
// and a jittered backoff; a busy answer costs one attempt and honours
// Retry-After. Registration fails only when the attempt budget is exhausted or
// the store returns a non-busy error.
func (r *GateWaiterRegistrar) Register(ctx context.Context, gateID, waiterID string) (GateRegisterResult, error) {
	if r == nil || r.Store == nil {
		return GateRegisterResult{}, domain.NewAppError(domain.CodeInvalidInput, "gate registrar: store is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	max := r.MaxAttempts
	if max <= 0 {
		max = DefaultGateMaxAttempts
	}
	base := r.BaseDelay
	if base <= 0 {
		base = DefaultGateBaseDelay
	}
	limit := r.MaxDelay
	if limit <= 0 {
		limit = DefaultGateMaxDelay
	}
	jitter := r.jitter
	if jitter == nil {
		jitter = rand.Float64
	}
	sleep := r.sleep
	if sleep == nil {
		sleep = sleepWithContext
	}

	delay := base
	for attempt := 1; attempt <= max; attempt++ {
		snap, err := r.Store.Gate(gateID)
		if err != nil {
			if hint, busy := busyDelay(err); busy {
				wait := hint
				if wait <= 0 {
					wait = jitteredDelay(delay, jitter)
				}
				if werr := sleep(ctx, wait); werr != nil {
					return GateRegisterResult{Attempts: attempt}, werr
				}
				continue
			}
			return GateRegisterResult{Attempts: attempt}, err
		}
		if _, exists := snap.Waiters[waiterID]; exists {
			return GateRegisterResult{Registered: true, Attempts: attempt}, nil
		}
		next := snap.Clone()
		if next.Waiters == nil {
			next.Waiters = make(map[string]struct{})
		}
		next.Waiters[waiterID] = struct{}{}
		ok, err := r.Store.CompareAndSwapGate(gateID, snap.Version, next)
		if err != nil {
			if hint, busy := busyDelay(err); busy {
				wait := hint
				if wait <= 0 {
					wait = jitteredDelay(delay, jitter)
				}
				if werr := sleep(ctx, wait); werr != nil {
					return GateRegisterResult{Attempts: attempt}, werr
				}
				continue
			}
			return GateRegisterResult{Attempts: attempt}, err
		}
		if ok {
			return GateRegisterResult{Registered: true, Attempts: attempt}, nil
		}
		// Version miss: double the delay with jitter over [d/2, d).
		if werr := sleep(ctx, jitteredDelay(delay, jitter)); werr != nil {
			return GateRegisterResult{Attempts: attempt}, werr
		}
		if delay < limit {
			delay *= 2
			if delay > limit {
				delay = limit
			}
		}
	}
	return GateRegisterResult{Attempts: max}, domain.NewAppError(domain.CodeLimitExceeded,
		"gate registrar: attempt budget exhausted for gate "+gateID)
}

// ---- SB-14: three time budgets for one agent action ----
//
// An action has three independent limits: one command, one install and the
// whole series. sweagent sets them in sweagent/tools/tools.py:139-152 and
// agent/agents.py:958-991. The stop reason names the budget that ran out, so
// nobody reads logs to explain a stop. This model stays separate from
// WaitStopReason. An action timeout is informational and never blocks main.

// ActionBudget names the limit that ended an action.
type ActionBudget string

const (
	ActionBudgetCommand ActionBudget = "command"
	ActionBudgetInstall ActionBudget = "install"
	ActionBudgetTotal   ActionBudget = "total"
)

// ActionTimeBudgets holds the three limits. A zero disables a budget so a
// caller can turn one off without changing the others.
type ActionTimeBudgets struct {
	Command time.Duration
	Install time.Duration
	Total   time.Duration
}

// DefaultActionTimeBudgets returns the sweagent limits. One command gets 30
// seconds, an install gets 300 and the whole series gets 1800.
func DefaultActionTimeBudgets() ActionTimeBudgets {
	return ActionTimeBudgets{
		Command: 30 * time.Second,
		Install: 300 * time.Second,
		Total:   1800 * time.Second,
	}
}

// Exceeded reports whether elapsed broke the limit for kind. An unknown kind
// or a disabled limit returns false. This is the single check point for the
// three budgets.
func (b ActionTimeBudgets) Exceeded(kind ActionBudget, elapsed time.Duration) bool {
	var limit time.Duration
	switch kind {
	case ActionBudgetCommand:
		limit = b.Command
	case ActionBudgetInstall:
		limit = b.Install
	case ActionBudgetTotal:
		limit = b.Total
	default:
		return false
	}
	return limit > 0 && elapsed > limit
}

// ActionStopReason names why a series stopped. It is a new type, so
// WaitStopReason and the wait_result exit codes keep their contract.
type ActionStopReason string

const (
	StopCommandTimeout ActionStopReason = "exit_command_timeout"
	StopInstallTimeout ActionStopReason = "exit_install_timeout"
	StopTotalTimeout   ActionStopReason = "exit_total_timeout"
)

// MaxConsecutiveCommandTimeouts is the sweagent rule. Three command timeouts
// in a row end the run.
const MaxConsecutiveCommandTimeouts = 3

// ActionTimeoutTracker counts consecutive command timeouts and records why a
// series stopped. A successful command clears the count. An install or total
// timeout stops the series on the first hit. A mutex guards the state because
// one agent can run more than one action at a time.
type ActionTimeoutTracker struct {
	mu          sync.Mutex
	consecutive int
	stopReason  ActionStopReason
}

// RecordTimeout adds one timeout of kind. A command timeout stops the series
// after three in a row. An install or total timeout stops it at once.
func (t *ActionTimeoutTracker) RecordTimeout(kind ActionBudget) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopReason != "" {
		return
	}
	switch kind {
	case ActionBudgetCommand:
		t.consecutive++
		if t.consecutive >= MaxConsecutiveCommandTimeouts {
			t.stopReason = StopCommandTimeout
		}
	case ActionBudgetInstall:
		t.stopReason = StopInstallTimeout
	case ActionBudgetTotal:
		t.stopReason = StopTotalTimeout
	}
}

// RecordSuccess clears the consecutive command timeout count. A later timeout
// starts a fresh count, so three unrelated timeouts do not end the series.
func (t *ActionTimeoutTracker) RecordSuccess() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.consecutive = 0
}

// StopReason returns why the series stopped, or empty while it still runs.
func (t *ActionTimeoutTracker) StopReason() ActionStopReason {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopReason
}

// ConsecutiveTimeouts returns the current run of command timeouts.
func (t *ActionTimeoutTracker) ConsecutiveTimeouts() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.consecutive
}
