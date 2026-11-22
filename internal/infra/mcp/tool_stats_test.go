package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// waitTelemetryReaderFake is a SnapshotReader that also reports wait windows,
// so the stats handler sees the optional telemetry source. The embedded fake
// keeps the same fixed snapshot and call counting as the other tests.
type waitTelemetryReaderFake struct {
	*snapshotReaderFake
	samples []WaitSample
}

func (f *waitTelemetryReaderFake) WaitSamples() []WaitSample { return f.samples }

// registryStatsFake is a mock registry port. It returns fixed counters and an
// optional read error, and counts calls so a test can prove the handler reads
// it once per request.
type registryStatsFake struct {
	stats RegistryStats
	err   error
	calls int
}

func (f *registryStatsFake) RegistryStats(context.Context) (RegistryStats, error) {
	f.calls++
	return f.stats, f.err
}

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
		"pointsThisHour", "budget", "activeWatches", "blocked", "persist", "loadErrors", "ladder", "version",
		"registeredAgents", "staleAgents", "configId",
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
	// Every call reads the store again: a cached counter can hide a budget
	// spike from the orchestrator.
	if reader.calls != 2 {
		t.Errorf("snapshot reads = %d, want 2 fresh reads", reader.calls)
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

// blockedWatchSnapshot puts two blocked watches on the radar. One carries a
// blocked status, the other a block record. Both stay in ActiveWatches through
// LiveWatches, so the active-watch count still includes them.
func blockedWatchSnapshot() StateSnapshot {
	snap := sampleSnapshot()
	byStatus := sampleWatch("b1", domain.VerdictWatching, 0)
	byStatus.Status = domain.StatusBlocked
	byRecord := sampleWatch("b2", domain.VerdictWatching, 0)
	byRecord.Blocked = &domain.BlockedInfo{Reason: domain.BlockReasonBaseChecksFailed}
	snap.LiveWatches = append([]domain.Watch{snap.ActiveWatches[0]}, byStatus, byRecord)
	return snap
}

func TestStatsBlockedCount(t *testing.T) {
	cases := []struct {
		name string
		snap StateSnapshot
		want int
	}{
		{"no blocked watches", sampleSnapshot(), 0},
		{"two blocked watches", blockedWatchSnapshot(), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := callStats(t, NewStatsHandler(&snapshotReaderFake{snap: tc.snap}))

			if out.Blocked != tc.want {
				t.Errorf("blocked = %d, want %d", out.Blocked, tc.want)
			}
			if out.ActiveWatches != len(tc.snap.RadarWatches()) {
				t.Errorf("activeWatches = %d, want %d", out.ActiveWatches, len(tc.snap.RadarWatches()))
			}

			var diagnostic string
			for _, entry := range out.LoadErrors {
				if strings.HasPrefix(entry, blockedWatchPrefix) {
					diagnostic = entry
				}
			}
			if tc.want == 0 {
				if diagnostic != "" {
					t.Errorf("blocked diagnostic = %q, want none", diagnostic)
				}
				return
			}
			if want := fmt.Sprintf("%s%d", blockedWatchPrefix, tc.want); diagnostic != want {
				t.Errorf("blocked diagnostic = %q, want %q", diagnostic, want)
			}
		})
	}
}

