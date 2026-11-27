package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/pkg/core"
)

const (
	configWatchPrefix = "config-watch-reload"
	harnessInitPrefix = "harness-init"

	// The one limiter this card prices. Both benchmarks report the same name.
	configWatchLimiterName = "os.ReadFile + sha256 + loadConfig re-read + forwarder cancel and drain"
	configWatchLimiterSlug = "os-readfile-plus-sha256-plus-loadconfig-reread-plus-forwarder-cancel-and-drain"

	// Counts taken from the reload path in config_watch.go. A changed file is
	// read once by reload and once by loadConfig, hashed once, parsed once, and
	// compared against the 19 restart fields. A finished generation evicts the
	// model cache on the next resolve.
	readsPerReload          int64 = 2
	hashesPerReload         int64 = 1
	parsesPerReload         int64 = 1
	restartChecksPerReload  int64 = 19
	cacheEvictionsPerReload int64 = 1

	// A reloadable change rebuilds the harness and broadcasts a relay swap that
	// retargets the event forwarder.
	buildsPerRealChange   int64 = 1
	forwardsPerRealChange int64 = 1

	// init starts a seed forwarder, then cancels and drains it when it starts
	// the forwarder for the current harness.
	initForwarderStarts int64 = 2
	initForwarderDrains int64 = 1
)

// configWatchBenchHarness gives init a real event channel. Both channels held
// by reloadableHarness point at this harness at startup, so the seed forwarder
// is cancelled and drained by the second setForwarder call.
type configWatchBenchHarness struct {
	bareHarness
	events chan core.HarnessEvent
}

func (h *configWatchBenchHarness) Events() <-chan core.HarnessEvent { return h.events }

type configWatchBenchFixture struct {
	b    testing.TB
	dir  string
	path string
	cw   *configWatcher
}

// configWatchBenchBody is a complete config with two reloadable fields. Model
// is handled without a restart. required_checks is a reloadable change that is
// not model-only, so it rebuilds the harness and broadcasts a relay swap.
func configWatchBenchBody(dir, model, check string) string {
	return fmt.Sprintf("state_db: %s\nworkspace_dir: %s\nbase_branch: main\n"+
		"task_source:\n  provider: markdown\n  dir: %s\n"+
		"scm:\n  provider: github\n  owner: owner\n  repo: repo\n"+
		"harness:\n  provider: opencode\n  endpoint: http://127.0.0.1:1\n  model: %s\n  token: worker-token\n"+
		"merge:\n  required_checks: [%s]\n",
		filepath.Join(dir, "state.db"), filepath.Join(dir, "worktrees"), dir, model, check)
}

func newConfigWatchBenchFixture(b testing.TB) *configWatchBenchFixture {
	b.Helper()
	b.Setenv("BEACON_MODEL", "")
	dir := b.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeConfigWatchBenchBody(b, path, configWatchBenchBody(dir, "model-a", "check-a"))

	cfg, root, err := loadConfig(path)
	if err != nil {
		b.Fatalf("loadConfig: %v", err)
	}
	_, _, token := loadTokens(cfg)
	h, err := newHarness(cfg, token, "", root)
	if err != nil {
		b.Fatalf("newHarness: %v", err)
	}
	cw := newConfigWatcher(path, root, "", newLiveState(cfg, h), time.Hour)
	fx := &configWatchBenchFixture{b: b, dir: dir, path: path, cw: cw}
	// Prime the cache so the first generation advance evicts a real entry.
	if ref := cw.modelResolver(""); ref.ID != "model-a" {
		b.Fatalf("seeded model = %q, want model-a", ref.ID)
	}
	return fx
}

func writeConfigWatchBenchBody(b testing.TB, path, body string) {
	b.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		b.Fatalf("write config: %v", err)
	}
}

func otherConfigWatchModel(model string) string {
	if model == "model-a" {
		return "model-b"
	}
	return "model-a"
}

func otherConfigWatchCheck(check string) string {
	if check == "check-a" {
		return "check-b"
	}
	return "check-a"
}

type configWatchPassCounts struct {
	work int64
	errs int64
}

