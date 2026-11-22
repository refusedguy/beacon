package main

import "testing"

// TestCatalogMatchesDispatch keeps `beacon list --format json` a complete
// catalog of the commands the dispatcher can run. The catalog slice and the
// runnerFor table are declared in different files, so a command added to one
// side without the other must fail this test.
func TestCatalogMatchesDispatch(t *testing.T) {
	want := make(map[string]bool, len(runnerFor))
	for name := range runnerFor {
		want[name] = true
	}

	seen := make(map[string]bool, len(commands()))
	for _, cmd := range commands() {
		if !want[cmd.Name] {
			t.Errorf("catalog command %q has no dispatch runner", cmd.Name)
		}
		if seen[cmd.Name] {
			t.Errorf("catalog repeats command %q", cmd.Name)
		}
		seen[cmd.Name] = true
	}

	for name := range want {
		if !seen[name] {
			t.Errorf("dispatch command %q is missing from the catalog", name)
		}
	}
}
