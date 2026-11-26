package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/internal/infra/workspace"
	"github.com/refusedguy/beacon/pkg/core"
)

// envOr returns the non-empty value of the environment variable named key,
// else fallback. The key stays at the read site so every config name remains
// visible where it is used.
func envOr(key, fallback string) string {
	return config.EnvOr(key, fallback)
}

// envString keeps the former name for callers outside this task's scope.
func envString(key, fallback string) string {
	return envOr(key, fallback)
}

// parentSessionFromEnv returns the orchestrator session carried by a CLI
// process. serve and mcp read the same variable, so dispatch and control must
// read it too or CLI-launched runs lose their merge notifications.
func parentSessionFromEnv() string {
	return envOr("BEACON_PARENT_SESSION", "")
}

// wireFn builds the wiring for a config path and a parent session. Commands
// receive one explicitly so tests can supply their own without mutating a
// package-level variable.
type wireFn func(path, parentSession string) (*wiring, error)

// wireCLI wires a CLI command with the parent session from the environment.
func wireCLI(fn wireFn, path string) (*wiring, error) {
	return fn(path, parentSessionFromEnv())
}

func wireReadOnly(path string) (config.Config, *sqlite.Store, *workspace.GitWorktree, error) {
	cfg, root, err := loadConfig(path)
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	store, err := sqlite.Open(cfg.StateDB)
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	wt, err := workspace.New(root, cfg.BaseBranch, cfg.WorkspaceDir)
	if err != nil {
		store.Close()
		return config.Config{}, nil, nil, err
	}
	return cfg, store, wt, nil
}

// daemonSnapshot converts the CLI's daemon liveness record into the app-level
// snapshot type. The daemon files stay owned by the composition root.
func daemonSnapshot(status daemonStatus) *app.DaemonSnapshot {
	out := &app.DaemonSnapshot{
		State: status.State, PID: status.PID,
		HeartbeatAgeSeconds: status.HeartbeatAgeSecond,
	}
	if status.Error != nil {
		out.Error = status.Error.Message
	}
	return out
}

// stateDir returns the directory holding the state database, the daemon lock,
// the daemon status record and the connection file.
func stateDir(cfg config.Config) string {
	return filepath.Dir(cfg.StateDB)
}

// liveDaemon returns the connection record of a daemon that answers on its
// loopback address. It reports false when no daemon owns the state database.
func liveDaemon(cfg config.Config) (*connectionInfo, bool) {
	info, err := readConnection(stateDir(cfg))
	if err != nil {
		logDiscard("read daemon connection", err)
		return nil, false
	}
	if !daemonAlive(info) {
		return nil, false
	}
	return info, true
}

func newDispatchCommand(configPath *string, fn wireFn) *cobra.Command {
	return &cobra.Command{
		Use:   "dispatch [n]",
		Short: "Start up to n non-overlapping tasks",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 1
			if len(args) == 1 {
				if _, err := fmt.Sscan(args[0], &n); err != nil {
					return err
				}
			}
			w, err := wireCLI(fn, *configPath)
			if err != nil {
				return err
			}
			defer w.close()
			res, err := w.dispatcher.Dispatch(cmd.Context(), n)
			if err != nil {
				return err
			}
			return printJSON(res)
		},
	}
}

// statusFlags holds the values parsed from the status command flags.
type statusFlags struct {
	recent         bool
	snapshot       bool
	recentLimit    int
	decisionsLimit int
	daemon         bool
}

func newStatusCommand(configPath *string) *cobra.Command {
	f := &statusFlags{}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Print active runs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if f.daemon {
				cfg, _, err := loadConfig(*configPath)
				if err != nil {
					return err
				}
				return printJSON(readDaemonStatus(stateDir(cfg)))
			}
			cfg, store, _, err := wireReadOnly(*configPath)
			if err != nil {
				return err
			}
			defer store.Close()
			if f.snapshot {
				result, err := (app.SnapshotService{
					Store: store, RecentLimit: f.recentLimit, DecisionsLimit: f.decisionsLimit,
					Repo:   cfg.RepoSlug(),
					Daemon: daemonSnapshot(readDaemonStatus(stateDir(cfg))),
				}).Snapshot(cmd.Context())
				if err != nil {
					return err
				}
				return printJSON(result)
			}
			if f.recent {
				runs, err := (app.StatusService{Store: store}).Recent(cmd.Context(), 0)
				if err != nil {
					return err
				}
				return printJSON(runs)
			}
			runs, err := store.ListActive(cmd.Context())
			if err != nil {
				return err
			}
			return printJSON(runs)
		},
	}
	cmd.Flags().BoolVar(&f.recent, "recent", false, "print recently finished runs instead of active ones")
	cmd.Flags().BoolVar(&f.snapshot, "snapshot", false, "print one batched snapshot with runs, history and metrics")
	cmd.Flags().IntVar(&f.recentLimit, "recent-limit", 0, "cap finished runs in a snapshot (0 uses the default)")
	cmd.Flags().IntVar(&f.decisionsLimit, "decisions-limit", 0, "cap decisions per run and run-less decisions in a snapshot (0 uses the default)")
	cmd.Flags().BoolVar(&f.daemon, "daemon", false, "print daemon liveness and heartbeat age instead of runs")
	return cmd
}

func newGCCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "gc",
		Short: "Remove worktrees that no active run uses",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, store, wt, err := wireReadOnly(*configPath)
			if err != nil {
				return err
			}
			defer store.Close()
			runs, err := store.ListActive(cmd.Context())
			if err != nil {
				return err
			}
			inUse := make(map[string]bool)
			for _, r := range runs {
				if r.WorkspacePath != "" {
					inUse[filepath.Clean(r.WorkspacePath)] = true
				}
			}
			entries, err := os.ReadDir(cfg.WorkspaceDir)
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				path := filepath.Join(cfg.WorkspaceDir, e.Name())
				if inUse[filepath.Clean(path)] {
					continue
				}
				if err := wt.ForceRemove(cmd.Context(), core.Workspace{Path: path}); err != nil {
					fmt.Fprintln(os.Stderr, "gc:", err)
				}
			}
			return nil
		},
	}
}

// controlResult is the stable JSON contract printed by beacon control on success.
type controlResult struct {
	OK     bool   `json:"ok"`
	Action string `json:"action"`
	RunID  string `json:"run_id"`
}

type controlRequest struct {
	Action  string `json:"action"`
	RunID   string `json:"run_id"`
	SHA     string `json:"sha"`
	PR      int    `json:"pr,omitempty"`
	Message string `json:"message,omitempty"`
}

// controlMessage picks the optional message used by nudge. The variadic form
// keeps older callers of the control helpers unchanged.
func controlMessage(messages []string) string {
	if len(messages) == 0 {
		return ""
	}
	return messages[0]
}

// controlFlags holds the values parsed from the control command flags. The
// json field stays bound because older scripts still pass the flag.
type controlFlags struct {
	action  string
	runID   string
	sha     string
	pr      int
	message string
	json    bool
}

// newControlCommand freezes the CLI contract. The json flag is accepted for
// scripting compatibility; a successful control action always prints JSON.
func newControlCommand(configPath *string, fn wireFn) *cobra.Command {
	f := &controlFlags{}
	cmd := &cobra.Command{
		Use:   "control",
		Short: "Control a run: " + strings.Join(core.ControlActionNames(), ", "),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateControl(f.action, f.runID, f.message); err != nil {
				return err
			}
			if err := dispatchControlWithPR(cmd.Context(), fn, *configPath, f.action, f.runID, f.sha, f.pr, f.message); err != nil {
				return err
			}
			return printJSON(controlResult{OK: true, Action: f.action, RunID: f.runID})
		},
	}
	cmd.Flags().StringVar(&f.action, "action", "", "one of "+strings.Join(core.ControlActionNames(), ", "))
	cmd.Flags().StringVar(&f.runID, "run-id", "", "run ID to control")
	cmd.Flags().StringVar(&f.sha, "sha", "", "observed SHA used by an approve action")
	cmd.Flags().IntVar(&f.pr, "pr", 0, "PR number bound by a watch action")
	cmd.Flags().StringVar(&f.message, "message", "", "guidance sent to the worker by a nudge action")
	cmd.Flags().BoolVar(&f.json, "json", false, "print a machine readable result")
	return cmd
}

func validateControl(action, runID string, message ...string) error {
	parsed := core.ParseControlAction(action)
	if !parsed.Valid() {
		return fmt.Errorf("unknown action %q", action)
	}
	if runID == "" {
		return errors.New("run-id is required")
	}
	if parsed == core.ControlNudge && controlMessage(message) == "" {
		return errors.New("message is required for nudge")
	}
	return nil
}

// dispatchControlWithPR forwards to the daemon when it owns the state database
// and connection file. Without a live daemon it applies the control directly.
func dispatchControlWithPR(ctx context.Context, fn wireFn, configPath, action, runID, sha string, pr int, message ...string) error {
	cfg, _, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if info, ok := liveDaemon(cfg); ok {
		return postControlWithPR(ctx, info, action, runID, sha, pr, message...)
	}
	w, err := wireCLI(fn, configPath)
	if err != nil {
		return err
	}
	defer w.close()
	return applyControlWithPR(ctx, w.control, action, runID, sha, pr, message...)
}

// controlPort is the single verb the command drives. Every action other than
// watch goes through Apply, so a new verb needs no change in this boundary.
type controlPort interface {
	Apply(ctx context.Context, action core.ControlAction, args app.ControlArgs) error
}

// watchPort binds a PR locally when no daemon owns the state database. The
// assertion stays in the helper because watch is a local-only capability.
type watchPort interface {
	WatchPR(ctx context.Context, id core.RunID, pr int) error
}

func applyControlWithPR(ctx context.Context, ctrl controlPort, action, runID, sha string, pr int, message ...string) error {
	parsed := core.ParseControlAction(action)
	if !parsed.Valid() {
		return fmt.Errorf("unknown action %q", action)
	}
	if parsed == core.ControlWatch {
		watcher, ok := ctrl.(watchPort)
		if !ok {
			return errors.New("watch requires the daemon API")
		}
		return watcher.WatchPR(ctx, core.RunID(runID), pr)
	}
	return ctrl.Apply(ctx, parsed, app.ControlArgs{
		RunID: core.RunID(runID), SHA: sha, Message: controlMessage(message),
	})
}

func postControlWithPR(ctx context.Context, info *connectionInfo, action, runID, sha string, pr int, message ...string) error {
	payload, err := json.Marshal(controlRequest{
		Action: action, RunID: runID, SHA: sha, PR: pr, Message: controlMessage(message),
	})
	if err != nil {
		return err
	}
	return postControlJSON(ctx, info, payload, &http.Client{Timeout: 5 * time.Second})
}

// postControlJSON sends a marshaled control body to the daemon's loopback
// endpoint. Callers build their own payload so the wire contract stays visible
// at the call site.
func postControlJSON(ctx context.Context, info *connectionInfo, body []byte, client *http.Client) error {
	addr := strings.TrimSuffix(info.Addr, "/") + "/api/v1/control"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, addr, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+info.Token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("beacon API status %d", resp.StatusCode)
	}
	return nil
}
