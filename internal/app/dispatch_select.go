package app

import (
	"github.com/refusedguy/beacon/pkg/core"
)

// dispatchSelection is the result of budgeting and candidate selection. It
// holds no IO state, so both rules can be tested without a launcher.
type dispatchSelection struct {
	Limit   int
	Chosen  []core.Task
	Skipped []SkippedTask
}

// selectDispatchCandidates caps the batch at the free in-flight slots and picks
// non-overlapping candidates within that cap. It does no IO and starts no
// agents. A non-positive cap skips planning entirely.
func selectDispatchCandidates(planner ConflictPlanner, ready []core.Task, active []*core.Run, n, inFlight int, strict bool) (dispatchSelection, error) {
	limit := n
	if free := inFlight - len(active); free < limit {
		limit = free
	}
	sel := dispatchSelection{Limit: limit}
	if limit <= 0 {
		return sel, nil
	}
	chosen, skipped, err := planner.Select(ready, active, limit, strict)
	if err != nil {
		return dispatchSelection{}, err
	}
	sel.Chosen = chosen
	sel.Skipped = skipped
	return sel, nil
}
