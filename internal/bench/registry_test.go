package bench

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestRegistryUniqueKeys(t *testing.T) {
	specs := Registry()
	if len(specs) == 0 {
		t.Fatal("Registry is empty")
	}
	seen := make(map[string]bool, len(specs))
	for _, spec := range specs {
		if spec.Key == "" {
			t.Error("registry has an empty key")
			continue
		}
		if seen[spec.Key] {
			t.Errorf("duplicate registry key %q", spec.Key)
		}
		seen[spec.Key] = true
	}
	if len(seen) != len(specs) {
		t.Fatalf("Registry has %d entries but %d unique keys", len(specs), len(seen))
	}
}

func TestRegistryValidPlacement(t *testing.T) {
	valid := map[string]bool{PlacementPR: true, PlacementNightly: true, PlacementBoth: true}
	for _, spec := range Registry() {
		if !valid[spec.Placement] {
			t.Errorf("key %q has invalid placement %q", spec.Key, spec.Placement)
		}
		if spec.Unit == "" {
			t.Errorf("key %q has no unit", spec.Key)
		}
		if spec.Limiter == "" {
			t.Errorf("key %q has no limiter", spec.Key)
		}
		if spec.ThresholdPct <= 0 {
			t.Errorf("key %q has non-positive threshold %v", spec.Key, spec.ThresholdPct)
		}
	}
}

// TestRegistryReusesReportLimiterStrings proves the limiter strings have a
// single owner: report.go. A limiter edited in one place and not the other
// fails here instead of drifting into two competing owners.
func TestRegistryReusesReportLimiterStrings(t *testing.T) {
	byKey := make(map[string]Spec)
	for _, spec := range Registry() {
		byKey[spec.Key] = spec
	}
	for _, name := range hotspotOrder {
		if got := byKey[name].Limiter; got != limiters[name] {
			t.Errorf("%s limiter = %q, want report.go value %q", name, got, limiters[name])
		}
	}
	for _, name := range []string{phaseReads, phaseAssembly, phaseTotal} {
		if got := byKey[name].Limiter; got != snapshotLimiters[name] {
			t.Errorf("%s limiter = %q, want report.go value %q", name, got, snapshotLimiters[name])
		}
	}
}

// harnessSources are the files that print canonical perf rows. Go benchmarks
// print through printPerfRow; the Node harnesses and the workspace latency
// test print the JSON payload themselves.
var harnessSources = []string{
	"report.go",
	"snapshot_bench_test.go",
	"ui_bench.js",
	"run_watcher_bench.js",
	"../infra/workspace/git_worktree_test.go",
}

// canonicalKeyRe finds the key a harness publishes for a canonical row. It
// matches the key field of a printed payload and the key argument of
// printPerfRow. A dynamic name such as s.Name is deliberately not matched:
// those names come from report.go and are covered by the registry table.
var canonicalKeyRe = regexp.MustCompile(`(?:\b[Kk]ey:\s*|printPerfRow\([^,\n]+,\s*)"([a-z0-9][a-z0-9-]*)"`)

// TestHarnessKeysAreRegistered checks the other direction of the cross-check:
// every canonical key a harness prints must have a registry entry. An
// unregistered key makes the advisory comment fall back to the harness for
// limiter text, which defeats the registry as the single source of truth.
func TestHarnessKeysAreRegistered(t *testing.T) {
	registered := make(map[string]bool, len(Registry()))
	for _, spec := range Registry() {
		registered[spec.Key] = true
	}
	for _, path := range harnessSources {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, match := range canonicalKeyRe.FindAllStringSubmatch(string(data), -1) {
			key := match[1]
			if !registered[key] {
				t.Errorf("%s prints canonical key %q with no registry entry", path, key)
			}
		}
	}
}

// TestRegistryKeysResolveToEmittedMetrics is the key-to-metric cross-check.
// Go cannot list benchmark names without running the harness, so the test
// reads the bench package source. A registry entry whose key is no longer
// named by a benchmark or report line, or whose work/err metric suffix is no
// longer emitted, fails the check.
func TestRegistryKeysResolveToEmittedMetrics(t *testing.T) {
	source := benchSource(t)
	for _, spec := range Registry() {
		if !strings.Contains(source, strconv.Quote(spec.Key)) {
			t.Errorf("registry key %q has no benchmark or report line in internal/bench", spec.Key)
		}
		for _, metric := range []string{spec.WorkMetric, spec.ErrMetric} {
			if metric == "" {
				continue
			}
			// Some metrics are a literal name in a ReportMetric call; the
			// per-benchmark ones are built as key + suffix.
			if strings.Contains(source, strconv.Quote(metric)) {
				continue
			}
			suffix := strings.TrimPrefix(metric, spec.Key)
			if suffix == metric || !strings.Contains(source, strconv.Quote(suffix)) {
				t.Errorf("metric %q for key %q is not emitted by any benchmark", metric, spec.Key)
			}
		}
	}
}

