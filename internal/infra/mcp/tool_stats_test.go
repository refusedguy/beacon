package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// waitTelemetryReaderFake is a SnapshotReader that also reports wait windows,
// so the stats handler sees the optional telemetry source. The embedded fake
// keeps the same fixed snapshot and call counting as the other tests.
type waitTelemetryReaderFake struct {
	*snapshotReaderFake
	samples []WaitSample
}

func (f *waitTelemetryReaderFake) WaitSamples() []WaitSample { return f.samples }

func callStats(t *testing.T, handler *StatsHandler) StatsOutput {
	t.Helper()
	result, out, err := handler.Handle(context.Background(), nil, StatsInput{})
	if err != nil {
		t.Fatalf("beacon_stats: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("beacon_stats returned an error result: %#v", result)
	}
	return out
}

func TestStatsResponseHasEveryField(t *testing.T) {
	reader := &snapshotReaderFake{snap: sampleSnapshot()}
	out := callStats(t, NewStatsHandler(reader))

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal stats output: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal stats output: %v", err)
	}

	for _, key := range []string{
		"pointsThisHour", "budget", "activeWatches", "persist", "loadErrors", "ladder", "version",
	} {
		if _, ok := fields[key]; !ok {
			t.Errorf("stats output missing field %q: %s", key, raw)
		}
	}
	for key, sub := range map[string][]string{
		"persist": {"seq", "lastError"},
		"ladder":  {"pending", "escalated"},
	} {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(fields[key], &nested); err != nil {
			t.Fatalf("unmarshal %s: %v", key, err)
		}
		for _, name := range sub {
			if _, ok := nested[name]; !ok {
				t.Errorf("stats.%s missing field %q: %s", key, name, fields[key])
			}
		}
	}
	if bytes.Equal(bytes.TrimSpace(fields["loadErrors"]), []byte("null")) {
		t.Errorf("loadErrors is JSON null, want an array")
	}
}

func TestStatsValuesComeFromSnapshot(t *testing.T) {
	snap := sampleSnapshot()
	reader := &snapshotReaderFake{snap: snap}
	out := callStats(t, NewStatsHandler(reader))

	if out.PointsThisHour != snap.PointsThisHour {
		t.Errorf("pointsThisHour = %d, want %d", out.PointsThisHour, snap.PointsThisHour)
	}
	if out.Budget != snap.Budget {
		t.Errorf("budget = %d, want %d", out.Budget, snap.Budget)
	}
	if out.ActiveWatches != len(snap.ActiveWatches) {
		t.Errorf("activeWatches = %d, want %d", out.ActiveWatches, len(snap.ActiveWatches))
	}
	if out.Persist.Seq != snap.PersistSeq {
		t.Errorf("persist.seq = %d, want %d", out.Persist.Seq, snap.PersistSeq)
	}
	if out.Persist.LastError != snap.LastPersistErr {
		t.Errorf("persist.lastError = %q, want %q", out.Persist.LastError, snap.LastPersistErr)
	}
	if !reflect.DeepEqual(out.LoadErrors, snap.LoadErrors) {
		t.Errorf("loadErrors = %v, want %v", out.LoadErrors, snap.LoadErrors)
	}
	if out.Ladder.Pending != snap.LadderPending || out.Ladder.Escalated != snap.LadderEscalated {
		t.Errorf("ladder = %+v, want pending %d escalated %d", out.Ladder, snap.LadderPending, snap.LadderEscalated)
	}
	if out.Version != snap.Version {
		t.Errorf("version = %q, want %q", out.Version, snap.Version)
	}
}

