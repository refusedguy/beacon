package registry

import (
	"reflect"
	"strings"
	"testing"
)

// TestUpsertFillsAgentCapabilities proves the derived field is filled from the
// roster declaration, not written by hand. The copy is canonical: duplicates
// are dropped and the names are sorted so the same roster yields the same
// derived value.
func TestUpsertFillsAgentCapabilities(t *testing.T) {
	record := &ExecutorRecord{
		ID:           "exec-1",
		Capabilities: []string{"watch", "read-verdict", "watch"},
	}
	index := NewCapabilityIndex()
	if err := index.Upsert(record); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	want := []string{"read-verdict", "watch"}
	if !reflect.DeepEqual(record.AgentCapabilities, want) {
		t.Fatalf("AgentCapabilities = %v, want %v", record.AgentCapabilities, want)
	}
}

// TestUpsertIndexesEveryDeclaration proves each declared capability maps to
// the opaque executor ID, and that names never appear as index values.
func TestUpsertIndexesEveryDeclaration(t *testing.T) {
	record := &ExecutorRecord{ID: "exec-1", Capabilities: []string{"watch", "read-verdict"}}
	index := NewCapabilityIndex()
	if err := index.Upsert(record); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	for _, capability := range []string{"watch", "read-verdict"} {
		got := index.Executors(capability)
		if want := []string{"exec-1"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Executors(%q) = %v, want %v", capability, got, want)
		}
	}
	if got := index.Capabilities(); !reflect.DeepEqual(got, []string{"read-verdict", "watch"}) {
		t.Fatalf("Capabilities = %v", got)
	}
}

// TestUpsertReplacesStaleCapabilities covers the central failure of a derived
// index: after a capability is removed from a record, that executor must no
// longer be found under the old capability.
func TestUpsertReplacesStaleCapabilities(t *testing.T) {
	record := &ExecutorRecord{ID: "exec-1", Capabilities: []string{"watch", "merge"}}
	index := NewCapabilityIndex()
	if err := index.Upsert(record); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	record.Capabilities = []string{"review"}
	if err := index.Upsert(record); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	if got := index.Executors("watch"); len(got) != 0 {
		t.Errorf("stale watch entry: %v", got)
	}
	if got := index.Executors("merge"); len(got) != 0 {
		t.Errorf("stale merge entry: %v", got)
	}
	if got := index.Executors("review"); !reflect.DeepEqual(got, []string{"exec-1"}) {
		t.Errorf("Executors(review) = %v", got)
	}
	if got := record.AgentCapabilities; !reflect.DeepEqual(got, []string{"review"}) {
		t.Errorf("AgentCapabilities = %v, want [review]", got)
	}
}

// TestRemoveDropsExecutorFromIndex proves an executor removed from the roster
// leaves the index, while every other executor stays indexed.
func TestRemoveDropsExecutorFromIndex(t *testing.T) {
	index := NewCapabilityIndex()
	for _, record := range []ExecutorRecord{
		{ID: "exec-1", Capabilities: []string{"watch", "review"}},
		{ID: "exec-2", Capabilities: []string{"watch"}},
	} {
		if err := index.Upsert(&record); err != nil {
			t.Fatalf("Upsert(%s): %v", record.ID, err)
		}
	}

	index.Remove("exec-1")
	if got := index.Executors("watch"); !reflect.DeepEqual(got, []string{"exec-2"}) {
		t.Errorf("Executors(watch) = %v, want [exec-2]", got)
	}
	if got := index.Executors("review"); len(got) != 0 {
		t.Errorf("review key survived its last executor: %v", got)
	}
	if got := index.Capabilities(); !reflect.DeepEqual(got, []string{"watch"}) {
		t.Errorf("Capabilities = %v, want [watch]", got)
	}
	// Removing an unknown executor is harmless.
	index.Remove("exec-1")
	if got := index.Executors("watch"); !reflect.DeepEqual(got, []string{"exec-2"}) {
		t.Errorf("repeat Remove changed index: %v", got)
	}
}

