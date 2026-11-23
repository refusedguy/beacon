// Package mcp: beacon_stats as a pure read of Beacon state.
package mcp

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
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
	// registryStatsErrorPrefix marks a failed registry read inside loadErrors.
	// It is a diagnostic, not a plan-limit warning (T5.60).
	registryStatsErrorPrefix = "registry: "
	// activeWatchWarningPrefix marks the active-watch limit warning inside
	// loadErrors, so a reader can tell it from a state load failure (T5.64).
	activeWatchWarningPrefix = "active watch limit warning: "
	// blockedWatchPrefix marks the blocked-watch count inside loadErrors. The
	// count also has its own StatsOutput.Blocked field (T3.440), but the
	// diagnostic stays for readers that already parse it from loadErrors.
	blockedWatchPrefix = "blocked watches: "
	// graphReadyPrefix marks the graph readiness marker inside loadErrors. The
	// schema of beacon_stats is derived from StatsOutput in schema.go, a file
	// owned by another card, so the marker uses the same diagnostic channel as
	// the blocked-watch count and the wait metric instead of a new field.
	graphReadyPrefix = "graphReady: "
	// graphIndexErrorPrefix records a failed graph index read. A read that
	// cannot be verified is reported as not ready, never as a current graph.
	graphIndexErrorPrefix = "graph index: "
	// pollerStatsPrefix marks the poller state inside loadErrors. The derived
	// schema is fixed in schema.go, so pollerActive and pollerPID ride in the
	// diagnostic array with the other counters.
	pollerStatsPrefix = "poller: "
	// activeWatchLimit is the global active-watch cap from ADR 0093
	// (maxActiveWatchesGlobal) and T1.4. T5.64 warns at 75% and 90% of it.
	activeWatchLimit = 200
	// outcomeSchemaVersionPrefix marks the schema version of the disjoint
	// outcome report inside loadErrors. The derived beacon_stats schema is
	// pinned by ADR 0094, so the report rides in the diagnostic array like the
	// graph and poller signals instead of adding a field to StatsOutput.
	outcomeSchemaVersionPrefix = "outcome schema version: "
	// outcomeListPrefix marks one outcome ID list. The body is name=id,id,...
	// with IDs in sorted order so an unchanged snapshot produces equal bytes.
	outcomeListPrefix = "outcome ids: "
	// sharedGatePrefix marks the process-wide CI gate snapshot inside
	// loadErrors. The stats schema is fixed in schema.go, so the gate counts use
	// the diagnostic channel like the graph and poller signals.
	sharedGatePrefix = "shared gate: "
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

// watchLimitLatch tracks the 75% and 90% active-watch warning thresholds.
// A threshold fires once on the rising edge and clears when usage drops below
// it, so repeated stats ticks at the same level do not repeat the warning.
// The latch is safe for concurrent Handle calls.
type watchLimitLatch struct {
	mu    sync.Mutex
	fired map[int]bool
	fires int
}

