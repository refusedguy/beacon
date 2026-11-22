package mcp

import (
	"slices"
	"testing"
)

func TestCopyGettersReturnCopies(t *testing.T) {
	wantVersions := slices.Clone(LegacyProtocolVersions)
	wantNames := slices.Clone(ToolNames)

	versions := LegacyProtocolVersionsCopy()
	if !slices.Equal(versions, wantVersions) {
		t.Fatalf("LegacyProtocolVersionsCopy() = %v, want %v", versions, wantVersions)
	}
	names := ToolNamesCopy()
	if !slices.Equal(names, wantNames) {
		t.Fatalf("ToolNamesCopy() = %v, want %v", names, wantNames)
	}

	versions[0] = "mutated"
	names[0] = "mutated"
	if !slices.Equal(LegacyProtocolVersions, wantVersions) {
		t.Fatalf("editing the version copy changed the global: got %v, want %v", LegacyProtocolVersions, wantVersions)
	}
	if !slices.Equal(ToolNames, wantNames) {
		t.Fatalf("editing the name copy changed the global: got %v, want %v", ToolNames, wantNames)
	}
}