// TestStatsBlockedInJSONAndSchema proves the count reaches MCP clients as a
// top-level field and that the derived schema accepts it.
func TestStatsBlockedInJSONAndSchema(t *testing.T) {
	schemas, err := AllOutputSchemas()
	if err != nil {
		t.Fatalf("AllOutputSchemas: %v", err)
	}
	statsSchema := schemas["beacon_stats"]
	if _, ok := statsSchema.Properties["blocked"]; !ok {
		t.Fatal("beacon_stats schema has no blocked property")
	}
	resolved, err := statsSchema.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve stats schema: %v", err)
	}

	out := callStats(t, NewStatsHandler(&snapshotReaderFake{snap: blockedWatchSnapshot()}))
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal stats output: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal stats fields: %v", err)
	}
	blockedRaw, ok := fields["blocked"]
	if !ok {
		t.Fatalf("stats output missing blocked field: %s", raw)
	}
	var got int
	if err := json.Unmarshal(blockedRaw, &got); err != nil {
		t.Fatalf("unmarshal blocked field: %v", err)
	}
	if got != 2 {
		t.Errorf("serialized blocked = %d, want 2: %s", got, raw)
	}

	var instance map[string]any
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatalf("unmarshal stats instance: %v", err)
	}
	if err := resolved.Validate(instance); err != nil {
		t.Fatalf("stats output with blocked fails derived schema: %v", err)
	}
	for key := range fields {
		if _, ok := statsSchema.Properties[key]; !ok {
			t.Errorf("stats output has field %q not in the derived schema", key)
		}
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

func TestStatsReadsFreshCountersEveryCall(t *testing.T) {
	reader := &snapshotReaderFake{snap: sampleSnapshot()}
	handler := NewStatsHandler(reader)

	first := callStats(t, handler)
	if reader.calls != 1 {
		t.Fatalf("snapshot reads = %d, want 1 after first call", reader.calls)
	}
	if first.PointsThisHour != 42 {
		t.Fatalf("pointsThisHour = %d, want 42 before refresh", first.PointsThisHour)
	}

	// The same instant, a changed counter: the second call must still hit the
	// store and report the new value with its warning.
	reader.snap.PointsThisHour = 380 // 76% of 500
	reader.snap.Budget = 500
	second := callStats(t, handler)
	if reader.calls != 2 {
		t.Fatalf("snapshot reads = %d, want 2 on the second call", reader.calls)
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

// TestStatsRegistryCountersFromPort proves the three T5.60 fields come from the
// registry port. The seven original fields keep their values and names.
func TestStatsRegistryCountersFromPort(t *testing.T) {
	snap := sampleSnapshot()
	port := &registryStatsFake{stats: RegistryStats{
		RegisteredAgents: 7,
		StaleAgents:      2,
		ConfigID:         "ci=stub,gpt=codex",
	}}
	handler := NewStatsHandler(&snapshotReaderFake{snap: snap})
	handler.SetRegistryStatsReader(port)
	out := callStats(t, handler)

	if out.RegisteredAgents != 7 {
		t.Errorf("registeredAgents = %d, want 7", out.RegisteredAgents)
	}
	if out.StaleAgents != 2 {
		t.Errorf("staleAgents = %d, want 2", out.StaleAgents)
	}
	if out.ConfigID != "ci=stub,gpt=codex" {
		t.Errorf("configId = %q, want ci=stub,gpt=codex", out.ConfigID)
	}
	if port.calls != 1 {
		t.Errorf("registry reads = %d, want 1", port.calls)
	}

	// The original contract is unchanged by the additive fields.
	if out.PointsThisHour != snap.PointsThisHour || out.Budget != snap.Budget ||
		out.ActiveWatches != len(snap.RadarWatches()) || out.Persist.Seq != snap.PersistSeq ||
		!reflect.DeepEqual(out.LoadErrors, snap.LoadErrors) ||
		out.Ladder.Pending != snap.LadderPending || out.Ladder.Escalated != snap.LadderEscalated ||
		out.Version != snap.Version {
		t.Errorf("original stats fields changed with a registry port: %+v", out)
	}
}

// TestStatsRegistryCountersAbsentWithoutPort keeps the pre-T5.60 behaviour for
// callers that have not wired a registry reader.
func TestStatsRegistryCountersAbsentWithoutPort(t *testing.T) {
	out := callStats(t, NewStatsHandler(&snapshotReaderFake{snap: sampleSnapshot()}))
	if out.RegisteredAgents != 0 || out.StaleAgents != 0 || out.ConfigID != "" {
		t.Errorf("registry counters without a port = %+v, want zero values", out)
	}
}

// TestStatsRegistryCountersFromSnapshotProjection ties the port to the registry
// projection already carried by StateSnapshot (T5.29): counts and config id are
// derived from the same rows list_running reads.
func TestStatsRegistryCountersFromSnapshotProjection(t *testing.T) {
	snap := sampleSnapshot()
	snap.Registry = []RegistryEntry{
		{ID: "a", Config: "cfg-1", Status: RegistryActive},
		{ID: "b", Config: "cfg-1", Status: RegistryStale},
		{ID: "c", Config: "cfg-1", Status: RegistryStale},
	}
	reader := &snapshotReaderFake{snap: snap}
	handler := NewStatsHandler(reader)
	handler.SetRegistryStatsReader(NewSnapshotRegistryStats(reader))
	out := callStats(t, handler)

	if out.RegisteredAgents != 3 {
		t.Errorf("registeredAgents = %d, want 3", out.RegisteredAgents)
	}
	if out.StaleAgents != 2 {
		t.Errorf("staleAgents = %d, want 2", out.StaleAgents)
	}
	if out.ConfigID != "cfg-1" {
		t.Errorf("configId = %q, want cfg-1", out.ConfigID)
	}
}

// TestStatsRegistryPortErrorIsDiagnostic proves a failed registry read is
// reported as a diagnostic and never produces invented counters.
func TestStatsRegistryPortErrorIsDiagnostic(t *testing.T) {
	want := errors.New("registry unavailable")
	handler := NewStatsHandler(&snapshotReaderFake{snap: sampleSnapshot()})
	handler.SetRegistryStatsReader(&registryStatsFake{err: want})
	out := callStats(t, handler)

	if entry := registryStatsError(out); !strings.Contains(entry, want.Error()) {
		t.Errorf("registry diagnostic = %q, want it to contain %q", entry, want.Error())
	}
	if out.RegisteredAgents != 0 || out.StaleAgents != 0 || out.ConfigID != "" {
		t.Errorf("counters after a failed registry read = %+v, want zero values", out)
	}
	if out.LoadErrors[0] != "line 3: bad payload" {
		t.Errorf("loadErrors[0] = %q, want the stored error", out.LoadErrors[0])
	}
}

// TestStatsRegistryPortCanBeRemoved proves the port is optional in both
// directions, so a caller can fall back to the legacy response.
func TestStatsRegistryPortCanBeRemoved(t *testing.T) {
	handler := NewStatsHandler(&snapshotReaderFake{snap: sampleSnapshot()})
	handler.SetRegistryStatsReader(&registryStatsFake{stats: RegistryStats{RegisteredAgents: 9}})
	handler.SetRegistryStatsReader(nil)
	out := callStats(t, handler)
	if out.RegisteredAgents != 0 || out.StaleAgents != 0 || out.ConfigID != "" {
		t.Errorf("counters after removing the port = %+v, want zero values", out)
	}
}

// TestStatsRegistryCountersInDerivedSchema proves the new fields are in the
// reflected output schema next to the seven original ones.
func TestStatsRegistryCountersInDerivedSchema(t *testing.T) {
	schemas, err := AllOutputSchemas()
	if err != nil {
		t.Fatalf("AllOutputSchemas: %v", err)
	}
	statsSchema := schemas["beacon_stats"]
	resolved, err := statsSchema.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve stats schema: %v", err)
	}

	handler := NewStatsHandler(&snapshotReaderFake{snap: sampleSnapshot()})
	handler.SetRegistryStatsReader(&registryStatsFake{stats: RegistryStats{
		RegisteredAgents: 5,
		StaleAgents:      1,
		ConfigID:         "cfg-1",
	}})
	raw, err := json.Marshal(callStats(t, handler))
	if err != nil {
		t.Fatalf("marshal stats output: %v", err)
	}
	var instance map[string]any
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatalf("unmarshal stats output: %v", err)
	}
	if err := resolved.Validate(instance); err != nil {
		t.Fatalf("stats output with registry counters fails derived schema: %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal stats fields: %v", err)
	}
	for _, key := range []string{
		"pointsThisHour", "budget", "activeWatches", "persist", "loadErrors", "ladder", "version",
		"registeredAgents", "staleAgents", "configId",
	} {
		if _, ok := fields[key]; !ok {
			t.Errorf("stats output missing field %q: %s", key, raw)
		}
	}
	for key := range fields {
		if _, ok := statsSchema.Properties[key]; !ok {
			t.Errorf("stats output has field %q not in the derived schema", key)
		}
	}
}

// watchCountSnapshot returns a sample snapshot with exactly n active watches.
// The active-watch limit is 200, so n drives the warning percentage directly.
func watchCountSnapshot(n int) StateSnapshot {
	snap := sampleSnapshot()
	watches := make([]domain.Watch, n)
	for i := range watches {
		watches[i] = sampleWatch(fmt.Sprintf("w%d", i), domain.VerdictWatching, 0)
	}
	snap.ActiveWatches = watches
	return snap
}

func TestActiveWatchLimitThresholds(t *testing.T) {
	cases := []struct {
		name     string
		watches  int
		pct      int
		warnings int
	}{
		{"below yellow", 148, 74, 0},
		{"at yellow", 150, 75, 1},
		{"between thresholds", 178, 89, 1},
		{"at red", 180, 90, 2},
		{"over limit", 200, 100, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &snapshotReaderFake{snap: watchCountSnapshot(tc.watches)}
			out := callStats(t, NewStatsHandler(reader))
			warnings := activeWatchWarnings(out)
			if len(warnings) != tc.warnings {
				t.Fatalf("warnings = %v, want %d entries", warnings, tc.warnings)
			}
			for _, w := range warnings {
				if !strings.HasPrefix(w, activeWatchWarningPrefix) {
					t.Errorf("warning %q missing prefix %q", w, activeWatchWarningPrefix)
				}
				if want := fmt.Sprintf("%d%%", tc.pct); !strings.Contains(w, want) {
					t.Errorf("warning %q missing percent %q", w, want)
				}
			}
			if out.ActiveWatches != tc.watches {
				t.Errorf("activeWatches = %d, want %d", out.ActiveWatches, tc.watches)
			}
		})
	}
}

