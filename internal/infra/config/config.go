// Package config loads the single .beacon/config.yaml file.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/shared"
	"github.com/refusedguy/beacon/pkg/core"
	"gopkg.in/yaml.v3"
)

// Required check sources. Manual keeps the explicit merge.required_checks
// list. Workflow resolves the names from .github/workflows on every load.
const (
	RequiredChecksManual   = "manual"
	RequiredChecksWorkflow = "workflow"
)

// DefaultMaxAutoRebases is the automatic rebase cap used when
// merge.max_auto_rebases is unset. It shares the reconciler's exported
// fallback so the configured and test-built paths cannot drift.
const DefaultMaxAutoRebases = app.DefaultMaxAutoRebases

// defaultPollInterval is the silent fallback for code-built configs that
// never passed through Load.
const defaultPollInterval = 30 * time.Second

// DefaultWorkerInstructions is prepended to worker spawn prompts when the
// operator does not configure harness.worker_instructions. It combines the
// shared weak-machine verification rule with the MCP-only tool rule, and stays
// language agnostic so no stack is told to run another stack's test command.
const DefaultWorkerInstructions = core.WeakMachineVerificationRule + " Use the beacon_* MCP tools only. Never locate, build or execute the beacon binary or cmd/beacon source from a shell."

// Config holds every setting loaded from .beacon/config.yaml.
type Config struct {
	StateDB      string      `yaml:"state_db"`
	WorkspaceDir string      `yaml:"workspace_dir"`
	BaseBranch   string      `yaml:"base_branch"`
	PollInterval string      `yaml:"poll_interval"`
	StrictScope  bool        `yaml:"strict_scope"`
	Concurrency  Concurrency `yaml:"concurrency"`
	TaskSource   TaskSource  `yaml:"task_source"`
	SCM          SCM         `yaml:"scm"`
	Harness      Harness     `yaml:"harness"`
	Merge        Merge       `yaml:"merge"`
	OnBaseRed    OnBaseRed   `yaml:"on_base_red"`
	Webhook      Webhook     `yaml:"webhook"`
	Notify       Notify      `yaml:"notify"`

	// interval is the parsed PollInterval. Load sets it once so readers of a
	// loaded config never parse the same value again. Code-built configs leave
	// it zero and Interval falls back to the silent default.
	interval time.Duration
}

// Concurrency bounds the daemon's concurrent work.
type Concurrency struct {
	MaxInFlight     int `yaml:"max_in_flight"`
	MaxActiveAgents int `yaml:"max_active_agents"`
}

// TaskSource selects where the daemon reads task cards.
type TaskSource struct {
	Provider string `yaml:"provider"`
	Dir      string `yaml:"dir"`
	Owner    string `yaml:"owner"`
	Repo     string `yaml:"repo"`
	TokenEnv string `yaml:"token_env"`
}

// SCM holds the source control provider settings.
type SCM struct {
	Provider string `yaml:"provider"`
	Owner    string `yaml:"owner"`
	Repo     string `yaml:"repo"`
	TokenEnv string `yaml:"token_env"`
	Token    string `yaml:"token"`
	BaseURL  string `yaml:"base_url"`
}

// Harness holds the shared settings any harness may read. The config package
// does not name a provider or supply provider-specific values. Each harness
// adapter interprets the fields it needs, and the composition root maps an
// empty Provider to its default harness.
type Harness struct {
	Provider           string `yaml:"provider"`
	Endpoint           string `yaml:"endpoint"`
	Model              string `yaml:"model"`
	Command            string `yaml:"command"`
	PromptTemplate     string `yaml:"prompt_template"`
	TokenEnv           string `yaml:"token_env"`
	Token              string `yaml:"token"`
	ParentSessionID    string `yaml:"parent_session_id"`
	WorkerInstructions string `yaml:"worker_instructions"`
}

