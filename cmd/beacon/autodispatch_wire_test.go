package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/internal/infra/workspace"
	"github.com/refusedguy/beacon/pkg/core"
)

// autoDispatchSource serves one ready card on every list. The store's active
// guard keeps a later tick from starting the same task again.
type autoDispatchSource struct{ task core.Task }

func (s *autoDispatchSource) ListAvailable(context.Context) ([]core.Task, error) {
	return []core.Task{s.task}, nil
}
func (s *autoDispatchSource) Claim(context.Context, core.TaskID) error    { return nil }
func (s *autoDispatchSource) Complete(context.Context, core.TaskID) error { return nil }
func (s *autoDispatchSource) MarkInWorkspace(context.Context, core.TaskID, string, string) error {
	return nil
}
func (s *autoDispatchSource) Create(context.Context, core.Task) (core.Task, error) {
	return core.Task{}, nil
}

var _ core.TaskSource = (*autoDispatchSource)(nil)

// newAutoDispatchWorktree builds a workspace backed by a local bare origin so
// Dispatcher.startOne can create a worktree without a network.
func newAutoDispatchWorktree(t *testing.T) *workspace.GitWorktree {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--bare", "--initial-branch=main", "origin.git")
	runGit(t, repo, "init", "--initial-branch=main")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "remote", "add", "origin", origin)
	runGit(t, repo, "push", "-u", "origin", "main")
	wt, err := workspace.New(repo, "main", filepath.Join(root, "worktrees"))
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	return wt
}

func wireAutoDispatch(t *testing.T, enabled, open bool) (*wiring, *sqlite.Store) {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	task := core.Task{
		ID:     "T-AUTO",
		Status: core.TaskAvailable,
		Scope:  []core.ScopePath{core.ScopeFile("README.md")},
	}
	infra := &wiredInfra{
		store:     store,
		source:    &autoDispatchSource{task: task},
		workspace: newAutoDispatchWorktree(t),
		scm:       stubSCM{},
		changes:   make(chan int, 64),
		decisions: app.DecisionLogger{Sink: store},
	}
	cfg := config.Config{
		BaseBranch:   "main",
		PollInterval: "5ms",
		AutoDispatch: config.AutoDispatch{Enabled: enabled, DispatchOpen: open},
	}
	w := wireServices(cfg, ".", "", infra, bareHarness{}, mergePolicy(cfg), nil)
	t.Cleanup(w.close)
	return w, store
}

func autoDispatchRuns(t *testing.T, store *sqlite.Store) int {
	t.Helper()
	runs, err := store.ListActive(context.Background())
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	var n int
	for _, run := range runs {
		if run.TaskID == "T-AUTO" {
			n++
		}
	}
	return n
}

func TestWiredReconcilerAutoDispatch(t *testing.T) {
	cases := []struct {
		name        string
		enabled     bool
		open        bool
		wantStarted bool
	}{
		{name: "enabled with brake open starts a run", enabled: true, open: true, wantStarted: true},
		{name: "brake closed starts none", enabled: true, open: false},
		{name: "disabled starts none", enabled: false, open: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, store := wireAutoDispatch(t, tc.enabled, tc.open)

			// The composition root connects the real dispatcher and copies the
			// same brake into both intake paths.
			if w.recon.Dispatch != w.dispatcher {
				t.Fatal("reconciler Dispatch is not the wired dispatcher")
			}
			if w.recon.AutoDispatchEnabled != tc.enabled {
				t.Fatalf("reconciler auto_dispatch enabled = %v, want %v", w.recon.AutoDispatchEnabled, tc.enabled)
			}
			if w.recon.DispatchOpen != tc.open {
				t.Fatalf("reconciler dispatch_open = %v, want %v", w.recon.DispatchOpen, tc.open)
			}
			if w.dispatcher.DispatchOpen == nil || *w.dispatcher.DispatchOpen != tc.open {
				t.Fatalf("dispatcher dispatch_open = %v, want %v", w.dispatcher.DispatchOpen, tc.open)
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- w.recon.Run(ctx, make(chan int)) }()

			if tc.wantStarted {
				deadline := time.Now().Add(5 * time.Second)
				for autoDispatchRuns(t, store) == 0 && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
			} else {
				time.Sleep(200 * time.Millisecond)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("reconciler Run error = %v, want context.Canceled", err)
			}

			got := autoDispatchRuns(t, store)
			if tc.wantStarted && got == 0 {
				t.Fatal("wired reconciler started no run with auto_dispatch enabled and dispatch_open true")
			}
			if !tc.wantStarted && got != 0 {
				t.Fatalf("wired reconciler started %d runs with the gate closed, want none", got)
			}
		})
	}
}