// TestBuildCapabilityIndexReflectsRoster proves the index is rebuilt from the
// roster passed in: an executor dropped from the roster has no entry, and one
// added to it appears under its capabilities.
func TestBuildCapabilityIndexReflectsRoster(t *testing.T) {
	roster := []ExecutorRecord{
		{ID: "exec-a", Capabilities: []string{"watch", "read-verdict"}},
		{ID: "exec-b", Capabilities: []string{"watch"}},
	}
	index, err := BuildCapabilityIndex(roster)
	if err != nil {
		t.Fatalf("BuildCapabilityIndex: %v", err)
	}
	if got := index.Executors("watch"); !reflect.DeepEqual(got, []string{"exec-a", "exec-b"}) {
		t.Fatalf("Executors(watch) = %v, want [exec-a exec-b]", got)
	}
	if got := roster[0].AgentCapabilities; !reflect.DeepEqual(got, []string{"read-verdict", "watch"}) {
		t.Fatalf("AgentCapabilities = %v", got)
	}

	// exec-b leaves the roster; the fresh index must not mention it.
	roster = roster[:1]
	rebuilt, err := BuildCapabilityIndex(roster)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if got := rebuilt.Executors("watch"); !reflect.DeepEqual(got, []string{"exec-a"}) {
		t.Fatalf("Executors(watch) after rebuild = %v, want [exec-a]", got)
	}
}

// TestUpsertDoesNotAliasCallerSlice proves the index and the derived field own
// their copy of the declarations. Editing the caller's slice afterwards cannot
// silently change the index.
func TestUpsertDoesNotAliasCallerSlice(t *testing.T) {
	declared := []string{"watch"}
	record := &ExecutorRecord{ID: "exec-1", Capabilities: declared}
	index := NewCapabilityIndex()
	if err := index.Upsert(record); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	declared[0] = "merge"
	record.Capabilities[0] = "merge"
	if got := index.Executors("watch"); !reflect.DeepEqual(got, []string{"exec-1"}) {
		t.Errorf("index aliased caller slice, watch = %v", got)
	}
	if got := record.AgentCapabilities; !reflect.DeepEqual(got, []string{"watch"}) {
		t.Errorf("AgentCapabilities aliased caller slice: %v", got)
	}
}

// TestUpsertRejectsEmptyFields proves malformed declarations fail at the
// boundary with a message naming both the field and the executor.
func TestUpsertRejectsEmptyFields(t *testing.T) {
	tests := []struct {
		name   string
		record ExecutorRecord
		field  string
	}{
		{
			name:   "empty id",
			record: ExecutorRecord{Capabilities: []string{"watch"}},
			field:  "id",
		},
		{
			name:   "empty capability",
			record: ExecutorRecord{ID: "exec-1", Capabilities: []string{"watch", ""}},
			field:  "capability",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index := NewCapabilityIndex()
			record := tt.record
			err := index.Upsert(&record)
			if err == nil {
				t.Fatal("Upsert accepted a malformed declaration")
			}
			if !strings.Contains(err.Error(), tt.field) {
				t.Errorf("error %q does not name field %q", err, tt.field)
			}
			if tt.field == "capability" && !strings.Contains(err.Error(), record.ID) {
				t.Errorf("error %q does not name executor %q", err, record.ID)
			}
		})
	}
}

// TestBuildCapabilityIndexEmptyRoster shows an empty roster yields an empty
// index, which T5.24 will reject against a non-empty roster declaration.
func TestBuildCapabilityIndexEmptyRoster(t *testing.T) {
	index, err := BuildCapabilityIndex(nil)
	if err != nil {
		t.Fatalf("BuildCapabilityIndex(nil): %v", err)
	}
	if got := index.Capabilities(); len(got) != 0 {
		t.Fatalf("Capabilities = %v, want none", got)
	}
}

// TestACPAgentRegistryRegisterFindList is the core T3.285 case: register agents
// with a name, capabilities and address, then find them by capability and by
// address and list them all.
func TestACPAgentRegistryRegisterFindList(t *testing.T) {
	reg := NewACPAgentRegistry()
	for _, agent := range []ACPAgent{
		{Name: "coder", Capabilities: []string{"code", "review"}, Address: "https://acp.example/coder"},
		{Name: "reviewer", Capabilities: []string{"review"}, Address: "https://acp.example/reviewer"},
	} {
		if err := reg.Register(agent); err != nil {
			t.Fatalf("Register(%s): %v", agent.Name, err)
		}
	}

	got := reg.FindByCapability("review")
	names := make([]string, 0, len(got))
	for _, agent := range got {
		names = append(names, agent.Name)
	}
	if !reflect.DeepEqual(names, []string{"coder", "reviewer"}) {
		t.Fatalf("FindByCapability(review) = %v, want [coder reviewer]", names)
	}
	if got := reg.FindByCapability("code"); len(got) != 1 || got[0].Name != "coder" {
		t.Fatalf("FindByCapability(code) = %v, want [coder]", got)
	}
	if got := reg.FindByCapability("missing"); len(got) != 0 {
		t.Fatalf("FindByCapability(missing) = %v, want none", got)
	}

	found, ok := reg.FindByAddress("https://acp.example/reviewer")
	if !ok || found.Name != "reviewer" {
		t.Fatalf("FindByAddress = %v, %v, want reviewer", found, ok)
	}
	if _, ok := reg.FindByAddress("https://acp.example/unknown"); ok {
		t.Fatal("FindByAddress found an unknown address")
	}

	names = names[:0]
	for _, agent := range reg.List() {
		names = append(names, agent.Name)
	}
	if !reflect.DeepEqual(names, []string{"coder", "reviewer"}) {
		t.Fatalf("List = %v, want [coder reviewer]", names)
	}
}