// Merge holds the automerge settings. RequiredChecksMode selects where the
// required check names come from. An empty mode means manual. MaxAutoRebases
// caps automatic branch updates separately from MaxAttempts.
type Merge struct {
	RequiredChecksMode string   `yaml:"required_checks_mode"`
	RequiredChecks     []string `yaml:"required_checks"`
	RequiredApprovals  int      `yaml:"required_approvals"`
	Labels             []string `yaml:"labels"`
	CommandGate        string   `yaml:"command_gate"`
	AutoRebase         bool     `yaml:"auto_rebase"`
	MaxAttempts        int      `yaml:"max_attempts"`
	MaxAutoRebases     int      `yaml:"max_auto_rebases"`
}

// OnBaseRed configures the response to a failing base branch.
type OnBaseRed struct {
	Mode           string `yaml:"mode"`
	MaxFixAttempts int    `yaml:"max_fix_attempts"`
}

// Webhook holds the webhook server settings.
type Webhook struct {
	Enabled   bool   `yaml:"enabled"`
	Addr      string `yaml:"addr"`
	Secret    string `yaml:"secret"`
	SecretEnv string `yaml:"secret_env"`
}

// Notify holds the Telegram notification settings.
type Notify struct {
	TelegramToken    string `yaml:"telegram_token"`
	TelegramTokenEnv string `yaml:"telegram_token_env"`
	ChatID           string `yaml:"chat_id"`
}

// Complete reports whether the config has enough settings to keep the daemon
// running. A hot reload rejects configs that fail this check so a truncated or
// empty file cannot replace the last good config.
func (c Config) Complete() bool {
	if c.StateDB == "" || c.WorkspaceDir == "" || c.BaseBranch == "" || c.Concurrency.MaxInFlight <= 0 {
		return false
	}
	if c.TaskSource.Provider == "" && c.TaskSource.Dir == "" {
		return false
	}
	if c.SCM.Provider == "" || c.SCM.Owner == "" || c.SCM.Repo == "" {
		return false
	}
	_, err := parseInterval(c.PollInterval)
	return err == nil
}

// parseInterval resolves the poll interval and returns the ParseDuration error
// for a malformed value. Load rejects a bad value at the boundary so the
// daemon never runs with a silently substituted interval.
func parseInterval(value string) (time.Duration, error) {
	if value == "" {
		return defaultPollInterval, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	return d, nil
}

// Interval returns the configured poll interval. Load parses PollInterval once
// and stores the result, so a loaded config does not parse it again. The
// fallback applies only to configs built directly in code.
func (c Config) Interval() time.Duration {
	if c.interval != 0 {
		return c.interval
	}
	d, err := parseInterval(c.PollInterval)
	if err != nil {
		return defaultPollInterval
	}
	return d
}

// RepoSlug returns the owner/repo pair used in notifications and snapshots.
func (c Config) RepoSlug() string {
	return c.SCM.Owner + "/" + c.SCM.Repo
}

// ValidateWebhook checks the shared webhook secret rule. An enabled webhook
// needs a literal secret or a secret_env name that resolves to one. Config
// load, serve and doctor call this instead of restating the rule.
func ValidateWebhook(w Webhook) error {
	if w.Enabled && w.Secret == "" {
		return fmt.Errorf("webhook.enabled requires webhook.secret or webhook.secret_env")
	}
	return nil
}

// validate rejects a config that cannot drive the daemon. An absent SCM
// section is left to Complete to reject on reload; a partial one fails here
// with a clear message. Load parses PollInterval once and passes the result
// here, so a malformed value surfaces the ParseDuration error instead of
// silently falling back, and the accepted duration is kept for Interval.
func (c *Config) validate(interval time.Duration, intervalErr error) error {
	set := 0
	for _, field := range []string{c.SCM.Provider, c.SCM.Owner, c.SCM.Repo} {
		if field != "" {
			set++
		}
	}
	if set > 0 && set < 3 {
		return fmt.Errorf("scm requires provider, owner and repo together")
	}
	if intervalErr != nil {
		return fmt.Errorf("invalid poll_interval %q: %w", c.PollInterval, intervalErr)
	}
	switch c.Merge.RequiredChecksMode {
	case "", RequiredChecksManual, RequiredChecksWorkflow:
	default:
		return fmt.Errorf("invalid merge.required_checks_mode %q", c.Merge.RequiredChecksMode)
	}
	c.interval = interval
	return nil
}

// Load reads the config at path and fills in defaults.
func Load(path string) (Config, error) {
	var c Config
	if path == "" {
		path = ".beacon/config.yaml"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaults(), nil
		}
		return c, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return defaults(), nil
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("config %s: %w", path, err)
	}
	expandNonSecrets(&c)
	c.Webhook.Secret = LiteralOrEnv(c.Webhook.Secret, c.Webhook.SecretEnv)
	if err := ValidateWebhook(c.Webhook); err != nil {
		return c, fmt.Errorf("config %s: %w", path, err)
	}
	c.Notify.TelegramToken = LiteralOrEnv(c.Notify.TelegramToken, c.Notify.TelegramTokenEnv)
	c.SCM.Token = LiteralOrEnv(c.SCM.Token, c.SCM.TokenEnv)
	interval, intervalErr := parseInterval(c.PollInterval)
	if err := c.validate(interval, intervalErr); err != nil {
		return c, fmt.Errorf("config %s: %w", path, err)
	}
	ApplyDefaults(&c)
	resolveRequiredChecks(&c, path)
	return c, nil
}

