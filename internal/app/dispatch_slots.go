package app

import (
	"context"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// slotReservation is one committed start. agent is set once the start holds an
// agent slot. A run still waiting for a slot reserves only an in-flight slot.
type slotReservation struct {
	runID  core.RunID
	taskID core.TaskID
	scope  []core.ScopePath
	agent  bool
}

// withReserved appends a virtual run for every committed start to a store
// active list. An agent reservation appears as a running run so the in-flight
// cap and the agent cap both count it. The caller holds slotsMu.
func (d *Dispatcher) withReserved(active []*core.Run) []*core.Run {
	if len(d.reserved) == 0 {
		return active
	}
	view := make([]*core.Run, 0, len(active)+len(d.reserved))
	view = append(view, active...)
	for _, res := range d.reserved {
		r := core.NewRun(res.runID, res.taskID, time.Time{})
		r.Scope = res.scope
		if res.agent {
			r.State = core.RunRunning
			r.HoldAgent()
		}
		view = append(view, r)
	}
	return view
}

// ReserveForWake checks and reserves one slot for runID inside a single
// slotsMu section. Two concurrent wakes cannot both pass the same free-slot
// check. The caller releases the reservation with ReleaseWake once the wake or
// the injected spawn has landed a run row.
func (d *Dispatcher) ReserveForWake(ctx context.Context, runID core.RunID) bool {
	d.slotsMu.Lock()
	defer d.slotsMu.Unlock()
	active, err := d.Store.ListActive(ctx)
	if err != nil {
		return false
	}
	if d.availableFromExcept(d.withReserved(active), runID) <= 0 {
		return false
	}
	if d.reserved == nil {
		d.reserved = make(map[core.RunID]*slotReservation)
	}
	if _, held := d.reserved[runID]; !held {
		d.reserved[runID] = &slotReservation{runID: runID, agent: true}
	}
	return true
}

// ReleaseWake frees a reservation taken by ReserveForWake. Dropping an ID that
// was already released is a no-op, so a caller can defer it safely.
func (d *Dispatcher) ReleaseWake(_ context.Context, runID core.RunID) {
	d.dropReservation(runID)
}

// availableFrom applies the agent-slot rule to a list the caller already has.
// Dispatch derives its budget from the same active view used for selection
// instead of calling Available, which would read the store a second time.
func (d *Dispatcher) availableFrom(active []*core.Run) int {
	return d.availableFromExcept(active, "")
}

// availableFromExcept is availableFrom with one run omitted from the used
// count. An empty except keeps the ordinary whole-view count.
func (d *Dispatcher) availableFromExcept(active []*core.Run, except core.RunID) int {
	limit := d.Concurrency.MaxActiveAgents
	if limit <= 0 {
		limit = d.inFlightLimit()
	}
	used := 0
	for _, r := range active {
		if r == nil || (except != "" && r.ID == except) {
			continue
		}
		// A persisted session flag keeps a woken run in waiting_ci counted
		// while its worker is still alive. The state check covers rows written
		// before the flag existed and in-flight reservations.
		if r.HoldsAgent() || r.State.ActiveAgent() {
			used++
		}
	}
	free := limit - used
	if free < 0 {
		return 0
	}
	return free
}
