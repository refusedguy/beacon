package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// TestGitWorktreeCreateLatencyAlternating measures Create. benchplan-03 added
// Remove latency and left Create unmeasured. Create fetches the base, clears a
// stale path, adds a worktree with -B and resolves the merge base. The bare git
// worktree add in scripts/bench-e2e.sh prices none of those calls.
//
// Work counts git subcommands with commandHook after setup. The groups are
// fetch, status, add, merge-base and prune. Each pass seeds a stale worktree
// that carries card-only dirt, so clearStalePath runs its status check and
// discards the marker quietly. Errs counts Create failures and stale-path
// conflicts that escape recovery. The seeded dirt is recoverable, so errs stay
// zero. The named limiter is the git subprocess spawn for fetch, stale status,
// add -B and merge-base.
//
// CI command:
//
//	go test ./internal/infra/workspace/ -run '^TestGitWorktreeCreateLatencyAlternating$' -count=1 -v
func TestGitWorktreeCreateLatencyAlternating(t *testing.T) {
	if testing.Short() {
		t.Skip("wall-clock measurement")
	}
	ctx := context.Background()
	const passes = 5
	walls := make([]float64, 0, passes)
	works := make([]int, 0, passes)
	statuses := make([]int, 0, passes)
	errs := 0
	for i := 1; i <= passes; i++ {
		wt, _ := newTestGitWorktree(t)
		task := core.Task{ID: core.TaskID(fmt.Sprintf("T-CRT-LAT-%d", i))}

		seed, err := wt.Create(ctx, task)
		if err != nil {
			t.Fatalf("seed Create() pass %d: %v", i, err)
		}
		markerDir := filepath.Join(seed.Path, "docs", "tasks")
		if err := os.MkdirAll(markerDir, 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q, 0o755): %v", markerDir, err)
		}
		marker := filepath.Join(markerDir, "stale.md")
		if err := os.WriteFile(marker, []byte("stale\n"), 0o644); err != nil {
			t.Fatalf("os.WriteFile(%q, ...): %v", marker, err)
		}

		var fetch, status, add, mergeBase, prune, total int
		wt.commandHook = func(_ string, args []string) error {
			total++
			if len(args) == 0 {
				return nil
			}
			switch args[0] {
			case "fetch":
				fetch++
			case "status":
				status++
			case "merge-base":
				mergeBase++
			case "worktree":
				if len(args) > 1 {
					switch args[1] {
					case "add":
						add++
					case "prune":
						prune++
					}
				}
			}
			return nil
		}

		start := time.Now()
		_, err = wt.Create(ctx, task)
		wall := time.Since(start).Seconds()
		walls = append(walls, wall)
		works = append(works, total)
		statuses = append(statuses, status)
		runErrs := 0
		if err != nil {
			runErrs = 1
			errs++
		}
		t.Logf("run %d: wall=%.3fs work=%d status=%d fetch=%d add=%d merge-base=%d prune=%d errs=%d",
			i, wall, total, status, fetch, add, mergeBase, prune, runErrs)
	}
	sort.Float64s(walls)
	sort.Ints(works)
	sort.Ints(statuses)
	fmt.Printf("worktree-create: median=%.3fs range=%.3fs-%.3fs work=%d status=%d errs=%d limiter=git subprocess spawn for fetch, stale status, add -B and merge-base\n",
		walls[len(walls)/2], walls[0], walls[len(walls)-1],
		works[len(works)/2], statuses[len(statuses)/2], errs)
}