// ApplyDefaults fills unset scalar settings. Load and beacon init both call
// it, so a generated config and a loaded config share the same values. It
// never overwrites a value the operator or init supplied explicitly.
func ApplyDefaults(c *Config) {
	if c.StateDB == "" {
		c.StateDB = ".beacon/state.db"
	}
	if c.WorkspaceDir == "" {
		c.WorkspaceDir = ".beacon/worktrees"
	}
	if c.BaseBranch == "" {
		c.BaseBranch = "main"
	}
	if c.Merge.MaxAttempts <= 0 {
		c.Merge.MaxAttempts = 12
	}
	if c.Merge.MaxAutoRebases <= 0 {
		c.Merge.MaxAutoRebases = DefaultMaxAutoRebases
	}
	if c.Concurrency.MaxInFlight <= 0 {
		c.Concurrency.MaxInFlight = 20
	}
	if c.Concurrency.MaxActiveAgents < 0 {
		c.Concurrency.MaxActiveAgents = 0
	}
	if c.OnBaseRed.Mode == "" {
		c.OnBaseRed.Mode = "spawn_fixer"
	}
	if c.OnBaseRed.MaxFixAttempts <= 0 {
		c.OnBaseRed.MaxFixAttempts = 2
	}
	if c.Harness.WorkerInstructions == "" {
		c.Harness.WorkerInstructions = DefaultWorkerInstructions
	}
}

// resolveRequiredChecks replaces the configured names with the job names in
// .github/workflows when the config selects workflow mode. A manual config, or
// a workflow directory with no job names, keeps the explicit list so an
// operator always has a working fallback.
func resolveRequiredChecks(c *Config, path string) {
	if c.Merge.RequiredChecksMode != RequiredChecksWorkflow {
		return
	}
	detected := DetectRequiredChecks(projectRootFromConfigPath(path))
	if len(detected) > 0 {
		c.Merge.RequiredChecks = detected
	}
}

// projectRootFromConfigPath resolves the repository root from a config path.
// A config at <root>/.beacon/config.yaml maps to <root>. Any other location
// maps to the config file's own directory, so a standalone config still reads
// workflows that sit beside it.
func projectRootFromConfigPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Dir(path)
	}
	dir := filepath.Dir(abs)
	if filepath.Base(dir) == ".beacon" {
		return filepath.Dir(dir)
	}
	return dir
}

// DetectRequiredChecks collects job names from .github/workflows/*.yml. It
// delegates to the shared parser so beacon init and config load cannot drift
// from the workflow check source.
func DetectRequiredChecks(root string) []string {
	return shared.DetectWorkflowJobNames(root)
}

// LiteralOrEnv returns the environment value named by envName when it is set,
// else literal. Callers that read a secret or token share this resolver so the
// precedence is the same in config, serve and doctor.
func LiteralOrEnv(literal, envName string) string {
	if v := os.Getenv(envName); v != "" {
		return v
	}
	return literal
}

