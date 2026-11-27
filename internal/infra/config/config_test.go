package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/policy"
	"github.com/refusedguy/beacon/pkg/core"
)

// stubCheckSource returns a fixed list of required-check names. It stands in
// for the source the daemon wires into the policy in production.
type stubCheckSource struct{ names []string }

func (s stubCheckSource) Names(ctx context.Context) ([]string, error) {
	return s.names, nil
}

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
	if c.Merge.MaxAutoRebases != 2 {
		t.Fatalf("MaxAutoRebases = %d, want 2", c.Merge.MaxAutoRebases)
	}
}

func TestLoadEmptyFileMatchesMissingFile(t *testing.T) {
	missing, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("Load(missing): %v", err)
	}
	cases := []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"whitespace", "  \n\t\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Load(writeConfig(t, tc.content))
			if err != nil {
				t.Fatalf("Load(%s file): %v", tc.name, err)
			}
			if !reflect.DeepEqual(got, missing) {
				t.Fatalf("config from %s file = %+v, want %+v", tc.name, got, missing)
			}
			if got.Concurrency.MaxActiveAgents != 4 {
				t.Fatalf("MaxActiveAgents = %d, want 4", got.Concurrency.MaxActiveAgents)
			}
		})
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

func TestLoadMergeMaxAutoRebasesOverride(t *testing.T) {
	c, err := Load(writeConfig(t, "merge:\n  max_auto_rebases: 5\n"))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, \"merge:\\n  max_auto_rebases: 5\\n\")): %v", err)
	}
	if c.Merge.MaxAutoRebases != 5 {
		t.Fatalf("MaxAutoRebases = %d, want 5", c.Merge.MaxAutoRebases)
	}
	if c.Merge.MaxAttempts != 12 {
		t.Fatalf("MaxAttempts = %d, want 12; the rebase cap must not change it", c.Merge.MaxAttempts)
	}
}

