// Package registry builds the agent registry configuration and its derived
// views. This file owns the capability index (T5.23, ADR 0089).
//
// The index is derived from the roster of registered executors. It is rebuilt
// on every upsert and is never authored by hand in YAML. Its key is a declared
// capability and its value is an opaque executor identifier. The index keeps no
// session name: a name is a delivery key at the boundary and plays no part in
// selecting a recipient.
package registry

import (
	"fmt"
	"sort"
)

// ExecutorRecord is one registered executor as the index sees it. ID is the
// opaque executor identifier stored as an index value. Capabilities is the
// declaration read from the roster. AgentCapabilities is the derived copy that
// an upsert fills from Capabilities; callers must not author it by hand.
type ExecutorRecord struct {
	ID                string
	Capabilities      []string
	AgentCapabilities []string
}

// CapabilityIndex maps a capability to the opaque IDs of executors that
// declare it. The zero value is not ready for use; create one with
// NewCapabilityIndex.
type CapabilityIndex struct {
	byCapability map[string]map[string]struct{}
}

// NewCapabilityIndex returns an empty capability index.
func NewCapabilityIndex() *CapabilityIndex {
	return &CapabilityIndex{byCapability: make(map[string]map[string]struct{})}
}

// Upsert inserts or replaces one executor's declarations. It first drops every
// entry for the record's ID, then fills AgentCapabilities from a canonical copy
// of Capabilities and indexes that copy under each capability. Replacing rather
// than appending is what keeps a changed capability set from leaving stale
// entries behind. An empty ID or an empty declared capability is rejected with
// an error naming the field and the executor.
func (idx *CapabilityIndex) Upsert(record *ExecutorRecord) error {
	if record.ID == "" {
		return fmt.Errorf("registry: capability index: id of executor is empty")
	}
	declared, err := canonicalCapabilities(record.Capabilities, record.ID)
	if err != nil {
		return err
	}

	idx.remove(record.ID)
	record.AgentCapabilities = declared
	for _, capability := range declared {
		members, ok := idx.byCapability[capability]
		if !ok {
			members = make(map[string]struct{})
			idx.byCapability[capability] = members
		}
		members[record.ID] = struct{}{}
	}
	return nil
}

// Remove drops one executor from every capability set. Capability keys with no
// remaining members are deleted, so the index never holds an empty entry. An
// ID that is not indexed is a no-op.
func (idx *CapabilityIndex) Remove(id string) {
	idx.remove(id)
}

// remove is the internal form of Remove used while replacing a record.
func (idx *CapabilityIndex) remove(id string) {
	for capability, members := range idx.byCapability {
		delete(members, id)
		if len(members) == 0 {
			delete(idx.byCapability, capability)
		}
	}
}

// Executors returns the sorted opaque IDs of executors declaring capability.
// An unknown capability returns an empty slice.
func (idx *CapabilityIndex) Executors(capability string) []string {
	members := idx.byCapability[capability]
	ids := make([]string, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Capabilities returns the sorted names of capabilities that have at least
// one executor.
func (idx *CapabilityIndex) Capabilities() []string {
	names := make([]string, 0, len(idx.byCapability))
	for capability := range idx.byCapability {
		names = append(names, capability)
	}
	sort.Strings(names)
	return names
}

// BuildCapabilityIndex builds a fresh index from the current roster. Every
// record is upserted in turn, so two records with the same ID resolve to the
// last declaration and an executor missing from the roster has no entry. The
// returned index reflects exactly the roster passed in.
func BuildCapabilityIndex(roster []ExecutorRecord) (*CapabilityIndex, error) {
	index := NewCapabilityIndex()
	for i := range roster {
		if err := index.Upsert(&roster[i]); err != nil {
			return nil, err
		}
	}
	return index, nil
}

// canonicalCapabilities returns a sorted, duplicate-free copy of the declared
// capabilities. Copying means later edits to the caller's slice cannot change
// the index or the derived AgentCapabilities field. An empty declaration has no
// place in the index and is rejected with the executor ID in the message.
func canonicalCapabilities(declared []string, executorID string) ([]string, error) {
	seen := make(map[string]bool, len(declared))
	out := make([]string, 0, len(declared))
	for _, capability := range declared {
		if capability == "" {
			return nil, fmt.Errorf("registry: capability index: capability of executor %q is empty", executorID)
		}
		if seen[capability] {
			continue
		}
		seen[capability] = true
		out = append(out, capability)
	}
	sort.Strings(out)
	return out, nil
}
