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
//
// An ack that carries an approve or reject decision resumes a stopped agent once
// (ADR 0058 §4). This is the decision side of an attention request: the wait was
// already interrupted and the human decision has arrived, so the agent
// continues in the same Watch from where it stopped. It is not the question
// flags of ADR 0028, which interrupt wait_result, and it is not the continueOf
// answer of T3.21, which returns a human answer to the same task. Both plain
// acks and continueOf acks behave exactly as before when no decision is set.
//
// A decision carrying the blocked: prefix classifies a red verdict instead of
// resuming an agent. blocked:not_mine marks the Watch blocked, recording the
// failed checks from the Watch, the time of the ack and the session ID from
// _meta as the blocker. blocked:resolved clears that block after a fix is
// confirmed: it applies the domain UnblockEvent and returns the Watch to
// watching. Both are idempotent: a repeat block cannot move the first block,
// and a repeat resolve on a Watch that is no longer blocked is a no-op. The
// fixFor link on the Watch and its subscribers stays in place, so the history
// of which fixer repaired the block survives. No MCP tool is added for this:
// the contract stays at exactly seven tools.
//
// A resolve also rotates the generation of every Watch it unblocks (T3.457).
// Returning the Watch to watching without restamping its claim would leave the
// previous owner's generation live, and the next swap would skip the row
// because the stale claim still matches. The stamp is applied on the same
// successful path as the unblock, so a repeat resolve cannot rotate it twice.
//
// A beacon_ack can also carry a tool-permission mode switch and rule updates
// (CS-02). These are control-channel data: the domain validates the mode and the
// rules, and the handler stores them under the session ID from _meta. A
// session-scoped update stays in that session. Tool permissions are a different
// axis from MergePolicy, which decides how a Watch merges.
//
// A decision=defer ack parks a tool call as result data for the Watch (CSx-03).
// The call carries a name and its input. A repeat defer for the same Watch is
// an idempotent no-op that cannot park a second call. Unlike approve and
// reject, defer never resumes the agent: the caller owns the decision about
// the parked call. The fields stay out of AckInput so the byte-pinned
// tools/list schema is unchanged.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// Decision is the human's answer to a stopped agent's request for attention
// (ADR 0058 §4). Both values resume the agent; they differ in the answer the
// agent receives when it continues. The set is closed so an unknown value
// cannot silently pass as a decision.
type Decision string

const (
	// DecisionApprove lets the stopped agent continue its work.
	DecisionApprove Decision = "approve"
	// DecisionReject sends a negative answer back to the stopped agent, which
	// still resumes so it can react to the rejection.
	DecisionReject Decision = "reject"
	// DecisionBlockedNotMine is the blocked:not_mine classification. Unlike
	// approve and reject it does not resume a stopped agent: it blocks the
	// Watch and records why. The blocked family is handled separately from
	// Valid so the closed approve/reject set stays unchanged.
	DecisionBlockedNotMine Decision = "blocked:not_mine"
	// DecisionBlockedResolved is the blocked:resolved classification. It clears
	// the block on the Watch named by the ack once a fix is confirmed. Like the
	// not_mine reason it is handled outside Valid and never resumes an agent.
	DecisionBlockedResolved Decision = "blocked:resolved"
	// DecisionDefer parks the caller's tool call on the Watch as result data
	// (CSx-03). It is handled outside Valid: a defer never resumes the stopped
	// agent. The caller decides what to do with the parked call.
	DecisionDefer Decision = "defer"
)

// decisionBlockedPrefix marks the blocked decision family. A decision with this
// prefix is classified by blockedDecisionReason, never by Decision.Valid.
const decisionBlockedPrefix = "blocked:"

// The blocked family is closed: only these reasons are dispatched by the ack
// path. Any other reason is refused before a Watch or an obligation is written.
// The values mirror domain.BlockReasonNotMine and domain.BlockReasonResolved,
// so a decision suffix maps onto the code stored on the block record.
const (
	blockedReasonNotMine  = "not_mine"
	blockedReasonResolved = "resolved"
)

// blockedDecisionReason returns the reason carried by a blocked: decision. The
// bool is false when d belongs to another decision family. An unknown reason is
// rejected by the ack path's switch, so it never moves a Watch.
func blockedDecisionReason(d string) (string, bool) {
	if !strings.HasPrefix(d, decisionBlockedPrefix) {
		return "", false
	}
	return strings.TrimPrefix(d, decisionBlockedPrefix), true
}

// Valid reports whether d is a supported resume decision. Blocked decisions
// are deliberately excluded: they take a different path and never resume.
func (d Decision) Valid() bool {
	return d == DecisionApprove || d == DecisionReject
}

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
	// Revision is the task revision carried by the event being acknowledged
	// (ADR 0036 §5). It is the Watch revision, not the event.id used to dedupe
	// webhook deliveries in ADR 0034 §4. Zero means the caller did not pin a
	// revision, so no staleness check is made for a plain ack. A revision older
	// than the one the Watch has committed is discarded before any write. A
	// blocked decision must pin the committed revision: zero and mismatched
	// revisions are discarded (ADR 0155:17).
	Revision uint64 `json:"revision,omitempty" jsonschema:"Watch revision this ack was given at; an older revision is discarded; not the webhook event.id"`
	// Decision, when set, is an approve or reject answer that resumes a stopped
	// agent (ADR 0058 §4), blocked:not_mine, which blocks the Watch with the
	// failed checks, the ack time and the calling session as blocker, or
	// blocked:resolved, which clears the block after a fix is confirmed. It is
	// additive to the plain ack contract: an empty value keeps ADR 0028
	// unchanged. No decision can be combined with continueOf.
	Decision string `json:"decision,omitempty" jsonschema:"approve or reject answer that resumes the stopped agent, blocked:not_mine to block the Watch, or blocked:resolved to clear a confirmed block; empty keeps the plain ack contract"`
	// DecisionID correlates the answer with the pending approval that raised
	// the notification (ADR 0058 §4). It is recorded with the decision so a
	// client can name which request was answered. It is optional for a plain
	// ack.
	DecisionID string `json:"decisionID,omitempty" jsonschema:"identifier of the pending decision this approve or reject answers"`
}