// runConfigWatchReloadPass drives one model-only reload and one reloadable
// change, checking that each injected edit reaches live state. Errs counts
// read or parse failures (reload returns false) and swaps that do not match
// the injected change. Work counts the operations each path performs. The
// record callback lets the caller time the reload calls without adding timing
// code to the pass.
func runConfigWatchReloadPass(
	fx *configWatchBenchFixture,
	model, check string,
	record func(time.Duration),
) (string, string, configWatchPassCounts) {
	var counts configWatchPassCounts

	nextModel := otherConfigWatchModel(model)
	writeConfigWatchBenchBody(fx.b, fx.path, configWatchBenchBody(fx.dir, nextModel, check))
	before := fx.cw.live.Load()
	beforeHarness, beforeSignal := before.harness, before.relay.reloadSignal()
	start := time.Now()
	applied := fx.cw.reload()
	record(time.Since(start))
	state := fx.cw.live.Load()
	switch {
	case !applied:
		counts.errs++
	case state.cfg.Harness.Model != nextModel:
		counts.errs++
	case state.generation != before.generation+1:
		counts.errs++
	case state.harness != beforeHarness:
		counts.errs++
	case state.relay.reloadSignal() != beforeSignal:
		counts.errs++
	default:
		counts.work += readsPerReload + hashesPerReload + parsesPerReload +
			restartChecksPerReload + cacheEvictionsPerReload
	}
	if ref := fx.cw.modelResolver(""); ref.ID != nextModel {
		counts.errs++
	}

	nextCheck := otherConfigWatchCheck(check)
	writeConfigWatchBenchBody(fx.b, fx.path, configWatchBenchBody(fx.dir, nextModel, nextCheck))
	before = state
	beforeHarness, beforeSignal = before.harness, before.relay.reloadSignal()
	start = time.Now()
	applied = fx.cw.reload()
	record(time.Since(start))
	state = fx.cw.live.Load()
	switch {
	case !applied:
		counts.errs++
	case len(state.cfg.Merge.RequiredChecks) != 1 || state.cfg.Merge.RequiredChecks[0] != nextCheck:
		counts.errs++
	case state.generation != before.generation+1:
		counts.errs++
	case state.harness == beforeHarness:
		counts.errs++
	case state.relay != before.relay:
		counts.errs++
	case state.relay.reloadSignal() == beforeSignal:
		counts.errs++
	default:
		counts.work += readsPerReload + hashesPerReload + parsesPerReload +
			restartChecksPerReload + cacheEvictionsPerReload +
			buildsPerRealChange + forwardsPerRealChange
	}
	if ref := fx.cw.modelResolver(""); ref.ID != nextModel {
		counts.errs++
	}
	return nextModel, nextCheck, counts
}

func configWatchWorkPerPass() int64 {
	base := readsPerReload + hashesPerReload + parsesPerReload +
		restartChecksPerReload + cacheEvictionsPerReload
	return 2*base + buildsPerRealChange + forwardsPerRealChange
}

