package bench

import "encoding/json"

//go:generate go run ../../cmd/bench-gen -out ../../docs/perf/bench-registry.json

// Placement names the pipeline that consumes a benchmark result. "pr" is the
// advisory two-tree job, "nightly" is the benchstat gate and "both" is every
// pipeline that reads the registry.
const (
	PlacementPR      = "pr"
	PlacementNightly = "nightly"
	PlacementBoth    = "both"
)

// Spec is one typed benchmark registry entry. Key is the canonical benchmark
// name. The limiter, unit, work and error metric names, placement and
// regression threshold travel with the key so no consumer hardcodes them.
type Spec struct {
	Key          string  `json:"key"`
	Limiter      string  `json:"limiter"`
	Unit         string  `json:"unit"`
	WorkMetric   string  `json:"workMetric"`
	ErrMetric    string  `json:"errMetric"`
	Placement    string  `json:"placement"`
	ThresholdPct float64 `json:"thresholdPct"`
}

// prHarnessLimiters are the limiter strings the advisory two-tree harnesses
// print in their canonical rows. Those harnesses live outside the bench
// package, so registry.go owns the strings instead of report.go.
var prHarnessLimiters = []struct{ key, limiter string }{
	{"snapshot", "SQLite VM, syscalls and event sort"},
	{"worktree-remove", "git subprocess spawn and worktree metadata"},
	{"tui-view", "normalizeSnapshot + render"},
	{"run-watcher-fp", "JSON.stringify character volume"},
	{"http-frame-emission", "json.Marshal + eight-subscriber lossy fanout"},
	{"go-tui-view", "truncate + padding cache + reused row buffer"},
}

// The in-package benchmarks the advisory job runs print these limiter strings.
// The registry owns them; the benchmarks print the constants so a limiter edit
// cannot put two competing names on the wire.
const (
	reconcileTickLimiter  = "source ListAvailable + SQLite Get per PR"
	baseRedCheckLimiter   = "source ListAvailable + SCM BaseBranchStatus on a red base"
	baseRedBlockLimiter   = "SCM Observe fanout over active runs on a red base"
	plannerSelectLimiter  = "injected overlap checker over active scope index"
	dispatchPromptLimiter = "strings.Builder + prompt bytes"
)

// telegramNotifyLimiter names the resource that bounds the Telegram send path.
// It lives here beside the key so the registry and the benchmark cannot drift
// apart. The benchmark prints this string in its canonical row.
const telegramNotifyLimiter = "redact regexp chain + json.Marshal + loopback POST"

// The CLI-only benchmarks live in internal/bench and cmd/beacon. Their
// limiters live here beside the keys so the registry and the canonical rows a
// benchmark prints cannot drift apart. The drift tests in registry_test.go
// cross-check each literal against the benchmark source.
const (
	openPreparedDBLimiter    = "open-prepared-limiter-ddl-scans"
	statusCLIPathsLimiter    = "json-reflection-plus-sqlite-read"
	configWatchReloadLimiter = "os.ReadFile + sha256 + loadConfig re-read + forwarder cancel and drain"
	controlNoDaemonLimiter   = "two config loads + one Control.Apply per action"
)

// prBenchLimiters are the canonical keys and limiter strings published by the
// benchmarks that run only in the advisory two-tree job, so their registry
// placement is pr. reconcile-tick-counts keeps a green base as the no-red
// regression guard; the two base-red keys run on a red base so their own
// counters can earn a calls-reduced verdict. The assembly row covers status.go
// and metrics.go work without SQLite or HTTP noise. The telegram row covers
// the redact, marshal and POST chain on the notification send path. The four
// CLI rows price the hot paths outside BenchmarkSQLiteSnapshot: the prepared
// open, the three status read paths, a changed-config reload and the local
// no-daemon control path.
var prBenchLimiters = []struct{ key, limiter string }{
	{"reconcile-tick-counts", reconcileTickLimiter},
	{"base-red-check", baseRedCheckLimiter},
	{"base-red-block", baseRedBlockLimiter},
	{"planner-select-injected-checker", plannerSelectLimiter},
	{"dispatch-prompt", dispatchPromptLimiter},
	{nameAssembly, limiterAssembly},
	{"telegram-notify", telegramNotifyLimiter},
	{"open-prepared-db", openPreparedDBLimiter},
	{"status-cli-paths", statusCLIPathsLimiter},
	{"config-watch-reload", configWatchReloadLimiter},
	{"control-no-daemon", controlNoDaemonLimiter},
}

// The pure control benchmark owns no Beacon code, so its canonical name and
// limiter string are declared here. The registry is their single owner; the
// drift test cross-checks them against the control's local constants.
const (
	controlSpecKey     = "json-control"
	controlSpecLimiter = "encoding/json reflection over a fixed struct"
)

