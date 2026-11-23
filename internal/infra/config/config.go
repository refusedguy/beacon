// Package config loads the single .beacon/config.yaml file.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

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
}

type Concurrency struct {
	MaxInFlight     int `yaml:"max_in_flight"`
	MaxActiveAgents int `yaml:"max_active_agents"`
}

type TaskSource struct {
	Provider string `yaml:"provider"`
	Dir      string `yaml:"dir"`
	Owner    string `yaml:"owner"`
	Repo     string `yaml:"repo"`
	TokenEnv string `yaml:"token_env"`
}

type SCM struct {
	Provider string `yaml:"provider"`
	Owner    string `yaml:"owner"`
	Repo     string `yaml:"repo"`
	TokenEnv string `yaml:"token_env"`
	Token    string `yaml:"token"`
	BaseURL  string `yaml:"base_url"`
}

type Harness struct {
	Provider        string `yaml:"provider"`
	Endpoint        string `yaml:"endpoint"`
	Model           string `yaml:"model"`
	Command         string `yaml:"command"`
	PromptTemplate  string `yaml:"prompt_template"`
	TokenEnv        string `yaml:"token_env"`
	Token           string `yaml:"token"`
	ParentSessionID string `yaml:"parent_session_id"`
}

type Merge struct {
	RequiredChecks    []string `yaml:"required_checks"`
	RequiredApprovals int      `yaml:"required_approvals"`
	Labels            []string `yaml:"labels"`
	CommandGate       string   `yaml:"command_gate"`
	AutoRebase        bool     `yaml:"auto_rebase"`
	MaxAttempts       int      `yaml:"max_attempts"`
}

type OnBaseRed struct {
	Mode           string `yaml:"mode"`
	MaxFixAttempts int    `yaml:"max_fix_attempts"`
}

type Webhook struct {
	Enabled   bool   `yaml:"enabled"`
	Addr      string `yaml:"addr"`
	Secret    string `yaml:"secret"`
	SecretEnv string `yaml:"secret_env"`
}

type Notify struct {
	TelegramToken    string `yaml:"telegram_token"`
	TelegramTokenEnv string `yaml:"telegram_token_env"`
	ChatID           string `yaml:"chat_id"`
}

func (c Config) Interval() time.Duration {
	if c.PollInterval == "" {
		return 30 * time.Second
	}
	d, err := time.ParseDuration(c.PollInterval)
	if err != nil {
		return 30 * time.Second
	}
	return d
}

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
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("config %s: %w", path, err)
	}
	expandNonSecrets(&c)
	c.Webhook.Secret = literalOrEnv(c.Webhook.Secret, c.Webhook.SecretEnv)
	c.Notify.TelegramToken = literalOrEnv(c.Notify.TelegramToken, c.Notify.TelegramTokenEnv)
	c.SCM.Token = literalOrEnv(c.SCM.Token, c.SCM.TokenEnv)
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
		c.Merge.MaxAttempts = 3
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
	return c, nil
}

func literalOrEnv(literal, envName string) string {
	if envName != "" {
		return os.Getenv(envName)
	}
	return literal
}

func defaults() Config {
	return Config{
		StateDB:      ".beacon/state.db",
		WorkspaceDir: ".beacon/worktrees",
		BaseBranch:   "main",
		PollInterval: "30s",
		Concurrency:  Concurrency{MaxInFlight: 20, MaxActiveAgents: 4},
		TaskSource:   TaskSource{Provider: "markdown", Dir: "docs/tasks"},
		SCM:          SCM{Provider: "github", TokenEnv: "GITHUB_TOKEN"},
		Harness:      Harness{Provider: "opencode", Endpoint: "http://127.0.0.1:4096"},
		Merge:        Merge{MaxAttempts: 3, AutoRebase: true},
		OnBaseRed:    OnBaseRed{Mode: "spawn_fixer", MaxFixAttempts: 2},
	}
}

// expandNonSecrets expands ${VAR} only in fields that are not secret literals.
func expandNonSecrets(v any) {
	rv := reflect.ValueOf(v).Elem()
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Field(i)
		switch f.Kind() {
		case reflect.String:
			if !strings.Contains(f.String(), "${") {
				continue
			}
			f.SetString(os.ExpandEnv(f.String()))
		case reflect.Struct:
			expandNonSecretsAddr(f.Addr().Interface())
		}
	}
}

func expandNonSecretsAddr(v any) {
	rv := reflect.ValueOf(v).Elem()
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Field(i)
		switch f.Kind() {
		case reflect.String:
			if strings.Contains(f.String(), "${") {
				f.SetString(os.ExpandEnv(f.String()))
			}
		case reflect.Struct:
			expandNonSecretsAddr(f.Addr().Interface())
		case reflect.Slice:
			for j := 0; j < f.Len(); j++ {
				e := f.Index(j)
				if e.Kind() == reflect.String && strings.Contains(e.String(), "${") {
					e.SetString(os.ExpandEnv(e.String()))
				}
			}
		}
	}
}

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
