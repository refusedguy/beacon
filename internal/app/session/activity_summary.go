// Package session holds the pure logic for observing session activity. This
// file builds a one-line summary from output a session already produced. It
// makes no model call and reports no readiness. The Watch verdict stays the
// only readiness signal (ADR 0029).
package session

import (
	"strings"
	"time"
)

// Observation states describe activity. They are not task readiness. The
// "ready" label is deliberately absent, because the Watch verdict owns it.
const (
	ActivityActive  = "active"
	ActivityIdle    = "idle"
	ActivityStalled = "stalled"
)

// DefaultSummaryThrottle is the shortest gap between OneLine rewrites while a
// session keeps producing output.
const DefaultSummaryThrottle = 15 * time.Second

// DefaultStallAfter is how long a session may stay silent before it is marked
// stalled. Stalled is an observation. It does not change a Watch verdict.
const DefaultStallAfter = 3 * time.Minute

// OutputEvent is one line a session wrote. At is the time the caller saw the
// line. Empty lines carry no information and are ignored.
type OutputEvent struct {
	At   time.Time
	Line string
}

// ActivitySummary is one row of an activity view. State, OneLine and
// NoOutputFor describe what was observed. None of them decides whether a task
// is ready.
type ActivitySummary struct {
	SessionID    string
	State        string
	OneLine      string
	LastOutputAt time.Time
	NoOutputFor  time.Duration
}

// BuildSummary folds new output into the previous summary. It reads only its
// arguments. There is no clock read beyond now, no I/O and no model request.
// A new meaningful line can rewrite OneLine only after throttle has passed
// since LastOutputAt. Silence grows NoOutputFor and flips State to stalled
// past the stall threshold. New output clears stalled. BuildSummary never sets
// a readiness state and never touches a Watch verdict.
func BuildSummary(events []OutputEvent, previous ActivitySummary, now time.Time, throttle time.Duration) ActivitySummary {
	out := previous

	line, at, ok := lastOutput(events)
	if ok {
		out.State = ActivityActive
		out.NoOutputFor = 0
		if out.LastOutputAt.IsZero() || at.Sub(out.LastOutputAt) >= throttle {
			out.OneLine = line
			out.LastOutputAt = at
		}
		return out
	}

	if out.LastOutputAt.IsZero() {
		return out
	}
	gap := now.Sub(out.LastOutputAt)
	if gap < 0 {
		gap = 0
	}
	out.NoOutputFor = gap
	if out.NoOutputFor >= DefaultStallAfter && out.State != ActivityStalled {
		out.State = ActivityStalled
	}
	return out
}

// lastOutput returns the most recent non-empty line from events. Lines are
// trimmed for the check, and the trimmed text becomes the summary line.
func lastOutput(events []OutputEvent) (string, time.Time, bool) {
	line := ""
	at := time.Time{}
	ok := false
	for _, event := range events {
		text := strings.TrimSpace(event.Line)
		if text == "" {
			continue
		}
		line, at, ok = text, event.At, true
	}
	return line, at, ok
}
