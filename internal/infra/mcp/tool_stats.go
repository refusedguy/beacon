// Package mcp: beacon_stats as a pure read of Beacon state.
package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// planLimitYellowPercent is the ADR 0037 §6 threshold that turns the plan
	// limit warning yellow.
	planLimitYellowPercent = 75
	// planLimitRedPercent is the ADR 0037 §6 threshold that turns the plan
	// limit warning red.
	planLimitRedPercent = 90
	// planLimitReadInterval is how often the limit counters are refreshed.
	// The counters come from a local state snapshot, so a refresh never calls
	// a model (ADR 0037 §6).
	planLimitReadInterval = 10 * time.Minute
	// planLimitWarningPrefix marks the plan-limit entry inside loadErrors so a
	// reader can tell a warning from a state load failure.
	planLimitWarningPrefix = "plan limit warning: "
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

// StatsInput is the empty input of beacon_stats. The tool takes no arguments.
type StatsInput struct{}

// StatsHandler implements beacon_stats over the read-only snapshot port.
// It holds no writer and takes no argument. The snapshot is refreshed at most
// once every ten minutes so the limit counters are read on that cadence.
type StatsHandler struct {
	reader SnapshotReader
	now    func() time.Time

	mu       sync.Mutex
	cached   StateSnapshot
	cachedAt time.Time
	hasCache bool
}

// NewStatsHandler builds the handler. Pass handler.Handle to mcpsdk.AddTool
// when wiring the tool into a server.
func NewStatsHandler(reader SnapshotReader) *StatsHandler {
	return &StatsHandler{reader: reader, now: time.Now}
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

// snapshot returns the cached snapshot until the ten-minute refresh window
// passes. The reader is the only dependency, so no model call can happen here.
func (h *StatsHandler) snapshot() (StateSnapshot, error) {
	now := h.now
	if now == nil {
		now = time.Now
	}
	t := now()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hasCache && t.Sub(h.cachedAt) < planLimitReadInterval {
		return h.cached, nil
	}
	snap, err := h.reader.Snapshot()
	if err != nil {
		return StateSnapshot{}, err
	}
	h.cached = snap
	h.cachedAt = t
	h.hasCache = true
	return snap, nil
}

// Handle copies the values already present in a state snapshot into the stats
// response and appends a plan-limit warning to loadErrors when a threshold is
// crossed. It performs no write and no model request. Slices are allocated even
// when empty so the response always carries an array, never JSON null.
func (h *StatsHandler) Handle(_ context.Context, _ *mcpsdk.CallToolRequest, _ StatsInput) (*mcpsdk.CallToolResult, StatsOutput, error) {
	snap, err := h.snapshot()
	if err != nil {
		return nil, StatsOutput{}, fmt.Errorf("beacon_stats: %w", err)
	}

	loadErrors := make([]string, len(snap.LoadErrors))
	copy(loadErrors, snap.LoadErrors)
	if entry := planLimitInfo(snap.PointsThisHour, snap.Budget).warningEntry(); entry != "" {
		loadErrors = append(loadErrors, entry)
	}

	out := StatsOutput{
		PointsThisHour: snap.PointsThisHour,
		Budget:         snap.Budget,
		ActiveWatches:  len(snap.ActiveWatches),
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
