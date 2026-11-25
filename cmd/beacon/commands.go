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

// parentSessionFromEnv returns the orchestrator session carried by a CLI
// process. serve and mcp read the same variable, so dispatch and control must
// read it too or CLI-launched runs lose their merge notifications.
func parentSessionFromEnv() string {
	return os.Getenv("BEACON_PARENT_SESSION")
}

// cliWire is the wiring entry point shared by the CLI commands. Tests replace
// it to observe the parent session handed to the wiring layer.
var cliWire = wire

// wireCLI wires a CLI command with the parent session from the environment.
func wireCLI(path string) (*wiring, error) {
	return cliWire(path, parentSessionFromEnv())
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

func newDispatchCommand(configPath *string) *cobra.Command {
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
			w, err := wireCLI(*configPath)
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

func newStatusCommand(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Print active runs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			daemon, _ := cmd.Flags().GetBool("daemon")
			if daemon {
				cfg, _, err := loadConfig(*configPath)
				if err != nil {
					return err
				}
				return printJSON(readDaemonStatus(filepath.Dir(cfg.StateDB)))
			}
			cfg, store, _, err := wireReadOnly(*configPath)
			if err != nil {
				return err
			}
			defer store.Close()
			snapshot, _ := cmd.Flags().GetBool("snapshot")
			if snapshot {
				recentLimit, _ := cmd.Flags().GetInt("recent-limit")
				decisionsLimit, _ := cmd.Flags().GetInt("decisions-limit")
				result, err := (app.SnapshotService{
					Store: store, RecentLimit: recentLimit, DecisionsLimit: decisionsLimit,
					Repo:   cfg.SCM.Owner + "/" + cfg.SCM.Repo,
					Daemon: daemonSnapshot(readDaemonStatus(filepath.Dir(cfg.StateDB))),
				}).Snapshot(cmd.Context())
				if err != nil {
					return err
				}
				return printJSON(result)
			}
			recent, _ := cmd.Flags().GetBool("recent")
			if recent {
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
	cmd.Flags().Bool("recent", false, "print recently finished runs instead of active ones")
	cmd.Flags().Bool("snapshot", false, "print one batched snapshot with runs, history and metrics")
	cmd.Flags().Int("recent-limit", 0, "cap finished runs in a snapshot (0 uses the default)")
	cmd.Flags().Int("decisions-limit", 0, "cap decisions per run and run-less decisions in a snapshot (0 uses the default)")
	cmd.Flags().Bool("daemon", false, "print daemon liveness and heartbeat age instead of runs")
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

// newControlCommand freezes the CLI contract. The json flag is accepted for
// scripting compatibility; a successful control action always prints JSON.
func newControlCommand(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "control",
		Short: "Control a run: " + strings.Join(core.ControlActionNames(), ", "),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			action, _ := cmd.Flags().GetString("action")
			runID, _ := cmd.Flags().GetString("run-id")
			sha, _ := cmd.Flags().GetString("sha")
			pr, _ := cmd.Flags().GetInt("pr")
			message, _ := cmd.Flags().GetString("message")
			if err := validateControl(action, runID, message); err != nil {
				return err
			}
			if err := dispatchControlWithPR(cmd.Context(), *configPath, action, runID, sha, pr, message); err != nil {
				return err
			}
			return printJSON(controlResult{OK: true, Action: action, RunID: runID})
		},
	}
	cmd.Flags().String("action", "", "one of "+strings.Join(core.ControlActionNames(), ", "))
	cmd.Flags().String("run-id", "", "run ID to control")
	cmd.Flags().String("sha", "", "observed SHA used by an approve action")
	cmd.Flags().Int("pr", 0, "PR number bound by a watch action")
	cmd.Flags().String("message", "", "guidance sent to the worker by a nudge action")
	cmd.Flags().Bool("json", false, "print a machine readable result")
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
func dispatchControlWithPR(ctx context.Context, configPath, action, runID, sha string, pr int, message ...string) error {
	cfg, _, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if info, err := readConnection(filepath.Dir(cfg.StateDB)); err == nil && daemonAlive(cfg.StateDB) {
		return postControlWithPR(ctx, info, action, runID, sha, pr, message...)
	}
	w, err := wireCLI(configPath)
	if err != nil {
		return err
	}
	defer w.close()
	return applyControlWithPR(ctx, w.control, action, runID, sha, pr, message...)
}

// controlPort is the slice of app.Control that the command drives.
type controlPort interface {
	Approve(ctx context.Context, id core.RunID, sha string) error
	Unblock(ctx context.Context, id core.RunID) error
	Cancel(ctx context.Context, id core.RunID) error
	Retry(ctx context.Context, id core.RunID) error
	Nudge(ctx context.Context, id core.RunID, message string) error
}

// watchPort binds a PR locally when no daemon owns the state database.
type watchPort interface {
	WatchPR(ctx context.Context, id core.RunID, pr int) error
}

func applyControl(ctx context.Context, ctrl controlPort, action, runID, sha string, message ...string) error {
	return applyControlWithPR(ctx, ctrl, action, runID, sha, 0, message...)
}

func applyControlWithPR(ctx context.Context, ctrl controlPort, action, runID, sha string, pr int, message ...string) error {
	id := core.RunID(runID)
	switch core.ParseControlAction(action) {
	case core.ControlApprove:
		return ctrl.Approve(ctx, id, sha)
	case core.ControlUnblock:
		return ctrl.Unblock(ctx, id)
	case core.ControlCancel:
		return ctrl.Cancel(ctx, id)
	case core.ControlRetry:
		return ctrl.Retry(ctx, id)
	case core.ControlNudge:
		return ctrl.Nudge(ctx, id, controlMessage(message))
	case core.ControlWatch:
		watcher, ok := ctrl.(watchPort)
		if !ok {
			return errors.New("watch requires the daemon API")
		}
		return watcher.WatchPR(ctx, id, pr)
	default:
		return fmt.Errorf("unknown action %q", action)
	}
}

func postControl(ctx context.Context, info *connectionInfo, action, runID, sha string, message ...string) error {
	return postControlWithPR(ctx, info, action, runID, sha, 0, message...)
}

func postControlWithPR(ctx context.Context, info *connectionInfo, action, runID, sha string, pr int, message ...string) error {
	payload, err := json.Marshal(controlRequest{
		Action: action, RunID: runID, SHA: sha, PR: pr, Message: controlMessage(message),
	})
	if err != nil {
		return err
	}
	addr := strings.TrimSuffix(info.Addr, "/") + "/api/v1/control"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, addr, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+info.Token)
	client := &http.Client{Timeout: 5 * time.Second}
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