func TestLoadUnsetMaxAutoRebasesFallsBackToTwo(t *testing.T) {
	c, err := Load(writeConfig(t, "merge:\n  max_attempts: 7\n"))
	if err != nil {
		t.Fatalf("Load(writeConfig(t, \"merge:\\n  max_attempts: 7\\n\")): %v", err)
	}
	if c.Merge.MaxAutoRebases != 2 {
		t.Fatalf("MaxAutoRebases = %d, want 2", c.Merge.MaxAutoRebases)
	}
	if c.Merge.MaxAttempts != 7 {
		t.Fatalf("MaxAttempts = %d, want 7", c.Merge.MaxAttempts)
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

func TestLiteralOrEnvPrefersEnv(t *testing.T) {
	const envName = "BEACON_TEST_LITERAL_OR_ENV"
	t.Setenv(envName, "from-env")
	if got := LiteralOrEnv("literal", envName); got != "from-env" {
		t.Fatalf("LiteralOrEnv = %q, want from-env", got)
	}
}

func TestLiteralOrEnvFallsBackToLiteral(t *testing.T) {
	const envName = "BEACON_TEST_LITERAL_OR_ENV_UNSET"
	t.Setenv(envName, "")
	if got := LiteralOrEnv("literal", envName); got != "literal" {
		t.Fatalf("LiteralOrEnv = %q, want literal", got)
	}
}

func TestValidateWebhook(t *testing.T) {
	cases := []struct {
		name    string
		webhook Webhook
		wantErr bool
	}{
		{"disabled", Webhook{}, false},
		{"enabled with literal secret", Webhook{Enabled: true, Secret: "s"}, false},
		{"enabled without secret", Webhook{Enabled: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWebhook(tc.webhook)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("ValidateWebhook: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("ValidateWebhook accepted an enabled webhook without a secret")
			}
			for _, want := range []string{"webhook.enabled", "webhook.secret", "webhook.secret_env"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to name %q", err, want)
				}
			}
		})
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

func TestDefaultWorkerInstructionsShareWeakMachineRule(t *testing.T) {
	if !strings.Contains(DefaultWorkerInstructions, core.WeakMachineVerificationRule) {
		t.Fatalf("default worker instructions do not state the shared rule: %q", DefaultWorkerInstructions)
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

func TestIntervalKeepsSilentDefaultForCodeBuiltConfig(t *testing.T) {
	// Configs built in code skip Load validation and keep the silent default.
	c := Config{PollInterval: "not-a-duration"}
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

// writeWorkflowConfig puts a config at <root>/.beacon/config.yaml and an
// optional workflow at <root>/.github/workflows/ci.yml. Load resolves workflow
// mode relative to that root.
func writeWorkflowConfig(t *testing.T, body, workflow string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".beacon"), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(.beacon): %v", err)
	}
	path := filepath.Join(root, ".beacon", "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("os.WriteFile(config.yaml): %v", err)
	}
	if workflow != "" {
		writeProjectWorkflow(t, root, workflow)
	}
	return path
}

func writeProjectWorkflow(t *testing.T, root, workflow string) {
	t.Helper()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(.github/workflows): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ci.yml"), []byte(workflow), 0o644); err != nil {
		t.Fatalf("os.WriteFile(ci.yml): %v", err)
	}
}

func TestLoadManualRequiredChecksKeepExplicitList(t *testing.T) {
	const workflow = "name: CI\njobs:\n  verify:\n    runs-on: ubuntu-latest\n"
	for _, mode := range []string{"", RequiredChecksManual} {
		t.Run("mode="+mode, func(t *testing.T) {
			body := "merge:\n  required_checks: [build, vet]\n"
			if mode != "" {
				body = "merge:\n  required_checks_mode: " + mode + "\n  required_checks: [build, vet]\n"
			}
			path := writeWorkflowConfig(t, body, workflow)
			c, err := Load(path)
			if err != nil {
				t.Fatalf("Load(path): %v", err)
			}
			want := []string{"build", "vet"}
			if !reflect.DeepEqual(c.Merge.RequiredChecks, want) {
				t.Fatalf("required_checks = %v, want %v", c.Merge.RequiredChecks, want)
			}
		})
	}
}

func TestLoadWorkflowRequiredChecksResolveJobNames(t *testing.T) {
	const workflow = "name: CI\non: push\njobs:\n" +
		"  test:\n    strategy:\n      matrix:\n        os: [ubuntu, macos]\n    steps:\n      - run: echo test\n" +
		"  lint:\n    runs-on: ubuntu-latest\n" +
		"  build:\n    runs-on: ubuntu-latest\n" +
		"  vet:\n    runs-on: ubuntu-latest\n"
	path := writeWorkflowConfig(t, "merge:\n  required_checks_mode: workflow\n  required_checks: [stale]\n", workflow)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load(path): %v", err)
	}
	// The matrix job is excluded; GitHub only emits expanded cell names.
	want := []string{"build", "lint", "vet"}
	if !reflect.DeepEqual(c.Merge.RequiredChecks, want) {
		t.Fatalf("required_checks = %v, want %v", c.Merge.RequiredChecks, want)
	}
}

func TestWorkflowRequiredChecksFollowRenameOnReload(t *testing.T) {
	const oldWorkflow = "name: CI\njobs:\n" +
		"  build:\n    runs-on: ubuntu-latest\n" +
		"  vet:\n    runs-on: ubuntu-latest\n" +
		"  test:\n    runs-on: ubuntu-latest\n" +
		"  lint:\n    runs-on: ubuntu-latest\n"
	path := writeWorkflowConfig(t, "merge:\n  required_checks_mode: workflow\n", oldWorkflow)

	first, err := Load(path)
	if err != nil {
		t.Fatalf("Load(path): %v", err)
	}
	wantOld := []string{"build", "lint", "test", "vet"}
	if !reflect.DeepEqual(first.Merge.RequiredChecks, wantOld) {
		t.Fatalf("initial required_checks = %v, want %v", first.Merge.RequiredChecks, wantOld)
	}

	const newWorkflow = "name: CI\njobs:\n" +
		"  verify:\n    runs-on: ubuntu-latest\n" +
		"  test:\n    strategy:\n      matrix:\n        os: [ubuntu, macos]\n    steps:\n      - run: echo test\n"
	writeProjectWorkflow(t, projectRootFromConfigPath(path), newWorkflow)

	// The config file is unchanged. A watcher reload calls Load again and must
	// pick up the new job names from the workflow alone.
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload Load(path): %v", err)
	}
	wantNew := []string{"verify"}
	if !reflect.DeepEqual(reloaded.Merge.RequiredChecks, wantNew) {
		t.Fatalf("reloaded required_checks = %v, want %v", reloaded.Merge.RequiredChecks, wantNew)
	}

	p := policy.New(policy.Config{RequiredCheckSource: stubCheckSource{names: reloaded.Merge.RequiredChecks}})
	obs := core.Observation{
		PRState: "open",
		Checks: []core.CheckResult{
			{Name: "verify", State: core.CheckSuccess},
			{Name: "test", State: core.CheckSuccess},
		},
	}
	if got := p.Evaluate(obs); got.Verdict != core.VerdictMerge {
		t.Fatalf("policy verdict after rename = %+v, want merge", got)
	}
	if got := p.Evaluate(core.Observation{PRState: "open", Checks: []core.CheckResult{
		{Name: "build", State: core.CheckSuccess}, {Name: "vet", State: core.CheckSuccess},
		{Name: "lint", State: core.CheckSuccess}, {Name: "test", State: core.CheckSuccess},
	}}); got.Verdict != core.VerdictWait || !strings.Contains(got.Reason, "verify") {
		t.Fatalf("old check names still satisfy the policy: %+v", got)
	}
}

func TestLoadWorkflowRequiredChecksFallsBackToExplicitList(t *testing.T) {
	path := writeWorkflowConfig(t, "merge:\n  required_checks_mode: workflow\n  required_checks: [manual-gate]\n", "")
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load(path): %v", err)
	}
	if !reflect.DeepEqual(c.Merge.RequiredChecks, []string{"manual-gate"}) {
		t.Fatalf("required_checks = %v, want the explicit fallback", c.Merge.RequiredChecks)
	}
}

