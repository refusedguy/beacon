// Package domain holds the Beacon entities, status enums and port interfaces.
//
// This file is the canonical dictionary for the three axes of the agent
// registry (T5.20, ADR 0089, ADR 0086): role, runtime and capability. The
// strings below are the only ones an author file may declare. A value outside
// these sets is rejected at parse time instead of becoming a random name that
// silently breaks capability matching. The dictionary lives in code so it can
// only be extended by a reviewed change backed by an ADR, never by a config
// edit or a model's guess.
package domain

import (
	"fmt"
	"strings"
)

// Role is the subscriber role on a Watch. The canonical values and their
// constants live in types.go; this dictionary adds validation so an author
// file cannot declare an unknown role. RoleFixer is a first-class role: a
// fixer repairs a blocked Watch and is a valid wake addressee.
func (r Role) Valid() bool {
	switch r {
	case RoleExecutor, RoleOrchestrator, RoleFixer:
		return true
	default:
		return false
	}
}

// ParseRole converts an external role string into a Role. Surrounding
// whitespace is trimmed, but the canonical spelling is otherwise exact. An
// unknown value is rejected so it cannot pass as a declared role.
func ParseRole(s string) (Role, bool) {
	r := Role(strings.TrimSpace(s))
	if !r.Valid() {
		return "", false
	}
	return r, true
}

var roleValues = []Role{RoleExecutor, RoleOrchestrator, RoleFixer}

// ValidRoles returns the canonical roles in declaration order. The returned
// slice is a copy, so a caller cannot mutate the dictionary.
func ValidRoles() []Role {
	out := make([]Role, len(roleValues))
	copy(out, roleValues)
	return out
}

// Seat is the stable identity of a role slot. It is canonical address text of
// the form "member@rig". A seat keeps its identity when the agent behind it
// restarts or changes session, so it can name an obligation owner without
// binding the owner to a run id. Seat is a value type: equal text yields equal
// values and a shared map key. The zero value is invalid; get a seat from
// ResolveAddress.
type Seat string

// String returns the canonical seat identity text.
func (s Seat) String() string { return string(s) }

// Valid reports whether s is canonical "member@rig" seat text.
func (s Seat) Valid() bool { return validSeatText(string(s)) }

// Runtime is the session mode an agent entry runs in.
type Runtime string

const (
	// RuntimeSubagent is a child session spawned for one task.
	RuntimeSubagent Runtime = "subagent"
	// RuntimePrimary is the long-lived session that owns the task.
	RuntimePrimary Runtime = "primary"
)

var runtimeValues = []Runtime{RuntimeSubagent, RuntimePrimary}

// Valid reports whether r is one of the two canonical runtimes. The zero value
// and any unknown string fail closed.
func (r Runtime) Valid() bool {
	switch r {
	case RuntimeSubagent, RuntimePrimary:
		return true
	default:
		return false
	}
}

// ParseRuntime converts an external runtime string into a Runtime. Surrounding
// whitespace is trimmed; an unknown value is rejected.
func ParseRuntime(s string) (Runtime, bool) {
	r := Runtime(strings.TrimSpace(s))
	if !r.Valid() {
		return "", false
	}
	return r, true
}

// ValidRuntimes returns the canonical runtimes in declaration order. The
// returned slice is a copy, so a caller cannot mutate the dictionary.
func ValidRuntimes() []Runtime {
	out := make([]Runtime, len(runtimeValues))
	copy(out, runtimeValues)
	return out
}

// Capability is one named ability an agent may declare in the roster. The
// dictionary is closed: a capability outside these eight values is an error,
// never a new silent name. Extending the set requires an ADR and a code change.
type Capability string

const (
	// CapabilityWatch subscribes to Watch results.
	CapabilityWatch Capability = "watch"
	// CapabilityReadVerdict reads a verdict through beacon_get_result.
	CapabilityReadVerdict Capability = "read-verdict"
	// CapabilityCIRetry re-runs failed CI checks.
	CapabilityCIRetry Capability = "ci-retry"
	// CapabilityRebase rebases the branch after a merge-state blocker.
	CapabilityRebase Capability = "rebase"
	// CapabilityReview performs the independent review gate.
	CapabilityReview Capability = "review"
	// CapabilityMerge merges a PR after a ready verdict.
	CapabilityMerge Capability = "merge"
	// CapabilityAck claims an obligation by recording an ack.
	CapabilityAck Capability = "ack"
	// CapabilitySpawn starts child agents for delegated work.
	CapabilitySpawn Capability = "spawn"
)

var capabilityValues = []Capability{
	CapabilityWatch,
	CapabilityReadVerdict,
	CapabilityCIRetry,
	CapabilityRebase,
	CapabilityReview,
	CapabilityMerge,
	CapabilityAck,
	CapabilitySpawn,
}

