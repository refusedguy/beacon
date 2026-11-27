package app

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// BenchmarkMakeMetrics times one makeMetrics call over a fixed fixture. The
// fixture folds active and recent snapshots, timeline events, decision
// histories and decision counts in one call, which is the aggregation cost
// that status_test.go's duration-only benchmark does not measure.
//
// Work is the number of input items folded in one call. Errs counts summary
// fields that differ from a baseline call, so a deterministic fixture reports
// zero. The named limiter counts the map writes and the strings.ToLower
// allocation in blockedReasonCategory that aggregation pays on every pass.
//
// CI command:
//
//	go test -vet=off -run '^$' -bench '^BenchmarkMakeMetrics$' -benchtime=1x -count=10 -benchmem ./internal/app/
const (
	nameMakeMetrics    = "make-metrics"
	limiterMakeMetrics = "map writes + strings.ToLower in blockedReasonCategory"

	metricsActiveRuns         = 60
	metricsRecentRuns         = 40
	metricsEventsPerRun       = 8
	metricsHistoryRuns        = 50
	metricsDecisionsPerRun    = 6
	metricsCountRounds        = 8
	metricsSnapshotsPerSource = len(metricsSources)
)

// metricsSources cycles through every launch path ParseLaunchSource accepts,
// plus an empty token that resolves to daemon.
var metricsSources = [...]string{"mcp", "cli", "ui", "daemon", ""}

// metricsReasons reaches every blockedReasonCategory branch, including other.
var metricsReasons = [...]string{
	"gate failed: ci red",
	"base_red: main is broken",
	"rebase attempts exhausted",
	"fix attempts exhausted",
	"idle: no agent picked up",
	"provider error with no category",
}

// metricsSummary is the comparable projection of Metrics that the benchmark
// checks against its baseline. Map entries are counted so growth is visible.
type metricsSummary struct {
	dispatchCount  int
	dispatchCycles int
	skipCount      int
	rebaseCount    int
	fixCount       int
	mergeCount     int
	blockedCount   int
	ciWaitSeconds  int64
	activeCount    int
	recentCount    int
	skipKeys       int
	blockedKeys    int
	launchKeys     int
}

func summaryOfMetrics(m Metrics) metricsSummary {
	return metricsSummary{
		dispatchCount:  m.DispatchCount,
		dispatchCycles: m.DispatchCycles,
		skipCount:      m.SkipCount,
		rebaseCount:    m.RebaseCount,
		fixCount:       m.FixCount,
		mergeCount:     m.MergeCount,
		blockedCount:   m.BlockedCount,
		ciWaitSeconds:  m.CIWaitSeconds,
		activeCount:    m.ActiveCount,
		recentCount:    m.RecentCount,
		skipKeys:       len(m.Skips),
		blockedKeys:    len(m.Blocked),
		launchKeys:     len(m.LaunchSources),
	}
}

type makeMetricsFixture struct {
	active    []RunSnapshot
	recent    []RunSnapshot
	timeline  map[string][]RunEvent
	histories map[string][]Decision
	counts    []DecisionCount
	want      metricsSummary
	work      int64
	limiter   int64
}

