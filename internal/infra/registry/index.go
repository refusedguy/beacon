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
	"strings"
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

// ===== ACP agent registry (T3.285) =====
// The capability index above answers "which executor declares capability X".
// An ACP host needs the reverse view: given a capability or an address, which
// registered agent should receive a wake. This section adds that view as a
// thin layer over CapabilityIndex. It stores no protocol state, so the ACP
// server and transports in wake stay untouched here.

// ACPAgent is one agent an ACP host can discover. Name identifies the agent,
// Address is the URL or command the host uses to reach it, and Capabilities are
// the declared capability names used for lookup.
type ACPAgent struct {
	Name         string
	Capabilities []string
	Address      string
}

// ACPAgentRegistry registers ACP agents and answers lookup by capability and
// by address. It reuses CapabilityIndex for capability membership, so the same
// canonical rules apply: names are sorted, duplicates are dropped and empty
// names are rejected. The zero value is not ready for use; create one with
// NewACPAgentRegistry.
type ACPAgentRegistry struct {
	index     *CapabilityIndex
	byName    map[string]ACPAgent
	byAddress map[string]string
}

// NewACPAgentRegistry returns an empty ACP agent registry.
func NewACPAgentRegistry() *ACPAgentRegistry {
	return &ACPAgentRegistry{
		index:     NewCapabilityIndex(),
		byName:    make(map[string]ACPAgent),
		byAddress: make(map[string]string),
	}
}

// Register adds agent to the registry. Name and address are required and are
// trimmed before storage. Re-registering a name replaces its capability set and
// address. If address already belongs to another name, that older registration
// is removed, so one address always maps to one agent.
func (r *ACPAgentRegistry) Register(agent ACPAgent) error {
	if r == nil {
		return fmt.Errorf("registry: acp: nil registry")
	}
	name := strings.TrimSpace(agent.Name)
	if name == "" {
		return fmt.Errorf("registry: acp: empty agent name")
	}
	address := strings.TrimSpace(agent.Address)
	if address == "" {
		return fmt.Errorf("registry: acp: empty address for agent %q", name)
	}
	if owner, ok := r.byAddress[address]; ok && owner != name {
		r.unregister(owner)
	}
	if old, ok := r.byName[name]; ok {
		delete(r.byAddress, old.Address)
		r.index.Remove(name)
	}
	record := &ExecutorRecord{ID: name, Capabilities: agent.Capabilities}
	if err := r.index.Upsert(record); err != nil {
		return err
	}
	agent.Name = name
	agent.Address = address
	agent.Capabilities = append([]string(nil), record.AgentCapabilities...)
	r.byName[name] = agent
	r.byAddress[address] = name
	return nil
}

// unregister drops one agent from the name, address and capability views.
func (r *ACPAgentRegistry) unregister(name string) {
	old, ok := r.byName[name]
	if !ok {
		return
	}
	delete(r.byAddress, old.Address)
	delete(r.byName, name)
	r.index.Remove(name)
}

// FindByCapability returns the agents declaring capability, sorted by name.
// An unknown capability returns an empty slice.
func (r *ACPAgentRegistry) FindByCapability(capability string) []ACPAgent {
	names := r.index.Executors(capability)
	out := make([]ACPAgent, 0, len(names))
	for _, name := range names {
		if agent, ok := r.byName[name]; ok {
			out = append(out, copyACPAgent(agent))
		}
	}
	return out
}

// FindByAddress returns the agent registered at address.
func (r *ACPAgentRegistry) FindByAddress(address string) (ACPAgent, bool) {
	name, ok := r.byAddress[strings.TrimSpace(address)]
	if !ok {
		return ACPAgent{}, false
	}
	agent, ok := r.byName[name]
	if !ok {
		return ACPAgent{}, false
	}
	return copyACPAgent(agent), true
}

// List returns every registered agent sorted by name.
func (r *ACPAgentRegistry) List() []ACPAgent {
	names := make([]string, 0, len(r.byName))
	for name := range r.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ACPAgent, 0, len(names))
	for _, name := range names {
		out = append(out, copyACPAgent(r.byName[name]))
	}
	return out
}

func copyACPAgent(agent ACPAgent) ACPAgent {
	agent.Capabilities = append([]string(nil), agent.Capabilities...)
	return agent
}
