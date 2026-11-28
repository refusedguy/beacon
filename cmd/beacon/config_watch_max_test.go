package main

import (
	"os"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/config"
)

// TestMaxAutoRebasesEditAppliesLive checks the classification an operator sees
// when only the automatic rebase cap changes. The reconciler reads that cap
// from live config on every tick, so the watcher must apply the edit instead
// of asking for a restart.
func TestMaxAutoRebasesEditAppliesLive(t *testing.T) {
	base := baseRestartConfig()
	base.Merge.MaxAutoRebases = config.DefaultMaxAutoRebases
	next := base
	next.Merge.MaxAutoRebases = config.DefaultMaxAutoRebases + 1

	restart, modelOnly, checksOnly := configChange(base, next)
	if len(restart) != 0 || modelOnly || checksOnly {
		t.Fatalf("configChange(max_auto_rebases only) = %v, %t, %t, want no restart, false, false",
			restart, modelOnly, checksOnly)
	}
}

// TestConfigReloadAppliesMaxAutoRebasesEdit drives a real config edit through
// the watcher and checks that it stores the new cap without a restart.
func TestConfigReloadAppliesMaxAutoRebasesEdit(t *testing.T) {
	logs := captureWatchLogs(t)
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	cw, _ := newTestWatcher(t, path)
	before := cw.live.Load()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeWatchFile(t, path, string(body)+"merge:\n  max_auto_rebases: 5\n")
	if !cw.reload() {
		t.Fatal("reload with only merge.max_auto_rebases changed was not applied")
	}
	after := cw.live.Load()
	if after == before {
		t.Fatal("applied reload kept the previous live state")
	}
	if got := after.cfg.Merge.MaxAutoRebases; got != 5 {
		t.Fatalf("stored max_auto_rebases = %d, want 5", got)
	}

	out := logs.String()
	if strings.Contains(out, "merge.max_auto_rebases") || strings.Contains(out, "restart beacon") {
		t.Fatalf("cap-only edit reported a restart: %s", out)
	}
}

// TestRestartMutationTableStaysComplete checks that the pinned restart table
// and its mutation map still describe the same fields. The automatic rebase
// cap is a live field, so it adds no row to either.
func TestRestartMutationTableStaysComplete(t *testing.T) {
	if len(restartFieldMutations) != len(restartFieldChecks) {
		t.Fatalf("mutation cases = %d, restart checks = %d", len(restartFieldMutations), len(restartFieldChecks))
	}
	for _, field := range restartFieldChecks {
		if _, ok := restartFieldMutations[field.name]; !ok {
			t.Fatalf("restart field %q has no mutation case", field.name)
		}
	}
}