// StaleRevisionError reports an event whose task revision is older than the
// revision the ack committed. The event is discarded: no claimedAt is stamped,
// no Watch is resumed and no continuation round is consumed. ADR 0036 §5
// applies the task revision after the delivery dedup of ADR 0034 §4; the two
// keys are distinct.
type StaleRevisionError struct {
	Event     uint64
	Committed uint64
}

func (e *StaleRevisionError) Error() string {
	return fmt.Sprintf("ack: event revision %d is older than committed revision %d", e.Event, e.Committed)
}

// IsStaleRevision reports whether err is a discarded stale-revision event.
func IsStaleRevision(err error) bool { return domain.IsErr[*StaleRevisionError](err) }

// AckReceipt is the revision an ack was given at. ADR 0036 §5: the ack fixes
// the task revision, and a later event carrying an older revision is dropped.
// It reports the Watch revision, never the event.id of ADR 0034 §4. When the
// ack carried a decision (ADR 0058 §4) the receipt also reports that decision
// and whether it resumed the stopped agent.
type AckReceipt struct {
	OK       bool   `json:"ok"`
	Revision uint64 `json:"revision"`
	// Decision is the approve/reject answer carried by this ack, empty for a
	// plain ack.
	Decision Decision `json:"decision,omitempty"`
	// DecisionID names the pending decision this answer resolved.
	DecisionID string `json:"decisionID,omitempty"`
	// Resumed is true only when this decision resumed the stopped agent. A
	// repeated decision leaves it false because the agent resumes at most once.
	Resumed bool `json:"resumed,omitempty"`
	// ResumeRevision is the Watch revision after the resume. It is zero when
	// no decision was applied.
	ResumeRevision uint64 `json:"resumeRevision,omitempty"`
	// AnswerRound is the Watch revision at which the answered question was
	// asked. A repeat answer keeps the round of that question, so a caller can
	// tell which question the decision belongs to. It is zero for an ack that
	// carried no decision.
	AnswerRound uint64 `json:"answerRound,omitempty"`
	// Blocked is true when this ack applied a blocked:not_mine decision. A
	// repeated block leaves it false because a Watch is blocked at most once.
	Blocked bool `json:"blocked,omitempty"`
	// Block is the block record applied by this ack, when any.
	Block *BlockedWatch `json:"block,omitempty"`
	// Unblocked is true when this ack applied a blocked:resolved decision to a
	// blocked Watch. A repeat leaves it false because a Watch unblocks at most
	// once.
	Unblocked bool `json:"unblocked,omitempty"`
	// Deferred is true when this ack parked a tool call for the first time. A
	// repeat defer for the same Watch leaves it false and returns the original
	// call in DeferredToolUse.
	Deferred bool `json:"deferred,omitempty"`
	// DeferredToolUse is the call parked by a defer decision. It is the same
	// record on a repeat, so the caller sees one call per Watch.
	DeferredToolUse *domain.DeferredToolUse `json:"deferred_tool_use,omitempty"`
}

// DecisionRecord is one decision applied to a stopped Watch. ResumedAt is the
// moment the agent resumed and Revision is the Watch revision it resumed at.
type DecisionRecord struct {
	DecisionID string
	WatchID    string
	Decision   Decision
	ResumedAt  time.Time
	Revision   uint64
	// Round is the Watch revision at which the question was asked. It joins
	// WatchID as the registry key, so a new question on the same Watch is a
	// new round while a repeat answer stays in the round it answered.
	Round uint64
}

// decisionKey identifies one question: the Watch and the revision at which it
// was parked for input. A new question carries a new Watch revision and hence
// a new key; a repeat answer keeps the key of the question it answers.
type decisionKey struct {
	watchID string
	round   uint64
}

// DecisionRegistry is the "requires attention" record of ADR 0058 §4. It keeps
// one decision per question so a repeat decision cannot resume the same agent
// twice and a later question on the same Watch can still be answered. The
// first decision wins within a round: an identical repeat is an idempotent
// no-op, while a different decision for an answered question is a conflict. It
// is safe for concurrent use.
type DecisionRegistry struct {
	mu       sync.Mutex
	records  map[decisionKey]DecisionRecord
	inFlight map[decisionKey]*decisionFlight
}

// decisionFlight reserves a watchID while resume runs outside the registry
// lock. A Decide call that finds an active flight waits on done instead of
// running resume a second time.
type decisionFlight struct {
	done chan struct{}
}

// NewDecisionRegistry builds an empty decision registry.
func NewDecisionRegistry() *DecisionRegistry {
	return &DecisionRegistry{
		records:  make(map[decisionKey]DecisionRecord),
		inFlight: make(map[decisionKey]*decisionFlight),
	}
}

