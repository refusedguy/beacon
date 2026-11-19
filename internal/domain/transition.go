// Package domain holds the Beacon entities, status enums and port interfaces.
// It imports only the standard library; adapters live in internal/infra.
//
// This file implements the Watch state machine. Transition is a pure function:
// it never mutates its argument, performs I/O or reads the system clock. Every
// timestamp it stamps comes from the Event supplied by the caller.
package domain

import "time"

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

// MaxContinueRounds is the ADR 0031 §3 cap on human-answer rounds for one
// Watch. A fifth answer may resume the task; a sixth is refused.
const MaxContinueRounds uint64 = 5

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
)

// CloseReason is the machine-readable cause stored on a closed Watch.
type CloseReason string

const (
	CloseReasonHeadChanged CloseReason = "head_changed"
	CloseReasonCancelled   CloseReason = "cancelled"
	CloseReasonMerged      CloseReason = "merged"
	CloseReasonClosed      CloseReason = "closed"
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
//	watching  -> closed     : terminal verdict, cancel or head_changed
//	notifying -> notifying  : same actionable verdict repeated (idempotent) or a new actionable verdict
//	notifying -> watching   : re-run downgrade to watching (ready/keep_working -> watching)
//	notifying -> closed     : terminal verdict, cancel or head_changed
//	watching  -> needs_input: the agent asked a human a question (ADR 0031 §3)
//	notifying -> needs_input: same question after an actionable verdict
//	needs_input -> watching : human answer via ContinueEvent with a valid round
//	needs_input -> closed   : cancel or head_changed
//	needs_input -> needs_input: repeated question or out-of-range answer (no-op)
//	closed    -> closed     : no-op (terminal)
//
// Forbidden transitions: anything leaving closed (closed -> watching,
// closed -> notifying, closed -> needs_input). A cancelled Watch therefore
// never becomes ready. A ContinueEvent whose ContinueOf is a different Watch
// is also a no-op: an answer can never become a new task.
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
	default:
		return next, nil
	}
}

// parkForInput moves a live Watch to needs_input. It is idempotent: a second
// question does not bump the revision again. The parked Watch keeps its ID and
// subscribers, so a later answer resumes this task rather than creating one.
func parkForInput(w Watch, e Event) (Watch, []Effect) {
	next := cloneWatch(w)
	if w.Status == StatusClosed || w.Status == StatusNeedsInput {
		return next, nil
	}
	next.Status = StatusNeedsInput
	next.Verdict = VerdictNeedsInput
	next.Revision = bumpRevision(w.Revision)
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

func transitionVerdict(w Watch, e Event) (Watch, []Effect) {
	next := cloneWatch(w)
	if w.Status == StatusClosed || !validVerdict(e.Verdict) {
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
