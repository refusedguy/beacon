// Package mcp: beacon_stats as a pure read of Beacon state.
package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

const (
	// planLimitYellowPercent is the ADR 0037 §6 threshold that turns the plan
	// limit warning yellow.
	planLimitYellowPercent = 75
	// planLimitRedPercent is the ADR 0037 §6 threshold that turns the plan
	// limit warning red.
	planLimitRedPercent = 90
	// planLimitWarningPrefix marks the plan-limit entry inside loadErrors so a
	// reader can tell a warning from a state load failure.
	planLimitWarningPrefix = "plan limit warning: "
	// waitTelemetryPrefix marks the wait idle metric inside loadErrors. The
	// prefix differs from planLimitWarningPrefix because ADR 0056 p6 measures
	// idle time and T3.52 warns about an approaching plan limit.
	waitTelemetryPrefix = "wait telemetry: "
)

// Plan limit levels reported in the stats response. A zero PlanLimitInfo uses
// PlanLimitOK.
const (
	PlanLimitOK     = "ok"
	PlanLimitYellow = "yellow"
	PlanLimitRed    = "red"
)

// PlanLimitInfo is the plan-limit warning derived from limit counters. It is
// computed from values already present in a state snapshot, so producing it
// never calls a model.
type PlanLimitInfo struct {
	UsedPercent int
	Level       string
	Warning     string
}

// WaitSample is one observation window of the wait telemetry (ADR 0056 p6).
// Wait is the time spent waiting for CI, Total is the wall time of the window,
// and Workers is the number of workers active during it. A negative duration
// is clamped to zero so a bad sample cannot produce a negative percentage.
type WaitSample struct {
	Wait    time.Duration
	Total   time.Duration
	Workers int
}

// WaitTelemetryInfo is the idle metric derived from a series of wait samples:
// the share of wall time spent waiting for CI and the mean number of
// concurrently working workers. It is a measurement only and never changes a
// plan-limit threshold (T3.52).
type WaitTelemetryInfo struct {
	WaitPercent int
	AvgWorkers  float64
	Windows     int
}

// WaitTelemetryReader is an optional extension of SnapshotReader. A reader
// that can report observed wait windows lets beacon_stats expose idle time.
// Readers without this method keep the exact previous response, so adding a
// producer of these samples elsewhere never changes existing callers.
type WaitTelemetryReader interface {
	WaitSamples() []WaitSample
}

// StatsInput is the empty input of beacon_stats. The tool takes no arguments.
type StatsInput struct{}

// StatsHandler implements beacon_stats over the read-only snapshot port. It
// holds no writer and takes no argument. Every call reads a fresh snapshot: a
// cached counter can hide a budget spike or a growing watch list from the
// orchestrator, so the tool reports what the store says right now.
type StatsHandler struct {
	reader SnapshotReader
}

// NewStatsHandler builds the handler. Pass handler.Handle to mcpsdk.AddTool
// when wiring the tool into a server.
func NewStatsHandler(reader SnapshotReader) *StatsHandler {
	return &StatsHandler{reader: reader}
}

// planLimitInfo computes the warning from used and budget. A missing or
// non-positive budget reports OK rather than dividing by zero.
func planLimitInfo(used, budget int) PlanLimitInfo {
	if budget <= 0 {
		return PlanLimitInfo{Level: PlanLimitOK}
	}
	pct := int(int64(used) * 100 / int64(budget))
	info := PlanLimitInfo{UsedPercent: pct, Level: PlanLimitOK}
	switch {
	case pct >= planLimitRedPercent:
		info.Level = PlanLimitRed
		info.Warning = fmt.Sprintf("red at %d%% used (threshold %d%%)", pct, planLimitRedPercent)
	case pct >= planLimitYellowPercent:
		info.Level = PlanLimitYellow
		info.Warning = fmt.Sprintf("yellow at %d%% used (threshold %d%%)", pct, planLimitYellowPercent)
	}
	return info
}

// warningEntry renders the warning as a loadErrors entry. StatsOutput has no
// dedicated warning field, and its schema forbids extra properties, so the
// warning rides in the existing diagnostic array with a stable prefix.
func (i PlanLimitInfo) warningEntry() string {
	if i.Level == PlanLimitOK {
		return ""
	}
	return planLimitWarningPrefix + i.Warning
}