// defaults returns the config used when no config file exists.
func defaults() Config {
	return Config{
		StateDB:      ".beacon/state.db",
		WorkspaceDir: ".beacon/worktrees",
		BaseBranch:   "main",
		PollInterval: "30s",
		interval:     defaultPollInterval,
		Concurrency:  Concurrency{MaxInFlight: 20, MaxActiveAgents: 4},
		TaskSource:   TaskSource{Provider: "markdown", Dir: "docs/tasks"},
		SCM:          SCM{Provider: "github", TokenEnv: "GITHUB_TOKEN"},
		Harness:      Harness{WorkerInstructions: DefaultWorkerInstructions},
		Merge:        Merge{MaxAttempts: 12, MaxAutoRebases: DefaultMaxAutoRebases, AutoRebase: true},
		OnBaseRed:    OnBaseRed{Mode: "spawn_fixer", MaxFixAttempts: 2},
	}
}

// expandNonSecrets resolves ${VAR} in ordinary settings. Secret values are
// deliberately excluded: a token or secret is taken literally, or from the
// environment variable named by its paired *_env field. A literal such as
// "p${word}" stays verbatim so an unrelated env var cannot rewrite a secret.
// Secret fields: SCM.Token, Harness.Token, Webhook.Secret,
// Notify.TelegramToken.
func expandNonSecrets(c *Config) {
	c.StateDB = expandEnv(c.StateDB)
	c.WorkspaceDir = expandEnv(c.WorkspaceDir)
	c.BaseBranch = expandEnv(c.BaseBranch)
	c.PollInterval = expandEnv(c.PollInterval)
	c.TaskSource.Provider = expandEnv(c.TaskSource.Provider)
	c.TaskSource.Dir = expandEnv(c.TaskSource.Dir)
	c.TaskSource.Owner = expandEnv(c.TaskSource.Owner)
	c.TaskSource.Repo = expandEnv(c.TaskSource.Repo)
	c.TaskSource.TokenEnv = expandEnv(c.TaskSource.TokenEnv)
	c.SCM.Provider = expandEnv(c.SCM.Provider)
	c.SCM.Owner = expandEnv(c.SCM.Owner)
	c.SCM.Repo = expandEnv(c.SCM.Repo)
	c.SCM.TokenEnv = expandEnv(c.SCM.TokenEnv)
	c.SCM.BaseURL = expandEnv(c.SCM.BaseURL)
	c.Harness.Provider = expandEnv(c.Harness.Provider)
	c.Harness.Endpoint = expandEnv(c.Harness.Endpoint)
	c.Harness.Model = expandEnv(c.Harness.Model)
	c.Harness.Command = expandEnv(c.Harness.Command)
	c.Harness.PromptTemplate = expandEnv(c.Harness.PromptTemplate)
	c.Harness.TokenEnv = expandEnv(c.Harness.TokenEnv)
	c.Harness.ParentSessionID = expandEnv(c.Harness.ParentSessionID)
	c.Harness.WorkerInstructions = expandEnv(c.Harness.WorkerInstructions)
	c.Merge.RequiredChecksMode = expandEnv(c.Merge.RequiredChecksMode)
	for i := range c.Merge.RequiredChecks {
		c.Merge.RequiredChecks[i] = expandEnv(c.Merge.RequiredChecks[i])
	}
	c.Merge.CommandGate = expandEnv(c.Merge.CommandGate)
	for i := range c.Merge.Labels {
		c.Merge.Labels[i] = expandEnv(c.Merge.Labels[i])
	}
	c.OnBaseRed.Mode = expandEnv(c.OnBaseRed.Mode)
	c.Webhook.Addr = expandEnv(c.Webhook.Addr)
	c.Webhook.SecretEnv = expandEnv(c.Webhook.SecretEnv)
	c.Notify.TelegramTokenEnv = expandEnv(c.Notify.TelegramTokenEnv)
	c.Notify.ChatID = expandEnv(c.Notify.ChatID)
}

// expandEnv keeps the original trigger: only ${VAR} references are expanded,
// so a bare $ in a template or path stays literal.
func expandEnv(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return os.ExpandEnv(s)
}

// WriteDefault writes c to path, refusing to overwrite unless force is set.
func WriteDefault(path string, c Config, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists; use --force", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
