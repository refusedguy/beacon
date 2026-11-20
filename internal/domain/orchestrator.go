// Package domain holds the Beacon entities, status enums and port interfaces.
// This file adds the Orchestrator entity: the domain-level identity of one
// orchestrator session. It is deliberately separate from Watch (ADR 0157) and
// references Watches only by ID, so Watch stays the unit of work and the
// orchestrator stays a distinct aggregate that points at Watches.
package domain

import (
	"strings"
	"time"
)

// DefaultSlotsTotal is the concurrency budget used when a caller has no
// explicit configuration value. It mirrors the registry default
// max_concurrent_agents, but the domain never reads the registry: the caller
// passes the configured value into NewOrchestrator.
const DefaultSlotsTotal = 4

// Orchestrator is the domain identity of one orchestrator session. ID is the
// opaque session ID carried in _meta["ai.opencode/sessionID"] and ProjectKey
// scopes the entity to one Beacon project. Role is kept for parity with the
// subscriber role, but the entity itself may only be an orchestrator.
//
// Subscriptions holds watch IDs, never Watches. SlotsUsed is maintained by
// Register and Unregister and always equals len(Subscriptions). Callers that
// mutate Subscriptions directly break that invariant and own the consequence.
type Orchestrator struct {
	ID            string    `json:"id"`
	Role          Role      `json:"role"`
	ProjectKey    string    `json:"projectKey"`
	SlotsTotal    int       `json:"slotsTotal"`
	SlotsUsed     int       `json:"slotsUsed"`
	Subscriptions []string  `json:"subscriptions"`
	RegisteredAt  time.Time `json:"registeredAt"`
}

// NewOrchestrator builds an Orchestrator for one session. id and projectKey
// are stored verbatim: they are opaque values, so only blankness is checked.
// An empty role means RoleOrchestrator; any other role is rejected so the
// entity cannot masquerade as an executor. A negative slotsTotal is
// INVALID_INPUT, while zero is a valid "no slots" configuration. A nil clock
// falls back to the wall clock, which matches NewFanout.
func NewOrchestrator(id string, role Role, projectKey string, slotsTotal int, clock Clock) (Orchestrator, error) {
	if strings.TrimSpace(id) == "" {
		return Orchestrator{}, NewAppError(CodeInvalidInput, "orchestrator: пустое поле id")
	}
	if strings.TrimSpace(projectKey) == "" {
		return Orchestrator{}, NewAppError(CodeInvalidInput, "orchestrator: пустое поле projectKey")
	}
	if role == "" {
		role = RoleOrchestrator
	}
	if role != RoleOrchestrator {
		return Orchestrator{}, NewAppError(CodeInvalidInput, "orchestrator: роль должна быть orchestrator")
	}
	if slotsTotal < 0 {
		return Orchestrator{}, NewAppError(CodeInvalidInput, "orchestrator: slotsTotal отрицательный")
	}
	if clock == nil {
		clock = wallClock{}
	}
	return Orchestrator{
		ID:            id,
		Role:          role,
		ProjectKey:    projectKey,
		SlotsTotal:    slotsTotal,
		Subscriptions: []string{},
		RegisteredAt:  clock.Now(),
	}, nil
}

// Key returns a stable identity that includes the project. Two sessions with
// the same ID in different projects never collapse into one orchestrator. The
// components are escaped with the same injective encoding as ObligationKey,
// so a separator inside either value cannot forge another key.
func (o Orchestrator) Key() string {
	return obligationKeyEscaper.Replace(o.ProjectKey) + ":" + obligationKeyEscaper.Replace(o.ID)
}

// CanAcceptTask reports whether the orchestrator has a free execution slot.
// A non-positive budget means no capacity at all, so zero slots never accept
// work. The limit comes from configuration via SlotsTotal, not from a field
// inside Watch.
func (o Orchestrator) CanAcceptTask() bool {
	return o.SlotsTotal > 0 && o.SlotsUsed < o.SlotsTotal
}

// Register subscribes the orchestrator to a Watch ID. It is idempotent: an
// already subscribed ID is a no-op and does not take another slot. A blank
// watchID is INVALID_INPUT; a full budget is LIMIT_EXCEEDED. Register never
// loads or changes the Watch itself.
func (o *Orchestrator) Register(watchID string) error {
	if o == nil {
		return NewAppError(CodePersistFailed, "orchestrator: сущность nil")
	}
	if strings.TrimSpace(watchID) == "" {
		return NewAppError(CodeInvalidInput, "orchestrator: пустой watchID")
	}
	if o.HasSubscription(watchID) {
		return nil
	}
	if !o.CanAcceptTask() {
		return NewAppError(CodeLimitExceeded, "orchestrator: нет свободных слотов")
	}
	o.Subscriptions = append(o.Subscriptions, watchID)
	o.SlotsUsed++
	return nil
}

// Unregister drops a watch ID and frees its slot. It is idempotent: false
// means the ID was not subscribed and nothing changed, including an empty ID.
func (o *Orchestrator) Unregister(watchID string) bool {
	if o == nil || watchID == "" {
		return false
	}
	for i, id := range o.Subscriptions {
		if id == watchID {
			o.Subscriptions = append(o.Subscriptions[:i], o.Subscriptions[i+1:]...)
			if o.SlotsUsed > 0 {
				o.SlotsUsed--
			}
			return true
		}
	}
	return false
}

// HasSubscription reports whether watchID is already registered.
func (o Orchestrator) HasSubscription(watchID string) bool {
	for _, id := range o.Subscriptions {
		if id == watchID {
			return true
		}
	}
	return false
}

// Workload is the count of subscribed Watches in the four states the daemon
// reports. Other verdicts are ignored, so Total counts only these buckets.
type Workload struct {
	Ready    int `json:"ready"`
	Retry    int `json:"retry"`
	Blocked  int `json:"blocked"`
	Watching int `json:"watching"`
}

// Total is the number of subscribed Watches counted in Workload.
func (w Workload) Total() int {
	return w.Ready + w.Retry + w.Blocked + w.Watching
}

// Workload projects the subscribed watch IDs through watches and counts the
// four reported states. A Watch is classified by verdict; blocked is also
// recognised by status so a blocked snapshot is counted even if its verdict
// has not been normalised yet. IDs missing from watches and verdicts outside
// the four buckets are skipped, because the orchestrator holds only IDs and
// cannot invent a state for a Watch it cannot resolve.
func (o Orchestrator) Workload(watches map[string]Watch) Workload {
	var out Workload
	for _, id := range o.Subscriptions {
		w, ok := watches[id]
		if !ok {
			continue
		}
		switch {
		case w.Verdict == VerdictBlocked || w.Status == StatusBlocked:
			out.Blocked++
		case w.Verdict == VerdictReady:
			out.Ready++
		case w.Verdict == VerdictRetry:
			out.Retry++
		case w.Verdict == VerdictWatching:
			out.Watching++
		}
	}
	return out
}
