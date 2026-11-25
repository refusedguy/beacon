package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("os.WriteFile(path, []byte(content), 0o600): %v", err)
	}
	return path
}

func TestLoadDefaultsToTwelveMergeAttempts(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("Load(filepath.Join(t.TempDir(), \"missing.yaml\")): %v", err)
	}
	if c.Merge.MaxAttempts != 12 {
		t.Fatalf("MaxAttempts = %d, want 12", c.Merge.MaxAttempts)
	}
}

func TestLoadMergeMaxAttemptsOverride(t *testing.T) {
	c, err := Load(writeConfig(t, "merge:\n  max_attempts: 7\n"))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, \"merge:\\n  max_attempts: 7\\n\")): %v", err)
	}
	if c.Merge.MaxAttempts != 7 {
		t.Fatalf("MaxAttempts = %d, want 7", c.Merge.MaxAttempts)
	}
}

func TestLoadUnsetMaxAttemptsFallsBackToTwelve(t *testing.T) {
	c, err := Load(writeConfig(t, "merge:\n  auto_rebase: true\n"))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, \"merge:\\n  auto_rebase: true\\n\")): %v", err)
	}
	if c.Merge.MaxAttempts != 12 {
		t.Fatalf("MaxAttempts = %d, want 12", c.Merge.MaxAttempts)
	}
}

func TestLoadWebhookSecretLiteral(t *testing.T) {
	path := writeConfig(t, "webhook:\n  enabled: true\n  secret: 'p$ss{w0rd'\n")
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load(path): %v", err)
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
		t.Fatalf("Load(path): %v", err)
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
		t.Fatalf("Load(path): %v", err)
	}
	if c.Webhook.Secret != "from-env-123" {
		t.Fatalf("secret = %q, want %q", c.Webhook.Secret, "from-env-123")
	}
}

func TestLoadDefaultWorkerInstructions(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("Load(filepath.Join(t.TempDir(), \"missing.yaml\")): %v", err)
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

func TestLoadDefaultsDoNotNameHarnessProvider(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("Load(filepath.Join(t.TempDir(), \"missing.yaml\")): %v", err)
	}
	if c.Harness.Provider != "" {
		t.Fatalf("default provider = %q, want empty", c.Harness.Provider)
	}
	if c.Harness.Endpoint != "" {
		t.Fatalf("default endpoint = %q, want empty", c.Harness.Endpoint)
	}
	if c.Harness.WorkerInstructions != DefaultWorkerInstructions {
		t.Fatal("neutral defaults dropped the worker instruction prefix")
	}
}

func TestLoadHarnessProviderWithoutEndpoint(t *testing.T) {
	const content = "harness:\n  provider: acp\n  command: fake-agent\n  model: model-a\n"
	c, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, content)): %v", err)
	}
	if c.Harness.Provider != "acp" {
		t.Fatalf("provider = %q, want acp", c.Harness.Provider)
	}
	if c.Harness.Command != "fake-agent" {
		t.Fatalf("command = %q, want fake-agent", c.Harness.Command)
	}
	if c.Harness.Endpoint != "" {
		t.Fatalf("endpoint = %q, want empty for a non-HTTP provider", c.Harness.Endpoint)
	}
}

func TestLoadPreservesExplicitHarnessEndpoint(t *testing.T) {
	const endpoint = "http://127.0.0.1:9999"
	c, err := Load(writeConfig(t, "harness:\n  endpoint: "+endpoint+"\n"))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, \"harness:\\n  endpoint: \"+endpoint+\"\\n\")): %v", err)
	}
	if c.Harness.Endpoint != endpoint {
		t.Fatalf("endpoint = %q, want %q", c.Harness.Endpoint, endpoint)
	}
}

func TestLoadWorkerInstructionsOverride(t *testing.T) {
	const custom = "Use targeted tests. Full builds run in CI."
	c, err := Load(writeConfig(t, "harness:\n  worker_instructions: "+strconv.Quote(custom)+"\n"))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, \"harness:\\n  worker_instructions: \"+strconv.Quote(custom)+\"\\n\")): %v", err)
	}
	if c.Harness.WorkerInstructions != custom {
		t.Fatalf("worker instructions = %q, want %q", c.Harness.WorkerInstructions, custom)
	}
}

