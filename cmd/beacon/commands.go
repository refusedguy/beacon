package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/internal/infra/workspace"
	"github.com/refusedguy/beacon/pkg/core"
)

func wireReadOnly(path string) (config.Config, *sqlite.Store, *workspace.GitWorktree, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	store, err := sqlite.Open(cfg.StateDB)
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	root, err := filepath.Abs(".")
	if err != nil {
		store.Close()
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
			_, store, _, err := wireReadOnly(*configPath)
			if err != nil {
				return err
			}
			defer store.Close()
			snapshot, _ := cmd.Flags().GetBool("snapshot")
			if snapshot {
				result, err := (app.SnapshotService{Store: store}).Snapshot(cmd.Context())
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