// TestRegistryControlSpec ties the registry's control entry to the constants
// in the control benchmark. The control stays independent of Beacon code, so
// this cross-check keeps the registry the owner of its name and limiter text.
func TestRegistryControlSpec(t *testing.T) {
	if controlSpecKey != controlKey || controlSpecLimiter != controlLimiter {
		t.Fatal("registry control name or limiter drifted from the benchmark")
	}
	var control Spec
	for _, spec := range Registry() {
		if spec.Key == controlKey {
			control = spec
		}
	}
	if control.Key == "" {
		t.Fatalf("registry has no spec for control key %q", controlKey)
	}
	if control.Limiter != controlLimiter {
		t.Errorf("control limiter = %q, want %q", control.Limiter, controlLimiter)
	}
	if control.Placement != PlacementBoth {
		t.Errorf("control placement = %q, want %q", control.Placement, PlacementBoth)
	}
	if control.Unit != "ns" || control.WorkMetric != "work" || control.ErrMetric != "errs" {
		t.Errorf("control metrics = %q, %q, %q; want ns, work, errs", control.Unit, control.WorkMetric, control.ErrMetric)
	}
	if control.ThresholdPct <= 0 {
		t.Errorf("control threshold = %v, want positive", control.ThresholdPct)
	}
}

// TestRegistryNoiseBlock proves the generated document carries a top-level
// noise block whose fields are owned by DefaultNoisePolicy in registry.go.
func TestRegistryNoiseBlock(t *testing.T) {
	data, err := EncodeRegistry()
	if err != nil {
		t.Fatalf("encode registry: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode registry: %v", err)
	}
	raw, ok := doc["noise"]
	if !ok {
		t.Fatal("registry document has no noise block")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode noise block: %v", err)
	}
	want := []string{"defaultFloorPct", "maxAgeHours", "minSamples", "quantile"}
	if len(fields) != len(want) {
		t.Fatalf("noise block has %d fields, want %d: %s", len(fields), len(want), raw)
	}
	for _, name := range want {
		if _, ok := fields[name]; !ok {
			t.Fatalf("noise block is missing %q: %s", name, raw)
		}
	}
	var got NoisePolicy
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode noise policy: %v", err)
	}
	if got != DefaultNoisePolicy() {
		t.Errorf("noise policy = %+v, want %+v", got, DefaultNoisePolicy())
	}
	policy := DefaultNoisePolicy()
	if policy.DefaultFloorPct <= 0 || policy.MaxAgeHours <= 0 || policy.MinSamples <= 0 {
		t.Errorf("noise policy has a non-positive field: %+v", policy)
	}
	if policy.Quantile <= 0 || policy.Quantile >= 1 {
		t.Errorf("noise quantile = %v, want between 0 and 1", policy.Quantile)
	}
}

// TestBenchRegistryUpToDate fails when the committed JSON differs from the
// generated document. It mirrors TestPermissionsUpToDate in the mcp package.
func TestBenchRegistryUpToDate(t *testing.T) {
	const committed = "../../docs/perf/bench-registry.json"
	want, err := os.ReadFile(committed)
	if err != nil {
		t.Fatalf("read %s: %v", committed, err)
	}
	got, err := EncodeRegistry()
	if err != nil {
		t.Fatalf("encode registry: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is stale. Run `go generate ./internal/bench/` and commit the result.\nwant:\n%s\ngot:\n%s", committed, want, got)
	}
}

// benchSource returns the concatenated Go source of the bench package, minus
// the registry files, so the cross-check only sees emitted benchmark names.
func benchSource(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/bench: %v", err)
	}
	var b strings.Builder
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if name == "registry.go" || name == "registry_test.go" {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		b.Write(data)
	}
	// The Node harnesses and the workspace latency test publish canonical
	// rows too, so their keys and field names count as emitted.
	for _, path := range harnessSources[2:] {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		b.Write(data)
	}
	return b.String()
}