// observe records one active-watch usage sample. It returns one warning entry
// per threshold currently crossed, in ascending order, and counts each rising
// crossing once. A zero or negative limit reports no usage and no warnings.
func (l *watchLimitLatch) observe(active, limit int) []string {
	pct := 0
	if limit > 0 {
		pct = int(int64(active) * 100 / int64(limit))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fired == nil {
		l.fired = make(map[int]bool)
	}
	var entries []string
	for _, threshold := range []int{planLimitYellowPercent, planLimitRedPercent} {
		if pct < threshold {
			delete(l.fired, threshold)
			continue
		}
		if !l.fired[threshold] {
			l.fired[threshold] = true
			l.fires++
		}
		entries = append(entries, fmt.Sprintf("%s%d%% of active-watch limit used (threshold %d%%)",
			activeWatchWarningPrefix, pct, threshold))
	}
	return entries
}

// FireCount reports how many rising threshold crossings have happened since
// the latch was created. Dropping below a threshold clears its flag and arms
// the next crossing, but does not lower this cumulative count.
func (l *watchLimitLatch) FireCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fires
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

// RegistryStats is the registry projection reported by beacon_stats (T5.60,
// ADR 0093). Counts are plain integers and ConfigID is the configuration
// identity shared by the registry rows. A mixed or empty registry has an empty
// ConfigID; the counters still describe the rows that are present.
type RegistryStats struct {
	RegisteredAgents int
	StaleAgents      int
	ConfigID         string
}

// RegistryStatsReader is the narrow read-only port for the registry counters.
// It is optional, like WaitTelemetryReader: a handler without one keeps the
// pre-T5.60 response with zero counters. The composition root supplies a
// DB-backed reader through Dependencies when registry state is available.
type RegistryStatsReader interface {
	RegistryStats(ctx context.Context) (RegistryStats, error)
}

// GraphIndexReader reports the current state of the task graph index. It is
// optional, like WaitTelemetryReader and RegistryStatsReader: a handler with
// no reader bound reports the graph as not ready. An agent that cannot verify
// the index must never treat the graph as built and current.
type GraphIndexReader interface {
	GraphStatus(ctx context.Context) (domain.GraphStatus, error)
}

// CIWaitSnapshot is a point-in-time view of the process-wide CI gate. Waiters
// maps each active key to the number of callers joined on it. CombinedRounds
// counts the rounds where a second caller joined a key. The shape matches the
// store snapshot, so an adapter can copy it without importing infra/store.
type CIWaitSnapshot struct {
	Waiters        map[string]int
	CombinedRounds int
}

// SharedGateSnapshotReader is the optional read-only port for the process-wide
// CI gate. A handler without one leaves the stats response byte-identical to
// the form from before FTE-03. A composition root that owns the gate binds an
// adapter with SetSharedGateSnapshotReader.
type SharedGateSnapshotReader interface {
	SharedGateSnapshot() CIWaitSnapshot
}

// SnapshotRegistryStats adapts the Registry projection already carried by a
// StateSnapshot (T5.29) to RegistryStatsReader. A wiring layer whose snapshot
// reader fills Registry can pass this adapter to SetRegistryStatsReader.
type SnapshotRegistryStats struct {
	reader SnapshotReader
}

// NewSnapshotRegistryStats builds an adapter around reader.
func NewSnapshotRegistryStats(reader SnapshotReader) *SnapshotRegistryStats {
	return &SnapshotRegistryStats{reader: reader}
}

// RegistryStats projects the snapshot's Registry rows onto registry counters.
// It reads the same store as the handler; a read error is returned to the
// caller, which records it as a diagnostic instead of inventing counters.
func (s *SnapshotRegistryStats) RegistryStats(_ context.Context) (RegistryStats, error) {
	snap, err := s.reader.Snapshot()
	if err != nil {
		return RegistryStats{}, err
	}
	return RegistryStats{
		RegisteredAgents: snap.RegisteredAgents(),
		StaleAgents:      snap.StaleAgents(),
		ConfigID:         snap.RegistryConfig(),
	}, nil
}

// PollerStatus is the observable state of the single CI poller. Active is true
// when a poller process is alive. PID names that process and is zero when none
// runs.
type PollerStatus struct {
	Active bool
	PID    int
}

var (
	pollerStatusMu      sync.RWMutex
	pollerStatus        PollerStatus
	pollerStatusOrdered bool
)

// SetPollerStatus publishes the poller state read by beacon_stats. The
// composition root calls it once the lock check finishes. Both values change
// under one lock, so a reader never sees a new PID with the old active flag.
func SetPollerStatus(s PollerStatus) {
	if s.PID < 0 || !s.Active {
		s.PID = 0
	}
	pollerStatusMu.Lock()
	pollerStatus = s
	pollerStatusOrdered = true
	pollerStatusMu.Unlock()
}

// ClearPollerStatus removes the published state and restores the response a
// server has before its first lock check.
func ClearPollerStatus() {
	pollerStatusMu.Lock()
	pollerStatus = PollerStatus{}
	pollerStatusOrdered = false
	pollerStatusMu.Unlock()
}

func currentPollerStatus() (PollerStatus, bool) {
	pollerStatusMu.RLock()
	defer pollerStatusMu.RUnlock()
	return pollerStatus, pollerStatusOrdered
}

// The ceilings bound the diagnostic array and the encoded stats response.
const (
	statsMaxRecords = 200
	statsMaxBytes   = 256 * 1024
)

const statsNarrowHint = "narrow the diagnostics by keeping fewer or shorter loadErrors in the snapshot; beacon_stats takes no filters"

// StatsInput is the empty input of beacon_stats. The tool takes no arguments.
type StatsInput struct{}

// StatsHandler implements beacon_stats over the read-only snapshot port. It
// holds no writer and takes no argument. Every call reads a fresh snapshot: a
// cached counter can hide a budget spike or a growing watch list from the
// orchestrator, so the tool reports what the store says right now.
type StatsHandler struct {
	reader     SnapshotReader
	registry   RegistryStatsReader
	graph      GraphIndexReader
	sharedGate SharedGateSnapshotReader
	watchLimit *watchLimitLatch
	// outcomeReport opts into the disjoint outcome report. A handler without
	// it keeps the exact pre-SB-12 response, so existing callers and tests are
	// not changed by the new projection. The composition root turns it on when
	// it wants per-outcome IDs in the stats response.
	outcomeReport bool
}

// NewStatsHandler builds the handler. Pass handler.Handle to mcpsdk.AddTool
// when wiring the tool into a server. The registry port is optional and is set
// with SetRegistryStatsReader by a composition root that has one.
func NewStatsHandler(reader SnapshotReader) *StatsHandler {
	return &StatsHandler{reader: reader, watchLimit: &watchLimitLatch{}}
}

// SetRegistryStatsReader binds the optional registry counter port. Passing nil
// removes it and restores the pre-T5.60 zero counters.
func (h *StatsHandler) SetRegistryStatsReader(r RegistryStatsReader) {
	h.registry = r
}

// SetGraphIndexReader binds the optional graph index port. Passing nil removes
// it and makes beacon_stats report graphReady as false, so a graph nobody can
// verify is never advertised as current.
func (h *StatsHandler) SetGraphIndexReader(r GraphIndexReader) {
	h.graph = r
}

// SetSharedGateSnapshotReader binds the optional process-wide CI gate port.
// Passing nil removes the snapshot. A handler with no port emits no gate
// diagnostic, so older readers see the same loadErrors bytes as before.
func (h *StatsHandler) SetSharedGateSnapshotReader(r SharedGateSnapshotReader) {
	h.sharedGate = r
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

// notifyingWatches counts watches whose wake is staged for delivery. A
// notifying watch left watching on an actionable verdict and waits for its
// subscriber to claim the wake, so the count is the current outbox depth. It
// reads the same radar slice that feeds beacon_stats, which keeps the published
// gauge consistent with the reported active-watch count.
func notifyingWatches(radar []domain.Watch) uint64 {
	var n uint64
	for _, w := range radar {
		if w.Status == domain.StatusNotifying {
			n++
		}
	}
	return n
}

// outcomeLists returns the eight report lists in emission order. The same
// order drives the wire form and its parser, so a name cannot drift between
// the producer and the reader.
func outcomeLists(r *OutcomeReport) []struct {
	name string
	ids  *[]string
} {
	return []struct {
		name string
		ids  *[]string
	}{
		{"completed_ids", &r.CompletedIDs},
		{"incomplete_ids", &r.IncompleteIDs},
		{"resolved_ids", &r.ResolvedIDs},
		{"unresolved_ids", &r.UnresolvedIDs},
		{"error_ids", &r.ErrorIDs},
		{"empty_patch_ids", &r.EmptyPatchIDs},
		{"infra_failure_ids", &r.InfraFailureIDs},
		{"ambiguous_failure_ids", &r.AmbiguousFailureIDs},
	}
}

// mainOutcome is the one main list a Watch belongs to. The cases are
// exhaustive. A Watch without a PR never produced a patch to evaluate, so it
// is incomplete regardless of its verdict.
func mainOutcome(w domain.Watch) string {
	if w.PR == 0 {
		return "incomplete_ids"
	}
	switch w.Verdict {
	case domain.VerdictReady, domain.VerdictMerged, domain.VerdictAccepted, domain.VerdictGreenBlocked:
		return "resolved_ids"
	case domain.VerdictFail, domain.VerdictRetry, domain.VerdictRebase,
		domain.VerdictKeepWorking, domain.VerdictTimeout, domain.VerdictBlocked,
		domain.VerdictRecheck, domain.VerdictNeedsInput:
		return "unresolved_ids"
	case domain.VerdictClosed:
		return "completed_ids"
	default:
		return "incomplete_ids"
	}
}

// infraFailure reads an infrastructure failure from the block record. The
// domain has no dedicated block reason for it, so a failed check whose name
// carries "infra" or "flaky" is the signal. The classification is additive.
func infraFailure(w domain.Watch) bool {
	if w.Blocked == nil {
		return false
	}
	for _, check := range w.Blocked.FailedChecks {
		name := strings.ToLower(check)
		if strings.Contains(name, "infra") || strings.Contains(name, "flaky") {
			return true
		}
	}
	return false
}

// errorOutcome reports a Watch whose run ended in an error. A blocked status
// or block record is an error too. Like every failure class, it is additive.
func errorOutcome(w domain.Watch) bool {
	switch w.Verdict {
	case domain.VerdictFail, domain.VerdictTimeout, domain.VerdictBlocked:
		return true
	}
	return w.Status == domain.StatusBlocked || w.Blocked != nil
}

// classifyOutcomes builds the disjoint outcome report from one radar slice.
// Every Watch enters exactly one main list. Failure classes are additive: a
// Watch classified as an infrastructure failure stays in its main list, so a
// classification never changes a denominator. Lists are sorted so a repeated
// call with unchanged state produces byte-identical entries.
func classifyOutcomes(watches []domain.Watch) OutcomeReport {
	report := OutcomeReport{SchemaVersion: StatsOutcomeSchemaVersion}
	for _, w := range watches {
		if w.ID == "" {
			continue
		}
		main := mainOutcome(w)
		for _, list := range outcomeLists(&report) {
			if list.name == main {
				*list.ids = append(*list.ids, w.ID)
				break
			}
		}
		if w.PR == 0 {
			report.EmptyPatchIDs = append(report.EmptyPatchIDs, w.ID)
		}
		if infraFailure(w) {
			report.InfraFailureIDs = append(report.InfraFailureIDs, w.ID)
		}
		if errorOutcome(w) && !infraFailure(w) {
			report.ErrorIDs = append(report.ErrorIDs, w.ID)
		}
		if w.Blocked != nil && w.Blocked.Reason == domain.BlockReasonNotMine {
			report.AmbiguousFailureIDs = append(report.AmbiguousFailureIDs, w.ID)
		}
	}
	for _, list := range outcomeLists(&report) {
		sort.Strings(*list.ids)
	}
	return report
}

// entries renders the report as deterministic loadErrors entries. The schema
// version comes first, then every non-empty list in a fixed order. Empty
// lists are omitted so an unchanged snapshot produces byte-identical entries.
func (r OutcomeReport) entries() []string {
	entries := []string{fmt.Sprintf("%s%d", outcomeSchemaVersionPrefix, r.SchemaVersion)}
	for _, list := range outcomeLists(&r) {
		if len(*list.ids) == 0 {
			continue
		}
		entries = append(entries, outcomeListPrefix+list.name+"="+strings.Join(*list.ids, ","))
	}
	return entries
}

// Handle copies the values already present in a state snapshot into the stats
// response. Blocked reports the number of watches with a blocked status or a
// block record. It appends a plan-limit warning to loadErrors when a
// threshold is crossed, an active-watch limit warning once per 75%/90% crossing (T5.64),
// and, when the reader also exposes wait samples, an idle metric with the wait
// share and the mean worker count (ADR 0056 p6). If a registry port is bound,
// the three T5.60 counters come from it; a failed read is recorded in
// loadErrors instead of producing invented counters. When a graph index port is
// bound, the graphReady marker reflects whether the graph is built and current;
// a failing read also reports false. With no port bound the marker is absent,
// which callers read as not ready. It writes no Beacon state and makes no model
// request, but it does refresh the active_watches and outbox_depth gauges from
// the same snapshot so the /metrics endpoint cannot drift from beacon_stats.
// Slices are allocated even when empty so the response always carries an array,
// never JSON null.
func (h *StatsHandler) Handle(ctx context.Context, _ *mcpsdk.CallToolRequest, _ StatsInput) (*mcpsdk.CallToolResult, StatsOutput, error) {
	snap, err := h.reader.Snapshot()
	if err != nil {
		observe.ActorError("beacon_stats", "", err)
		return nil, StatsOutput{}, fmt.Errorf("beacon_stats: %w", err)
	}

	radar := snap.RadarWatches()
	activeWatches := len(radar)
	// Refresh the two point-in-time gauges from the same snapshot the response
	// is built from. The /metrics endpoint serves observe.Default() in this
	// process, so each stats call updates what a scrape sees. Deriving both
	// values here keeps /metrics and beacon_stats from drifting apart.
	observe.ActiveWatches(uint64(activeWatches))
	observe.OutboxDepth(notifyingWatches(radar))
	loadErrors := make([]string, len(snap.LoadErrors))
	copy(loadErrors, snap.LoadErrors)
	// T3.440: show blocked watches in stats. The field carries the count; the
	// diagnostic stays only for readers that already parse loadErrors.
	blockedWatches := blockedWatchCount(radar)
	if blockedWatches > 0 {
		loadErrors = append(loadErrors, fmt.Sprintf("%s%d", blockedWatchPrefix, blockedWatches))
	}
	if entry := planLimitInfo(snap.PointsThisHour, snap.Budget).warningEntry(); entry != "" {
		loadErrors = append(loadErrors, entry)
	}
	// T5.64: warn once per threshold on the rising edge of active-watch usage.
	// The entries ride in loadErrors, the journal diagnostic array, so the
	// derived beacon_stats schema needs no new field. No write or model call.
	loadErrors = append(loadErrors, h.watchLimit.observe(activeWatches, activeWatchLimit)...)
	if reader, ok := h.reader.(WaitTelemetryReader); ok {
		if entry := waitTelemetry(reader.WaitSamples()).entry(); entry != "" {
			loadErrors = append(loadErrors, entry)
		}
	}
	if status, ok := currentPollerStatus(); ok {
		loadErrors = append(loadErrors, fmt.Sprintf("%spollerActive=%t pollerPID=%d",
			pollerStatsPrefix, status.Active, status.PID))
	}
	// FTE-03: publish the process-wide CI gate from the optional port. The two
	// counts ride in loadErrors because StatsOutput is pinned by ADR 0094.
	if h.sharedGate != nil {
		gate := h.sharedGate.SharedGateSnapshot()
		active := 0
		for _, waiters := range gate.Waiters {
			active += waiters
		}
		loadErrors = append(loadErrors, fmt.Sprintf("%sactiveWaiters=%d combinedRounds=%d",
			sharedGatePrefix, active, gate.CombinedRounds))
	}

	var registry RegistryStats
	if h.registry != nil {
		registry, err = h.registry.RegistryStats(ctx)
		if err != nil {
			observe.ActorError("beacon_stats", "", err)
			loadErrors = append(loadErrors, registryStatsErrorPrefix+err.Error())
			registry = RegistryStats{}
		}
	}

	// IDX-09: report whether the task graph is built and current. A missing
	// reader or a failed read fails closed, so an agent never trusts a graph it
	// cannot verify. The marker rides in loadErrors because the stats schema is
	// derived in schema.go and forbids extra properties.
	if h.graph != nil {
		graph, err := h.graph.GraphStatus(ctx)
		if err != nil {
			observe.ActorError("beacon_stats", "", err)
			loadErrors = append(loadErrors, graphIndexErrorPrefix+err.Error())
			graph = domain.GraphStatus{}
		}
		loadErrors = append(loadErrors, fmt.Sprintf("%s%t", graphReadyPrefix, graph.Ready()))
	}

	// SB-12: publish the disjoint outcome report from the same radar slice that
	// feeds the counters. Emission is opt-in, so a handler that has not enabled
	// it keeps its previous loadErrors bytes; the projection itself is pure.
	if h.outcomeReport {
		loadErrors = append(loadErrors, classifyOutcomes(radar).entries()...)
	}

	out := StatsOutput{
		PointsThisHour: snap.PointsThisHour,
		Budget:         snap.Budget,
		ActiveWatches:  activeWatches,
		Blocked:        blockedWatches,
		Persist: PersistInfo{
			Seq:       snap.PersistSeq,
			LastError: snap.LastPersistErr,
		},
		LoadErrors: loadErrors,
		Ladder: LadderInfo{
			Pending:   snap.LadderPending,
			Escalated: snap.LadderEscalated,
		},
		Version:          snap.Version,
		RegisteredAgents: registry.RegisteredAgents,
		StaleAgents:      registry.StaleAgents,
		ConfigID:         registry.ConfigID,
	}

	limits := responseLimits{records: statsMaxRecords, bytes: statsMaxBytes}
	if err := checkResponseLimits("beacon_stats", "loadError", len(out.LoadErrors), limits, statsNarrowHint, out); err != nil {
		return nil, StatsOutput{}, err
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

// registryStatsError returns the registry diagnostic present in a stats
// response, if any. Like the other prefixes it keeps the caller independent of
// the entry's position in loadErrors.
func registryStatsError(out StatsOutput) string {
	for _, entry := range out.LoadErrors {
		if strings.HasPrefix(entry, registryStatsErrorPrefix) {
			return entry
		}
	}
	return ""
}

// activeWatchWarnings returns the active-watch limit entries in a stats
// response. A helper for callers and tests that must not depend on their
// position in loadErrors.
func activeWatchWarnings(out StatsOutput) []string {
	var warnings []string
	for _, entry := range out.LoadErrors {
		if strings.HasPrefix(entry, activeWatchWarningPrefix) {
			warnings = append(warnings, entry)
		}
	}
	return warnings
}

// blockedWatchCount counts watches that are blocked by status or by a block
// record. A watch with both is counted once. The handler calls it on the radar
// watches and reports the result as StatsOutput.Blocked (T3.440).
func blockedWatchCount(watches []domain.Watch) int {
	count := 0
	for _, w := range watches {
		if w.Status == domain.StatusBlocked || w.Blocked != nil {
			count++
		}
	}
	return count
}

// graphReadyEntry returns the graph readiness marker present in a stats
// response, if any. Callers and tests read the marker through this helper so
// they do not depend on its position in loadErrors. The string stays as a
// compatible output for readers built before the typed signal existed.
func graphReadyEntry(out StatsOutput) string {
	for _, entry := range out.LoadErrors {
		if strings.HasPrefix(entry, graphReadyPrefix) {
			return entry
		}
	}
	return ""
}

// GraphReadySource explains why a readiness signal has its value. The four
// cases are exhaustive: a bound reader proved the graph current, it proved the
// graph stale, it could not be read, or no reader was bound at all.
type GraphReadySource string

const (
	GraphReadyFresh     GraphReadySource = "fresh"
	GraphReadyStale     GraphReadySource = "stale"
	GraphReadyNoReader  GraphReadySource = "no-reader"
	GraphReadyReadError GraphReadySource = "read-error"
)

// GraphReadySignal is the typed form of the graph readiness marker. Ready is
// true only when a bound reader proved the graph is built and current. The
// Source field lets a caller tell a stale graph from an unverifiable one
// without parsing loadErrors.
type GraphReadySignal struct {
	Ready  bool
	Source GraphReadySource
}

// graphIndexErrorSeen reports whether a failed graph read left a diagnostic in
// the response. The marker alone cannot distinguish a stale graph from a read
// that failed; the diagnostic can.
func graphIndexErrorSeen(out StatsOutput) bool {
	for _, entry := range out.LoadErrors {
		if strings.HasPrefix(entry, graphIndexErrorPrefix) {
			return true
		}
	}
	return false
}

// ReadGraphReady lifts the legacy graphReady string into a typed signal. A
// missing marker or a failed read both fail closed: Ready stays false and the
// Source names the reason. The string marker is kept in loadErrors so older
// readers that parse it keep working during the transition.
func ReadGraphReady(out StatsOutput) GraphReadySignal {
	entry := graphReadyEntry(out)
	if entry == "" {
		if graphIndexErrorSeen(out) {
			return GraphReadySignal{Source: GraphReadyReadError}
		}
		return GraphReadySignal{Source: GraphReadyNoReader}
	}
	if entry != graphReadyPrefix+"true" {
		if graphIndexErrorSeen(out) {
			return GraphReadySignal{Source: GraphReadyReadError}
		}
		return GraphReadySignal{Source: GraphReadyStale}
	}
	return GraphReadySignal{Ready: true, Source: GraphReadyFresh}
}

// GraphReady is the field-like read of graph readiness on a stats response.
// Callers use out.GraphReady().Ready instead of scanning a string array.
func (o StatsOutput) GraphReady() GraphReadySignal { return ReadGraphReady(o) }

// graphReady reports whether the stats response advertises a current graph.
// It is the boolean shorthand over the typed signal. A response with no marker
// is not ready, so a caller built before IDX-09 never mistakes silence for a
// green signal.
func graphReady(out StatsOutput) bool {
	return ReadGraphReady(out).Ready
}

// pollerStatsEntry returns the poller diagnostic in a stats response, if any.
// It keeps callers independent of the entry's position in loadErrors.
func pollerStatsEntry(out StatsOutput) string {
	for _, entry := range out.LoadErrors {
		if strings.HasPrefix(entry, pollerStatsPrefix) {
			return entry
		}
	}
	return ""
}

// CIWaitSignal is the typed form of the CI gate diagnostic. Bound is false
// when no reader was wired, so the two counts carry no meaning in that case.
type CIWaitSignal struct {
	Bound          bool
	ActiveWaiters  int
	CombinedRounds int
}

// sharedGateEntry returns the gate diagnostic in a stats response, if any.
func sharedGateEntry(out StatsOutput) string {
	for _, entry := range out.LoadErrors {
		if strings.HasPrefix(entry, sharedGatePrefix) {
			return entry
		}
	}
	return ""
}

// ReadCIWaitSignal lifts the gate diagnostic into a typed signal. A missing or
// malformed entry fails closed with Bound false, so no caller reports gate
// state that the stats response did not actually carry.
func ReadCIWaitSignal(out StatsOutput) CIWaitSignal {
	entry := strings.TrimPrefix(sharedGateEntry(out), sharedGatePrefix)
	if entry == "" {
		return CIWaitSignal{}
	}
	signal := CIWaitSignal{}
	for _, field := range strings.Fields(entry) {
		name, value, ok := strings.Cut(field, "=")
		if !ok {
			return CIWaitSignal{}
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return CIWaitSignal{}
		}
		switch name {
		case "activeWaiters":
			signal.ActiveWaiters = n
		case "combinedRounds":
			signal.CombinedRounds = n
		default:
			return CIWaitSignal{}
		}
	}
	if !strings.Contains(entry, "activeWaiters=") || !strings.Contains(entry, "combinedRounds=") {
		return CIWaitSignal{}
	}
	signal.Bound = true
	return signal
}

// CIWaitSignal is the field-like read of the CI gate snapshot on a stats
// response. Callers use out.CIWaitSignal() instead of parsing loadErrors.
func (o StatsOutput) CIWaitSignal() CIWaitSignal { return ReadCIWaitSignal(o) }
