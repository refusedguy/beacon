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