// Valid reports whether c is one of the canonical capabilities. The zero value
// and any unknown string fail closed.
func (c Capability) Valid() bool {
	for _, known := range capabilityValues {
		if c == known {
			return true
		}
	}
	return false
}

// ParseCapability converts an external capability string into a Capability.
// Surrounding whitespace is trimmed, but the canonical spelling is otherwise
// exact. An unknown value is rejected so it cannot become an arbitrary name.
func ParseCapability(s string) (Capability, bool) {
	c := Capability(strings.TrimSpace(s))
	if !c.Valid() {
		return "", false
	}
	return c, true
}

// ValidCapabilities returns the canonical capabilities in declaration order.
// The returned slice is a copy, so a caller cannot mutate the dictionary.
func ValidCapabilities() []Capability {
	out := make([]Capability, len(capabilityValues))
	copy(out, capabilityValues)
	return out
}

// ParseCapabilities converts a list of external capability strings. It keeps
// the caller's order, trims each entry and returns an INVALID_INPUT AppError
// naming the first unknown value. An empty list is valid and yields an empty,
// non-nil slice.
func ParseCapabilities(values []string) ([]Capability, error) {
	out := make([]Capability, 0, len(values))
	for _, raw := range values {
		c, ok := ParseCapability(raw)
		if !ok {
			return nil, NewAppError(CodeInvalidInput,
				fmt.Sprintf("capabilities: неизвестная capability %q", raw))
		}
		out = append(out, c)
	}
	return out, nil
}

// Scope is one independent right a scope token may carry (T3.235). The three
// rights are deliberately separate: a token that can read status cannot wake a
// worker or stop an obligation unless that right was granted at issue time.
// Scopes extend neither Role nor Capability; those dictionaries stay closed.
type Scope string

const (
	// ScopeWake lets a holder wake a worker or accept an obligation.
	ScopeWake Scope = "wake"
	// ScopeRead lets a holder read status and verdicts.
	ScopeRead Scope = "read"
	// ScopeStop lets a holder stop or revoke an obligation.
	ScopeStop Scope = "stop"
)

var scopeValues = []Scope{ScopeWake, ScopeRead, ScopeStop}

// Valid reports whether s is one of the canonical scopes. The zero value and
// any unknown string fail closed.
func (s Scope) Valid() bool {
	for _, known := range scopeValues {
		if s == known {
			return true
		}
	}
	return false
}

// ParseScope converts an external scope string into a Scope. Whitespace is
// trimmed; an unknown value is rejected so a misspelled right cannot silently
// become an empty grant.
func ParseScope(s string) (Scope, bool) {
	sc := Scope(strings.TrimSpace(s))
	if !sc.Valid() {
		return "", false
	}
	return sc, true
}

// ValidScopes returns the canonical scopes in declaration order as a copy.
func ValidScopes() []Scope {
	out := make([]Scope, len(scopeValues))
	copy(out, scopeValues)
	return out
}

// ScopeMask is a set of Scope rights encoded as bits. It is the privilege
// ceiling of a scope token: the mask records the maximum rights the token may
// ever exercise, and an authorization check can only test membership in that
// set, never widen it. The zero value grants nothing.
type ScopeMask uint8

const (
	ScopeFlagWake ScopeMask = 1 << iota
	ScopeFlagRead
	ScopeFlagStop

	scopeFlagAll = ScopeFlagWake | ScopeFlagRead | ScopeFlagStop
)

// Flag returns the single bit representing s. An unknown scope has no bit and
// is therefore never granted.
func (s Scope) Flag() ScopeMask {
	switch s {
	case ScopeWake:
		return ScopeFlagWake
	case ScopeRead:
		return ScopeFlagRead
	case ScopeStop:
		return ScopeFlagStop
	default:
		return 0
	}
}

// Allows reports whether the ceiling m grants scope s. Unknown scopes are
// never granted, so a typo in a handler cannot widen access.
func (m ScopeMask) Allows(s Scope) bool {
	flag := s.Flag()
	return flag != 0 && m&flag == flag
}

// Valid reports whether m contains only known scope bits.
func (m ScopeMask) Valid() bool {
	return m&^scopeFlagAll == 0
}

// ParseScopes builds a ceiling mask from a list of external scope strings. It
// treats the list as a set, so order and duplicates do not matter, trims each
// entry and returns an INVALID_INPUT AppError naming the first unknown value.
// An empty list yields the zero mask that grants nothing.
func ParseScopes(values []string) (ScopeMask, error) {
	var m ScopeMask
	for _, raw := range values {
		s, ok := ParseScope(raw)
		if !ok {
			return 0, NewAppError(CodeInvalidInput,
				fmt.Sprintf("scope: неизвестное право %q", raw))
		}
		m |= s.Flag()
	}
	return m, nil
}