func TestLoadEmptyWorkerInstructionsUsesDefault(t *testing.T) {
	c, err := Load(writeConfig(t, "harness:\n  worker_instructions: \"\"\n"))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, \"harness:\\n  worker_instructions: \\\"\\\"\\n\")): %v", err)
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
		t.Fatalf("Load(writeConfig(t, completeConfig)): %v", err)
	}
	if !c.Complete() {
		t.Fatal("complete config was rejected")
	}
}

func TestCompleteRejectsEmptyConfig(t *testing.T) {
	c, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, \"\")): %v", err)
	}
	if c.Complete() {
		t.Fatal("empty config was accepted")
	}
}

func TestCompleteRejectsPartialConfig(t *testing.T) {
	c, err := Load(writeConfig(t, "harness:\n  model: model-a\n"))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, \"harness:\\n  model: model-a\\n\")): %v", err)
	}
	if c.Complete() {
		t.Fatal("partial config was accepted")
	}
}

func TestCompleteRequiresAllSCMFields(t *testing.T) {
	cases := []struct {
		name string
		scm  SCM
		want bool
	}{
		{"full", SCM{Provider: "github", Owner: "acme", Repo: "demo"}, true},
		{"missing provider", SCM{Owner: "acme", Repo: "demo"}, false},
		{"missing owner", SCM{Provider: "github", Repo: "demo"}, false},
		{"missing repo", SCM{Provider: "github", Owner: "acme"}, false},
		{"provider only", SCM{Provider: "github"}, false},
		{"token only", SCM{TokenEnv: "GITHUB_TOKEN"}, false},
		{"none", SCM{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{
				StateDB:      ".beacon/state.db",
				WorkspaceDir: ".beacon/worktrees",
				BaseBranch:   "main",
				Concurrency:  Concurrency{MaxInFlight: 20},
				TaskSource:   TaskSource{Provider: "markdown", Dir: "docs/tasks"},
				SCM:          tc.scm,
			}
			if got := c.Complete(); got != tc.want {
				t.Fatalf("Complete() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCompleteRejectsBadInterval(t *testing.T) {
	c := Config{
		StateDB:      ".beacon/state.db",
		WorkspaceDir: ".beacon/worktrees",
		BaseBranch:   "main",
		Concurrency:  Concurrency{MaxInFlight: 20},
		TaskSource:   TaskSource{Provider: "markdown", Dir: "docs/tasks"},
		SCM:          SCM{Provider: "github", Owner: "acme", Repo: "demo"},
		PollInterval: "not-a-duration",
	}
	if c.Complete() {
		t.Fatal("config with a bad interval was accepted")
	}
}

func TestLoadRejectsPartialSCM(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"provider only", "scm:\n  provider: github\n"},
		{"owner only", "scm:\n  owner: acme\n"},
		{"repo only", "scm:\n  repo: demo\n"},
		{"provider and owner", "scm:\n  provider: github\n  owner: acme\n"},
		{"provider and repo", "scm:\n  provider: github\n  repo: demo\n"},
		{"owner and repo", "scm:\n  owner: acme\n  repo: demo\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body))
			if err == nil {
				t.Fatal("Load accepted a partial SCM")
			}
			if !strings.Contains(err.Error(), "scm requires provider, owner and repo together") {
				t.Fatalf("error = %q, want an SCM message", err)
			}
		})
	}
}

func TestLoadAcceptsCompleteSCM(t *testing.T) {
	const body = "scm:\n  provider: github\n  owner: acme\n  repo: demo\n"
	c, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, body)): %v", err)
	}
	if c.SCM.Provider != "github" || c.SCM.Owner != "acme" || c.SCM.Repo != "demo" {
		t.Fatalf("SCM = %+v, want github/acme/demo", c.SCM)
	}
}

func TestLoadAcceptsAbsentSCM(t *testing.T) {
	if _, err := Load(writeConfig(t, "merge:\n  max_attempts: 7\n")); err != nil {
		t.Fatalf("Load rejected a config with no SCM section: %v", err)
	}
}

func TestLoadRejectsBadInterval(t *testing.T) {
	// "30sec" is the typo the old silent fallback masked as 30 seconds.
	for _, value := range []string{"nope", "30", "30sec", "5x"} {
		t.Run(value, func(t *testing.T) {
			path := writeConfig(t, "poll_interval: "+strconv.Quote(value)+"\n")
			_, err := Load(path)
			if err == nil {
				t.Fatalf("Load accepted poll_interval %q", value)
			}
			if !strings.Contains(err.Error(), "invalid poll_interval") ||
				!strings.Contains(err.Error(), value) ||
				!strings.Contains(err.Error(), "time:") {
				t.Fatalf("error = %q, want the ParseDuration detail", err)
			}
		})
	}
}