func TestActiveWatchWarningFiresOncePerCrossingAndResets(t *testing.T) {
	reader := &snapshotReaderFake{snap: watchCountSnapshot(148)}
	handler := NewStatsHandler(reader)

	check := func(wantWarnings, wantFires int, label string) {
		t.Helper()
		out := callStats(t, handler)
		if got := activeWatchWarnings(out); len(got) != wantWarnings {
			t.Errorf("%s: warnings = %v, want %d", label, got, wantWarnings)
		}
		if got := handler.watchLimit.FireCount(); got != wantFires {
			t.Errorf("%s: fires = %d, want %d", label, got, wantFires)
		}
	}

	check(0, 0, "74%")
	reader.snap = watchCountSnapshot(150)
	check(1, 1, "75%")
	reader.snap = watchCountSnapshot(180)
	check(2, 2, "90%")
	// A second tick at the same level adds no duplicate and no new fire.
	check(2, 2, "90% repeat")
	// Dropping below both thresholds clears the flags.
	reader.snap = watchCountSnapshot(148)
	check(0, 2, "74% after drop")
	// Rising again fires both thresholds anew.
	reader.snap = watchCountSnapshot(180)
	check(2, 4, "90% again")
}

func TestActiveWatchWarningDoesNotChangeVerdictOrOtherFields(t *testing.T) {
	lowOut := callStats(t, NewStatsHandler(&snapshotReaderFake{snap: watchCountSnapshot(148)}))
	highOut := callStats(t, NewStatsHandler(&snapshotReaderFake{snap: watchCountSnapshot(180)}))

	if got := activeWatchWarnings(lowOut); len(got) != 0 {
		t.Fatalf("low snapshot warnings = %v, want none", got)
	}
	if got := activeWatchWarnings(highOut); len(got) != 2 {
		t.Fatalf("high snapshot warnings = %v, want 2", got)
	}
	if highOut.PointsThisHour != lowOut.PointsThisHour ||
		highOut.Budget != lowOut.Budget ||
		highOut.Persist != lowOut.Persist ||
		highOut.Ladder != lowOut.Ladder ||
		highOut.Version != lowOut.Version ||
		highOut.RegisteredAgents != lowOut.RegisteredAgents ||
		highOut.StaleAgents != lowOut.StaleAgents ||
		highOut.ConfigID != lowOut.ConfigID {
		t.Errorf("warning changed non-watch fields: low=%+v high=%+v", lowOut, highOut)
	}
	if got, want := planLimitWarning(highOut), planLimitWarning(lowOut); got != want {
		t.Errorf("plan-limit warning changed: %q, want %q", got, want)
	}
}

