package main

import (
	"os"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/config"
)

// TestMaxAutoRebasesEditNeedsRestart checks the classification an operator sees
// when only the automatic rebase cap changes. The reconciler captures the cap
// at construction, so the watcher must ask for a restart instead of storing a
// config the running reconciler will never read.
func TestMaxAutoRebasesEditNeedsRestart(t *testing.T) {
	base := baseRestartConfig()
	base.Merge.MaxAutoRebases = config.DefaultMaxAutoRebases
	next := base
	next.Merge.MaxAutoRebases = config.DefaultMaxAutoRebases + 1

	restart, modelOnly, checksOnly := configChange(base, next)
	if len(restart) != 1 || restart[0] != "merge.max_auto_rebases" || modelOnly || checksOnly {
		t.Fatalf("configChange(max_auto_rebases only) = %v, %t, %t, want [merge.max_auto_rebases], false, false",
			restart, modelOnly, checksOnly)
	}
}

// TestConfigReloadRejectsMaxAutoRebasesEdit drives a real config edit through
// the watcher and checks that it keeps the last good config and reports the
// restart to the operator.
func TestConfigReloadRejectsMaxAutoRebasesEdit(t *testing.T) {
	logs := captureWatchLogs(t)
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	cw, _ := newTestWatcher(t, path)
	before := cw.live.Load()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeWatchFile(t, path, string(body)+"merge:\n  max_auto_rebases: 5\n")
	if cw.reload() {
		t.Fatal("reload with only merge.max_auto_rebases changed was applied")
	}
	if got := cw.live.Load(); got != before {
		t.Fatal("rejected reload replaced the live state")
	}
	if got := before.cfg.Merge.MaxAutoRebases; got != config.DefaultMaxAutoRebases {
		t.Fatalf("stored max_auto_rebases = %d, want %d", got, config.DefaultMaxAutoRebases)
	}

	out := logs.String()
	if !strings.Contains(out, "merge.max_auto_rebases") {
		t.Fatalf("restart warning missing field name: %s", out)
	}
	if !strings.Contains(out, "restart beacon") {
		t.Fatalf("restart warning missing action: %s", out)
	}
}

// TestRestartMutationTableStaysComplete checks that the pinned restart table
// and its mutation map still describe the same fields. The max_auto_rebases
// check lives in configChange, so it adds no row to the table and leaves the
// map complete.
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