// NoisePolicy is the calibration policy for the advisory comparison. A floor
// is calibrated from no-change samples; until enough recent samples exist the
// consumer uses DefaultFloorPct.
type NoisePolicy struct {
	DefaultFloorPct float64 `json:"defaultFloorPct"`
	MaxAgeHours     int     `json:"maxAgeHours"`
	MinSamples      int     `json:"minSamples"`
	Quantile        float64 `json:"quantile"`
}

// DefaultNoisePolicy returns the policy the registry publishes. One week of
// nightly no-change samples is enough for the high quantile, with a five
// sample minimum before a calibrated floor replaces the default.
func DefaultNoisePolicy() NoisePolicy {
	return NoisePolicy{
		DefaultFloorPct: 2,
		MaxAgeHours:     168,
		MinSamples:      5,
		Quantile:        0.95,
	}
}

// AggregateLimit is one nightly sec/op threshold. benchstat emits sec/op, so
// an aggregate has no named Go metric for the key-to-metric cross-check.
type AggregateLimit struct {
	Key          string  `json:"key"`
	Metric       string  `json:"metric"`
	Bench        string  `json:"bench"`
	ThresholdPct float64 `json:"thresholdPct"`
}

// AggregateLimits returns the aggregate sec/op limits the nightly gate
// enforces. They moved out of the gate script into the registry so a review
// sees the number beside every other threshold.
func AggregateLimits() []AggregateLimit {
	return []AggregateLimit{
		{Key: "hot-paths-sec/op", Metric: "sec/op", Bench: "HotPaths", ThresholdPct: 15},
		{Key: "e2e-sec/op", Metric: "sec/op", Bench: "E2EOperatorPaths", ThresholdPct: 20},
	}
}

// registryDocument is the on-disk shape of the generated JSON. Keeping the
// specs under a key lets a consumer add document metadata without changing the
// array shape.
type registryDocument struct {
	Noise      NoisePolicy      `json:"noise"`
	Aggregates []AggregateLimit `json:"aggregates"`
	Specs      []Spec           `json:"specs"`
}

// Registry returns the typed benchmark registry in report order. The name
// constants and limiter strings stay in report.go, their only owner. This
// table adds the unit, work and error metric names, placement and threshold
// for each benchmark that reports a median, so the generated JSON has one
// authoritative source.
func Registry() []Spec {
	specs := make([]Spec, 0, len(hotspotOrder)+1+len(snapshotLimiters)+len(prHarnessLimiters)+len(prBenchLimiters))
	for _, name := range hotspotOrder {
		specs = append(specs, Spec{
			Key:          name,
			Limiter:      limiters[name],
			Unit:         "ns",
			WorkMetric:   name + "-work",
			ErrMetric:    name + "-errs",
			Placement:    PlacementBoth,
			ThresholdPct: 15,
		})
	}
	for _, name := range []string{phaseReads, phaseAssembly, phaseTotal} {
		work, errs := name+"-work", "snapshot-errs"
		if name == phaseTotal {
			work, errs = "", ""
		}
		specs = append(specs, Spec{
			Key:          name,
			Limiter:      snapshotLimiters[name],
			Unit:         "ns",
			WorkMetric:   work,
			ErrMetric:    errs,
			Placement:    PlacementNightly,
			ThresholdPct: 15,
		})
	}
	// Only the advisory two-tree job runs these harnesses. Their work and
	// error counts arrive in the canonical row fields, not as named Go
	// metrics, so the field names are the work and err metrics here.
	for _, h := range prHarnessLimiters {
		specs = append(specs, Spec{
			Key:          h.key,
			Limiter:      h.limiter,
			Unit:         "ns",
			WorkMetric:   "work",
			ErrMetric:    "errs",
			Placement:    PlacementPR,
			ThresholdPct: 15,
		})
	}
	// The reconcile, planner and prompt benchmarks emit fixed work and zero
	// errs in the canonical row fields. They run only in the advisory job, so
	// their keys are pr-only like the external harnesses above.
	for _, h := range prBenchLimiters {
		specs = append(specs, Spec{
			Key:          h.key,
			Limiter:      h.limiter,
			Unit:         "ns",
			WorkMetric:   "work",
			ErrMetric:    "errs",
			Placement:    PlacementPR,
			ThresholdPct: 15,
		})
	}
	// The control emits fixed work and errs in the canonical row fields, so
	// those field names are its work and err metrics. Both pipelines read the
	// control row: the advisory job compares ratios to it, the nightly gate
	// rejects a pass when it moves.
	specs = append(specs, Spec{
		Key:          controlSpecKey,
		Limiter:      controlSpecLimiter,
		Unit:         "ns",
		WorkMetric:   "work",
		ErrMetric:    "errs",
		Placement:    PlacementBoth,
		ThresholdPct: 15,
	})
	return specs
}

// EncodeRegistry renders Registry() as the committed JSON document. The
// generator and the drift test both call it, so the wire format has one owner.
func EncodeRegistry() ([]byte, error) {
	data, err := json.MarshalIndent(registryDocument{
		Noise:      DefaultNoisePolicy(),
		Aggregates: AggregateLimits(),
		Specs:      Registry(),
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