// Lookup returns the decision from the latest round recorded for watchID. The
// bool is false when no decision was recorded yet. A fresh answer that arrives
// after the Watch left needs_input falls back to this round when its DecisionID
// is unknown.
func (r *DecisionRegistry) Lookup(watchID string) (DecisionRecord, bool) {
	if r == nil {
		return DecisionRecord{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var latest DecisionRecord
	found := false
	for key, rec := range r.records {
		if key.watchID != watchID {
			continue
		}
		if !found || key.round > latest.Round {
			latest = rec
			found = true
		}
	}
	return latest, found
}

// LookupDecisionID finds the round answered by decisionID on watchID. A repeat
// of an older answer carries the same DecisionID, so it lands back in that
// round even after later rounds were answered. When an ID was reused by more
// than one round, the latest round wins.
func (r *DecisionRegistry) LookupDecisionID(watchID, decisionID string) (DecisionRecord, bool) {
	if r == nil {
		return DecisionRecord{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var latest DecisionRecord
	found := false
	for key, rec := range r.records {
		if key.watchID != watchID || rec.DecisionID != decisionID {
			continue
		}
		if !found || key.round > latest.Round {
			latest = rec
			found = true
		}
	}
	return latest, found
}

// Decide records rec for watchID at rec.Round and runs resume at most once.
// The round is the Watch revision at which the question was asked, so a later
// question on the same Watch is a different round. resume runs without the
// registry lock, so a callback that calls back into the registry cannot
// deadlock and Lookup never waits for a delivery. The round is reserved under
// the lock while resume runs; a concurrent Decide for the same Watch and round
// waits for that reservation to clear, then reads the recorded result. It
// returns the stored record and whether resume ran. An identical repeat
// returns the stored record with applied false; a conflicting decision in the
// same round is refused with CodeInvalidInput.
func (r *DecisionRegistry) Decide(watchID string, rec DecisionRecord, resume func() (uint64, error)) (DecisionRecord, bool, error) {
	if r == nil {
		return DecisionRecord{}, false, domain.NewAppError(domain.CodePersistFailed, "ack: decision registry is nil")
	}
	key := decisionKey{watchID: watchID, round: rec.Round}
	for {
		r.mu.Lock()
		if r.records == nil {
			r.records = make(map[decisionKey]DecisionRecord)
		}
		if r.inFlight == nil {
			r.inFlight = make(map[decisionKey]*decisionFlight)
		}
		if existing, ok := r.records[key]; ok {
			if existing.Decision != rec.Decision || existing.DecisionID != rec.DecisionID {
				r.mu.Unlock()
				return DecisionRecord{}, false, domain.NewAppError(domain.CodeInvalidInput, "ack: a different decision was already recorded for this Watch")
			}
			r.mu.Unlock()
			return existing, false, nil
		}
		if flight, ok := r.inFlight[key]; ok {
			r.mu.Unlock()
			<-flight.done
			continue
		}
		flight := &decisionFlight{done: make(chan struct{})}
		r.inFlight[key] = flight
		r.mu.Unlock()

		revision, err := resume()

		r.mu.Lock()
		delete(r.inFlight, key)
		if err != nil {
			r.mu.Unlock()
			close(flight.done)
			return DecisionRecord{}, false, err
		}
		rec.Revision = revision
		r.records[key] = rec
		r.mu.Unlock()
		close(flight.done)
		return rec, true, nil
	}
}

// failedCheckNames projects the failed checks of an ack onto the string slice
// the domain BlockedInfo carries. It reuses the same failure filter as
// beacon_get_result, so the block cannot invent a passing check.
func failedCheckNames(in []FailedCheck) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, c := range in {
		out = append(out, c.Name)
	}
	return out
}

// BlockedWatch is the block record written by a blocked:not_mine ack. Reason is
// the machine-readable verdict class, FailedChecks comes from the Watch at the
// moment of the ack, Since is the ack time and BlockedBy is the opaque session
// ID from _meta. OrchestratorID is the parent session already persisted on the
// Watch; cibridge uses it to steer the blocked notice when the Watch has no
// orchestrator subscriber. The record is never rebuilt from client data.
type BlockedWatch struct {
	WatchID        string             `json:"watchID"`
	Reason         domain.BlockReason `json:"reason"`
	FailedChecks   []FailedCheck      `json:"failedChecks"`
	Since          time.Time          `json:"since"`
	BlockedBy      string             `json:"blockedBy"`
	OrchestratorID string             `json:"orchestratorID,omitempty"`
}

// BlockedRegistry keeps one block record per Watch. The first block wins: a
// repeat blocked:not_mine is an idempotent no-op that cannot move Since or
// change who is named as blocker. It is safe for concurrent use.
type BlockedRegistry struct {
	mu      sync.Mutex
	records map[string]BlockedWatch
}

// NewBlockedRegistry builds an empty block registry.
func NewBlockedRegistry() *BlockedRegistry {
	return &BlockedRegistry{records: make(map[string]BlockedWatch)}
}

// Lookup returns the block recorded for watchID. The bool is false when the
// Watch was never blocked.
func (r *BlockedRegistry) Lookup(watchID string) (BlockedWatch, bool) {
	if r == nil {
		return BlockedWatch{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[watchID]
	return rec, ok
}

// Block records rec for watchID once. It returns the stored record and whether
// this call wrote it. A repeat returns the original record with applied false.
func (r *BlockedRegistry) Block(watchID string, rec BlockedWatch) (BlockedWatch, bool) {
	if r == nil {
		return BlockedWatch{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.records == nil {
		r.records = make(map[string]BlockedWatch)
	}
	if existing, ok := r.records[watchID]; ok {
		return existing, false
	}
	r.records[watchID] = rec
	return rec, true
}

// Unblock forgets the local mirror of a block. The SQLite incident is
// authoritative when a blocker port is installed; without this, a restarted
// handler could still claim a resolved Watch is blocked.
func (r *BlockedRegistry) Unblock(watchID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.records, watchID)
}

// DeferredRegistry keeps one parked tool call per Watch. The first defer wins:
// a repeat returns the original record with applied false, so a second ack
// cannot park a second call or move the first one. It is safe for concurrent
// use.
type DeferredRegistry struct {
	mu      sync.Mutex
	records map[string]domain.DeferredToolUse
}

// NewDeferredRegistry builds an empty deferred-call registry.
func NewDeferredRegistry() *DeferredRegistry {
	return &DeferredRegistry{records: make(map[string]domain.DeferredToolUse)}
}

// Lookup returns the call parked for watchID. The bool is false when the Watch
// has no parked call.
func (r *DeferredRegistry) Lookup(watchID string) (domain.DeferredToolUse, bool) {
	if r == nil {
		return domain.DeferredToolUse{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	call, ok := r.records[watchID]
	return call, ok
}

// Park stores call for watchID once. A repeat returns the original call with
// applied false. The call is copied, so a later mutation of the caller's value
// cannot change what the Watch recorded.
func (r *DeferredRegistry) Park(watchID string, call domain.DeferredToolUse) (domain.DeferredToolUse, bool) {
	if r == nil {
		return domain.DeferredToolUse{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.records == nil {
		r.records = make(map[string]domain.DeferredToolUse)
	}
	if existing, ok := r.records[watchID]; ok {
		return existing, false
	}
	stored := call
	stored.Input = append(json.RawMessage(nil), call.Input...)
	r.records[watchID] = stored
	return stored, true
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

// PermissionRegistry keeps one SessionPermission per session ID. The key is the
// opaque session from _meta, so a session-scoped update writes only that
// session's record and never leaks into another. It is safe for concurrent use.
type PermissionRegistry struct {
	mu     sync.Mutex
	states map[string]domain.SessionPermission
}

// NewPermissionRegistry builds an empty permission registry.
func NewPermissionRegistry() *PermissionRegistry {
	return &PermissionRegistry{states: make(map[string]domain.SessionPermission)}
}

// Lookup returns the state stored for sessionID. The bool is false when the
// session has no state yet.
func (r *PermissionRegistry) Lookup(sessionID string) (domain.SessionPermission, bool) {
	if r == nil {
		return domain.SessionPermission{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[sessionID]
	return state, ok
}

// Apply switches mode and appends rule updates to one session's state. Every
// command is validated before the state is written, so a rejected mode or an
// incomplete update leaves the stored state untouched. The caller supplies a
// domain function that returns the next state; the registry writes it only on
// success. SessionID comes from _meta and is never taken from client input.
func (r *PermissionRegistry) Apply(sessionID string, apply func(domain.SessionPermission) (domain.SessionPermission, error)) (domain.SessionPermission, error) {
	if r == nil {
		return domain.SessionPermission{}, domain.NewAppError(domain.CodePersistFailed, "ack: permission registry is nil")
	}
	if sessionID == "" {
		return domain.SessionPermission{}, domain.NewAppError(domain.CodeInvalidInput, "ack: permission change requires sessionID from _meta")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.states == nil {
		r.states = make(map[string]domain.SessionPermission)
	}
	next, err := apply(r.states[sessionID])
	if err != nil {
		return domain.SessionPermission{}, err
	}
	if next.SessionID != sessionID {
		return domain.SessionPermission{}, domain.NewAppError(domain.CodeInvalidInput, "ack: permission change targeted another session")
	}
	if next.Mode == "" {
		next.Mode = domain.PermissionModeDefault
	}
	r.states[sessionID] = next
	return next, nil
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
// it also applies the domain continue transition to the same parked Watch. With
// a decision it resumes a stopped agent once (ADR 0058 §4). The handler never
// writes an obligation verdict pointer (ADR 0028).
type AckHandler struct {
	store         resultStoreReader
	stamper       ObligationStamper
	clock         domain.Clock
	continuations *ContinuationRegistry
	decisions     *DecisionRegistry
	blocked       *BlockedRegistry
	// deferred holds the parked tool call per Watch for CSx-03. A repeat defer
	// returns the first call instead of parking a second one.
	deferred *DeferredRegistry
	// permissions holds the per-session permission state for CS-02. Instances
	// built before this port was added keep their ack behaviour; a nil
	// permission registry skips the control-channel fields.
	permissions *PermissionRegistry
	blockers    domain.BlockerIncidents
	// generations stamps a fresh generation on every Watch a resolve unblocks.
	// It is optional so handlers built before the port was added keep their
	// idempotency and block behaviour unchanged; a nil store simply skips the
	// stamp. The composition root shares one ledger with beacon_claim and the CI
	// bridge so all three move the same generation for a Watch.
	generations GenerationLedger
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
		decisions:     NewDecisionRegistry(),
		blocked:       NewBlockedRegistry(),
		deferred:      NewDeferredRegistry(),
		permissions:   NewPermissionRegistry(),
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

// SetBlockerIncidents injects the durable incident port. With it installed the
// SQLite incident is the source of truth for block and unblock, so a process
// restart cannot forget why a Watch was blocked. A nil argument keeps the
// legacy in-memory behaviour for callers that have not wired the port yet.
func (h *AckHandler) SetBlockerIncidents(b domain.BlockerIncidents) {
	if h != nil {
		h.blockers = b
	}
}

// SetGenerationStore shares the generation ledger with a composition root so
// blocked:resolved rotates the same generation that beacon_claim stamps. A nil
// argument leaves the handler without a stamp, which keeps older callers
// unchanged.
func (h *AckHandler) SetGenerationStore(g GenerationLedger) {
	if h != nil {
		h.generations = g
	}
}

// rotateGenerations restamps every Watch a resolve just unblocked. The
// generation ledger is idempotent, so a Watch with no live claim is left
// alone and a repeated resolve cannot rotate it twice.
func (h *AckHandler) rotateGenerations(watchIDs ...string) {
	if h == nil || h.generations == nil {
		return
	}
	for _, id := range watchIDs {
		h.generations.ResolveBlocked(id)
	}
}

// ApplyPermissionChange switches one session's mode and applies its rule updates
// on the control channel. Mode and rules live in domain data; the handler holds
// no per-mode branch. The registry validates and writes all or nothing, so a
// rejected update cannot leave a partial rule behind. sessionID comes from
// _meta, which keeps a session-scoped change inside its own session.
func (h *AckHandler) ApplyPermissionChange(sessionID string, mode domain.PermissionMode, updates []domain.PermissionUpdate) (domain.SessionPermission, error) {
	if h == nil || h.permissions == nil {
		return domain.SessionPermission{}, domain.NewAppError(domain.CodePersistFailed, "ack: permission registry is nil")
	}
	return h.permissions.Apply(sessionID, func(state domain.SessionPermission) (domain.SessionPermission, error) {
		if mode != "" {
			cmd := domain.SetPermissionMode{SessionID: sessionID, Mode: mode}
			next, err := state.ApplyMode(cmd)
			if err != nil {
				return domain.SessionPermission{}, err
			}
			state = next
		}
		if len(updates) > 0 {
			next, err := state.ApplyUpdates(sessionID, updates)
			if err != nil {
				return domain.SessionPermission{}, err
			}
			state = next
		}
		return state, nil
	})
}

type ackWallClock struct{}

func (ackWallClock) Now() time.Time { return time.Now() }

// permissionControlFields are the CS-02 control fields carried alongside a
// beacon_ack. They stay out of AckInput so the byte-pinned tools/list input
// schema does not change: the SDK derives that schema from the typed input.
// The fields are read from the raw request arguments instead.
type permissionControlFields struct {
	SetPermissionMode domain.PermissionMode     `json:"setPermissionMode"`
	PermissionUpdates []domain.PermissionUpdate `json:"permissionUpdates"`
}

// permissionControlFromRequest reads the permission control fields from the raw
// request arguments. An absent or malformed value yields no change: the fields
// are optional extensions, and an unknown shape must not turn a plain ack into
// an error. Validation of a present value happens in the domain, before a write.
func permissionControlFromRequest(req *mcpsdk.CallToolRequest) (domain.PermissionMode, []domain.PermissionUpdate) {
	if req == nil || req.Params == nil || req.Params.Arguments == nil {
		return "", nil
	}
	raw, err := json.Marshal(req.Params.Arguments)
	if err != nil {
		return "", nil
	}
	var fields permissionControlFields
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", nil
	}
	return fields.SetPermissionMode, fields.PermissionUpdates
}

// deferredControlFields are the CSx-03 fields carried alongside a beacon_ack
// with decision=defer. They stay out of AckInput so the byte-pinned tools/list
// input schema does not change: the SDK derives that schema from the typed
// input. The fields are read from the raw request arguments instead.
type deferredControlFields struct {
	ToolCallID string          `json:"deferredToolCallID"`
	ToolName   string          `json:"deferredToolName"`
	ToolInput  json.RawMessage `json:"deferredToolInput"`
}

// deferredCallFromRequest reads the parked call from the raw request arguments.
// A missing or malformed value yields an empty call, so an absent field cannot
// park a nameless tool. The caller validates the name before any write.
func deferredCallFromRequest(req *mcpsdk.CallToolRequest) domain.DeferredToolUse {
	if req == nil || req.Params == nil || req.Params.Arguments == nil {
		return domain.DeferredToolUse{}
	}
	raw, err := json.Marshal(req.Params.Arguments)
	if err != nil {
		return domain.DeferredToolUse{}
	}
	var fields deferredControlFields
	if err := json.Unmarshal(raw, &fields); err != nil {
		return domain.DeferredToolUse{}
	}
	return domain.DeferredToolUse{
		ID:    fields.ToolCallID,
		Name:  fields.ToolName,
		Input: fields.ToolInput,
	}
}

// Handle adapts Ack to the go-sdk typed tool handler signature and returns an
// ok response. A plain ack keeps the byte-identical {"ok":true} shape because
// the decision fields have omitempty. A decision ack also carries resumed,
// resumeRevision, blocked and unblocked from AckReceipt, so the caller sees
// which effect the first application had. The response carries no verdict: a
// caller still has to call beacon_get_result or beacon_wait_result to read one
// (ADR 0028). A stale-revision event (ADR 0036 §5) is discarded with ok:false
// and no error; the fixed revision itself is available from AckReceipt. A
// blocked:not_mine decision takes the session ID from req's _meta and names it
// as the blocker in the block record. A blocked:resolved decision needs no
// session: it clears the block on the Watch named by the ack.
//
// The raw request may also carry setPermissionMode and permissionUpdates. Those
// are control-channel fields (CS-02): they switch the calling session's mode and
// append validated rule updates before the ack is stamped. They are read from
// the raw arguments so the byte-pinned tools/list input schema stays unchanged.
func (h *AckHandler) Handle(ctx context.Context, req *mcpsdk.CallToolRequest, in AckInput) (*mcpsdk.CallToolResult, OKOutput, error) {
	var sessionID string
	if id := SessionIDFromRequest(req); id != nil {
		sessionID = *id
	}
	// Permission control is applied before the ack write. The domain validates
	// mode and updates first, so an incomplete update writes no state and also
	// prevents the ack from being stamped on that path.
	mode, updates := permissionControlFromRequest(req)
	if mode != "" || len(updates) > 0 {
		if _, err := h.ApplyPermissionChange(sessionID, mode, updates); err != nil {
			return nil, OKOutput{}, err
		}
	}
	parked := deferredCallFromRequest(req)
	receipt, err := h.ackReceiptWithSession(ctx, in, sessionID, parked)
	if err != nil && !IsStaleRevision(err) {
		return nil, OKOutput{}, err
	}
	return nil, OKOutput{
		OK:             receipt.OK,
		Resumed:        receipt.Resumed,
		ResumeRevision: receipt.ResumeRevision,
		Blocked:        receipt.Blocked,
		Unblocked:      receipt.Unblocked,
	}, nil
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
//
// AckReceipt returns the Watch revision the ack was given at (ADR 0036 §5).
// An event whose Revision is older than that committed revision is discarded
// before any stamp or transition. Use IsStaleRevision to recognise a discard.
func (h *AckHandler) Ack(ctx context.Context, in AckInput) error {
	_, err := h.AckReceipt(ctx, in)
	return err
}

// AckReceipt performs the ack and reports the committed revision it observed.
// The revision is captured before a continueOf or blocked transition, so the
// receipt names the version the ack actually acknowledged, not its successor.
// It carries no session ID: a blocked:not_mine decision needs one from _meta,
// so callers that want to block should use Handle or ackReceiptWithSession.
func (h *AckHandler) AckReceipt(ctx context.Context, in AckInput) (AckReceipt, error) {
	return h.ackReceiptWithSession(ctx, in, "")
}

// ackReceiptWithSession is the ack path with the caller's opaque session ID from
// _meta. The session is used only by a blocked:not_mine decision, as BlockedBy;
// plain acks, approve/reject decisions and blocked:resolved ignore it. The
// optional parked call is the CSx-03 defer payload read from the raw request;
// it is accepted only when the decision is defer.
func (h *AckHandler) ackReceiptWithSession(ctx context.Context, in AckInput, sessionID string, parked ...domain.DeferredToolUse) (AckReceipt, error) {
	if in.WatchID == "" || in.ObligationID == "" {
		return AckReceipt{}, domain.NewAppError(domain.CodeInvalidInput, "ack: watchID and obligationID are required")
	}
	if h == nil || h.store == nil || h.stamper == nil {
		return AckReceipt{}, domain.NewAppError(domain.CodePersistFailed, "ack: store or stamper is nil")
	}

	o, err := h.store.Obligation(in.ObligationID)
	if err != nil {
		return AckReceipt{}, mapResultReadError(err, "ack: obligation")
	}
	if o.WatchID != in.WatchID {
		return AckReceipt{}, domain.NewAppError(domain.CodeInvalidInput, "ack: obligation does not belong to the Watch")
	}

	w, err := h.store.Watch(in.WatchID)
	if err != nil {
		return AckReceipt{}, mapResultReadError(err, "ack: watch")
	}
	committed := w.Revision
	reason, isBlocked := blockedDecisionReason(in.Decision)
	// A blocked decision must pin the exact revision it was decided at. Zero
	// and mismatched revisions are discarded, so a stale decision cannot move
	// a Watch (ADR 0155:17). A plain ack may leave Revision unset; when it is
	// set, only an older event is discarded (ADR 0036 §5).
	stale := in.Revision != 0 && in.Revision < committed
	if isBlocked {
		stale = in.Revision == 0 || in.Revision != committed
	}
	if stale {
		return AckReceipt{Revision: committed}, &StaleRevisionError{Event: in.Revision, Committed: committed}
	}

	if in.Decision != "" && in.ContinueOf != "" {
		return AckReceipt{}, domain.NewAppError(domain.CodeInvalidInput, "ack: decision and continueOf cannot be combined")
	}

	var receipt AckReceipt
	if isBlocked {
		switch reason {
		case blockedReasonNotMine:
			rec, applied, err := h.applyBlockedNotMine(in, w, sessionID)
			if err != nil {
				return AckReceipt{}, err
			}
			block := rec
			receipt = AckReceipt{
				Decision:   Decision(in.Decision),
				DecisionID: in.DecisionID,
				Blocked:    applied,
				Block:      &block,
			}
		case blockedReasonResolved:
			applied, err := h.applyBlockedResolved(w)
			if err != nil {
				return AckReceipt{}, err
			}
			receipt = AckReceipt{
				Decision:   Decision(in.Decision),
				DecisionID: in.DecisionID,
				Unblocked:  applied,
			}
		default:
			return AckReceipt{}, domain.NewAppError(domain.CodeInvalidInput, "ack: unknown blocked decision reason: "+reason)
		}
	} else if in.Decision == string(DecisionDefer) {
		var call domain.DeferredToolUse
		if len(parked) > 0 {
			call = parked[0]
		}
		stored, applied, err := h.applyDefer(in, call)
		if err != nil {
			return AckReceipt{}, err
		}
		receipt = AckReceipt{
			Decision:        DecisionDefer,
			DecisionID:      in.DecisionID,
			Deferred:        applied,
			DeferredToolUse: &stored,
		}
	} else if in.Decision != "" {
		var rec DecisionRecord
		var applied bool
		rec, applied, err = h.applyDecision(in, w, Decision(in.Decision))
		if err != nil {
			return AckReceipt{}, err
		}
		receipt = AckReceipt{
			Decision:       rec.Decision,
			DecisionID:     rec.DecisionID,
			Resumed:        applied,
			ResumeRevision: rec.Revision,
			AnswerRound:    rec.Round,
		}
	}

	if in.ContinueOf != "" {
		if err := h.continueWatch(in, w); err != nil {
			return AckReceipt{}, err
		}
	}
	if _, _, err := h.stamper.StampClaimed(ctx, in.ObligationID, h.clock.Now()); err != nil {
		return AckReceipt{}, err
	}
	receipt.OK = true
	receipt.Revision = committed
	return receipt, nil
}

// applyDecision records the human decision and resumes the stopped agent once.
// A first decision answers a request for attention and needs a Watch parked for
// input. A repeat is an idempotent no-op even after the Watch left that state;
// the registry makes it so and refuses a conflicting second answer.
func (h *AckHandler) applyDecision(in AckInput, w domain.Watch, d Decision) (DecisionRecord, bool, error) {
	if !d.Valid() {
		return DecisionRecord{}, false, domain.NewAppError(domain.CodeInvalidInput, "ack: decision must be approve or reject")
	}
	if h.decisions == nil {
		return DecisionRecord{}, false, domain.NewAppError(domain.CodePersistFailed, "ack: decision registry is nil")
	}
	// A question is identified by the Watch revision at which it was parked.
	// The first answer runs while that Watch is still needs_input. After the
	// resume the Watch has moved past the question, so a repeat uses the round
	// of the answer it repeats instead of the new Watch revision. A new
	// question therefore passes the status guard in its own round.
	round := w.Revision
	if w.Status != domain.StatusNeedsInput {
		if answered, ok := h.decisions.LookupDecisionID(in.WatchID, in.DecisionID); ok {
			round = answered.Round
		} else if last, ok := h.decisions.Lookup(in.WatchID); ok {
			round = last.Round
		} else {
			return DecisionRecord{}, false, domain.NewAppError(domain.CodeInvalidInput, "ack: decision requires a stopped Watch")
		}
	}
	rec := DecisionRecord{
		DecisionID: in.DecisionID,
		WatchID:    in.WatchID,
		Decision:   d,
		ResumedAt:  h.clock.Now(),
		Round:      round,
	}
	return h.decisions.Decide(in.WatchID, rec, func() (uint64, error) {
		next, err := h.resumeWatch(w)
		if err != nil {
			return 0, err
		}
		return next.Revision, nil
	})
}

// applyDefer parks call once per Watch. The first call is stored in the
// registry and returned with applied true. A repeat returns the stored call
// with applied false and writes nothing. No Watch transition runs, so status,
// verdict and revision stay put and no agent is resumed. The caller owns the
// parked call and decides what to do with it.
func (h *AckHandler) applyDefer(in AckInput, call domain.DeferredToolUse) (domain.DeferredToolUse, bool, error) {
	if call.Name == "" {
		return domain.DeferredToolUse{}, false, domain.NewAppError(domain.CodeInvalidInput, "ack: defer requires a tool name")
	}
	if h.deferred == nil {
		return domain.DeferredToolUse{}, false, domain.NewAppError(domain.CodePersistFailed, "ack: deferred registry is nil")
	}
	stored, applied := h.deferred.Park(in.WatchID, call)
	return stored, applied, nil
}

// applyBlockedNotMine records a blocked:not_mine classification and blocks the
// Watch once. The session ID comes from the request _meta and is never taken
// from the client supplied input. With a blocker port installed the SQLite
// incident is authoritative: it dedupes the Watch in BlockedWatchIDs and
// survives a restart, so the in-memory registry is only a local mirror. A
// repeat is an idempotent no-op even when the Watch is already blocked.
func (h *AckHandler) applyBlockedNotMine(in AckInput, w domain.Watch, sessionID string) (BlockedWatch, bool, error) {
	if sessionID == "" {
		return BlockedWatch{}, false, domain.NewAppError(domain.CodeInvalidInput, "ack: blocked decision requires sessionID from _meta")
	}
	if h.blocked == nil {
		return BlockedWatch{}, false, domain.NewAppError(domain.CodePersistFailed, "ack: blocked registry is nil")
	}
	failed := collectFailedChecks(w)
	now := h.clock.Now()
	rec := BlockedWatch{
		WatchID:        in.WatchID,
		Reason:         domain.BlockReasonNotMine,
		FailedChecks:   failed,
		Since:          now,
		BlockedBy:      sessionID,
		OrchestratorID: w.OrchestratorID,
	}
	info := domain.BlockedInfo{
		Reason:       rec.Reason,
		FailedChecks: failedCheckNames(failed),
		Since:        rec.Since,
		BlockedBy:    rec.BlockedBy,
	}

	alreadyBlocked := w.Status == domain.StatusBlocked || w.Blocked != nil
	blockerID := ""
	if h.blockers != nil {
		// GetOrCreateOpenBlocker appends watchID at most once, so a repeated
		// ack cannot open a second incident or add a duplicate Watch.
		incident, _, err := h.blockers.GetOrCreateOpenBlocker(
			w.Repo, w.BaseRef, info.FailedChecks, in.WatchID, now)
		if err != nil {
			observe.ActorError("ack-blocker-open", in.WatchID, err)
			return BlockedWatch{}, false, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
		}
		blockerID = incident.ID
		if alreadyBlocked && w.BlockerID == "" {
			// A Watch blocked before the port was wired has no incident link.
			// Attach it without moving the existing block or its revision.
			linked := w
			linked.BlockerID = blockerID
			if writer, ok := h.store.(ackWatchWriter); ok {
				if err := writer.SaveWatch(linked); err != nil {
					observe.ActorError("ack-blocker-link", in.WatchID, err)
					return BlockedWatch{}, false, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
				}
			}
		}
	}

	// The persisted Watch is the durable idempotency signal when SQLite is in
	// play: a restarted process has an empty in-memory registry but still sees
	// StatusBlocked on the Watch. Without the port the registry is the only
	// guard, so keep its applied flag as the legacy behaviour.
	if h.blockers != nil {
		rec, _ = h.blocked.Block(in.WatchID, rec)
		if alreadyBlocked {
			return rec, false, nil
		}
	} else {
		var applied bool
		rec, applied = h.blocked.Block(in.WatchID, rec)
		if !applied {
			return rec, false, nil
		}
	}
	if err := h.blockWatch(w, info, blockerID); err != nil {
		return BlockedWatch{}, false, err
	}
	observe.BlockEscalated(in.WatchID, blockedReasonNotMine)
	if blockerID != "" {
		observe.Event(observe.LevelInfo, "blocker_opened", observe.Fields{
			"blockerID": blockerID, "watchID": in.WatchID, "repo": w.Repo, "baseRef": w.BaseRef,
		})
	}
	return rec, true, nil
}

// applyBlockedResolved clears the block on a Watch whose fix was confirmed.
// With a blocker port installed it resolves the incident, which returns the
// Watch and every other Watch attached to it to watching in one transaction.
// Without the port it applies the domain UnblockEvent to the named Watch only.
// A Watch that is not blocked is left alone, so a repeat resolve changes
// nothing. The fixFor link stays in the payload as the history of the repair.
func (h *AckHandler) applyBlockedResolved(w domain.Watch) (bool, error) {
	if h.blockers != nil {
		blockerID := w.BlockerID
		if blockerID == "" {
			// A Watch written before the block was linked still carries the
			// blocked payload; find its open incident by membership.
			active, err := h.blockers.ActiveBlockers()
			if err != nil {
				observe.ActorError("ack-blocker-list", w.ID, err)
				return false, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
			}
			for _, incident := range active {
				for _, id := range incident.BlockedWatchIDs {
					if id == w.ID {
						blockerID = incident.ID
						break
					}
				}
				if blockerID != "" {
					break
				}
			}
		}
		if blockerID == "" {
			return false, nil
		}
		unblocked, err := h.blockers.ResolveBlocker(blockerID, h.clock.Now())
		if err != nil {
			observe.ActorError("ack-blocker-resolve", w.ID, err)
			return false, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
		}
		ids := make([]string, 0, len(unblocked))
		for _, unwound := range unblocked {
			ids = append(ids, unwound.ID)
		}
		h.rotateGenerations(ids...)
		h.blocked.Unblock(w.ID)
		observe.Event(observe.LevelInfo, "blocker_resolved", observe.Fields{
			"blockerID": blockerID, "watchID": w.ID, "watches": len(unblocked),
		})
		return len(unblocked) > 0, nil
	}

	next, effects := domain.Transition(w, domain.UnblockEvent(h.clock.Now()))
	if next.Status != domain.StatusWatching || next.Verdict != domain.VerdictWatching || next.Blocked != nil {
		return false, domain.NewAppError(domain.CodeInvalidInput, "ack: unblock requires a blocked Watch")
	}
	if next.Revision == w.Revision {
		// The domain transition was a no-op: the Watch was not blocked.
		return false, nil
	}
	for _, effect := range effects {
		if effect.Kind == domain.EffectCreateWatch {
			return false, domain.NewAppError(domain.CodeInvalidInput, "ack: unblock must not create a new Watch")
		}
	}
	if writer, ok := h.store.(ackWatchWriter); ok {
		if err := writer.SaveWatch(next); err != nil {
			return false, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
		}
	}
	h.rotateGenerations(w.ID)
	h.blocked.Unblock(w.ID)
	return true, nil
}

// blockWatch applies the domain block transition to the Watch and persists it.
// Transition fills StatusBlocked, VerdictBlocked and the Blocked info while
// keeping the Watch identity: blocking changes one task, it never retries or
// creates a successor. blockerID links the Watch to the durable incident; it is
// empty only on the legacy in-memory path. A Watch that is already blocked is a
// no-op, so neither the revision nor the original block details move.
func (h *AckHandler) blockWatch(w domain.Watch, info domain.BlockedInfo, blockerID string) error {
	next, effects := domain.Transition(w, domain.BlockEvent(info, info.Since))
	if next.Status != domain.StatusBlocked || next.Verdict != domain.VerdictBlocked || next.Blocked == nil {
		return domain.NewAppError(domain.CodeInvalidInput, "ack: Watch is not live for a block")
	}
	next.BlockerID = blockerID
	if next.Revision == w.Revision {
		// Already blocked: the domain transition is a no-op, nothing to persist.
		return nil
	}
	for _, effect := range effects {
		if effect.Kind == domain.EffectCreateWatch {
			return domain.NewAppError(domain.CodeInvalidInput, "ack: block must not create a new Watch")
		}
	}
	writer, ok := h.store.(ackWatchWriter)
	if !ok {
		return nil
	}
	if err := writer.SaveWatch(next); err != nil {
		return domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
	}
	return nil
}

// resumeWatch resumes a stopped Watch after a decision (ADR 0058 §4). It uses
// the domain continue transition so the agent continues in the same task at the
// next revision. The domain never emits EffectCreateWatch here, and that
// invariant is checked again before the Watch is saved. With no write side, the
// decision is still recorded but the transition is not persisted.
func (h *AckHandler) resumeWatch(w domain.Watch) (domain.Watch, error) {
	next, effects := domain.Transition(w, domain.ContinueEvent(w.ID, 1, h.clock.Now()))
	if next.Status != domain.StatusWatching || next.Revision == w.Revision {
		return w, domain.NewAppError(domain.CodeLimitExceeded, "ack: Watch is not parked for a decision")
	}
	for _, effect := range effects {
		if effect.Kind == domain.EffectCreateWatch {
			return w, domain.NewAppError(domain.CodeInvalidInput, "ack: decision must not create a new Watch")
		}
	}
	if writer, ok := h.store.(ackWatchWriter); ok {
		if err := writer.SaveWatch(next); err != nil {
			return w, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
		}
	}
	return next, nil
}

// Decide applies an approve or reject decision that resumes a stopped agent
// (ADR 0058 §4). It is the typed entry point for callers that need the decision
// receipt; the MCP tool goes through Handle and keeps its ok-only response.
func (h *AckHandler) Decide(ctx context.Context, in AckInput, decision Decision, decisionID string) (AckReceipt, error) {
	in.Decision = string(decision)
	in.DecisionID = decisionID
	return h.AckReceipt(ctx, in)
}

// continueWatch applies the domain continue transition to the same Watch. It
// checks that the Watch is still parked before reserving an answer round, so a
// refused answer leaves the round, the Watch and the obligation unchanged. The
// domain transition never emits EffectCreateWatch; that invariant is checked
// here as well.
func (h *AckHandler) continueWatch(in AckInput, w domain.Watch) error {
	if in.ContinueOf != in.WatchID {
		return domain.NewAppError(domain.CodeInvalidInput, "ack: continueOf must name the same Watch")
	}
	if w.Status != domain.StatusNeedsInput {
		return domain.NewAppError(domain.CodeLimitExceeded, "ack: Watch is not parked for input")
	}
	round, err := h.continuations.Next(in.WatchID)
	if err != nil {
		return err
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
			return domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
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
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "beacon_ack",
		Description: "Record the intention to claim one obligation by stamping claimedAt; idempotent. This is an acknowledgement, not a result: read the verdict with beacon_get_result or beacon_wait_result. With decision=approve or decision=reject it resumes a stopped agent once in the same Watch. With decision=blocked:not_mine it blocks the Watch, recording the failed checks and the calling session as blocker. With decision=blocked:resolved it clears a confirmed block and returns the Watch to watching.",
		Annotations: attachToolAnnotations(true),
	}, handler.Handle)
}