func TestWatchLimitLatchZeroLimit(t *testing.T) {
	latch := &watchLimitLatch{}
	if got := latch.observe(180, 0); len(got) != 0 {
		t.Errorf("zero-limit warnings = %v, want none", got)
	}
	if got := latch.FireCount(); got != 0 {
		t.Errorf("zero-limit fires = %d, want 0", got)
	}
}

func TestActiveWatchWarningInJSONAndSchema(t *testing.T) {
	schemas, err := AllOutputSchemas()
	if err != nil {
		t.Fatalf("AllOutputSchemas: %v", err)
	}
	statsSchema := schemas["beacon_stats"]
	resolved, err := statsSchema.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve stats schema: %v", err)
	}

	out := callStats(t, NewStatsHandler(&snapshotReaderFake{snap: watchCountSnapshot(180)}))
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
	var found int
	for _, entry := range fields.LoadErrors {
		if strings.HasPrefix(entry, activeWatchWarningPrefix) {
			found++
		}
	}
	if found != 2 {
		t.Errorf("serialized stats has %d active-watch warnings, want 2: %s", found, raw)
	}

	var instance map[string]any
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatalf("unmarshal stats instance: %v", err)
	}
	if err := resolved.Validate(instance); err != nil {
		t.Fatalf("stats output with active-watch warnings fails derived schema: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal stats fields: %v", err)
	}
	for key := range top {
		if _, ok := statsSchema.Properties[key]; !ok {
			t.Errorf("stats output has field %q not in the derived schema", key)
		}
	}
}

