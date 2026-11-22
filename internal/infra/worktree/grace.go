package worktree

import "time"

// DefaultGrace is how long a clean candidate must stay unchanged before a
// second status check allows removal. The window gives an agent time to write
// to a worktree that a scan has just classified as clean.
const DefaultGrace = 30 * time.Second

// Candidate is a worktree a scan first saw clean at ObservedAt. Status is the
// exact porcelain output captured at that sighting.
type Candidate struct {
	Dir        string
	ObservedAt time.Time
	Status     string
}

// GraceOutcome is what a guarded check decided for one candidate.
type GraceOutcome string

const (
	GraceDeferred  GraceOutcome = "deferred"
	GraceChanged   GraceOutcome = "changed"
	GraceStatusErr GraceOutcome = "status_error"
	GraceRemoved   GraceOutcome = "removed"
	GraceRemoveErr GraceOutcome = "remove_error"
)

// Guard holds the grace window and the operations used in the second check.
// The caller supplies Status and Remove so the guard uses the same git Runner
// as collection. Now is replaceable to keep tests free of sleeps.
type Guard struct {
	Grace  time.Duration
	Now    func() time.Time
	Status func(dir string) (string, error)
	Remove func(dir string) error
}

// NewGuard builds a Guard with DefaultGrace and the real clock. The status and
// remove functions come from the caller, which already owns the Runner.
func NewGuard(status func(dir string) (string, error), remove func(dir string) error) Guard {
	return Guard{
		Grace:  DefaultGrace,
		Now:    time.Now,
		Status: status,
		Remove: remove,
	}
}

// Check applies the grace window and the second status check. It calls Remove
// once and only when the window has elapsed and status still matches the first
// sighting. Any change cancels the candidate before Remove runs. A status or
// remove error keeps the candidate and reports the failure as an outcome.
func (g Guard) Check(c Candidate) GraceOutcome {
	now := g.Now
	if now == nil {
		now = time.Now
	}
	if now().Sub(c.ObservedAt) < g.Grace {
		return GraceDeferred
	}
	current, err := g.Status(c.Dir)
	if err != nil {
		return GraceStatusErr
	}
	if current != c.Status {
		return GraceChanged
	}
	if err := g.Remove(c.Dir); err != nil {
		return GraceRemoveErr
	}
	return GraceRemoved
}
