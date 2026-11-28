package main

import (
	"os"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/config"
)

// baseRestartConfig has a distinct value for every restart field so a single
// mutation cannot accidentally leave the field unchanged.
func baseRestartConfig() config.Config {
	return config.Config{
		StateDB:      "state.db",
		WorkspaceDir: "worktrees",
		BaseBranch:   "main",
		PollInterval: "30s",
		StrictScope:  false,
		Concurrency:  config.Concurrency{MaxInFlight: 20, MaxActiveAgents: 4},
		Harness: config.Harness{
			WorkerInstructions: "base instructions", Command: "base command",
			PromptTemplate: "base prompt", ParentSessionID: "base session", Model: "model-a",
		},
		Merge:      config.Merge{MaxAttempts: 5, AutoRebase: true},
		OnBaseRed:  config.OnBaseRed{Mode: "fix", MaxFixAttempts: 2},
		TaskSource: config.TaskSource{Provider: "markdown"},
		SCM:        config.SCM{Provider: "github", Owner: "owner", Repo: "repo"},
		Notify:     config.Notify{ChatID: "chat"},
		Webhook:    config.Webhook{Enabled: true},
	}
}

// restartFieldMutations pairs each classification with one edit that changes
// only that field. A new entry in restartFieldChecks without an entry here
// fails TestRestartFieldsCoverEveryClassification.
var restartFieldMutations = map[string]func(*config.Config){
	"harness.worker_instructions":   func(c *config.Config) { c.Harness.WorkerInstructions = "changed" },
	"harness.command":               func(c *config.Config) { c.Harness.Command = "changed" },
	"harness.prompt_template":       func(c *config.Config) { c.Harness.PromptTemplate = "changed" },
	"harness.parent_session_id":     func(c *config.Config) { c.Harness.ParentSessionID = "changed" },
	"concurrency.max_in_flight":     func(c *config.Config) { c.Concurrency.MaxInFlight = 21 },
	"concurrency.max_active_agents": func(c *config.Config) { c.Concurrency.MaxActiveAgents = 5 },
	"merge.max_attempts":            func(c *config.Config) { c.Merge.MaxAttempts = 6 },
	"merge.auto_rebase":             func(c *config.Config) { c.Merge.AutoRebase = false },
	"on_base_red.mode":              func(c *config.Config) { c.OnBaseRed.Mode = "block" },
	"on_base_red.max_fix_attempts":  func(c *config.Config) { c.OnBaseRed.MaxFixAttempts = 3 },
	"base_branch":                   func(c *config.Config) { c.BaseBranch = "develop" },
	"state_db":                      func(c *config.Config) { c.StateDB = "other.db" },
	"workspace_dir":                 func(c *config.Config) { c.WorkspaceDir = "other" },
	"strict_scope":                  func(c *config.Config) { c.StrictScope = true },
	"poll_interval":                 func(c *config.Config) { c.PollInterval = "10s" },
	"task_source":                   func(c *config.Config) { c.TaskSource.Provider = "github" },
	"scm":                           func(c *config.Config) { c.SCM.Owner = "other" },
	"notify":                        func(c *config.Config) { c.Notify.ChatID = "other" },
	"webhook":                       func(c *config.Config) { c.Webhook.Enabled = false },
}

// TestRestartFieldsCoverEveryClassification walks the production table and
// mutates each field in turn. Every field must be reported by name and nothing
// else. The count is pinned to the benchmark's work constant, so a new field
// cannot land without a mutation case.
func TestRestartFieldsCoverEveryClassification(t *testing.T) {
	base := baseRestartConfig()
	if got := int64(len(restartFieldChecks)); got != restartChecksPerReload {
		t.Fatalf("restart field checks = %d, want %d", got, restartChecksPerReload)
	}
	seen := make(map[string]bool, len(restartFieldChecks))
	for _, field := range restartFieldChecks {
		if seen[field.name] {
			t.Fatalf("restart field %q is classified twice", field.name)
		}
		seen[field.name] = true
		mutate, ok := restartFieldMutations[field.name]
		if !ok {
			t.Fatalf("restart field %q has no mutation case", field.name)
		}
		next := base
		mutate(&next)
		got := restartFields(base, next)
		if len(got) != 1 || got[0] != field.name {
			t.Fatalf("mutating %q reported %v, want [%s]", field.name, got, field.name)
		}
	}
	for name := range restartFieldMutations {
		if !seen[name] {
			t.Fatalf("mutation case %q names no restart field", name)
		}
	}
	if got := restartFields(base, base); len(got) != 0 {
		t.Fatalf("unchanged config reported restart fields %v", got)
	}
}

// TestConfigReloadRotatesModelKeepsCacheFresh toggles the model several times
// and checks that each generation invalidates the resolver cache. A stale
// cache would return the first model after the second rotation.
func TestConfigReloadRotatesModelKeepsCacheFresh(t *testing.T) {
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	cw, _ := newTestWatcher(t, path)
	current := "model-a"
	if got := cw.modelResolver("ses_rotate").ID; got != current {
		t.Fatalf("initial model = %q, want %q", got, current)
	}

	for i := 1; i <= 4; i++ {
		next := "model-b"
		if current == "model-b" {
			next = "model-a"
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeWatchFile(t, path, strings.Replace(string(body), "model: "+current, "model: "+next, 1))
		if !cw.reload() {
			t.Fatalf("rotation %d was rejected", i)
		}
		state := cw.live.Load()
		if state.cfg.Harness.Model != next || state.generation != uint64(i) {
			t.Fatalf("rotation %d: model = %q, generation = %d, want %q/%d",
				i, state.cfg.Harness.Model, state.generation, next, i)
		}
		if got := cw.modelResolver("ses_rotate").ID; got != next {
			t.Fatalf("rotation %d resolved %q, want %q", i, got, next)
		}
		current = next
	}
}

// TestConfigReloadReadFailureKeepsPreviousState proves a missing config file
// leaves the last good state and hash in place, and a restored file still
// reloads.
func TestConfigReloadReadFailureKeepsPreviousState(t *testing.T) {
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	cw, _ := newTestWatcher(t, path)
	before := cw.live.Load()
	hash := cw.hash
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if cw.reload() {
		t.Fatal("a missing config file triggered a reload")
	}
	if got := cw.live.Load(); got != before {
		t.Fatal("read failure replaced the live state")
	}
	if cw.hash != hash {
		t.Fatal("read failure changed the watch hash")
	}

	writeWatchFile(t, path, strings.Replace(string(body), "model: model-a", "model: model-b", 1))
	if !cw.reload() {
		t.Fatal("restored config was not applied")
	}
	if got := cw.live.Load().cfg.Harness.Model; got != "model-b" {
		t.Fatalf("model after restore = %q, want model-b", got)
	}
}