func TestStatsDoesNotWriteState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"seq":1}`), 0600); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	before, beforeMod := stateFingerprint(t, path)

	reader := &snapshotReaderFake{path: path, snap: sampleSnapshot()}
	handler := NewStatsHandler(reader)
	callStats(t, handler)
	callStats(t, handler)

	after, afterMod := stateFingerprint(t, path)
	if !bytes.Equal(before, after) {
		t.Errorf("state file bytes changed: %q -> %q", before, after)
	}
	if !beforeMod.Equal(afterMod) {
		t.Errorf("state file mtime changed: %v -> %v", beforeMod, afterMod)
	}
	// Both calls land inside the ten-minute refresh window, so the counter is
	// read once and the second call is served from the cache (ADR 0037 §6).
	if reader.calls != 1 {
		t.Errorf("snapshot reads = %d, want 1 within the %s refresh window", reader.calls, planLimitReadInterval)
	}
}

func TestStatsRepeatable(t *testing.T) {
	reader := &snapshotReaderFake{snap: sampleSnapshot()}
	handler := NewStatsHandler(reader)

	first := callStats(t, handler)
	second := callStats(t, handler)

	if !reflect.DeepEqual(first, second) {
		t.Errorf("two calls with unchanged state differ:\n%#v\n%#v", first, second)
	}
}

func TestStatsEmptySlicesStayArrays(t *testing.T) {
	snap := sampleSnapshot()
	snap.ActiveWatches = nil
	snap.LoadErrors = nil
	reader := &snapshotReaderFake{snap: snap}
	out := callStats(t, NewStatsHandler(reader))

	if out.LoadErrors == nil {
		t.Error("loadErrors is nil, want an empty array")
	}
	if out.ActiveWatches != 0 {
		t.Errorf("activeWatches = %d, want 0", out.ActiveWatches)
	}
}

func TestStatsPropagatesReadError(t *testing.T) {
	want := errors.New("store unavailable")
	reader := &snapshotReaderFake{err: want}
	handler := NewStatsHandler(reader)

	_, _, err := handler.Handle(context.Background(), nil, StatsInput{})
	if !errors.Is(err, want) {
		t.Fatalf("handler error = %v, want it to wrap %v", err, want)
	}
}

func TestStatsPlanLimitThresholds(t *testing.T) {
	const budget = 500
	cases := []struct {
		name  string
		used  int
		pct   int
		level string
	}{
		{"below yellow", 374, 74, PlanLimitOK},
		{"at yellow", 375, 75, PlanLimitYellow},
		{"between thresholds", 449, 89, PlanLimitYellow},
		{"at red", 450, 90, PlanLimitRed},
		{"over budget", 500, 100, PlanLimitRed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := planLimitInfo(tc.used, budget)
			if info.UsedPercent != tc.pct {
				t.Errorf("usedPercent = %d, want %d", info.UsedPercent, tc.pct)
			}
			if info.Level != tc.level {
				t.Errorf("level = %q, want %q", info.Level, tc.level)
			}
			if tc.level == PlanLimitOK {
				if info.Warning != "" {
					t.Errorf("warning = %q, want empty below thresholds", info.Warning)
				}
			} else if info.Warning == "" {
				t.Error("warning is empty at a threshold")
			}

			snap := sampleSnapshot()
			snap.PointsThisHour = tc.used
			snap.Budget = budget
			out := callStats(t, NewStatsHandler(&snapshotReaderFake{snap: snap}))
			warning := planLimitWarning(out)
			if tc.level == PlanLimitOK {
				if warning != "" {
					t.Errorf("stats warning = %q, want empty below thresholds", warning)
				}
				return
			}
			if !strings.Contains(warning, tc.level) {
				t.Errorf("stats warning = %q, want it to contain %q", warning, tc.level)
			}
		})
	}
}

func TestStatsPlanLimitWarningIsVisibleInJSON(t *testing.T) {
	snap := sampleSnapshot()
	snap.PointsThisHour = 450
	snap.Budget = 500
	out := callStats(t, NewStatsHandler(&snapshotReaderFake{snap: snap}))

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal stats output: %v", err)
	}
	var fields struct {
		LoadErrors []string `json:"loadErrors"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal stats output: %v", err)
	}
	var found bool
	for _, entry := range fields.LoadErrors {
		if strings.HasPrefix(entry, planLimitWarningPrefix) && strings.Contains(entry, PlanLimitRed) {
			found = true
		}
	}
	if !found {
		t.Errorf("serialized stats output has no red plan-limit warning: %s", raw)
	}
}

