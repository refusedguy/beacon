// Package scheduler holds Beacon's scheduling model. This file defines the
// binding of a scheduled task to the target that receives its wake. The model
// is pure: it reads no clock, store or network. Callers own persistence and
// delivery, and a wake with no binding starts a new session.
package scheduler

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ScheduleType is the trigger mode of a scheduled task. It covers the three
// schedule_task modes: a repeating interval, a fixed local time and a webhook
// mark. The zero value is invalid and fails closed.
type ScheduleType string

const (
	ScheduleInterval ScheduleType = "interval"
	ScheduleFixed    ScheduleType = "fixed"
	ScheduleWebhook  ScheduleType = "webhook"
)

var scheduleTypes = []ScheduleType{ScheduleInterval, ScheduleFixed, ScheduleWebhook}

// Valid reports whether t is a known trigger mode.
func (t ScheduleType) Valid() bool {
	for _, known := range scheduleTypes {
		if t == known {
			return true
		}
	}
	return false
}

// String returns the wire form of the trigger mode.
func (t ScheduleType) String() string { return string(t) }

// ParseScheduleType trims surrounding whitespace and validates an external
// string. An unknown value returns false so a typo cannot become a new mode.
func ParseScheduleType(s string) (ScheduleType, bool) {
	t := ScheduleType(strings.TrimSpace(s))
	if !t.Valid() {
		return "", false
	}
	return t, true
}

// Binding ties one schedule to the target that receives its result. A schedule
// has at most one binding. ScheduleID and TargetID are opaque values owned by
// the caller; TargetID names the task, card or session the wake addresses.
type Binding struct {
	ScheduleID string       `json:"scheduleID"`
	Type       ScheduleType `json:"type"`
	TargetID   string       `json:"bindTarget"`
}

// Validate returns ErrInvalidBinding when a required field is empty or the
// trigger mode is unknown. It performs no I/O.
func (b Binding) Validate() error {
	if strings.TrimSpace(b.ScheduleID) == "" {
		return fmt.Errorf("%w: schedule id is empty", ErrInvalidBinding)
	}
	if !b.Type.Valid() {
		return fmt.Errorf("%w: unknown schedule type %q", ErrInvalidBinding, b.Type)
	}
	if strings.TrimSpace(b.TargetID) == "" {
		return fmt.Errorf("%w: target id is empty", ErrInvalidBinding)
	}
	return nil
}

func (b Binding) normalize() Binding {
	b.ScheduleID = strings.TrimSpace(b.ScheduleID)
	b.TargetID = strings.TrimSpace(b.TargetID)
	return b
}

func (b Binding) same(other Binding) bool {
	return b.ScheduleID == other.ScheduleID &&
		b.Type == other.Type &&
		b.TargetID == other.TargetID
}

var (
	// ErrInvalidBinding is reported for a binding that fails Validate.
	ErrInvalidBinding = errors.New("scheduler binding: invalid binding")
	// ErrBindingConflict is reported when a schedule already points to a
	// different target or trigger mode.
	ErrBindingConflict = errors.New("scheduler binding: conflicting binding")
)

// ConflictError carries both sides of a rejected bind. Callers use errors.Is
// with ErrBindingConflict and read the fields to explain the refusal.
type ConflictError struct {
	ScheduleID string
	Existing   Binding
	Requested  Binding
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("scheduler binding: schedule %q already bound to target %q (%s), cannot bind target %q (%s)",
		e.ScheduleID, e.Existing.TargetID, e.Existing.Type, e.Requested.TargetID, e.Requested.Type)
}

// Is makes errors.Is(err, ErrBindingConflict) true for a ConflictError.
func (e *ConflictError) Is(target error) bool { return target == ErrBindingConflict }

// Bindings is the in-memory set of active bindings keyed by schedule ID. One
// schedule maps to one binding, so a repeated Bind cannot add a duplicate. The
// zero value is ready to use. Callers serialize writes if they share one
// Bindings across goroutines.
type Bindings struct {
	items map[string]Binding
}

// NewBindings returns an empty binding set.
func NewBindings() *Bindings { return &Bindings{items: make(map[string]Binding)} }

// Bind adds binding unless an identical one is already present. It returns
// true for a new binding, false for an identical repeat, and a ConflictError
// when the schedule already points somewhere else. An invalid binding is
// rejected without changing the set.
func (bs *Bindings) Bind(binding Binding) (bool, error) {
	binding = binding.normalize()
	if err := binding.Validate(); err != nil {
		return false, err
	}
	if bs.items == nil {
		bs.items = make(map[string]Binding)
	}
	if existing, ok := bs.items[binding.ScheduleID]; ok {
		if existing.same(binding) {
			return false, nil
		}
		return false, &ConflictError{
			ScheduleID: binding.ScheduleID,
			Existing:   existing,
			Requested:  binding,
		}
	}
	bs.items[binding.ScheduleID] = binding
	return true, nil
}

// Unbind removes the schedule's binding and reports whether one was removed.
// Unbinding an unknown schedule changes nothing.
func (bs *Bindings) Unbind(scheduleID string) bool {
	if _, ok := bs.items[scheduleID]; !ok {
		return false
	}
	delete(bs.items, scheduleID)
	return true
}

// Get returns the binding for scheduleID and whether one exists.
func (bs *Bindings) Get(scheduleID string) (Binding, bool) {
	b, ok := bs.items[scheduleID]
	return b, ok
}

// TargetOf returns the target for scheduleID and whether the schedule is
// bound. A false bound means the wake must create a new session.
func (bs *Bindings) TargetOf(scheduleID string) (string, bool) {
	b, ok := bs.items[scheduleID]
	if !ok {
		return "", false
	}
	return b.TargetID, true
}

// List returns all bindings sorted by schedule ID. The result is a copy, so a
// caller cannot mutate the stored bindings through it.
func (bs *Bindings) List() []Binding {
	out := make([]Binding, 0, len(bs.items))
	for _, b := range bs.items {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ScheduleID < out[j].ScheduleID })
	return out
}

// Len reports the number of active bindings.
func (bs *Bindings) Len() int { return len(bs.items) }
