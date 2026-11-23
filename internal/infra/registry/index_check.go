// Package registry checks the derived capability index against the roster that
// produced it (T5.24, ADR 0089). The index is derived state: the core builds it
// from registered executors and never stores it in the author file by hand.
// This file verifies both directions so a stale or empty index fails before
// the core starts choosing recipients by capability.
package registry

import (
	"fmt"
	"maps"
	"slices"
	"sort"

	"github.com/refusedguy/beacon/internal/domain"
)

// CheckCapabilityIndex reconciles roster declarations with a derived index.
//
// declarations maps a known executor identity to the capabilities declared in
// the roster "capabilities" field. index maps a capability to the opaque
// executor identities recorded in "agent_capabilities". The identity in the
// index is opaque to the core: it is not a session name and plays no part in
// delivery (ADR 0089).
//
// Two directions are checked. Every declared capability must have an entry for
// its executor in the index, and every index entry must name a known executor
// that declares that capability. A reference to a non-existent executor is
// rejected: an index entry pointing at an unknown agent is a configuration
// error, never a silently ignored extra. An empty index over a non-empty roster
// is also a configuration error. That is the failure Overstory saw after init
// and it must not pass silently.
//
// Errors are *domain.AppError with CodeInvalidInput. The message names the
// field ("capabilities" or "agent_capabilities") and the executor, so the
// author can fix the roster or rebuild the index. Reporting is sorted, so the
// same inputs always name the same field and agent.
func CheckCapabilityIndex(declarations, index map[string][]string) error {
	agents := checkSortedKeys(declarations)
	if len(agents) > 0 && checkIndexEntryCount(index) == 0 {
		return checkIndexError("agent_capabilities", "empty index with a non-empty roster", agents[0])
	}

	for _, agent := range agents {
		for _, capability := range checkSortedUnique(declarations[agent]) {
			members, ok := index[capability]
			if !ok || !checkContains(members, agent) {
				return checkIndexError("agent_capabilities", fmt.Sprintf("capability %q has no entry for", capability), agent)
			}
		}
	}

	for _, capability := range checkSortedKeys(index) {
		for _, agent := range checkSortedUnique(index[capability]) {
			declared, known := declarations[agent]
			if !known {
				return checkIndexError("agent_capabilities", fmt.Sprintf("capability %q references a non-existent agent", capability), agent)
			}
			if !checkContains(declared, capability) {
				return checkIndexError("capabilities", fmt.Sprintf("does not declare capability %q", capability), agent)
			}
		}
	}
	return nil
}

// checkIndexError builds a configuration error that names the offending field
// and executor. Empty field values are not possible here: every caller passes
// one of the two index fields and a non-empty executor identity.
func checkIndexError(field, reason, agent string) error {
	message := fmt.Sprintf("%s: %s, agent %q", field, reason, agent)
	return domain.NewAppError(domain.CodeInvalidInput, message)
}

// checkIndexEntryCount counts the actual index entries. A map with capability
// keys and empty member lists has no entries and counts as an empty index.
func checkIndexEntryCount(index map[string][]string) int {
	count := 0
	for _, members := range index {
		count += len(members)
	}
	return count
}

// checkSortedKeys returns the keys of set in byte order, so a check never
// depends on map iteration order.
func checkSortedKeys(set map[string][]string) []string {
	keys := slices.Sorted(maps.Keys(set))
	return keys
}

// checkSortedUnique returns values in byte order without duplicates. Repeated
// entries in a roster or index do not change the check.
func checkSortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func checkContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