func TestStatsCounterReadEveryTenMinutes(t *testing.T) {
	reader := &snapshotReaderFake{snap: sampleSnapshot()}
	clock := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	handler := &StatsHandler{reader: reader, now: func() time.Time { return clock }}

	first := callStats(t, handler)
	if reader.calls != 1 {
		t.Fatalf("snapshot reads = %d, want 1 after first call", reader.calls)
	}
	if first.PointsThisHour != 42 {
		t.Fatalf("pointsThisHour = %d, want 42 before refresh", first.PointsThisHour)
	}

	clock = clock.Add(5 * time.Minute)
	callStats(t, handler)
	if reader.calls != 1 {
		t.Fatalf("snapshot reads = %d, want 1 within the %s window", reader.calls, planLimitReadInterval)
	}

	clock = clock.Add(6 * time.Minute)
	reader.snap.PointsThisHour = 380 // 76% of 500
	reader.snap.Budget = 500
	second := callStats(t, handler)
	if reader.calls != 2 {
		t.Fatalf("snapshot reads = %d, want 2 after %s elapsed", reader.calls, planLimitReadInterval)
	}
	if second.PointsThisHour != 380 {
		t.Errorf("pointsThisHour = %d, want 380 after refresh", second.PointsThisHour)
	}
	if warning := planLimitWarning(second); !strings.Contains(warning, PlanLimitYellow) {
		t.Errorf("warning after refresh = %q, want yellow", warning)
	}
}

func TestStatsPlanLimitZeroBudget(t *testing.T) {
	snap := sampleSnapshot()
	snap.Budget = 0
	out := callStats(t, NewStatsHandler(&snapshotReaderFake{snap: snap}))

	if warning := planLimitWarning(out); warning != "" {
		t.Errorf("warning with zero budget = %q, want empty", warning)
	}
}

func TestWaitTelemetryComputation(t *testing.T) {
	cases := []struct {
		name    string
		samples []WaitSample
		pct     int
		avg     float64
		windows int
	}{
		{"no windows", nil, 0, 0, 0},
		{
			"one window",
			[]WaitSample{{Wait: 40 * time.Second, Total: 100 * time.Second, Workers: 2}},
			40, 2, 1,
		},
		{
			"two windows averaged",
			[]WaitSample{
				{Wait: 30 * time.Second, Total: 100 * time.Second, Workers: 1},
				{Wait: 60 * time.Second, Total: 100 * time.Second, Workers: 2},
			},
			45, 1.5, 2,
		},
		{
			"integer percent",
			[]WaitSample{{Wait: time.Second, Total: 3 * time.Second, Workers: 1}},
			33, 1, 1,
		},
		{
			"zero wall time",
			[]WaitSample{{Wait: 10 * time.Second, Workers: 3}},
			0, 3, 1,
		},
		{
			"negative values clamped",
			[]WaitSample{{Wait: -5 * time.Second, Total: -10 * time.Second, Workers: -2}},
			0, 0, 1,
		},
		{
			"overlapping windows clamped",
			[]WaitSample{{Wait: 200 * time.Second, Total: 100 * time.Second, Workers: 1}},
			100, 1, 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := waitTelemetry(tc.samples)
			if info.WaitPercent != tc.pct {
				t.Errorf("waitPercent = %d, want %d", info.WaitPercent, tc.pct)
			}
			if info.AvgWorkers != tc.avg {
				t.Errorf("avgWorkers = %v, want %v", info.AvgWorkers, tc.avg)
			}
			if info.Windows != tc.windows {
				t.Errorf("windows = %d, want %d", info.Windows, tc.windows)
			}
		})
	}
}