func newMakeMetricsFixture() makeMetricsFixture {
	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	f := makeMetricsFixture{
		timeline:  map[string][]RunEvent{},
		histories: map[string][]Decision{},
	}

	snapshot := func(id string, i int) RunSnapshot {
		snap := RunSnapshot{
			ID:           id,
			TaskID:       "T-" + id,
			LaunchSource: metricsSources[i%metricsSnapshotsPerSource],
			Durations: Durations{
				WaitingCI:        int64(60 + i),
				WaitingCIInitial: int64(30 + i),
				WaitingCIRebase:  int64(20 + i),
				WaitingCIFix:     int64(10 + i),
				Running:          int64(i),
				Blocked:          int64(i),
			},
			CreatedAt: now.Add(-time.Duration(i+1) * time.Minute),
			UpdatedAt: now.Add(-time.Duration(i) * time.Minute),
		}
		switch {
		case i%17 == 0:
			snap.State = string(core.RunMerged)
		case i%11 == 0:
			snap.State = string(core.RunBlocked)
			snap.Reason = metricsReasons[i%len(metricsReasons)]
		default:
			snap.State = string(core.RunWaitingCI)
		}
		return snap
	}

	for i := 0; i < metricsActiveRuns; i++ {
		f.active = append(f.active, snapshot(fmt.Sprintf("active-%03d", i), i))
	}
	for i := 0; i < metricsRecentRuns; i++ {
		f.recent = append(f.recent, snapshot(fmt.Sprintf("recent-%03d", i), i+metricsActiveRuns))
	}

	var lowerCalls, mapWrites int64
	for groupIndex, group := range [][]RunSnapshot{f.active, f.recent} {
		for idx, snap := range group {
			events := make([]RunEvent, 0, metricsEventsPerRun)
			for j := 0; j < metricsEventsPerRun; j++ {
				kind := "state"
				if j == 0 && groupIndex == 0 && idx%13 == 0 {
					kind = "merged"
				}
				events = append(events, RunEvent{
					RunID: snap.ID,
					Kind:  kind,
					State: snap.State,
					At:    snap.CreatedAt.Add(time.Duration(j) * time.Minute),
				})
			}
			f.timeline[snap.ID] = events
			mapWrites++ // one LaunchSources entry per snapshot
			if snap.State == string(core.RunBlocked) {
				lowerCalls++ // one blockedReasonCategory call
			}
		}
	}

	for _, events := range f.timeline {
		for _, event := range events {
			if event.Kind == "merged" {
				mapWrites++ // one merged-map entry per merged event
			}
		}
	}

	for i := 0; i < metricsHistoryRuns; i++ {
		key := fmt.Sprintf("hist-%03d", i)
		decisions := make([]Decision, 0, metricsDecisionsPerRun)
		for j := 0; j < metricsDecisionsPerRun; j++ {
			d := Decision{
				RunID:  fmt.Sprintf("hrun-%03d-%d", i, j),
				TaskID: key,
				At:     now,
			}
			switch j % 3 {
			case 0:
				d.Kind = "transition.merge"
			case 1:
				d.Kind = "reconcile.decision"
				d.Verdict = string(core.VerdictBlocked)
				d.Reason = metricsReasons[(i+j)%len(metricsReasons)]
				lowerCalls++ // one blockedReasonCategory call
			default:
				d.Kind = "transition.spawn"
			}
			decisions = append(decisions, d)
		}
		f.histories[key] = decisions
	}

	countRows := []DecisionCount{
		{Kind: "dispatch.candidates", Verdict: "wait", Count: 3},
		{Kind: "dispatch.skip", Verdict: "wait", Count: 2},
		{Kind: "transition.spawn", Verdict: "wait", Count: 5},
		{Kind: "transition.rebase", Verdict: "needs_rebase", Action: "rebased", Count: 4},
		{Kind: "transition.rebase", Verdict: "needs_rebase", Action: "wrong", Count: 7},
		{Kind: "transition.wake", Verdict: "needs_agent_fix", Action: "woke", Count: 6},
		{Kind: "transition.wake", Verdict: "needs_agent_fix", Action: "wrong", Count: 9},
		{Kind: "unknown.kind", Verdict: "noop", Count: 11},
	}
	for round := 0; round < metricsCountRounds; round++ {
		f.counts = append(f.counts, countRows...)
	}
	mapWrites += int64(len(f.counts)) // one counter or skip map write per row

	f.work = int64(len(f.active) + len(f.recent) + len(f.counts))
	for _, events := range f.timeline {
		f.work += int64(len(events))
	}
	for _, decisions := range f.histories {
		f.work += int64(len(decisions))
	}
	f.limiter = lowerCalls + mapWrites
	f.want = summaryOfMetrics(makeMetrics(f.active, f.recent, f.timeline, f.histories, f.counts))
	return f
}

// runMakeMetrics folds one fixture through makeMetrics and returns work, errs
// and the limiter count. Work and limiter are fixed by the fixture; errs is a
// comparison against the baseline summary computed when the fixture was built.
func runMakeMetrics(f makeMetricsFixture) (int64, int64, int64) {
	var errs int64
	if got := summaryOfMetrics(makeMetrics(f.active, f.recent, f.timeline, f.histories, f.counts)); got != f.want {
		errs++
	}
	return f.work, errs, f.limiter
}

// TestMakeMetricsWorkStable pins the counts the CI parsing compares between
// the base and head sides. Two passes over the same fixture must report the
// same work, errs and limiter, with positive work and no errors.
func TestMakeMetricsWorkStable(t *testing.T) {
	f := newMakeMetricsFixture()
	work, errs, limiter := runMakeMetrics(f)
	againWork, againErrs, againLimiter := runMakeMetrics(f)
	if work != againWork || errs != againErrs || limiter != againLimiter {
		t.Fatalf("make-metrics work is not stable: first=%d/%d/%d second=%d/%d/%d",
			work, errs, limiter, againWork, againErrs, againLimiter)
	}
	if work <= 0 || errs != 0 || limiter <= 0 {
		t.Fatalf("work=%d errs=%d limiter=%d, want positive work, no errors and a positive limiter", work, errs, limiter)
	}
}

func BenchmarkMakeMetrics(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	f := newMakeMetricsFixture()

	durations := make([]time.Duration, 0, b.N)
	var work, errs, limiter int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		work, errs, limiter = runMakeMetrics(f)
		durations = append(durations, time.Since(start))
	}
	b.StopTimer()

	median := medianDuration(durations)
	fmt.Fprintf(os.Stdout, "%s: median=%s work=%d errs=%d limiter=%s\n",
		nameMakeMetrics, median, work, errs, limiterMakeMetrics)
	b.ReportMetric(float64(median.Nanoseconds()), nameMakeMetrics+"-median-ns")
	b.ReportMetric(float64(work), nameMakeMetrics+"-work")
	b.ReportMetric(float64(errs), nameMakeMetrics+"-errs")
	b.ReportMetric(float64(limiter), nameMakeMetrics+"-limiter")
}