// TestACPAgentRegistryReRegisterReplacesState proves a re-registration drops
// stale capabilities and moves the address mapping to the new agent.
func TestACPAgentRegistryReRegisterReplacesState(t *testing.T) {
	reg := NewACPAgentRegistry()
	if err := reg.Register(ACPAgent{Name: "bot", Capabilities: []string{"old"}, Address: "addr-1"}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := reg.Register(ACPAgent{Name: "bot", Capabilities: []string{"new"}, Address: "addr-2"}); err != nil {
		t.Fatalf("second Register: %v", err)
	}
	if got := reg.FindByCapability("old"); len(got) != 0 {
		t.Fatalf("stale capability survived re-registration: %v", got)
	}
	if got := reg.FindByCapability("new"); len(got) != 1 || got[0].Name != "bot" {
		t.Fatalf("FindByCapability(new) = %v, want [bot]", got)
	}
	if _, ok := reg.FindByAddress("addr-1"); ok {
		t.Fatal("old address is still mapped")
	}
	if agent, ok := reg.FindByAddress("addr-2"); !ok || agent.Name != "bot" {
		t.Fatalf("FindByAddress(addr-2) = %v, %v, want bot", agent, ok)
	}

	if err := reg.Register(ACPAgent{Name: "other", Capabilities: []string{"new"}, Address: "addr-2"}); err != nil {
		t.Fatalf("takeover Register: %v", err)
	}
	if agent, ok := reg.FindByAddress("addr-2"); !ok || agent.Name != "other" {
		t.Fatalf("address owner = %v, %v, want other", agent, ok)
	}
	if got := reg.FindByCapability("new"); len(got) != 1 || got[0].Name != "other" {
		t.Fatalf("capability owner after takeover = %v", got)
	}
}

// TestACPAgentRegistryRejectsIncompleteAgents checks the registration boundary:
// empty names, addresses and capabilities fail without changing the registry.
func TestACPAgentRegistryRejectsIncompleteAgents(t *testing.T) {
	reg := NewACPAgentRegistry()
	tests := []struct {
		name  string
		agent ACPAgent
		field string
	}{
		{"empty name", ACPAgent{Address: "addr"}, "name"},
		{"empty address", ACPAgent{Name: "bot"}, "address"},
		{"empty capability", ACPAgent{Name: "bot", Address: "addr", Capabilities: []string{"ok", ""}}, "capability"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reg.Register(tt.agent)
			if err == nil {
				t.Fatal("Register accepted an incomplete agent")
			}
			if !strings.Contains(err.Error(), tt.field) {
				t.Errorf("error %q does not name %q", err, tt.field)
			}
		})
	}
	if got := reg.List(); len(got) != 0 {
		t.Fatalf("failed registrations left entries: %v", got)
	}
}

// TestACPAgentRegistryCopiesReturns proves callers cannot mutate registry state
// through a returned agent or through the slice passed to Register.
func TestACPAgentRegistryCopiesReturns(t *testing.T) {
	reg := NewACPAgentRegistry()
	declared := []string{"watch"}
	if err := reg.Register(ACPAgent{Name: "bot", Capabilities: declared, Address: "addr"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	declared[0] = "merge"
	if got := reg.FindByCapability("watch"); len(got) != 1 {
		t.Fatalf("registry aliased caller capability slice: %v", got)
	}

	listed := reg.List()
	listed[0].Capabilities[0] = "changed"
	if got := reg.FindByCapability("watch"); len(got) != 1 {
		t.Fatalf("List aliased stored capabilities: %v", got)
	}
}