func TestLoadWorkflowModeUsesDetectedNamesOverConfiguredFallback(t *testing.T) {
	// The live config selects workflow mode and keeps build/vet/test/lint only
	// as a fallback. With a workflow present, detection wins and the fallback
	// names stop satisfying the policy. Names are read from the workflow, so
	// this test never pins a particular CI job name.
	fallback := []string{"build", "vet", "test", "lint"}
	const workflow = "name: CI\njobs:\n" +
		"  gate:\n    runs-on: ubuntu-latest\n" +
		"  suite:\n    runs-on: ubuntu-latest\n"
	path := writeWorkflowConfig(t,
		"merge:\n  required_checks_mode: workflow\n  required_checks: [build, vet, test, lint]\n",
		workflow)

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load(path): %v", err)
	}
	if c.Merge.RequiredChecksMode != RequiredChecksWorkflow {
		t.Fatalf("required_checks_mode = %q, want %q", c.Merge.RequiredChecksMode, RequiredChecksWorkflow)
	}
	want := DetectRequiredChecks(projectRootFromConfigPath(path))
	if !reflect.DeepEqual(c.Merge.RequiredChecks, want) {
		t.Fatalf("required_checks = %v, want detected %v", c.Merge.RequiredChecks, want)
	}
	if reflect.DeepEqual(c.Merge.RequiredChecks, fallback) {
		t.Fatalf("configured fallback was used despite workflow detection: %v", c.Merge.RequiredChecks)
	}

	p := policy.New(policy.Config{RequiredCheckSource: stubCheckSource{names: c.Merge.RequiredChecks}})
	obs := core.Observation{PRState: "open", Checks: []core.CheckResult{
		{Name: "build", State: core.CheckSuccess},
		{Name: "vet", State: core.CheckSuccess},
		{Name: "test", State: core.CheckSuccess},
		{Name: "lint", State: core.CheckSuccess},
	}}
	if got := p.Evaluate(obs); got.Verdict != core.VerdictWait || got.Criterion != "required_checks" {
		t.Fatalf("stale fallback names satisfied the policy: %+v", got)
	}
}

func TestLoadRejectsUnknownRequiredChecksMode(t *testing.T) {
	_, err := Load(writeConfig(t, "merge:\n  required_checks_mode: magic\n"))
	if err == nil {
		t.Fatal("Load accepted an unknown required_checks_mode")
	}
	if !strings.Contains(err.Error(), "merge.required_checks_mode") {
		t.Fatalf("error = %q, want the mode field in the message", err)
	}
}

