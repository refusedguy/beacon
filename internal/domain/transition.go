// Package domain holds the Beacon entities, status enums and port interfaces.
// It imports only the standard library; adapters live in internal/infra.
//
// This file implements the Watch state machine. Transition is a pure function:
// it never mutates its argument, performs I/O or reads the system clock. Every
// timestamp it stamps comes from the Event supplied by the caller.
package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// EventKind identifies what happened to a Watch.
type EventKind string

// StatusNeedsInput is the parked state of a Watch whose agent asked a human a
// question. It is a lifecycle state alongside watching, notifying and closed
// (ADR 0031 §3). The parked Watch keeps its ID: answering resumes the same
// task through ContinueEvent instead of creating a successor Watch.
const StatusNeedsInput Status = "needs_input"

// VerdictNeedsInput is the verdict carried while a Watch is parked. It marks
// the reason the wait stopped early and, like watching, is not a result that
// marks an obligation read.
const VerdictNeedsInput Verdict = "needs_input"

// StatusParked is the lifecycle state of a Watch whose execution step failed
// with a limit, an exhausted budget, a crash or an external-step error
// (ADR 0039 §1). Parking keeps the Watch and its full error text for a human.
// It never schedules a blind retry. It is not a branch of the ADR 0017
// delivery ladder: that ladder classifies notification outcomes, while parking
// classifies the execution step and the external verdict.
const StatusParked Status = "parked"

// VerdictParked is the verdict carried while a Watch is parked after a failed
// execution step. Like watching, it is not a result that marks an obligation read.
const VerdictParked Verdict = "parked"

// VerdictAccepted is the verdict on a Watch closed by a successful explicit
// acceptance command (ADR 0059 §6). The verdict engine never produces it:
// validVerdict rejects it, so a verdict event cannot stand in for the command.
const VerdictAccepted Verdict = "accepted"

// ParkReason is the machine-readable cause kept with a parked Watch in the
// journal (ADR 0039 §1).
type ParkReason string

const (
	ParkReasonLimit  ParkReason = "limit"
	ParkReasonBudget ParkReason = "budget"
	ParkReasonCrash  ParkReason = "crash"
	ParkReasonError  ParkReason = "error"
)

// MaxContinueRounds is the ADR 0031 §3 cap on human-answer rounds for one
// Watch. A fifth answer may resume the task; a sixth is refused.
const MaxContinueRounds uint64 = 5

// SessionTranscriptRecord is one persisted line of an agent session.
// SessionID is the canonical session id, Parent names the subagent's parent
// session and is empty for the root session. Seq orders records within a
// session and starts at one. Kind names the event kind and Payload is the
// serialized event. Records live in the external store so a dead agent can be
// resumed from a different process (CS-04).
type SessionTranscriptRecord struct {
	SessionID string
	Parent    string
	Seq       uint64
	Kind      string
	Payload   json.RawMessage
}

// ErrInvalidSessionID reports a resume id that is not a canonical UUID.
// Materialization refuses it before any directory is created, so a value with
// a path separator cannot escape the transcript root.
var ErrInvalidSessionID = fmt.Errorf("domain: invalid session id: want a UUID")

// IsUUID reports whether raw is a canonical 8-4-4-4-12 hexadecimal UUID. The
// check is strict: each group has the exact length and every byte is a hex
// digit. It rejects slashes, dots and any other path syntax before the value
// can be used as a directory name.
func IsUUID(raw string) bool {
	if len(raw) != 36 {
		return false
	}
	for i := 0; i < len(raw); i++ {
		switch i {
		case 8, 13, 18, 23:
			if raw[i] != '-' {
				return false
			}
		default:
			c := raw[i]
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
				return false
			}
		}
	}
	return true
}

