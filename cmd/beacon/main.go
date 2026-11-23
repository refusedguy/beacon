// Command beacon is the composition root of the controller.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/refusedguy/beacon/harnesses/opencode"
	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/internal/infra/harness"
	"github.com/refusedguy/beacon/internal/infra/notify"
	"github.com/refusedguy/beacon/internal/infra/planner"
	"github.com/refusedguy/beacon/internal/infra/policy"
	"github.com/refusedguy/beacon/internal/infra/scm"
	"github.com/refusedguy/beacon/internal/infra/shared"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/internal/infra/tasksource"
	"github.com/refusedguy/beacon/internal/infra/workspace"
	"github.com/refusedguy/beacon/pkg/core"
)

type wiring struct {
	cfg        config.Config
	store      *sqlite.Store
	source     core.TaskSource
	harness    core.AgentHarness
	workspace  *workspace.GitWorktree
	scm        core.ScmGateway
	policy     core.MergePolicy
	changes    chan int
	dispatcher *app.Dispatcher
	control    *app.Control
	status     app.StatusService
	wait       app.WaitService
	recon      *app.Reconciler
}

func wire(path, parentSession string) (*wiring, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	scmToken := os.Getenv(cfg.SCM.TokenEnv)
	sourceToken := os.Getenv(cfg.TaskSource.TokenEnv)
	if sourceToken == "" {
		sourceToken = scmToken
	}
	harnessToken := os.Getenv(cfg.Harness.TokenEnv)
	store, err := sqlite.Open(cfg.StateDB)
	if err != nil {
		return nil, err
	}
	source, err := tasksource.New(tasksource.Config{
		Provider: cfg.TaskSource.Provider, Dir: cfg.TaskSource.Dir,
		Owner: cfg.SCM.Owner, Repo: cfg.SCM.Repo, GitlabURL: cfg.SCM.BaseURL,
	}, sourceToken)
	if err != nil {
		return nil, err
	}
	scmGW, err := scm.New(scm.Config{
		Provider: cfg.SCM.Provider, Owner: cfg.SCM.Owner, Repo: cfg.SCM.Repo,
		BaseURL: cfg.SCM.BaseURL, BaseBranch: cfg.BaseBranch,
	}, scmToken)
	if err != nil {
		return nil, err
	}
	h, err := newHarness(cfg, harnessToken, parentSession)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(".")
	if err != nil {
		return nil, err
	}
	wt, err := workspace.New(root, cfg.BaseBranch, cfg.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	changes := make(chan int, 64)
	disp := &app.Dispatcher{
		Source:    source,
		Planner:   planner.New(planner.Options{ByPath: true, ByDependency: true, Strict: cfg.StrictScope}),
		Workspace: wt, Harness: h, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: cfg.Concurrency.MaxInFlight, MaxActiveAgents: cfg.Concurrency.MaxActiveAgents},
		StrictScope: cfg.StrictScope,
		MaxAttempts: cfg.Merge.MaxAttempts,
	}
	ctrl := &app.Control{Store: store, Harness: h, Workspace: wt, OnChange: func(pr int) {
		select {
		case changes <- pr:
		default:
		}
	}}
	recon := &app.Reconciler{
		Store: store, SCM: scmGW, Policy: policy.New(policy.Config{
			RequiredChecks: cfg.Merge.RequiredChecks, RequiredApprovals: cfg.Merge.RequiredApprovals,
			Labels: cfg.Merge.Labels, CommandGate: cfg.Merge.CommandGate,
		}), Harness: h,
		Source: source, Workspace: wt,
		Notifier:   notify.NewTelegram(cfg.Notify.TelegramToken, cfg.Notify.ChatID),
		Dispatcher: disp, Changes: changes, Repo: cfg.SCM.Owner + "/" + cfg.SCM.Repo,
		BaseBranch: cfg.BaseBranch, BaseRedMode: cfg.OnBaseRed.Mode,
		MaxFixTries: cfg.OnBaseRed.MaxFixAttempts, ParentSessionID: parentSession,
		Interval:   cfg.Interval(),
		AutoRebase: cfg.Merge.AutoRebase,
	}
	return &wiring{
		cfg: cfg, store: store, source: source, harness: h, workspace: wt,
		scm: scmGW, policy: recon.Policy, changes: changes, dispatcher: disp,
		control: ctrl, status: app.StatusService{Store: store},
		wait: app.WaitService{Store: store, Interval: time.Second}, recon: recon,
	}, nil
}

func newHarness(cfg config.Config, token, parent string) (core.AgentHarness, error) {
	switch cfg.Harness.Provider {
	case "opencode", "":
		h := opencode.New(cfg.Harness.Endpoint, cfg.Harness.Model, cfg.Harness.Command, token, firstNonEmpty(parent, cfg.Harness.ParentSessionID))
		return h, nil
	default:
		return harness.New(harness.Config{
			Provider: cfg.Harness.Provider, Command: cfg.Harness.Command,
			PromptTemplate: cfg.Harness.PromptTemplate,
		})
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (w *wiring) close() { _ = w.store.Close() }

func main() {
	slog := shared.NewLogger(os.Getenv("BEACON_LOG_LEVEL"))
	_ = slog
	root := &cobra.Command{Use: "beacon", Short: "Parallel AI agent controller"}
	configPath := root.PersistentFlags().String("config", "", "path to config.yaml")

	root.AddCommand(newInitCommand())
	root.AddCommand(newDispatchCommand(configPath))
	root.AddCommand(newStatusCommand(configPath))
	root.AddCommand(newMCPCommand(configPath))
	root.AddCommand(newServeCommand(configPath))
	root.AddCommand(newGCCommand(configPath))

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
