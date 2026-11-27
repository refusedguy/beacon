package bench

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// The planner fixtures use available cards with file scopes and pass
// strict=false, so Select never builds a bad_status, active_run or no_scope
// reason and never takes the broad-claim fallback in activeScopeIndex.matching.
// This benchmark drives those four paths in one Select over a fixed fixture.
// Every candidate lands in exactly one bucket, so the per-code skip counts and
// the selected count are known before the timed loop. CI checks that the counts
// match in both smoke and full mode.
const (
	skipBadStatus  = 50
	skipActiveRun  = 50
	skipNoScope    = 50
	skipBroadAll   = 25
	skipBroadGlob  = 25
	skipCandidates = skipBadStatus + skipActiveRun + skipNoScope + skipBroadAll + skipBroadGlob
)

// namePlannerSkipPaths is the perf key for this benchmark.
const namePlannerSkipPaths = "planner-skip-paths"

// plannerSkipLimiter names the resource that bounds this path: building the
// skip reason strings for each skipped card and the full active-run scan that
// an all-files or leading-wildcard claim forces in activeScopeIndex.matching.
const plannerSkipLimiter = "skip reason strings + broad-claim full scan"

type plannerSkipFixture struct {
	candidates []core.Task
	active     []*core.Run
}

func plannerSkipFixtures() plannerSkipFixture {
	// Active runs give the busy-ID check something to hit and give broad claims
	// a live scope to overlap. Their file scopes also exercise overlapSuffix.
	active := make([]*core.Run, skipActiveRun)
	for i := range active {
		active[i] = &core.Run{
			ID:     core.RunID(fmt.Sprintf("run-skip-%04d", i)),
			TaskID: core.TaskID(fmt.Sprintf("A-skip-%04d", i)),
			State:  core.RunRunning,
			Scope:  []core.ScopePath{core.ScopeFile(fmt.Sprintf("internal/skip%04d/file.go", i))},
		}
	}

	candidates := make([]core.Task, 0, skipCandidates)
	for i := 0; i < skipBadStatus; i++ {
		candidates = append(candidates, core.Task{
			ID:     core.TaskID(fmt.Sprintf("T-bad-%04d", i)),
			Status: core.TaskDone,
			Scope:  []core.ScopePath{core.ScopeFile(fmt.Sprintf("internal/bad%04d/file.go", i))},
		})
	}
	for i := 0; i < skipActiveRun; i++ {
		candidates = append(candidates, core.Task{
			ID:     core.TaskID(fmt.Sprintf("A-skip-%04d", i)),
			Status: core.TaskAvailable,
			Scope:  []core.ScopePath{core.ScopeFile(fmt.Sprintf("internal/busy%04d/file.go", i))},
		})
	}
	for i := 0; i < skipNoScope; i++ {
		candidates = append(candidates, core.Task{
			ID:     core.TaskID(fmt.Sprintf("T-noscope-%04d", i)),
			Status: core.TaskAvailable,
		})
	}
	for i := 0; i < skipBroadAll; i++ {
		candidates = append(candidates, core.Task{
			ID:     core.TaskID(fmt.Sprintf("T-all-%04d", i)),
			Status: core.TaskAvailable,
			Scope:  []core.ScopePath{core.ScopeAll()},
		})
	}
	for i := 0; i < skipBroadGlob; i++ {
		candidates = append(candidates, core.Task{
			ID:     core.TaskID(fmt.Sprintf("T-glob-%04d", i)),
			Status: core.TaskAvailable,
			Scope:  []core.ScopePath{core.ScopePattern("**/*.go")},
		})
	}
	return plannerSkipFixture{candidates: candidates, active: active}
}

type skipPathCounts struct {
	badStatus, activeRun, noScope, activeOverlap, selectedOverlap, selected int
}

func countSkipPaths(skipped []app.SkippedTask) skipPathCounts {
	var out skipPathCounts
	for _, s := range skipped {
		switch s.Code {
		case app.SkipBadStatus:
			out.badStatus++
		case app.SkipActiveRun:
			out.activeRun++
		case app.SkipNoScope:
			out.noScope++
		case app.SkipActiveScopeOverlap:
			out.activeOverlap++
		case app.SkipSelectedScopeOverlap:
			out.selectedOverlap++
		}
	}
	return out
}