// ValidateResumeID applies the CS-04 boundary check to a resume id. A path,
// an empty id or a non-UUID name is refused. The returned string is the
// validated id, safe to use as a single path component.
func ValidateResumeID(raw string) (string, error) {
	if !IsUUID(raw) || strings.ContainsAny(raw, `/\`) {
		return "", fmt.Errorf("%w: %q", ErrInvalidSessionID, raw)
	}
	return raw, nil
}

// SessionTranscriptSubkey groups the records of one subagent transcript.
// Key identifies the child session, Parent is the resuming session and Records
// are its lines in storage order. listSubkeys is the domain-level operation a
// store adapter implements to restore subagent transcripts on resume.
type SessionTranscriptSubkey struct {
	Key     string
	Parent  string
	Records []SessionTranscriptRecord
}

// ListSessionTranscriptSubkeys groups the child transcripts of parent by child
// session id. A record belongs to parent when its Parent field names parent; a
// record with an empty Parent is treated as a legacy child of parent. The root
// session's own records and records of another parent are skipped. Order is
// preserved for callers whose input is already sorted by session and sequence.
// The function is pure: the store adapter turns the result into files.
func ListSessionTranscriptSubkeys(parent string, records []SessionTranscriptRecord) []SessionTranscriptSubkey {
	byKey := make(map[string][]SessionTranscriptRecord)
	var order []string
	for _, rec := range records {
		if rec.SessionID == "" || rec.SessionID == parent {
			continue
		}
		if rec.Parent != "" && rec.Parent != parent {
			continue
		}
		if _, seen := byKey[rec.SessionID]; !seen {
			order = append(order, rec.SessionID)
		}
		byKey[rec.SessionID] = append(byKey[rec.SessionID], rec)
	}
	out := make([]SessionTranscriptSubkey, 0, len(order))
	for _, key := range order {
		out = append(out, SessionTranscriptSubkey{
			Key:     key,
			Parent:  parent,
			Records: byKey[key],
		})
	}
	return out
}

// MaterializedTranscript is the in-memory result of materializing a session.
// Root holds the root records and Subkeys holds the child transcripts, so a
// caller can write JSONL into a temp dir and restore every subagent.
type MaterializedTranscript struct {
	SessionID string
	Root      []SessionTranscriptRecord
	Subkeys   []SessionTranscriptSubkey
}

// MaterializeSessionTranscript validates id, then splits records into the root
// transcript and the subagent subkeys. A non-UUID id fails before any data is
// read, matching the SDK boundary check. The caller owns the temp directory and
// must remove it on cancel or error.
func MaterializeSessionTranscript(id string, records []SessionTranscriptRecord) (MaterializedTranscript, error) {
	validated, err := ValidateResumeID(id)
	if err != nil {
		return MaterializedTranscript{}, err
	}
	var root []SessionTranscriptRecord
	for _, rec := range records {
		if rec.SessionID == validated {
			root = append(root, rec)
		}
	}
	return MaterializedTranscript{
		SessionID: validated,
		Root:      root,
		Subkeys:   ListSessionTranscriptSubkeys(validated, records),
	}, nil
}

const (
	// EventVerdict carries the verdict computed by the verdict engine (T1.5).
	EventVerdict EventKind = "verdict"
	// EventCancel is a destructive cancel_watch call (T2.8), idempotent and terminal.
	EventCancel EventKind = "cancel"
	// EventHeadChanged means the PR head SHA moved (ADR 0019).
	EventHeadChanged EventKind = "head_changed"
	// EventNeedsInput parks a Watch because the agent asked a human a question.
	EventNeedsInput EventKind = "needs_input"
	// EventContinue resumes a parked Watch with a human answer.
	EventContinue EventKind = "continue"
	// EventPark parks a Watch after a failed execution step: a limit, an
	// exhausted budget, a crash or an external-step error (ADR 0039 §1).
	EventPark EventKind = "park"
	// EventAccept reports the result of the explicit acceptance command. Only a
	// successful run closes a Watch (ADR 0059 §6).
	EventAccept EventKind = "accept"
	// EventBlock blocks a live Watch when required checks fail in a way the
	// executor cannot clear on its own. The Watch stays open and wakes nobody.
	EventBlock EventKind = "block"
	// EventUnblock resumes a blocked Watch to watching, clearing the block.
	EventUnblock EventKind = "unblock"
	// EventUnpark resumes a parked Watch to watching after every subscriber has
	// been notified of the park. Parking stays in force until then, so a CI
	// refresh cannot silently revive a Watch nobody has seen (ATX-32).
	EventUnpark EventKind = "unpark"
	// EventRevoke closes one failed wake delivery. It is the boundary that ties
	// a retry to the delivery it replaces: the ladder records the revoke,
	// advances the rung and arms the next attempt. A revoke changes no verdict
	// and stages no wake by itself.
	EventRevoke EventKind = "revoke"
	// EventTranscriptRecord appends one line to a session's external transcript
	// (CS-04). It changes no Watch state and stages no wake; the adapter writes
	// the record to the store.
	EventTranscriptRecord EventKind = "transcript_record"
	// EventResumeTranscript materializes a session's transcript from the
	// external store after a restart. It changes no Watch state and stages the
	// materialize effect for the adapter.
	EventResumeTranscript EventKind = "resume_transcript"
)

// DeliveryRevoke describes one revoked wake delivery. ObligationID names the
// delivery to close, Outcome is what the adapter observed and At is when the
// revoke was recorded. Retries are tied to this record, so one incident issues
// a single human alert even after several failed deliveries.
type DeliveryRevoke struct {
	ObligationID string
	Outcome      AttemptOutcome
	At           time.Time
}

// CloseReason is the machine-readable cause stored on a closed Watch.
type CloseReason string

const (
	CloseReasonHeadChanged CloseReason = "head_changed"
	CloseReasonCancelled   CloseReason = "cancelled"
	CloseReasonMerged      CloseReason = "merged"
	CloseReasonClosed      CloseReason = "closed"
	// CloseReasonAccepted marks a Watch closed by a successful explicit
	// acceptance command (ADR 0059 §6), not by an agent exit or a done flag.
	CloseReasonAccepted CloseReason = "accepted"
)

// Event is one input to Transition. Only the fields relevant to Kind are set;
// At is optional and supplies timestamps so Transition never calls time.Now.
// ContinueOf names the Watch a human answer resumes and Round is the 1-based
// answer round; both are used only by EventContinue.
type Event struct {
	Kind       EventKind
	Verdict    Verdict
	NewHeadSHA string
	At         time.Time
	ContinueOf string
	Round      uint64
	// ParkReason and ParkError are used only by EventPark. ParkError is the
	// full error text and is never truncated by the domain.
	ParkReason ParkReason
	ParkError  string
	// AcceptOK is used only by EventAccept. It reports whether the explicit
	// acceptance command ran successfully. False never closes the Watch.
	AcceptOK bool
	// Blocked is used only by EventBlock and carries the block details. It is
	// copied into the Watch so later caller mutation cannot change the Watch.
	Blocked *BlockedInfo
	// Revoke is used only by EventRevoke. It names the failed delivery whose
	// retry the ladder must arm.
	Revoke *DeliveryRevoke
	// Transcript is used only by EventTranscriptRecord and EventResumeTranscript.
	// It carries the record to persist or the validated resume id.
	Transcript *SessionTranscriptRecord
	// ResumeID is the session id validated before materialization.
	ResumeID string
}

// EffectKind identifies a side effect the caller must perform after Transition.
type EffectKind string

const (
	// EffectWakeSubscribers stages one obligation per subscriber at Revision.
	EffectWakeSubscribers EffectKind = "wake_subscribers"
	// EffectCreateWatch creates the successor Watch after head_changed, for the
	// same subscribers (ADR 0019).
	EffectCreateWatch EffectKind = "create_watch"
	// EffectContinueWatch resumes the same parked Watch after a human answer.
	// It never carries a new Watch ID: the task identity is preserved.
	EffectContinueWatch EffectKind = "continue_watch"
	// EffectLogPark writes the full error text and the park reason into the
	// journal. It is the only effect of parking, so no retry is ever staged.
	EffectLogPark EffectKind = "log_park"
	// EffectRevokeDelivery tells the adapter to apply a delivery revoke to the
	// ladder. It is the only effect of EventRevoke.
	EffectRevokeDelivery EffectKind = "revoke_delivery"
	// EffectSaveTranscript tells the adapter to persist one transcript record.
	// It is the only effect of EventTranscriptRecord.
	EffectSaveTranscript EffectKind = "save_transcript"
	// EffectMaterializeTranscript tells the adapter to materialize a resumed
	// session from the store into a temp dir. It is the only effect of
	// EventResumeTranscript.
	EffectMaterializeTranscript EffectKind = "materialize_transcript"
)

// Effect describes work deferred to the adapter layer. Transition only returns
// effects; it never executes them.
type Effect struct {
	Kind        EffectKind
	WatchID     string
	Revision    uint64
	Verdict     Verdict
	HeadSHA     string
	Subscribers []Subscriber
	// ParkReason and ParkError are used only by EffectLogPark. They carry the
	// reason and the full error text to the journal, which performs the I/O.
	ParkReason ParkReason
	ParkError  string
	// Revoke is used only by EffectRevokeDelivery. It is copied from the event.
	Revoke *DeliveryRevoke
	// Transcript carries the record to persist for EffectSaveTranscript, or the
	// validated session id for EffectMaterializeTranscript.
	Transcript *SessionTranscriptRecord
	ResumeID   string
}

// Constructors keep callers from building malformed events by hand.
func VerdictEvent(v Verdict, at time.Time) Event {
	return Event{Kind: EventVerdict, Verdict: v, At: at}
}

func CancelEvent(at time.Time) Event {
	return Event{Kind: EventCancel, At: at}
}

func HeadChangedEvent(newHeadSHA string, at time.Time) Event {
	return Event{Kind: EventHeadChanged, NewHeadSHA: newHeadSHA, At: at}
}

// NeedsInputEvent parks a Watch while the agent waits for a human answer.
func NeedsInputEvent(at time.Time) Event {
	return Event{Kind: EventNeedsInput, At: at}
}

// ContinueEvent resumes the Watch named by continueOf at the given answer
// round. Transition ignores it unless the Watch is parked and the round is
// inside the ADR 0031 §3 cap.
func ContinueEvent(continueOf string, round uint64, at time.Time) Event {
	return Event{Kind: EventContinue, ContinueOf: continueOf, Round: round, At: at}
}

// ParkEvent parks a Watch after a failed execution step. reason is the cause
// written to the journal and parkError is the full error text, carried through
// unchanged to EffectLogPark.
func ParkEvent(reason ParkReason, parkError string, at time.Time) Event {
	return Event{Kind: EventPark, ParkReason: reason, ParkError: parkError, At: at}
}

// AcceptEvent reports the result of the explicit acceptance command. ok=false
// leaves the Watch open: a failed command is not a completed task (ADR 0059 §6).
func AcceptEvent(ok bool, at time.Time) Event {
	return Event{Kind: EventAccept, AcceptOK: ok, At: at}
}

// BlockEvent blocks a live Watch with the supplied reason. The BlockedInfo is
// copied so a caller cannot mutate the Watch through the original value.
func BlockEvent(info BlockedInfo, at time.Time) Event {
	return Event{Kind: EventBlock, Blocked: &info, At: at}
}

// UnblockEvent resumes a blocked Watch. It is a no-op on a Watch that is not
// blocked.
func UnblockEvent(at time.Time) Event {
	return Event{Kind: EventUnblock, At: at}
}

// UnparkEvent resumes a parked Watch after its park notice reached every
// subscriber. It is a no-op on a Watch that is not parked, so a repeated notice
// cannot revive a Watch twice.
func UnparkEvent(at time.Time) Event {
	return Event{Kind: EventUnpark, At: at}
}

// RevokeEvent builds the event that closes one failed wake delivery. The
// revoke time is set from at so the caller does not have to fill it twice.
func RevokeEvent(r DeliveryRevoke, at time.Time) Event {
	r.At = at
	return Event{Kind: EventRevoke, Revoke: &r, At: at}
}

// TranscriptRecordEvent builds the event that persists one transcript line.
func TranscriptRecordEvent(rec SessionTranscriptRecord, at time.Time) Event {
	return Event{Kind: EventTranscriptRecord, Transcript: &rec, At: at}
}

// ResumeTranscriptEvent builds the event that materializes a resumed session.
// The id is validated when Transition handles the event, not here.
func ResumeTranscriptEvent(id string, at time.Time) Event {
	return Event{Kind: EventResumeTranscript, ResumeID: id, At: at}
}

// CanContinueRound reports whether round is a permitted human-answer round.
// Rounds start at one and stop at MaxContinueRounds.
func CanContinueRound(round uint64) bool {
	return round >= 1 && round <= MaxContinueRounds
}

// Transition applies e to w and returns the next Watch plus the effects the
// caller must run. It never mutates w.
//
// Allowed status transitions (from -> to, with the triggering event):
//
//	watching  -> watching   : verdict watching repeated (no revision, no effect)
//	watching  -> notifying  : actionable verdict (ready/rebase/retry/keep_working/timeout)
//	watching  -> closed     : terminal verdict, cancel, head_changed or successful acceptance
//	notifying -> notifying  : same actionable verdict repeated (idempotent) or a new actionable verdict
//	notifying -> watching   : re-run downgrade to watching (ready/keep_working -> watching)
//	notifying -> closed     : terminal verdict, cancel, head_changed or successful acceptance
//	watching  -> needs_input: the agent asked a human a question (ADR 0031 §3)
//	notifying -> needs_input: same question after an actionable verdict
//	needs_input -> watching : human answer via ContinueEvent with a valid round
//	needs_input -> closed   : cancel, head_changed or successful acceptance
//	needs_input -> needs_input: repeated question, out-of-range answer or a CI verdict (no-op)
//	watching  -> parked    : failed execution step (limit/budget/crash/error)
//	notifying -> parked    : same failure after an actionable verdict
//	needs_input -> parked   : same failure while waiting for a human answer
//	parked    -> parked    : repeated failure or a verdict/answer/needs_input event (no-op)
//	parked    -> watching  : explicit unpark after all subscribers saw the park notice
//	parked    -> closed    : cancel, head_changed or a successful acceptance command
//	watching  -> blocked   : required checks fail and need outside action (no wake)
//	notifying -> blocked   : same block after an actionable verdict (no wake)
//	blocked   -> blocked   : repeated block (no-op)
//	blocked   -> watching  : explicit unblock; block details are cleared (no wake)
//	blocked   -> closed    : cancel, head_changed or a successful acceptance command
//	closed    -> closed     : no-op (terminal)
//	live      -> same       : EventRevoke stages EffectRevokeDelivery and changes no state
//	live      -> same       : EventTranscriptRecord stages EffectSaveTranscript, changes no state
//	live      -> same       : EventResumeTranscript stages EffectMaterializeTranscript on a valid UUID id
//
// A successful EventAccept closes a live Watch from any status with
// CloseReasonAccepted and VerdictAccepted (ADR 0059 §6). That command is the
// only way the acceptance path closes a Watch: a failed command, a done-flag
// verdict and a bare agent process exit all leave the Watch open. The adapter
// that ran the command owns the receipt, so acceptance stages no effect.
//
// Forbidden transitions: anything leaving closed (closed -> watching,
// closed -> notifying, closed -> needs_input, closed -> parked). A cancelled
// Watch therefore never becomes ready. A parked Watch never resumes from a
// verdict, a ContinueEvent or a needs_input event: leaving park needs either an
// explicit unpark after all subscribers saw the notice, another human action
// that starts a new record, or a head change. Head change
// still closes a parked Watch and creates a successor, as it does for any live
// Watch (ADR 0019); that is a new PR head, not a retry of the failure. A
// ContinueEvent whose ContinueOf is a different Watch is also a no-op: an
// answer can never become a new task. A blocked Watch ignores verdicts like a
// parked one: only an explicit unblock resumes it, so a CI refresh cannot wake
// subscribers of a Watch that is waiting on outside action. A needs_input
// Watch likewise ignores CI verdicts: no verdict can wake its subscribers or
// close it before the human answers (ADR 0031). A ContinueEvent, cancel, head
// change, acceptance or park still moves it as listed above.
//
// Revision grows only when the verdict changes, including a re-run downgrade
// to watching at the same SHA. It never grows on watching -> watching. There
// is no time-based transition: TTL closure is gone (ADR 0019).
func Transition(w Watch, e Event) (Watch, []Effect) {
	next := cloneWatch(w)
	switch e.Kind {
	case EventCancel:
		if w.Status == StatusClosed {
			return next, nil
		}
		next.Status = StatusClosed
		next.CloseReason = string(CloseReasonCancelled)
		next.ClosedAt = e.At
		return next, nil
	case EventHeadChanged:
		if w.Status == StatusClosed || w.HeadSHA == e.NewHeadSHA {
			return next, nil
		}
		next.Status = StatusClosed
		next.CloseReason = string(CloseReasonHeadChanged)
		next.ClosedAt = e.At
		return next, []Effect{{
			Kind:        EffectCreateWatch,
			WatchID:     w.ID,
			HeadSHA:     e.NewHeadSHA,
			Subscribers: cloneSubscribers(w.Subscribers),
		}}
	case EventVerdict:
		return transitionVerdict(w, e)
	case EventNeedsInput:
		return parkForInput(w, e)
	case EventContinue:
		return continueWatch(w, e)
	case EventPark:
		return parkOnFailure(w, e)
	case EventAccept:
		return acceptWatch(w, e)
	case EventBlock:
		return blockWatch(w, e)
	case EventUnblock:
		return unblockWatch(w, e)
	case EventUnpark:
		return unparkParked(w, e)
	case EventRevoke:
		return revokeDelivery(w, e)
	case EventTranscriptRecord:
		return recordTranscript(w, e)
	case EventResumeTranscript:
		return materializeTranscript(w, e)
	default:
		return next, nil
	}
}

// parkForInput moves a live Watch to needs_input. It is idempotent: a second
// question does not bump the revision again. The parked Watch keeps its ID and
// subscribers, so a later answer resumes this task rather than creating one.
func parkForInput(w Watch, e Event) (Watch, []Effect) {
	next := cloneWatch(w)
	if w.Status == StatusClosed || w.Status == StatusNeedsInput || w.Status == StatusParked {
		return next, nil
	}
	next.Status = StatusNeedsInput
	next.Verdict = VerdictNeedsInput
	next.Revision = bumpRevision(w.Revision)
	return next, nil
}

// parkOnFailure parks a live Watch after a failed execution step (ADR 0039 §1).
// It is idempotent: re-parking a parked Watch changes nothing and emits no
// second journal entry. A closed Watch cannot be parked. The full error text
// and reason travel in EffectLogPark, so the domain never truncates text or
// performs I/O. No wake or continue effect is produced, so nothing retries the
// failed step; a human clears the cause and starts a new record to resume work.
func parkOnFailure(w Watch, e Event) (Watch, []Effect) {
	next := cloneWatch(w)
	if w.Status == StatusClosed || w.Status == StatusParked {
		return next, nil
	}
	next.Status = StatusParked
	next.Verdict = VerdictParked
	next.Revision = bumpRevision(w.Revision)
	return next, []Effect{{
		Kind:       EffectLogPark,
		WatchID:    w.ID,
		Revision:   next.Revision,
		ParkReason: e.ParkReason,
		ParkError:  e.ParkError,
	}}
}

// blockWatch moves a live Watch to blocked. It is idempotent: a second block
// changes nothing, including the original BlockedInfo. Blocking keeps the ID
// and subscribers, clears no obligation and stages no effect, so no subscriber
// is woken. A closed Watch cannot be blocked, and a block without details or
// with a reason outside the closed set is ignored, so only known codes reach
// logs and the UI.
func blockWatch(w Watch, e Event) (Watch, []Effect) {
	next := cloneWatch(w)
	if w.Status == StatusClosed || w.Status == StatusBlocked || e.Blocked == nil || !e.Blocked.Reason.Valid() {
		return next, nil
	}
	next.Status = StatusBlocked
	next.Verdict = VerdictBlocked
	next.Blocked = cloneBlockedInfo(e.Blocked)
	next.Revision = bumpRevision(w.Revision)
	return next, nil
}

// unblockWatch resumes a blocked Watch to watching. It is a no-op on a Watch
// that is not blocked. The block details are cleared and the revision grows so
// consumers can see the resume; no effect is staged, so nobody is woken.
func unblockWatch(w Watch, e Event) (Watch, []Effect) {
	next := cloneWatch(w)
	if w.Status != StatusBlocked {
		return next, nil
	}
	next.Status = StatusWatching
	next.Verdict = VerdictWatching
	next.Blocked = nil
	next.Revision = bumpRevision(w.Revision)
	return next, nil
}

// unparkParked resumes a parked Watch to watching once its park notice reached
// every subscriber. It is a no-op on any other status and stages no effect, so
// no wake is armed by the resume itself. The revision grows so consumers see
// that the Watch re-entered the control loop.
func unparkParked(w Watch, e Event) (Watch, []Effect) {
	next := cloneWatch(w)
	if w.Status != StatusParked {
		return next, nil
	}
	next.Status = StatusWatching
	next.Verdict = VerdictWatching
	next.Revision = bumpRevision(w.Revision)
	return next, nil
}

// revokeDelivery stages the revoke effect without changing the Watch. A revoke
// is a delivery fact, not a verdict: it moves no revision, sets no status and
// wakes nobody on its own. A closed Watch ignores it.
func revokeDelivery(w Watch, e Event) (Watch, []Effect) {
	next := cloneWatch(w)
	if w.Status == StatusClosed || e.Revoke == nil || e.Revoke.ObligationID == "" {
		return next, nil
	}
	return next, []Effect{{
		Kind:     EffectRevokeDelivery,
		WatchID:  w.ID,
		Revision: w.Revision,
		Verdict:  w.Verdict,
		Revoke:   e.Revoke,
	}}
}

// recordTranscript stages one transcript record for the adapter. It changes no
// Watch state and emits no wake: transcript persistence is a side effect of the
// ongoing session, not a verdict. A closed Watch and an empty record are
// ignored.
func recordTranscript(w Watch, e Event) (Watch, []Effect) {
	next := cloneWatch(w)
	if w.Status == StatusClosed || e.Transcript == nil || e.Transcript.SessionID == "" {
		return next, nil
	}
	rec := *e.Transcript
	if rec.Payload != nil {
		rec.Payload = append(json.RawMessage(nil), rec.Payload...)
	}
	return next, []Effect{{
		Kind:       EffectSaveTranscript,
		WatchID:    w.ID,
		Revision:   w.Revision,
		Transcript: &rec,
	}}
}

// materializeTranscript stages transcript materialization after a restart. The
// resume id is validated here, at the domain boundary, so a path cannot reach
// the temp directory. An invalid id stages no effect and leaves the Watch
// unchanged. A closed Watch is likewise ignored.
func materializeTranscript(w Watch, e Event) (Watch, []Effect) {
	next := cloneWatch(w)
	if w.Status == StatusClosed {
		return next, nil
	}
	validated, err := ValidateResumeID(e.ResumeID)
	if err != nil {
		return next, nil
	}
	return next, []Effect{{
		Kind:     EffectMaterializeTranscript,
		WatchID:  w.ID,
		Revision: w.Revision,
		ResumeID: validated,
	}}
}

// acceptWatch closes a live Watch only after the explicit acceptance command
// succeeded (ADR 0059 §6). A failed command changes nothing, so neither an agent
// process exit nor a done flag can end the task. The closed Watch keeps its ID
// and subscribers; the acceptance is recorded as its close reason and verdict.
// The adapter that ran the command owns the receipt, so no effect is staged.
func acceptWatch(w Watch, e Event) (Watch, []Effect) {
	next := cloneWatch(w)
	if w.Status == StatusClosed || !e.AcceptOK {
		return next, nil
	}
	next.Status = StatusClosed
	next.Verdict = VerdictAccepted
	next.Revision = bumpRevision(w.Revision)
	next.CloseReason = string(CloseReasonAccepted)
	next.ClosedAt = e.At
	return next, nil
}

// continueWatch resumes a parked Watch on a human answer. It refuses an answer
// for a different Watch, an unparked Watch, and a round past MaxContinueRounds.
// The only effect is EffectContinueWatch, never EffectCreateWatch.
func continueWatch(w Watch, e Event) (Watch, []Effect) {
	next := cloneWatch(w)
	if w.Status != StatusNeedsInput || e.ContinueOf != w.ID || !CanContinueRound(e.Round) {
		return next, nil
	}
	next.Status = StatusWatching
	next.Verdict = VerdictWatching
	next.Revision = bumpRevision(w.Revision)
	return next, []Effect{{
		Kind:     EffectContinueWatch,
		WatchID:  w.ID,
		Revision: next.Revision,
	}}
}

// transitionVerdict ignores a Watch waiting for a human answer. A CI verdict
// must not move needs_input: the answer resumes the same Watch under ADR 0031,
// and a terminal verdict would close it before the human replies.
func transitionVerdict(w Watch, e Event) (Watch, []Effect) {
	next := cloneWatch(w)
	if w.Status == StatusClosed || w.Status == StatusParked || w.Status == StatusBlocked || w.Status == StatusNeedsInput || !validVerdict(e.Verdict) {
		return next, nil
	}
	v := e.Verdict
	if w.Verdict == v {
		// Same verdict: watching -> watching moves neither revision nor effects.
		// A repeated actionable verdict is the same obligation, so also a no-op.
		return next, nil
	}

	// Verdict changed: revision grows by one, including re-run to watching.
	next.Verdict = v
	next.Revision = bumpRevision(w.Revision)

	switch {
	case isTerminalVerdict(v):
		next.Status = StatusClosed
		next.CloseReason = string(v)
		next.ClosedAt = e.At
		return next, nil
	case v == VerdictWatching:
		next.Status = StatusWatching
		return next, nil
	default:
		next.Status = StatusNotifying
		return next, []Effect{{
			Kind:     EffectWakeSubscribers,
			WatchID:  w.ID,
			Revision: next.Revision,
			Verdict:  v,
		}}
	}
}

func isTerminalVerdict(v Verdict) bool {
	return v == VerdictMerged || v == VerdictClosed
}

func validVerdict(v Verdict) bool {
	switch v {
	case VerdictWatching, VerdictReady, VerdictRebase, VerdictRetry,
		VerdictKeepWorking, VerdictTimeout, VerdictMerged, VerdictClosed:
		return true
	default:
		return false
	}
}

func bumpRevision(r uint64) uint64 {
	if r == ^uint64(0) {
		return r
	}
	return r + 1
}

func cloneWatch(w Watch) Watch {
	out := w
	if w.Checks != nil {
		out.Checks = make(map[string]Check, len(w.Checks))
		for k, v := range w.Checks {
			out.Checks[k] = v
		}
	}
	out.Subscribers = cloneSubscribers(w.Subscribers)
	out.Blocked = cloneBlockedInfo(w.Blocked)
	if w.Flaky.Names != nil {
		out.Flaky.Names = append([]string(nil), w.Flaky.Names...)
	}
	if w.Flaky.Used != nil {
		out.Flaky.Used = make(map[string]int, len(w.Flaky.Used))
		for k, v := range w.Flaky.Used {
			out.Flaky.Used[k] = v
		}
	}
	return out
}

func cloneSubscribers(s []Subscriber) []Subscriber {
	if s == nil {
		return nil
	}
	return append([]Subscriber(nil), s...)
}

// cloneBlockedInfo copies the block details, including the FailedChecks slice,
// so a transition never shares mutable state with its input or its event.
func cloneBlockedInfo(b *BlockedInfo) *BlockedInfo {
	if b == nil {
		return nil
	}
	out := *b
	if b.FailedChecks != nil {
		out.FailedChecks = append([]string(nil), b.FailedChecks...)
	}
	return &out
}
