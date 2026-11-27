package bench

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// This benchmark measures the in-memory half of snapshot assembly. A stub
// store serves runs, events and decisions from slices, so groupHistory,
// makeRunSnapshot and makeMetrics run without SQLite reads or HTTP encoding.
// Work is runs assembled plus events and decisions grouped. Errs counts stub
// store errors. The limiter is map growth in groupHistory and field copying in
// makeRunSnapshot.

// nameAssembly and limiterAssembly live in report.go beside the other
// canonical keys, so the registry and this benchmark cannot drift apart.

// assemblyFixture holds the rows the stub store serves. Sizes come from the
// shared fixture constants, so base and head assemble the same amount of work.
type assemblyFixture struct {
	active    []*core.Run
	recent    []*core.Run
	events    []app.RunEvent
	decisions []app.Decision
	counts    []app.DecisionCount
	work      int64
}

func newAssemblyFixture() assemblyFixture {
	runs := runFixtures(fixtureRuns)
	active := runs[:fixtureRuns/3]
	recent := runs[fixtureRuns/3 : fixtureRuns/2]
	events := assemblyEvents(active, recent)
	ids := runIDs(append(append([]*core.Run{}, active...), recent...))
	decisions := decisionFixtures(fixtureDecisions, ids)
	// Blocked reconcile decisions fold into metrics.go's bounded blocked map
	// and give status.go blocked reasons to carry into run snapshots. Without
	// them a status or metrics variant would be measured on a path that never
	// visits either branch. The Repo field is set on the service below for the
	// same reason: makeRunSnapshot uses it for the PR link.
	blockedReasons := []string{
		"gate failed: flaky check",
		"base_red: main is red",
		"rebase attempts exhausted",
		"idle: no agent response",
	}
	for i, reason := range blockedReasons {
		run := active[i%len(active)]
		decisions = append(decisions, app.Decision{
			At:       fixtureNow.Add(time.Duration(i) * time.Minute),
			CycleID:  fmt.Sprintf("cycle-blocked-%02d", i),
			Kind:     "reconcile.decision",
			RunID:    string(run.ID),
			TaskID:   string(run.TaskID),
			PRNumber: run.PRNumber,
			Verdict:  string(core.VerdictBlocked),
			Action:   "blocked",
			Reason:   reason,
			Fields:   map[string]any{"attempt": i + 1},
		})
	}
	counts := []app.DecisionCount{
		{Kind: "dispatch.candidates", Verdict: "ready", Count: 12},
		{Kind: "dispatch.skip", Verdict: "wait", Count: 7},
		{Kind: "transition.spawn", Count: 9},
		{Kind: "transition.rebase", Action: "rebased", Count: 5},
		{Kind: "transition.wake", Action: "woke", Count: 3},
	}
	return assemblyFixture{
		active:    active,
		recent:    recent,
		events:    events,
		decisions: decisions,
		counts:    counts,
		work:      int64(len(active) + len(recent) + len(events) + len(decisions)),
	}
}

// assemblyEvents gives each run a timeline with state transitions and a
// closing merge event. deriveDurations and makeMetrics read both shapes.
func assemblyEvents(groups ...[]*core.Run) []app.RunEvent {
	states := []core.RunState{
		core.RunRunning, core.RunWaitingCI, core.RunNeedsRebase,
		core.RunWaitingCI, core.RunNeedsAgentFix, core.RunWaitingCI, core.RunMerged,
	}
	out := make([]app.RunEvent, 0, len(states))
	var id int64
	for _, group := range groups {
		for _, run := range group {
			for i, state := range states {
				id++
				kind := "state"
				if i == len(states)-1 {
					kind = "merged"
				}
				out = append(out, app.RunEvent{
					ID:       id,
					RunID:    string(run.ID),
					Kind:     kind,
					State:    string(state),
					At:       run.CreatedAt.Add(time.Duration(i) * 90 * time.Second),
					HeadSHA:  run.HeadSHA,
					PRNumber: run.PRNumber,
					Reason:   "snapshot assembly fixture",
				})
			}
		}
	}
	return out
}

// inMemorySnapshotStore serves the fixture without SQLite. A non-nil err
// makes every read fail, which covers the error path of snapshot assembly.
type inMemorySnapshotStore struct {
	fixture assemblyFixture
	err     error
}

func (s *inMemorySnapshotStore) ListActive(context.Context) ([]*core.Run, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.fixture.active, nil
}

func (s *inMemorySnapshotStore) ListRecent(context.Context, int) ([]*core.Run, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.fixture.recent, nil
}

func (s *inMemorySnapshotStore) ListRunEvents(context.Context, []string) ([]app.RunEvent, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.fixture.events, nil
}

func (s *inMemorySnapshotStore) ListDecisions(context.Context, []string, int, int) ([]app.Decision, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.fixture.decisions, nil
}

func (s *inMemorySnapshotStore) DecisionCounts(context.Context, []string) ([]app.DecisionCount, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.fixture.counts, nil
}

