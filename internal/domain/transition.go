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

const (
	// EventVerdict carries the verdict computed by the verdict engine (T1.5).
	EventVerdict EventKind = "verdict"
	// EventCancel is a destructive cancel_watch call (T2.8), idempotent and terminal.
	EventCancel EventKind = "cancel"
	// EventHeadChanged means the PR head SHA moved (ADR 0019).
	EventHeadChanged EventKind = "head_changed"
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
type Event struct {
	Kind       EventKind
	Verdict    Verdict
	NewHeadSHA string
	At         time.Time
}

// EffectKind identifies a side effect the caller must perform after Transition.
type EffectKind string

const (
	// EffectWakeSubscribers stages one obligation per subscriber at Revision.
	EffectWakeSubscribers EffectKind = "wake_subscribers"
	// EffectCreateWatch creates the successor Watch after head_changed, for the
	// same subscribers (ADR 0019).
	EffectCreateWatch EffectKind = "create_watch"
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
//	closed    -> closed     : no-op (terminal)
//
// Forbidden transitions: anything leaving closed (closed -> watching,
// closed -> notifying). A cancelled Watch therefore never becomes ready.
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
	default:
		return next, nil
	}
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
