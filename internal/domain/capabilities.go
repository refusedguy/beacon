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