// loadErrorSnapshot builds a snapshot with exactly n diagnostics. The handler
// appends no extra entry for this small, healthy snapshot.
func loadErrorSnapshot(n int) StateSnapshot {
	snap := sampleSnapshot()
	snap.LoadErrors = make([]string, n)
	for i := range snap.LoadErrors {
		snap.LoadErrors[i] = fmt.Sprintf("diagnostic %d", i)
	}
	return snap
}

func TestStatsRecordLimit(t *testing.T) {
	cases := []struct {
		name    string
		entries int
		wantErr bool
	}{
		{"empty", 0, false},
		{"at limit", statsMaxRecords, false},
		{"over limit", statsMaxRecords + 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, out, err := NewStatsHandler(&snapshotReaderFake{snap: loadErrorSnapshot(tc.entries)}).Handle(context.Background(), nil, StatsInput{})
			if tc.wantErr {
				assertLimitError(t, err, "200-loadError", "fewer or shorter loadErrors")
				return
			}
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if len(out.LoadErrors) != tc.entries {
				t.Fatalf("loadErrors = %d, want %d", len(out.LoadErrors), tc.entries)
			}
		})
	}
}

func TestStatsByteLimit(t *testing.T) {
	snap := sampleSnapshot()
	snap.LoadErrors = []string{strings.Repeat("e", statsMaxBytes+1)}
	_, _, err := NewStatsHandler(&snapshotReaderFake{snap: snap}).Handle(context.Background(), nil, StatsInput{})
	assertLimitError(t, err, "262144-byte", "fewer or shorter loadErrors")
}

func TestStatsLimitIsToolError(t *testing.T) {
	session := connectTestSession(t, Dependencies{Snapshot: SnapshotFunc(func() (StateSnapshot, error) {
		return loadErrorSnapshot(statsMaxRecords + 1), nil
	})})
	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "beacon_stats",
		Arguments: StatsInput{},
	})
	if err != nil {
		t.Fatalf("tools/call beacon_stats: %v", err)
	}
	if !result.IsError {
		t.Fatalf("oversized stats did not set isError: %+v", result.StructuredContent)
	}
	body, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatalf("marshal error content: %v", err)
	}
	if !strings.Contains(string(body), "200-loadError") || !strings.Contains(string(body), "fewer or shorter loadErrors") {
		t.Fatalf("tool error text = %s, want the limit and the narrowing hint", body)
	}
}