func TestIntervalUsesConfiguredValue(t *testing.T) {
	c, err := Load(writeConfig(t, "poll_interval: 45s\n"))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, \"poll_interval: 45s\\n\")): %v", err)
	}
	if got := c.Interval(); got != 45*time.Second {
		t.Fatalf("Interval() = %v, want 45s", got)
	}
}

func TestIntervalDefaultsWhenUnset(t *testing.T) {
	c, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, \"\")): %v", err)
	}
	if got := c.Interval(); got != 30*time.Second {
		t.Fatalf("Interval() = %v, want 30s", got)
	}
}

func TestLoadDoesNotExpandSecrets(t *testing.T) {
	const envName = "BEACON_TEST_SECRET_EXPANSION"
	t.Setenv(envName, "expanded-value")
	body := "scm:\n  provider: github\n  owner: acme\n  repo: demo\n" +
		"  token: 'scm-${" + envName + "}'\n" +
		"harness:\n  token: 'harness-${" + envName + "}'\n" +
		"webhook:\n  secret: 'webhook-${" + envName + "}'\n" +
		"notify:\n  telegram_token: 'notify-${" + envName + "}'\n"
	c, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, body)): %v", err)
	}
	want := map[string]string{
		"scm token":      "scm-${" + envName + "}",
		"harness token":  "harness-${" + envName + "}",
		"webhook secret": "webhook-${" + envName + "}",
		"notify token":   "notify-${" + envName + "}",
	}
	got := map[string]string{
		"scm token":      c.SCM.Token,
		"harness token":  c.Harness.Token,
		"webhook secret": c.Webhook.Secret,
		"notify token":   c.Notify.TelegramToken,
	}
	for name, wantValue := range want {
		if got[name] != wantValue {
			t.Errorf("%s = %q, want literal %q", name, got[name], wantValue)
		}
	}
}

func TestLoadExpandsNonSecretFields(t *testing.T) {
	const envName = "BEACON_TEST_NON_SECRET_EXPANSION"
	t.Setenv(envName, "expanded")
	body := "state_db: '${" + envName + "}/state.db'\n" +
		"scm:\n  provider: github\n  owner: acme\n  repo: demo\n" +
		"  base_url: 'https://${" + envName + "}.example.com'\n" +
		"merge:\n  required_checks: ['${" + envName + "}']\n  labels: ['label-${" + envName + "}']\n"
	c, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, body)): %v", err)
	}
	if c.StateDB != "expanded/state.db" {
		t.Errorf("state_db = %q, want expanded/state.db", c.StateDB)
	}
	if c.SCM.BaseURL != "https://expanded.example.com" {
		t.Errorf("base_url = %q, want expanded host", c.SCM.BaseURL)
	}
	if len(c.Merge.RequiredChecks) != 1 || c.Merge.RequiredChecks[0] != "expanded" {
		t.Errorf("required_checks = %v, want [expanded]", c.Merge.RequiredChecks)
	}
	if len(c.Merge.Labels) != 1 || c.Merge.Labels[0] != "label-expanded" {
		t.Errorf("labels = %v, want [label-expanded]", c.Merge.Labels)
	}
}

func TestLoadKeepsBareDollarLiteral(t *testing.T) {
	const envName = "BEACON_TEST_BARE_DOLLAR"
	t.Setenv(envName, "expanded")
	body := "state_db: '$" + envName + "/state.db'\n"
	c, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, body)): %v", err)
	}
	want := "$" + envName + "/state.db"
	if c.StateDB != want {
		t.Fatalf("state_db = %q, want %q", c.StateDB, want)
	}
}

func TestLoadSCMTokenFromEnv(t *testing.T) {
	const envName = "BEACON_TEST_SCM_TOKEN"
	t.Setenv(envName, "from-env")
	body := "scm:\n  provider: github\n  owner: acme\n  repo: demo\n" +
		"  token: 'literal'\n  token_env: " + envName + "\n"
	c, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, body)): %v", err)
	}
	if c.SCM.Token != "from-env" {
		t.Fatalf("SCM.Token = %q, want from-env", c.SCM.Token)
	}
}
