package mcp

import "testing"

// TestControlDescriptionCarriesCoreVerbs pins the advertised description. It
// fails if a verb disappears from core or if the catalog stops deriving from it.
func TestControlDescriptionCarriesCoreVerbs(t *testing.T) {
	const want = "Control a run: approve, unblock, cancel, retry, nudge or watch."
	for _, spec := range ToolCatalog() {
		if spec.Name != "beacon_control" {
			continue
		}
		if spec.Description != want {
			t.Fatalf("beacon_control description = %q, want %q", spec.Description, want)
		}
		return
	}
	t.Fatal("beacon_control is missing from ToolCatalog")
}