// waitTelemetry aggregates observation windows into the idle metric. The
// computation clamps the percentage to 0-100 even for overlapping samples, and
// a window with no wall time adds no wait share. The mean counts only observed
// windows, so an empty series reports zero instead of dividing by zero.
func waitTelemetry(samples []WaitSample) WaitTelemetryInfo {
	if len(samples) == 0 {
		return WaitTelemetryInfo{}
	}
	var waited, total time.Duration
	var workers int64
	for _, s := range samples {
		if s.Wait < 0 {
			s.Wait = 0
		}
		if s.Total < 0 {
			s.Total = 0
		}
		if s.Workers < 0 {
			s.Workers = 0
		}
		waited += s.Wait
		total += s.Total
		workers += int64(s.Workers)
	}
	info := WaitTelemetryInfo{
		AvgWorkers: float64(workers) / float64(len(samples)),
		Windows:    len(samples),
	}
	if total > 0 {
		pct := int(100 * float64(waited) / float64(total))
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		info.WaitPercent = pct
	}
	return info
}

// entry renders the metric as a loadErrors entry. StatsOutput has no dedicated
// telemetry field and its schema forbids extra properties, so the metric goes
// into the existing diagnostic array under a stable prefix, the same way T3.52
// reports its plan-limit warning. An empty series produces no entry.
func (i WaitTelemetryInfo) entry() string {
	if i.Windows == 0 {
		return ""
	}
	return fmt.Sprintf("%sci wait %d%% of wall time; avg %.1f workers over %d windows",
		waitTelemetryPrefix, i.WaitPercent, i.AvgWorkers, i.Windows)
}

// Handle copies the values already present in a state snapshot into the stats
// response. It appends a plan-limit warning to loadErrors when a threshold is
// crossed and, when the reader also exposes wait samples, an idle metric with
// the wait share and the mean worker count (ADR 0056 p6). It performs no write
// and no model request. Slices are allocated even when empty so the response
// always carries an array, never JSON null.
func (h *StatsHandler) Handle(_ context.Context, _ *mcpsdk.CallToolRequest, _ StatsInput) (*mcpsdk.CallToolResult, StatsOutput, error) {
	snap, err := h.reader.Snapshot()
	if err != nil {
		observe.ActorError("beacon_stats", "", err)
		return nil, StatsOutput{}, fmt.Errorf("beacon_stats: %w", err)
	}

	loadErrors := make([]string, len(snap.LoadErrors))
	copy(loadErrors, snap.LoadErrors)
	if entry := planLimitInfo(snap.PointsThisHour, snap.Budget).warningEntry(); entry != "" {
		loadErrors = append(loadErrors, entry)
	}
	if reader, ok := h.reader.(WaitTelemetryReader); ok {
		if entry := waitTelemetry(reader.WaitSamples()).entry(); entry != "" {
			loadErrors = append(loadErrors, entry)
		}
	}

	out := StatsOutput{
		PointsThisHour: snap.PointsThisHour,
		Budget:         snap.Budget,
		ActiveWatches:  len(snap.RadarWatches()),
		Persist: PersistInfo{
			Seq:       snap.PersistSeq,
			LastError: snap.LastPersistErr,
		},
		LoadErrors: loadErrors,
		Ladder: LadderInfo{
			Pending:   snap.LadderPending,
			Escalated: snap.LadderEscalated,
		},
		Version: snap.Version,
	}
	return &mcpsdk.CallToolResult{}, out, nil
}

// planLimitWarning returns the warning entry present in a stats response, if
// any. It is a helper for callers and tests that need to read the warning back.
func planLimitWarning(out StatsOutput) string {
	for _, entry := range out.LoadErrors {
		if strings.HasPrefix(entry, planLimitWarningPrefix) {
			return entry
		}
	}
	return ""
}

// waitTelemetryEntry returns the wait telemetry entry present in a stats
// response, if any. It is a helper for callers and tests that need to read the
// idle metric back without depending on its position in loadErrors.
func waitTelemetryEntry(out StatsOutput) string {
	for _, entry := range out.LoadErrors {
		if strings.HasPrefix(entry, waitTelemetryPrefix) {
			return entry
		}
	}
	return ""
}