func TestDetectRequiredChecksSkipsMatrixJobs(t *testing.T) {
	const workflow = "name: CI\njobs:\n" +
		"  verify:\n    runs-on: ubuntu-latest\n" +
		"  test-modules:\n    runs-on: ubuntu-latest\n" +
		"    strategy:\n      matrix:\n        module: [cmd, pkg]\n" +
		"  test:\n    needs: test-modules\n    runs-on: ubuntu-latest\n"
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ci.yml"), []byte(workflow), 0o600); err != nil {
		t.Fatal(err)
	}
	got := DetectRequiredChecks(root)
	want := []string{"test", "verify"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DetectRequiredChecks = %v, want %v", got, want)
	}
}

func TestDetectRequiredChecksSkipsMatrixJobWhenFailFastComesFirst(t *testing.T) {
	// fail-fast sits before matrix in the live CI file. A parser that leaves
	// strategy: on the first sibling key will miss the matrix and advertise a
	// job name GitHub never emits.
	const workflow = "name: CI\njobs:\n" +
		"  build:\n    runs-on: ubuntu-latest\n" +
		"  test-modules:\n    runs-on: ubuntu-latest\n" +
		"    strategy:\n      fail-fast: false\n      matrix:\n        module: [cmd, pkg]\n" +
		"  verify:\n    runs-on: ubuntu-latest\n"
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(dir): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ci.yml"), []byte(workflow), 0o600); err != nil {
		t.Fatalf("os.WriteFile(ci.yml): %v", err)
	}
	got := DetectRequiredChecks(root)
	want := []string{"build", "verify"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DetectRequiredChecks = %v, want %v", got, want)
	}
}

func TestResolveRequiredChecksUsesExplicitProjectRoot(t *testing.T) {
	// A config under <root>/.beacon must resolve workflows from <root>, not
	// from the config file's own directory or the process working directory.
	const workflow = "name: CI\njobs:\n  gate:\n    runs-on: ubuntu-latest\n"
	path := writeWorkflowConfig(t, "merge:\n  required_checks_mode: workflow\n", workflow)
	root := projectRootFromConfigPath(path)
	if root == filepath.Dir(path) {
		t.Fatalf("project root = %q, want the parent of .beacon", root)
	}
	want := []string{"gate"}
	if got := DetectRequiredChecks(root); !reflect.DeepEqual(got, want) {
		t.Fatalf("DetectRequiredChecks(%q) = %v, want %v", root, got, want)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load(path): %v", err)
	}
	if !reflect.DeepEqual(c.Merge.RequiredChecks, want) {
		t.Fatalf("required_checks = %v, want the workflow from the explicit root %v", c.Merge.RequiredChecks, want)
	}

	// A standalone config has no .beacon parent and resolves beside itself.
	standaloneDir := t.TempDir()
	standalone := filepath.Join(standaloneDir, "config.yaml")
	if err := os.WriteFile(standalone, []byte("merge:\n  required_checks_mode: workflow\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(standalone): %v", err)
	}
	writeProjectWorkflow(t, standaloneDir, workflow)
	if got := projectRootFromConfigPath(standalone); got != standaloneDir {
		t.Fatalf("standalone root = %q, want %q", got, standaloneDir)
	}
	sc, err := Load(standalone)
	if err != nil {
		t.Fatalf("Load(standalone): %v", err)
	}
	if !reflect.DeepEqual(sc.Merge.RequiredChecks, want) {
		t.Fatalf("standalone required_checks = %v, want %v", sc.Merge.RequiredChecks, want)
	}
}

