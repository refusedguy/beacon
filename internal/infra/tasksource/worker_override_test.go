package tasksource

import (
	"path/filepath"
	"testing"
)

func TestWorkerOverrideFrontmatter(t *testing.T) {
	card := `---
id: T-WORKER
title: Override worker
status: available
worker_agent: table-driven
worker_prompt: |
  Use table-driven tests.
  Skip local builds.
unknown_key: ignored
---
# T-WORKER. Override worker
`
	got, ok, _ := parseCardFields(card, filepath.Join(t.TempDir(), "T-WORKER.md"))
	if !ok {
		t.Fatal("card not parsed")
	}
	if got.WorkerAgent != "table-driven" {
		t.Errorf("WorkerAgent = %q, want table-driven", got.WorkerAgent)
	}
	want := "Use table-driven tests.\nSkip local builds."
	if got.WorkerPrompt != want {
		t.Errorf("WorkerPrompt = %q, want %q", got.WorkerPrompt, want)
	}
}

func TestWorkerOverrideAbsentAndUnknownKeysIgnored(t *testing.T) {
	card := `---
id: T-PLAIN
title: Plain worker
status: available
future_key: whatever
another_unknown: [a, b]
---
# T-PLAIN. Plain worker
`
	got, ok, _ := parseCardFields(card, filepath.Join(t.TempDir(), "T-PLAIN.md"))
	if !ok {
		t.Fatal("card with unknown keys was not parsed")
	}
	if got.WorkerAgent != "" || got.WorkerPrompt != "" {
		t.Fatalf("unexpected overrides: %q %q", got.WorkerAgent, got.WorkerPrompt)
	}
}
