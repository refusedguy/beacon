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
			w, err := wire(*configPath, "")
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
			_, store, _, err := wireReadOnly(*configPath)
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

const (
	controlApprove = "approve"
	controlUnblock = "unblock"
	controlCancel  = "cancel"
	controlRetry   = "retry"
)

// controlResult is the stable JSON contract printed by beacon control on success.
type controlResult struct {
	OK     bool   `json:"ok"`
	Action string `json:"action"`
	RunID  string `json:"run_id"`
}

type controlRequest struct {
	Action string `json:"action"`
	RunID  string `json:"run_id"`
	SHA    string `json:"sha"`
}

// newControlCommand freezes the CLI contract. The json flag is accepted for
// scripting compatibility; a successful control action always prints JSON.
func newControlCommand(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "control",
		Short: "Approve, unblock, cancel or retry a run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			action, _ := cmd.Flags().GetString("action")
			runID, _ := cmd.Flags().GetString("run-id")
			sha, _ := cmd.Flags().GetString("sha")
			if err := validateControl(action, runID); err != nil {
				return err
			}
			if err := dispatchControl(cmd.Context(), *configPath, action, runID, sha); err != nil {
				return err
			}
			return printJSON(controlResult{OK: true, Action: action, RunID: runID})
		},
	}
	cmd.Flags().String("action", "", "approve, unblock, cancel or retry")
	cmd.Flags().String("run-id", "", "run ID to control")
	cmd.Flags().String("sha", "", "observed SHA used by an approve action")
	cmd.Flags().Bool("json", false, "print a machine readable result")
	return cmd
}

func validateControl(action, runID string) error {
	switch action {
	case controlApprove, controlUnblock, controlCancel, controlRetry:
	default:
		return fmt.Errorf("unknown action %q", action)
	}
	if runID == "" {
		return errors.New("run-id is required")
	}
	return nil
}

// dispatchControl forwards to the daemon when it owns the state database and
// connection file. Without a live daemon it applies the control directly.
func dispatchControl(ctx context.Context, configPath, action, runID, sha string) error {
	cfg, _, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if info, err := readConnection(filepath.Dir(cfg.StateDB)); err == nil && daemonAlive(cfg.StateDB) {
		return postControl(ctx, info, action, runID, sha)
	}
	w, err := wire(configPath, "")
	if err != nil {
		return err
	}
	defer w.close()
	return applyControl(ctx, w.control, action, runID, sha)
}

// controlPort is the slice of app.Control that the command drives.
type controlPort interface {
	Approve(ctx context.Context, id core.RunID, sha string) error
	Unblock(ctx context.Context, id core.RunID) error
	Cancel(ctx context.Context, id core.RunID) error
	Retry(ctx context.Context, id core.RunID) error
}

func applyControl(ctx context.Context, ctrl controlPort, action, runID, sha string) error {
	id := core.RunID(runID)
	switch action {
	case controlApprove:
		return ctrl.Approve(ctx, id, sha)
	case controlUnblock:
		return ctrl.Unblock(ctx, id)
	case controlCancel:
		return ctrl.Cancel(ctx, id)
	case controlRetry:
		return ctrl.Retry(ctx, id)
	default:
		return fmt.Errorf("unknown action %q", action)
	}
}

func postControl(ctx context.Context, info *connectionInfo, action, runID, sha string) error {
	payload, err := json.Marshal(controlRequest{Action: action, RunID: runID, SHA: sha})
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
