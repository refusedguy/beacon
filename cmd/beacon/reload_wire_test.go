package main

import (
	"os"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/config"
)

// TestReloadedRebaseCapReachesReconciler proves the live cap path end to end.
// A full wiring holds one reconciler for the life of the daemon. An edit to
// merge.max_auto_rebases changes the cap that reconciler enforces through the
// provider wired at construction, with no rebuild and no restart.
func TestReloadedRebaseCapReachesReconciler(t *testing.T) {
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	w, err := wire(path, "")
	if err != nil {
		t.Fatalf("wire(path, \"\"): %v", err)
	}
	defer w.close()

	if w.recon.RebaseCap == nil {
		t.Fatal("reconciler has no live cap provider")
	}
	if got := w.recon.RebaseCap(); got != config.DefaultMaxAutoRebases {
		t.Fatalf("initial cap = %d, want %d", got, config.DefaultMaxAutoRebases)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeWatchFile(t, path, string(body)+"merge:\n  max_auto_rebases: 5\n")
	if !w.watcher.reload() {
		t.Fatal("cap-only reload was not applied")
	}
	if got := w.recon.RebaseCap(); got != 5 {
		t.Fatalf("cap after reload = %d, want 5", got)
	}
	if got := w.watcher.live.Load().cfg.Merge.MaxAutoRebases; got != 5 {
		t.Fatalf("live config cap = %d, want 5", got)
	}
}

// TestReloadedCapSurvivesUnrelatedReload checks that a later model-only reload
// does not revert the cap the operator set.
func TestReloadedCapSurvivesUnrelatedReload(t *testing.T) {
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	w, err := wire(path, "")
	if err != nil {
		t.Fatalf("wire(path, \"\"): %v", err)
	}
	defer w.close()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeWatchFile(t, path, string(body)+"merge:\n  max_auto_rebases: 4\n")
	if !w.watcher.reload() {
		t.Fatal("cap-only reload was not applied")
	}
	writeWatchFile(t, path, strings.Replace(string(body)+"merge:\n  max_auto_rebases: 4\n", "model: model-a", "model: model-b", 1))
	if !w.watcher.reload() {
		t.Fatal("model-only reload was not applied")
	}
	if got := w.recon.RebaseCap(); got != 4 {
		t.Fatalf("cap after model reload = %d, want 4", got)
	}
}