func TestApplyDefaultsFillsUnsetValues(t *testing.T) {
	var c Config
	ApplyDefaults(&c)
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"state db", c.StateDB, ".beacon/state.db"},
		{"workspace dir", c.WorkspaceDir, ".beacon/worktrees"},
		{"base branch", c.BaseBranch, "main"},
		{"max attempts", c.Merge.MaxAttempts, 12},
		{"max auto rebases", c.Merge.MaxAutoRebases, DefaultMaxAutoRebases},
		{"max in flight", c.Concurrency.MaxInFlight, DefaultMaxInFlight},
		{"max active agents", c.Concurrency.MaxActiveAgents, DefaultMaxActiveAgents},
		{"on base red mode", c.OnBaseRed.Mode, "spawn_fixer"},
		{"max fix attempts", c.OnBaseRed.MaxFixAttempts, 2},
		{"worker instructions", c.Harness.WorkerInstructions, DefaultWorkerInstructions},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestApplyDefaultsKeepsExplicitValues(t *testing.T) {
	c := Config{
		StateDB:      "custom/state.db",
		WorkspaceDir: "custom/worktrees",
		BaseBranch:   "develop",
		PollInterval: "45s",
		Harness:      Harness{WorkerInstructions: "custom instructions"},
		Merge:        Merge{MaxAttempts: 7, MaxAutoRebases: 5},
		OnBaseRed:    OnBaseRed{Mode: "report_only", MaxFixAttempts: 4},
	}
	ApplyDefaults(&c)
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"state db", c.StateDB, "custom/state.db"},
		{"workspace dir", c.WorkspaceDir, "custom/worktrees"},
		{"base branch", c.BaseBranch, "develop"},
		{"poll interval", c.PollInterval, "45s"},
		{"worker instructions", c.Harness.WorkerInstructions, "custom instructions"},
		{"max attempts", c.Merge.MaxAttempts, 7},
		{"max auto rebases", c.Merge.MaxAutoRebases, 5},
		{"on base red mode", c.OnBaseRed.Mode, "report_only"},
		{"max fix attempts", c.OnBaseRed.MaxFixAttempts, 4},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestDefaultSourcesAgreeOnConcurrency(t *testing.T) {
	fromDefaults := defaults()
	var viaApply Config
	ApplyDefaults(&viaApply)
	if viaApply.Concurrency != fromDefaults.Concurrency {
		t.Fatalf("ApplyDefaults gave %+v, defaults gave %+v", viaApply.Concurrency, fromDefaults.Concurrency)
	}
	if fromDefaults.Concurrency.MaxInFlight != DefaultMaxInFlight ||
		fromDefaults.Concurrency.MaxActiveAgents != DefaultMaxActiveAgents {
		t.Fatalf("defaults concurrency = %+v, want %d/%d", fromDefaults.Concurrency,
			DefaultMaxInFlight, DefaultMaxActiveAgents)
	}
}

func writeProjectWorkflowNamed(t *testing.T, root, name, workflow string) {
	t.Helper()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(.github/workflows): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(workflow), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%s): %v", name, err)
	}
}

func TestLoadRejectsUnreportableRequiredCheckFallback(t *testing.T) {
	const workflow = "name: CI\njobs:\n  verify:\n    runs-on: ubuntu-latest\n" +
		"  test-modules:\n    runs-on: ubuntu-latest\n" +
		"    strategy:\n      matrix:\n        module: [cmd, pkg]\n"
	path := writeWorkflowConfig(t,
		"merge:\n  required_checks_mode: workflow\n  required_checks: [verify, test-modules]\n",
		workflow)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted a matrix parent name in the fallback")
	}
	for _, want := range []string{"merge.required_checks", "test-modules", "never reports"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

func TestLoadRejectsReusableWorkflowJobInFallback(t *testing.T) {
	root := filepath.Dir(filepath.Dir(writeWorkflowConfig(t, "", "")))
	writeProjectWorkflowNamed(t, root, "ci.yml", "name: CI\njobs:\n  verify:\n    runs-on: ubuntu-latest\n")
	writeProjectWorkflowNamed(t, root, "reusable.yml", "name: Reusable\non:\n  workflow_call:\n"+
		"jobs:\n  inner:\n    runs-on: ubuntu-latest\n")
	path := filepath.Join(root, ".beacon", "config.yaml")
	body := "merge:\n  required_checks_mode: workflow\n  required_checks: [inner]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("os.WriteFile(config.yaml): %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted a reusable workflow inner job in the fallback")
	}
	if !strings.Contains(err.Error(), "inner") {
		t.Fatalf("error = %q, want the rejected name", err)
	}
}

func TestLoadAcceptsReportableManualFallback(t *testing.T) {
	const workflow = "name: CI\njobs:\n  verify:\n    runs-on: ubuntu-latest\n" +
		"  test-modules:\n    runs-on: ubuntu-latest\n" +
		"    strategy:\n      matrix:\n        module: [cmd, pkg]\n"
	path := writeWorkflowConfig(t, "merge:\n  required_checks: [verify, test]\n", workflow)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load(path): %v", err)
	}
	if want := []string{"verify", "test"}; !reflect.DeepEqual(c.Merge.RequiredChecks, want) {
		t.Fatalf("required_checks = %v, want %v", c.Merge.RequiredChecks, want)
	}
}