// runAssembly assembles one snapshot. Work is fixed by the fixture, errs is
// one when the stub store fails and zero otherwise, and the limiter count is
// the map insertions and field copies the fixture forces.
func runAssembly(ctx context.Context, svc app.SnapshotService, fixture assemblyFixture) (int64, int64, int64) {
	if _, err := svc.Snapshot(ctx); err != nil {
		return 0, 1, 0
	}
	return fixture.work, 0, fixture.work
}

func printAssemblyReport(w io.Writer, d time.Duration, work, errs int64) {
	fmt.Fprintf(w, "%s: median=%s work=%d errs=%d limiter=%s\n",
		nameAssembly, d, work, errs, limiterAssembly)
}

func TestSnapshotAssemblyWorkStable(t *testing.T) {
	f := newAssemblyFixture()
	svc := app.SnapshotService{
		Store: &inMemorySnapshotStore{fixture: f},
		Repo:  "acme/beacon",
	}
	ctx := context.Background()
	work, errs, limiter := runAssembly(ctx, svc, f)
	againWork, againErrs, againLimiter := runAssembly(ctx, svc, f)
	if work != againWork || errs != againErrs || limiter != againLimiter {
		t.Fatalf("assembly work is not stable: first=%d/%d/%d second=%d/%d/%d",
			work, errs, limiter, againWork, againErrs, againLimiter)
	}
	if work <= 0 || errs != 0 || limiter != work {
		t.Fatalf("assembly work=%d errs=%d limiter=%d, want positive work, no errors and one limiter op per work unit",
			work, errs, limiter)
	}

	snap, err := svc.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Active) != len(f.active) || len(snap.Recent) != len(f.recent) {
		t.Fatalf("snapshot has %d active and %d recent runs, want %d and %d",
			len(snap.Active), len(snap.Recent), len(f.active), len(f.recent))
	}
	if snap.Metrics.ActiveCount != len(f.active) || snap.Metrics.RecentCount != len(f.recent) {
		t.Fatalf("metrics count %d active and %d recent runs, want %d and %d",
			snap.Metrics.ActiveCount, snap.Metrics.RecentCount, len(f.active), len(f.recent))
	}
	if snap.Metrics.DispatchCycles != 12 || snap.Metrics.RebaseCount != 5 {
		t.Fatalf("metrics did not consume the decision counts: dispatch=%d rebase=%d",
			snap.Metrics.DispatchCycles, snap.Metrics.RebaseCount)
	}
}

func TestSnapshotAssemblyStoreError(t *testing.T) {
	f := newAssemblyFixture()
	svc := app.SnapshotService{
		Store: &inMemorySnapshotStore{fixture: f, err: fmt.Errorf("stub store failed")},
	}
	work, errs, limiter := runAssembly(context.Background(), svc, f)
	if work != 0 || errs != 1 || limiter != 0 {
		t.Fatalf("failed assembly returned work=%d errs=%d limiter=%d, want 0/1/0",
			work, errs, limiter)
	}
}

// BenchmarkSnapshotAssemblyInMemory times groupHistory, makeRunSnapshot and
// makeMetrics over a fixed fixture. The fixture is built outside the timed
// loop and the reported counts are fixed, so smoke and full passes agree.
//
// CI runs the bounded smoke pass with -benchtime=1x -count=10:
//
//	BEACON_BENCH_SMOKE=1 GOMAXPROCS=1 go test -vet=off -run '^$' -bench '^BenchmarkSnapshotAssemblyInMemory$' -benchtime=1x -count=10 -benchmem ./internal/bench/
func BenchmarkSnapshotAssemblyInMemory(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	f := newAssemblyFixture()
	svc := app.SnapshotService{
		Store: &inMemorySnapshotStore{fixture: f},
		Repo:  "acme/beacon",
	}
	ctx := context.Background()
	if !smokeMode() {
		_, _, _ = runAssembly(ctx, svc, f)
	}

	durations := make([]time.Duration, 0, b.N)
	var work, errs, limiter int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		work, errs, limiter = runAssembly(ctx, svc, f)
		durations = append(durations, time.Since(start))
	}
	b.StopTimer()

	median := medianDuration(durations)
	min, max := median, median
	for _, d := range durations {
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
	}
	// Keep both lines off stdout: testing.B writes its result line there when
	// this function returns, so benchstat would drop this benchmark and its
	// B/op and allocs/op columns. CI merges stderr into the perf log.
	var report bytes.Buffer
	printAssemblyReport(&report, median, work, errs)
	printPerfRow(&report, nameAssembly, median, min, max, work, errs, limiterAssembly)
	_, _ = os.Stderr.Write(report.Bytes())
	b.ReportMetric(float64(median.Nanoseconds()), nameAssembly+"-median-ns")
	b.ReportMetric(float64(work), nameAssembly+"-work")
	b.ReportMetric(float64(errs), nameAssembly+"-errs")
	b.ReportMetric(float64(limiter), nameAssembly+"-limiter-ops")
}
