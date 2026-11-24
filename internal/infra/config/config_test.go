package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaultsToTwelveMergeAttempts(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Merge.MaxAttempts != 12 {
		t.Fatalf("MaxAttempts = %d, want 12", c.Merge.MaxAttempts)
	}
}

func TestLoadMergeMaxAttemptsOverride(t *testing.T) {
	c, err := Load(writeConfig(t, "merge:\n  max_attempts: 7\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Merge.MaxAttempts != 7 {
		t.Fatalf("MaxAttempts = %d, want 7", c.Merge.MaxAttempts)
	}
}

func TestLoadUnsetMaxAttemptsFallsBackToTwelve(t *testing.T) {
	c, err := Load(writeConfig(t, "merge:\n  auto_rebase: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Merge.MaxAttempts != 12 {
		t.Fatalf("MaxAttempts = %d, want 12", c.Merge.MaxAttempts)
	}
}

func TestLoadWebhookSecretLiteral(t *testing.T) {
	path := writeConfig(t, "webhook:\n  enabled: true\n  secret: 'p$ss{w0rd'\n")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Webhook.Enabled {
		t.Fatal("webhook.enabled not loaded")
	}
	if c.Webhook.Secret != "p$ss{w0rd" {
		t.Fatalf("secret = %q, want %q", c.Webhook.Secret, "p$ss{w0rd")
	}
}

func TestLoadSecretLiteralFallsBackWhenEnvUnset(t *testing.T) {
	const envName = "BEACON_TEST_UNSET_SECRET"
	t.Setenv(envName, "")
	path := writeConfig(t, "webhook:\n  secret: 'literal-secret'\n  secret_env: "+envName+"\n")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Webhook.Secret != "literal-secret" {
		t.Fatalf("secret = %q, want literal fallback", c.Webhook.Secret)
	}
}

func TestLoadWebhookSecretEnv(t *testing.T) {
	const envName = "BEACON_TEST_WEBHOOK_SECRET"
	t.Setenv(envName, "from-env-123")
	path := writeConfig(t, "webhook:\n  secret: 'unused-literal'\n  secret_env: "+envName+"\n")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Webhook.Secret != "from-env-123" {
		t.Fatalf("secret = %q, want %q", c.Webhook.Secret, "from-env-123")
	}
}

func TestLoadDefaultWorkerInstructions(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Harness.WorkerInstructions != DefaultWorkerInstructions {
		t.Fatalf("worker instructions = %q, want default %q", c.Harness.WorkerInstructions, DefaultWorkerInstructions)
	}
	if !strings.Contains(c.Harness.WorkerInstructions, "weak PC") ||
		!strings.Contains(c.Harness.WorkerInstructions, "targeted tests") ||
		!strings.Contains(c.Harness.WorkerInstructions, "CI") {
		t.Fatalf("default lacks weak-PC rule: %q", c.Harness.WorkerInstructions)
	}
	if strings.Contains(c.Harness.WorkerInstructions, "go test") ||
		strings.Contains(c.Harness.WorkerInstructions, "golangci-lint") {
		t.Fatalf("default is not language agnostic: %q", c.Harness.WorkerInstructions)
	}
}

func TestDefaultWorkerInstructionsForbidCLIFallback(t *testing.T) {
	want := "Use the beacon_* MCP tools only. Never locate, build or execute the beacon binary or cmd/beacon source from a shell."
	if !strings.Contains(DefaultWorkerInstructions, want) {
		t.Fatalf("default lacks the MCP-only rule: %q", DefaultWorkerInstructions)
	}
}

func TestLoadWorkerInstructionsOverride(t *testing.T) {
	const custom = "Use targeted tests. Full builds run in CI."
	c, err := Load(writeConfig(t, "harness:\n  worker_instructions: "+strconv.Quote(custom)+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Harness.WorkerInstructions != custom {
		t.Fatalf("worker instructions = %q, want %q", c.Harness.WorkerInstructions, custom)
	}
}

func TestLoadEmptyWorkerInstructionsUsesDefault(t *testing.T) {
	c, err := Load(writeConfig(t, "harness:\n  worker_instructions: \"\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Harness.WorkerInstructions != DefaultWorkerInstructions {
		t.Fatalf("worker instructions = %q, want default", c.Harness.WorkerInstructions)
	}
}

const completeConfig = "state_db: .beacon/state.db\n" +
	"workspace_dir: .beacon/worktrees\n" +
	"base_branch: main\n" +
	"task_source:\n  provider: markdown\n  dir: docs/tasks\n" +
	"scm:\n  provider: github\n  owner: owner\n  repo: repo\n" +
	"harness:\n  provider: opencode\n  model: model-a\n"

func TestCompleteAcceptsFullConfig(t *testing.T) {
	c, err := Load(writeConfig(t, completeConfig))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Complete() {
		t.Fatal("complete config was rejected")
	}
}

func TestCompleteRejectsEmptyConfig(t *testing.T) {
	c, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if c.Complete() {
		t.Fatal("empty config was accepted")
	}
}

func TestCompleteRejectsPartialConfig(t *testing.T) {
	c, err := Load(writeConfig(t, "harness:\n  model: model-a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Complete() {
		t.Fatal("partial config was accepted")
	}
}