func printPlannerSkipReport(w io.Writer, c skipPathCounts, work, errs float64) {
	fmt.Fprintf(w, "planner skip paths: candidates=%d bad_status=%d active_run=%d no_scope=%d active_overlap=%d selected_overlap=%d selected=%d\n",
		skipCandidates, c.badStatus, c.activeRun, c.noScope, c.activeOverlap, c.selectedOverlap, c.selected)
	fmt.Fprintf(w, "planner skip paths: work=%.0f errs=%.0f limiter=%s\n", work, errs, plannerSkipLimiter)
}

// BenchmarkPlannerSkipPaths runs one Select with strict=true over 200 candidates:
// bad-status, active-run, no-scope, an all-files claim and a leading-wildcard
// claim. It reports SkippedTask counts by code plus the selected count. Work is
// the number of candidates accounted for by skipped plus selected. Errs counts
// Select errors and per-iteration count mismatches against the fixture.
//
// CI runs a bounded smoke pass with BEACON_BENCH_SMOKE=1 and -benchtime=1x:
//
//	BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' -bench '^BenchmarkPlannerSkipPaths$' -benchtime=1x -count=1 -benchmem ./internal/bench/
//
// Smoke mode skips the warm-up Select. Fixture, counts, work and assertions are
// the same in both modes.
func BenchmarkPlannerSkipPaths(b *testing.B) {
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	fixture := plannerSkipFixtures()
	planner := app.NewPlanner(nil)

	if !smokeMode() {
		_, _, _ = planner.Select(fixture.candidates, fixture.active, skipCandidates, true)
	}

	want := skipPathCounts{
		badStatus:     skipBadStatus,
		activeRun:     skipActiveRun,
		noScope:       skipNoScope,
		activeOverlap: skipBroadAll + skipBroadGlob,
	}

	var totals skipPathCounts
	var totalWork, totalErrs int64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		selected, skipped, err := planner.Select(fixture.candidates, fixture.active, skipCandidates, true)
		if err != nil {
			totalErrs++
		}
		counts := countSkipPaths(skipped)
		counts.selected = len(selected)
		if counts != want {
			totalErrs++
		}
		totals.badStatus += counts.badStatus
		totals.activeRun += counts.activeRun
		totals.noScope += counts.noScope
		totals.activeOverlap += counts.activeOverlap
		totals.selectedOverlap += counts.selectedOverlap
		totals.selected += counts.selected
		totalWork += int64(len(skipped) + len(selected))
	}
	b.StopTimer()

	runs := int64(b.N)
	perOp := func(n int64) float64 {
		if runs == 0 {
			return 0
		}
		return float64(n) / float64(runs)
	}
	workPerOp := perOp(totalWork)
	errsPerOp := perOp(totalErrs)
	avg := skipPathCounts{
		badStatus:       int(perOp(int64(totals.badStatus))),
		activeRun:       int(perOp(int64(totals.activeRun))),
		noScope:         int(perOp(int64(totals.noScope))),
		activeOverlap:   int(perOp(int64(totals.activeOverlap))),
		selectedOverlap: int(perOp(int64(totals.selectedOverlap))),
		selected:        int(perOp(int64(totals.selected))),
	}
	printPlannerSkipReport(os.Stdout, avg, workPerOp, errsPerOp)

	b.ReportMetric(workPerOp, namePlannerSkipPaths+"-work")
	b.ReportMetric(errsPerOp, namePlannerSkipPaths+"-errs")
	b.ReportMetric(float64(avg.badStatus), "bad-status-skipped-per-select")
	b.ReportMetric(float64(avg.activeRun), "active-run-skipped-per-select")
	b.ReportMetric(float64(avg.noScope), "no-scope-skipped-per-select")
	b.ReportMetric(float64(avg.activeOverlap), "active-overlap-skipped-per-select")
	b.ReportMetric(float64(avg.selected), "selected-per-select")

	if runs == 0 {
		b.Fatal("benchmark ran zero iterations")
	}
	if totalErrs != 0 {
		b.Fatalf("planner skip paths: errs=%d, want 0; skip counts must match the fixture", totalErrs)
	}
}