func configWatchBenchMedian(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

func printConfigWatchReport(w io.Writer, name string, d time.Duration, work, errs int64) {
	fmt.Fprintf(w, "%s: median=%s work=%d errs=%d limiter=%s\n",
		name, d, work, errs, configWatchLimiterName)
}

// runHarnessInitPass builds one reloadableHarness over a fake harness and
// calls init. It reports whether one forwarder survived the startup swap and
// drains that forwarder so no goroutine outlives the pass.
func runHarnessInitPass(record func(time.Duration)) bool {
	events := make(chan core.HarnessEvent, 64)
	fake := &configWatchBenchHarness{events: events}
	state := newLiveState(config.Config{}, fake)
	var live atomic.Pointer[liveState]
	live.Store(state)
	h := &reloadableHarness{live: &live, events: fake.Events()}

	start := time.Now()
	h.init()
	record(time.Since(start))
	if h.activeForwarder() == nil {
		return false
	}
	h.setForwarder(nil)
	return true
}

// TestConfigWatchBenchWorkStable pins the counts the CI comparison reads.
// Every pass over a fresh fixture reports the same work and no errors.
func TestConfigWatchBenchWorkStable(t *testing.T) {
	fx := newConfigWatchBenchFixture(t)
	model, check := "model-a", "check-a"
	var want configWatchPassCounts
	for pass := 0; pass < 3; pass++ {
		nextModel, nextCheck, got := runConfigWatchReloadPass(fx, model, check, func(time.Duration) {})
		model, check = nextModel, nextCheck
		if pass == 0 {
			want = got
			continue
		}
		if got != want {
			t.Fatalf("pass %d counts = %+v, want %+v", pass, got, want)
		}
	}
	if want.work != configWatchWorkPerPass() || want.errs != 0 {
		t.Fatalf("pass counts = %+v, want work=%d and no errors", want, configWatchWorkPerPass())
	}
}

// TestHarnessInitBenchLeavesOneForwarder confirms the startup double swap
// settles with one live forwarder on every pass.
func TestHarnessInitBenchLeavesOneForwarder(t *testing.T) {
	for pass := 0; pass < 3; pass++ {
		if !runHarnessInitPass(func(time.Duration) {}) {
			t.Fatalf("pass %d left no active forwarder", pass)
		}
	}
}

// BenchmarkConfigWatchReload prices two changed-config reloads per operation:
// a model-only edit that keeps the harness and relay, and a reloadable edit
// that rebuilds the harness and broadcasts a forwarder retarget. Work counts
// file reads, hashes, config parses, restart-field checks, cache evictions,
// harness builds and forwarder swaps. Errs counts read or parse failures and
// swaps that do not match the injected change.
//
// CI runs:
//
//	go test ./cmd/beacon -run '^$' -bench '^BenchmarkConfigWatchReload$|^BenchmarkHarnessInit$' -benchmem -benchtime=2000x -count=1
func BenchmarkConfigWatchReload(b *testing.B) {
	fx := newConfigWatchBenchFixture(b)
	model, check := "model-a", "check-a"
	durations := make([]time.Duration, 0, b.N)
	var totalWork, totalErrs int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		record := func(d time.Duration) { durations = append(durations, d) }
		var counts configWatchPassCounts
		model, check, counts = runConfigWatchReloadPass(fx, model, check, record)
		totalWork += counts.work
		totalErrs += counts.errs
	}
	b.StopTimer()

	if totalErrs != 0 {
		b.Fatalf("%s: errs=%d, want zero", configWatchPrefix, totalErrs)
	}
	work := totalWork / int64(b.N)
	errs := totalErrs / int64(b.N)
	median := configWatchBenchMedian(durations)
	printConfigWatchReport(os.Stdout, configWatchPrefix, median, work, errs)
	b.ReportMetric(float64(median.Nanoseconds()), configWatchPrefix+"-median-ns")
	b.ReportMetric(float64(work), configWatchPrefix+"-work")
	b.ReportMetric(float64(errs), configWatchPrefix+"-errs")
	b.ReportMetric(1, configWatchPrefix+"-limiter-"+configWatchLimiterSlug)
}

// BenchmarkHarnessInit prices the startup forwarder setup. init calls
// setForwarder twice with the same source channel, so the first forwarder is
// cancelled and drained before the second starts. Work counts the two starts
// and the one drain. Errs counts passes that end without a live forwarder.
//
// CI runs the same command as BenchmarkConfigWatchReload.
func BenchmarkHarnessInit(b *testing.B) {
	durations := make([]time.Duration, 0, b.N)
	var totalWork, totalErrs int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		record := func(d time.Duration) { durations = append(durations, d) }
		if !runHarnessInitPass(record) {
			totalErrs++
			continue
		}
		totalWork += initForwarderStarts + initForwarderDrains
	}
	b.StopTimer()

	if totalErrs != 0 {
		b.Fatalf("%s: errs=%d, want zero", harnessInitPrefix, totalErrs)
	}
	work := totalWork / int64(b.N)
	errs := totalErrs / int64(b.N)
	median := configWatchBenchMedian(durations)
	printConfigWatchReport(os.Stdout, harnessInitPrefix, median, work, errs)
	b.ReportMetric(float64(median.Nanoseconds()), harnessInitPrefix+"-median-ns")
	b.ReportMetric(float64(work), harnessInitPrefix+"-work")
	b.ReportMetric(float64(errs), harnessInitPrefix+"-errs")
	b.ReportMetric(1, harnessInitPrefix+"-limiter-"+configWatchLimiterSlug)
}
