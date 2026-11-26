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

// The planner benchmark exercises the active-scope index path that the end-to-end
// fixtures miss. In those fixtures candidate scopes never overlap an active
// run, so Select never reaches activeConflict and never calls the injected
// overlap checker. Here 150 of the 200 candidates overlap an active run by path.
//
// The injected checker is the authority on those overlaps. It allows the
// "allowed" candidates even though their paths collide, and blocks the
// "blocked" ones. If activeConflict ever stops calling the checker and falls
// back to a path comparison, the allowed candidates are skipped and the
// assertion at the end of the benchmark fails.
const (
	plannerActiveRuns = 1000
	plannerAllowed    = 100
	plannerBlocked    = 50
	plannerClean      = 50
	plannerCandidates = plannerAllowed + plannerBlocked + plannerClean

	plannerAllowMarker = "internal/allow/file.go"
)

type plannerSelectFixture struct {
	candidates []core.Task
	active     []*core.Run
}

func plannerSelectFixtures() plannerSelectFixture {
	active := make([]*core.Run, plannerActiveRuns)
	for i := range active {
		active[i] = &core.Run{
			ID:     core.RunID(fmt.Sprintf("run-active-%04d", i)),
			TaskID: core.TaskID(fmt.Sprintf("A-%04d", i)),
			State:  core.RunRunning,
			Scope:  []core.ScopePath{core.ScopeFile(fmt.Sprintf("internal/active%04d/file.go", i))},
		}
	}

	candidates := make([]core.Task, 0, plannerCandidates)
	for i := 0; i < plannerAllowed; i++ {
		candidates = append(candidates, core.Task{
			ID:     core.TaskID(fmt.Sprintf("T-allow-%04d", i)),
			Status: core.TaskAvailable,
			Scope: []core.ScopePath{
				core.ScopeFile(plannerAllowMarker),
				core.ScopeFile(fmt.Sprintf("internal/active%04d/file.go", i)),
			},
		})
	}
	for i := 0; i < plannerBlocked; i++ {
		candidates = append(candidates, core.Task{
			ID:     core.TaskID(fmt.Sprintf("T-block-%04d", i)),
			Status: core.TaskAvailable,
			Scope:  []core.ScopePath{core.ScopeFile(fmt.Sprintf("internal/active%04d/file.go", plannerAllowed+i))},
		})
	}
	for i := 0; i < plannerClean; i++ {
		candidates = append(candidates, core.Task{
			ID:     core.TaskID(fmt.Sprintf("T-clean-%04d", i)),
			Status: core.TaskAvailable,
			Scope:  []core.ScopePath{core.ScopeFile(fmt.Sprintf("internal/clean%04d/file.go", i))},
		})
	}
	return plannerSelectFixture{candidates: candidates, active: active}
}

// plannerChecker decides overlap from the candidate scope. A candidate carrying
// the allow marker is always allowed, even when its paths overlap an active
// run. Every other pair uses the production path and dependency comparison.
func plannerChecker(a, b core.Task) bool {
	for _, p := range a.Scope {
		if p.Value() == plannerAllowMarker {
			return false
		}
	}
	return core.PathsAndDependencies(a, b)
}

type plannerSkipCounts struct {
	active, selected int
}

func countPlannerSkips(skipped []app.SkippedTask) plannerSkipCounts {
	var out plannerSkipCounts
	for _, s := range skipped {
		switch s.Code {
		case app.SkipActiveScopeOverlap:
			out.active++
		case app.SkipSelectedScopeOverlap:
			out.selected++
		}
	}
	return out
}

func printPlannerReport(w io.Writer, checks, selected, activeSkipped, selectedSkipped float64) {
	fmt.Fprintf(w, "planner select: active=%d candidates=%d allowed=%d blocked=%d clean=%d\n",
		plannerActiveRuns, plannerCandidates, plannerAllowed, plannerBlocked, plannerClean)
	fmt.Fprintf(w, "checks/select=%.1f selected/select=%.1f active-skipped/select=%.1f selected-skipped/select=%.1f\n",
		checks, selected, activeSkipped, selectedSkipped)
}

// BenchmarkPlannerSelectInjectedChecker runs one Select over 1000 active runs
// and 200 candidates. It reports checker calls and both kinds of skip. The
// assertions after the timed loop fail when the injected checker stops deciding
// active conflicts, so the benchmark cannot silently fall back to a path-only
// comparison.
//
// CI runs a bounded smoke pass with BEACON_BENCH_SMOKE=1 and -benchtime=1x:
//
//	BEACON_BENCH_SMOKE=1 GOMAXPROCS=1 go test -run '^$' -bench '^BenchmarkPlannerSelectInjectedChecker$' -benchtime=1x -count=1 ./internal/bench/
//
// Smoke mode skips the warm-up Select. The fixture, counts and assertions stay
// the same.
func BenchmarkPlannerSelectInjectedChecker(b *testing.B) {
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	fixture := plannerSelectFixtures()
	var checks int
	planner := app.NewPlanner(func(a, c core.Task) bool {
		checks++
		return plannerChecker(a, c)
	})

	if !smokeMode() {
		_, _, _ = planner.Select(fixture.candidates, fixture.active, plannerCandidates, false)
	}

	var totalChecks, totalSelected, totalActiveSkip, totalSelectedSkip int64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		checks = 0
		chosen, skipped, err := planner.Select(fixture.candidates, fixture.active, plannerCandidates, false)
		if err != nil {
			b.Fatal(err)
		}
		counts := countPlannerSkips(skipped)
		totalChecks += int64(checks)
		totalSelected += int64(len(chosen))
		totalActiveSkip += int64(counts.active)
		totalSelectedSkip += int64(counts.selected)
	}
	b.StopTimer()

	runs := int64(b.N)
	if totalChecks == 0 {
		b.Fatalf("injected overlap checker was never called; activeConflict is not using it")
	}
	wantActiveSkip := int64(plannerBlocked) * runs
	if totalActiveSkip != wantActiveSkip {
		b.Fatalf("active scope skips = %d, want %d; the injected checker no longer decides active conflicts (selected=%d, want %d)",
			totalActiveSkip, wantActiveSkip, totalSelected, int64(plannerAllowed+plannerClean)*runs)
	}

	perOp := func(n int64) float64 {
		if runs == 0 {
			return 0
		}
		return float64(n) / float64(runs)
	}
	checksPerOp := perOp(totalChecks)
	selectedPerOp := perOp(totalSelected)
	activeSkipPerOp := perOp(totalActiveSkip)
	selectedSkipPerOp := perOp(totalSelectedSkip)
	b.ReportMetric(checksPerOp, "checks-per-select")
	b.ReportMetric(selectedPerOp, "selected-per-select")
	b.ReportMetric(activeSkipPerOp, "active-skipped-per-select")
	b.ReportMetric(selectedSkipPerOp, "selected-skipped-per-select")
	printPlannerReport(os.Stdout, checksPerOp, selectedPerOp, activeSkipPerOp, selectedSkipPerOp)
}