func TestWaitTelemetryInStats(t *testing.T) {
	reader := &waitTelemetryReaderFake{
		snapshotReaderFake: &snapshotReaderFake{snap: sampleSnapshot()},
		samples: []WaitSample{
			{Wait: 30 * time.Second, Total: 100 * time.Second, Workers: 1},
			{Wait: 60 * time.Second, Total: 100 * time.Second, Workers: 2},
		},
	}
	out := callStats(t, NewStatsHandler(reader))

	entry := waitTelemetryEntry(out)
	if !strings.Contains(entry, "ci wait 45%") {
		t.Errorf("wait telemetry = %q, want wait share 45%%", entry)
	}
	if !strings.Contains(entry, "avg 1.5 workers") {
		t.Errorf("wait telemetry = %q, want average 1.5 workers", entry)
	}
	if !strings.Contains(entry, "over 2 windows") {
		t.Errorf("wait telemetry = %q, want two windows", entry)
	}
	if !strings.HasPrefix(entry, waitTelemetryPrefix) {
		t.Errorf("wait telemetry = %q, want prefix %q", entry, waitTelemetryPrefix)
	}
	// The original load error is still first; telemetry is an added entry.
	if out.LoadErrors[0] != "line 3: bad payload" {
		t.Errorf("loadErrors[0] = %q, want the stored error", out.LoadErrors[0])
	}
	// Idle time is not a plan-limit warning (ADR 0056 p6, T3.52).
	if warning := planLimitWarning(out); warning != "" {
		t.Errorf("idle metric leaked into plan-limit warning: %q", warning)
	}
}

func TestWaitTelemetryAbsentWithoutSamples(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reader SnapshotReader
	}{
		{"plain reader", &snapshotReaderFake{snap: sampleSnapshot()}},
		{"empty samples", &waitTelemetryReaderFake{snapshotReaderFake: &snapshotReaderFake{snap: sampleSnapshot()}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := callStats(t, NewStatsHandler(tc.reader))
			if entry := waitTelemetryEntry(out); entry != "" {
				t.Errorf("wait telemetry = %q, want no entry", entry)
			}
			if !reflect.DeepEqual(out.LoadErrors, sampleSnapshot().LoadErrors) {
				t.Errorf("loadErrors = %v, want unchanged snapshot errors", out.LoadErrors)
			}
		})
	}
}

func TestWaitTelemetryCoexistsWithPlanLimit(t *testing.T) {
	snap := sampleSnapshot()
	snap.PointsThisHour = 450
	snap.Budget = 500
	reader := &waitTelemetryReaderFake{
		snapshotReaderFake: &snapshotReaderFake{snap: snap},
		samples:            []WaitSample{{Wait: time.Minute, Total: 2 * time.Minute, Workers: 1}},
	}
	out := callStats(t, NewStatsHandler(reader))

	if warning := planLimitWarning(out); !strings.Contains(warning, PlanLimitRed) {
		t.Errorf("plan-limit warning = %q, want red", warning)
	}
	if entry := waitTelemetryEntry(out); !strings.Contains(entry, "ci wait 50%") {
		t.Errorf("wait telemetry = %q, want a 50%% wait share", entry)
	}
	if len(out.LoadErrors) != len(snap.LoadErrors)+2 {
		t.Errorf("loadErrors = %v, want stored error plus warning and telemetry", out.LoadErrors)
	}
}

// TestWaitTelemetryDoesNotBreakOutputSchema proves the metric uses only the
// existing loadErrors array: the derived beacon_stats schema forbids extra
// properties, so a new top-level field would be rejected by MCP clients.
func TestWaitTelemetryDoesNotBreakOutputSchema(t *testing.T) {
	schemas, err := AllOutputSchemas()
	if err != nil {
		t.Fatalf("AllOutputSchemas: %v", err)
	}
	statsSchema := schemas["beacon_stats"]
	resolved, err := statsSchema.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve stats schema: %v", err)
	}

	snap := sampleSnapshot()
	snap.PointsThisHour = 450
	snap.Budget = 500
	reader := &waitTelemetryReaderFake{
		snapshotReaderFake: &snapshotReaderFake{snap: snap},
		samples:            []WaitSample{{Wait: 40 * time.Second, Total: time.Minute, Workers: 2}},
	}
	out := callStats(t, NewStatsHandler(reader))

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal stats output: %v", err)
	}
	var instance map[string]any
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatalf("unmarshal stats output: %v", err)
	}
	if err := resolved.Validate(instance); err != nil {
		t.Fatalf("stats output with wait telemetry fails derived schema: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal stats fields: %v", err)
	}
	for key := range fields {
		if _, ok := statsSchema.Properties[key]; !ok {
			t.Errorf("stats output has field %q not in the derived schema", key)
		}
	}
}
