package mcp

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// hourlyPointBudget mirrors the shared GitHub hourly point budget. It is a
	// local constant so this package does not import a sibling infra package
	// just to read a number (ADR 0005).
	hourlyPointBudget = 500

	// performanceMemoryCeiling is the allowed memory growth of one measured
	// batch of ticks. 100 MiB is the contract ceiling from the work order.
	performanceMemoryCeiling = 100 << 20

	// performanceTickCeiling bounds the average wall-clock time of one tick.
	performanceTickCeiling = 200 * time.Millisecond

	// performanceTickSamples amortises scheduler noise across several ticks.
	performanceTickSamples = 20
)

// perfCI is an in-memory CIProvider standing in for GitHub. PRState is a pure
// lookup and spends one point, like a real read. No network is involved.
type perfCI struct {
	points int
}

func (c *perfCI) PRState(_ context.Context, _ string, _ int) (domain.PRState, error) {
	c.points++
	return domain.PRState{HeadRefOid: "abc123", BaseRefName: "main"}, nil
}

func (c *perfCI) reset() { c.points = 0 }

// perfReader is a read-only SnapshotReader. It performs no I/O and has no
// write methods, like the production snapshot reader.
type perfReader struct{ snap StateSnapshot }

func (r *perfReader) Snapshot() (StateSnapshot, error) { return r.snap, nil }

// perfSnapshot builds a snapshot with n active Watches. Each Watch carries a
// few checks and a subscriber, so projection work scales with n.
func perfSnapshot(n int) StateSnapshot {
	watches := make([]domain.Watch, n)
	for i := range watches {
		id := fmt.Sprintf("watch-%03d", i)
		checks := make(map[string]domain.Check, 4)
		for _, name := range []string{"build", "test", "audit", "lint"} {
			checks[name] = domain.Check{
				State: "SUCCESS", Conclusion: "SUCCESS", Required: true,
				DetailsURL: "https://example.test/" + name,
			}
		}
		watches[i] = domain.Watch{
			ID: id, Repo: "refusedguy/beacon", PR: i + 1,
			HeadSHA: "abc123", BaseRef: "main",
			Status: domain.StatusWatching, Verdict: domain.VerdictWatching,
			Revision:    uint64(i + 1),
			Checks:      checks,
			Flaky:       domain.FlakyConfig{Names: []string{"lint"}, MaxRetries: 1, Used: map[string]int{}},
			Subscribers: []domain.Subscriber{{ID: id + "-exec", Role: domain.RoleExecutor}},
		}
	}
	return StateSnapshot{
		ActiveWatches:  watches,
		PointsThisHour: 0,
		Budget:         hourlyPointBudget,
		LoadErrors:     []string{},
		Version:        ServerVersion,
	}
}

// perfTick is one daemon cycle observed at the MCP boundary: read PR state for
// every active Watch through the mock GitHub, then project the two read-only
// tools over the current snapshot. It does no network or filesystem I/O.
func perfTick(ci *perfCI, watches []domain.Watch, list *ListRunningHandler, stats *StatsHandler) {
	ctx := context.Background()
	for i := range watches {
		_, _ = ci.PRState(ctx, watches[i].Repo, watches[i].PR)
	}
	_, _, _ = list.Handle(ctx, nil, ListRunningInput{})
	_, _, _ = stats.Handle(ctx, nil, StatsInput{})
}

// memStats reads the Go runtime's memory counters once. Callers take the
// difference between two readings so test-binary baseline memory is excluded.
func memStats() runtime.MemStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m
}

// vmHWMBytes reads the peak resident set size from /proc/self/status on Linux.
// The bool is false when the file or field is unavailable; memory assertions
// that need portability use runtime.MemStats instead.
func vmHWMBytes() (uint64, bool) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "VmHWM:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}

// TestMCPTickPerformanceBudget checks the three per-tick limits at 15, 30 and
// 60 active Watches: duration, memory growth and point spend. Growth is used
// for memory so an unrelated baseline allocation in the test binary cannot
// fail the contract. VmHWM is read where Linux exposes it and logged always.
func TestMCPTickPerformanceBudget(t *testing.T) {
	for _, n := range []int{15, 30, 60} {
		t.Run(fmt.Sprintf("watches=%d", n), func(t *testing.T) {
			snap := perfSnapshot(n)
			reader := &perfReader{snap: snap}
			list := NewListRunningHandler(reader)
			stats := NewStatsHandler(reader)
			ci := &perfCI{}

			// Warm the paths so the timed run measures steady state, not
			// first-call map growth or lazy initialisation.
			for i := 0; i < 3; i++ {
				perfTick(ci, snap.ActiveWatches, list, stats)
			}

			runtime.GC()
			before := memStats()
			hwmBefore, haveHWMBefore := vmHWMBytes()

			start := time.Now()
			ci.reset()
			for i := 0; i < performanceTickSamples; i++ {
				perfTick(ci, snap.ActiveWatches, list, stats)
			}
			elapsed := time.Since(start)
			pointsPerTick := ci.points / performanceTickSamples

			runtime.GC()
			after := memStats()
			hwmAfter, haveHWMAfter := vmHWMBytes()

			perTick := elapsed / time.Duration(performanceTickSamples)
			heapGrowth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
			allocGrowth := int64(after.TotalAlloc) - int64(before.TotalAlloc)
			hwmGrowth := int64(hwmAfter) - int64(hwmBefore)
			t.Logf("watches=%d: %s/tick, heap growth=%d B, alloc growth=%d B, VmHWM=%d B (growth %d B), points/tick=%d",
				n, perTick, heapGrowth, allocGrowth, hwmAfter, hwmGrowth, pointsPerTick)

			if perTick > performanceTickCeiling {
				t.Errorf("one tick took %s, over the %s budget", perTick, performanceTickCeiling)
			}
			if heapGrowth > performanceMemoryCeiling {
				t.Errorf("heap grew by %d B, over the %d B budget", heapGrowth, performanceMemoryCeiling)
			}
			if allocGrowth > performanceMemoryCeiling {
				t.Errorf("allocated %d B across the measured ticks, over the %d B budget", allocGrowth, performanceMemoryCeiling)
			}
			if haveHWMBefore && haveHWMAfter && hwmGrowth > performanceMemoryCeiling {
				t.Errorf("VmHWM grew by %d B, over the %d B budget", hwmGrowth, performanceMemoryCeiling)
			}
			if pointsPerTick > hourlyPointBudget {
				t.Errorf("one tick spent %d points, over the %d/hour budget", pointsPerTick, hourlyPointBudget)
			}
		})
	}
}

// BenchmarkMCPTick reports the steady-state cost of one MCP observability tick
// per watch count. Benchmarks are opt-in, so the test above carries the hard
// ceilings that CI enforces on every run.
func BenchmarkMCPTick(b *testing.B) {
	for _, n := range []int{15, 30, 60} {
		b.Run(fmt.Sprintf("watches=%d", n), func(b *testing.B) {
			snap := perfSnapshot(n)
			reader := &perfReader{snap: snap}
			list := NewListRunningHandler(reader)
			stats := NewStatsHandler(reader)
			ci := &perfCI{}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				perfTick(ci, snap.ActiveWatches, list, stats)
			}
		})
	}
}
