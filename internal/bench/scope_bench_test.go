package bench

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// Scope globs reach branches that no other benchmark touches. planner_bench_test
// builds only ScopeFile values, and card fixtures parse patterns without timing
// them. Here the overlap benchmark drives ScopePath.Covers through globMatch
// and commonPrefix, while the build benchmark drives ScopePattern and cleanPath.
//
// Both fixtures are seeded and fixed, so the same machine measures the same
// amount of work on every pass. Work and errs are per-pass counts, which keeps
// them identical in smoke mode and in a full run.

const scopeRepeats = 20

// scopeOverlapCase pairs two scopes with the Covers result it must produce.
// The want values are fixed by the pattern structure; seeded fillers give each
// repetition concrete but deterministic names.
type scopeOverlapCase struct {
	a, b core.ScopePath
	want bool
}

func scopeGlobOverlapCases() []scopeOverlapCase {
	r := seeded()
	seg := func() string { return hexString(r, 6) }
	return []scopeOverlapCase{
		{core.ScopePattern("docs/**"), core.ScopeFile("docs/" + seg() + "/a.md"), true},
		{core.ScopePattern("docs/**"), core.ScopeFile("internal/" + seg() + "/a.go"), false},
		{core.ScopePattern("*.go"), core.ScopeFile(seg() + ".go"), true},
		{core.ScopePattern("*.go"), core.ScopeFile("internal/" + seg() + ".go"), false},
		{core.ScopePattern("internal/*/file.go"), core.ScopeFile("internal/" + seg() + "/file.go"), true},
		{core.ScopePattern("internal/*/file.go"), core.ScopeFile("internal/" + seg() + "/sub/file.go"), false},
		{core.ScopePattern("internal/**/file.go"), core.ScopeFile("internal/file.go"), true},
		{core.ScopePattern("internal/**/file.go"), core.ScopeFile("internal/" + seg() + "/file.go"), true},
		{core.ScopePattern("pkg/*/**"), core.ScopePattern("pkg/core/**"), true},
		{core.ScopePattern("pkg/*/**"), core.ScopePattern("docs/**"), false},
		{core.ScopePattern("a?c/*.go"), core.ScopeFile("abc/" + seg() + ".go"), true},
		{core.ScopePattern("a?c/*.go"), core.ScopeFile("abd/" + seg() + ".go"), false},
	}
}

func scopeGlobOverlapFixtures() []scopeOverlapCase {
	base := scopeGlobOverlapCases()
	out := make([]scopeOverlapCase, 0, len(base)*scopeRepeats)
	for i := 0; i < scopeRepeats; i++ {
		out = append(out, base...)
	}
	return out
}

func runScopeGlobOverlap(pairs []scopeOverlapCase) (work, errs int64) {
	for _, c := range pairs {
		work++
		if c.a.Covers(c.b) != c.want {
			errs++
		}
	}
	return work, errs
}

// scopeBuildCase names a constructor, its input and the Kind and Value the
// constructor must return. Inputs with backslashes, "./" prefixes and trailing
// slashes make cleanPath run every string pass.
type scopeBuildCase struct {
	in    string
	build func(string) core.ScopePath
	kind  byte
	value string
}

func scopeBuildCases() []scopeBuildCase {
	r := seeded()
	seg := hexString(r, 6)
	return []scopeBuildCase{
		{"docs/**", core.ScopePattern, 'g', "docs/**"},
		{"./docs\\**", core.ScopePattern, 'g', "docs/**"},
		{"./internal\\bench\\*/", core.ScopePattern, 'g', "internal/bench/*"},
		{"*.go", core.ScopePattern, 'g', "*.go"},
		{"internal/*/file.go", core.ScopePattern, 'g', "internal/*/file.go"},
		{"a?c/*.go", core.ScopePattern, 'g', "a?c/*.go"},
		{"docs/perf/**", core.ScopePattern, 'g', "docs/perf/**"},
		{"internal/" + seg + "/*.go", core.ScopePattern, 'g', "internal/" + seg + "/*.go"},
		{"pkg/core/run.go", core.ScopeFile, 'f', "pkg/core/run.go"},
		{"pkg/core/" + seg + ".go", core.ScopeFile, 'f', "pkg/core/" + seg + ".go"},
		{"internal/bench/", core.ScopeDir, 'd', "internal/bench"},
		{"./internal/bench/", core.ScopeDir, 'd', "internal/bench"},
	}
}

func scopeBuildFixtures() []scopeBuildCase {
	base := scopeBuildCases()
	out := make([]scopeBuildCase, 0, len(base)*scopeRepeats)
	for i := 0; i < scopeRepeats; i++ {
		out = append(out, base...)
	}
	return out
}

func runScopeBuild(cases []scopeBuildCase) (work, errs int64) {
	for _, c := range cases {
		work++
		got := c.build(c.in)
		if got.Kind() != c.kind || got.Value() != c.value {
			errs++
		}
	}
	return work, errs
}

func printScopeReport(w io.Writer, name, limiterLabel, limiterMetric string, median time.Duration, work, errs int64, count, iterations int) {
	fmt.Fprintf(w, "%s: scopes=%d iterations=%d\n", name, count, iterations)
	fmt.Fprintf(w, "%s: median=%s work=%d errs=%d limiter=%s (%s)\n",
		name, median, work, errs, limiterLabel, limiterMetric)
}

func TestScopeGlobOverlapWorkStable(t *testing.T) {
	pairs := scopeGlobOverlapFixtures()
	work, errs := runScopeGlobOverlap(pairs)
	againWork, againErrs := runScopeGlobOverlap(pairs)
	if work != againWork || errs != againErrs {
		t.Fatalf("scope overlap work is not stable: first=%d/%d second=%d/%d", work, errs, againWork, againErrs)
	}
	if work <= 0 || errs != 0 {
		t.Fatalf("scope overlap work=%d errs=%d, want positive work and no errors", work, errs)
	}
}

func TestScopeBuildWorkStable(t *testing.T) {
	cases := scopeBuildFixtures()
	work, errs := runScopeBuild(cases)
	againWork, againErrs := runScopeBuild(cases)
	if work != againWork || errs != againErrs {
		t.Fatalf("scope build work is not stable: first=%d/%d second=%d/%d", work, errs, againWork, againErrs)
	}
	if work <= 0 || errs != 0 {
		t.Fatalf("scope build work=%d errs=%d, want positive work and no errors", work, errs)
	}
}

// BenchmarkScopeGlobOverlap times ScopePath.Covers over glob scope pairs. The
// named limiter is the recursive strings.Split plus path.Match per segment in
// globMatch. CI runs a bounded smoke pass with BEACON_BENCH_SMOKE=1 and
// -benchtime=1x. Smoke mode skips the warm pass; the fixture, work and errs
// stay the same.
func BenchmarkScopeGlobOverlap(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	pairs := scopeGlobOverlapFixtures()
	if !smokeMode() {
		_, _ = runScopeGlobOverlap(pairs)
	}

	b.ReportAllocs()
	b.ResetTimer()
	ds := make([]time.Duration, 0, b.N)
	var work, errs int64
	for i := 0; i < b.N; i++ {
		start := time.Now()
		w, e := runScopeGlobOverlap(pairs)
		ds = append(ds, time.Since(start))
		work, errs = w, e
	}
	b.StopTimer()
	if work <= 0 || errs != 0 {
		b.Fatalf("scope overlap work=%d errs=%d, want positive work and no errors", work, errs)
	}

	median := medianDuration(ds)
	printScopeReport(os.Stdout, "scope-glob",
		"strings.Split plus path.Match per segment", "scope-glob-limiter-split-match",
		median, work, errs, len(pairs), b.N)
	b.ReportMetric(float64(median.Nanoseconds()), "scope-glob-median-ns")
	b.ReportMetric(float64(work), "scope-glob-work")
	b.ReportMetric(float64(errs), "scope-glob-errs")
	b.ReportMetric(1, "scope-glob-limiter-split-match")
}

// BenchmarkScopeBuild times scope construction through ScopePattern, ScopeFile
// and ScopeDir. The named limiter is the cleanPath string passes. CI runs a
// bounded smoke pass with BEACON_BENCH_SMOKE=1 and -benchtime=1x. Smoke mode
// skips the warm pass; the fixture, work and errs stay the same.
func BenchmarkScopeBuild(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	cases := scopeBuildFixtures()
	if !smokeMode() {
		_, _ = runScopeBuild(cases)
	}

	b.ReportAllocs()
	b.ResetTimer()
	ds := make([]time.Duration, 0, b.N)
	var work, errs int64
	for i := 0; i < b.N; i++ {
		start := time.Now()
		w, e := runScopeBuild(cases)
		ds = append(ds, time.Since(start))
		work, errs = w, e
	}
	b.StopTimer()
	if work <= 0 || errs != 0 {
		b.Fatalf("scope build work=%d errs=%d, want positive work and no errors", work, errs)
	}

	median := medianDuration(ds)
	printScopeReport(os.Stdout, "scope-build",
		"cleanPath string passes", "scope-build-limiter-cleanpath",
		median, work, errs, len(cases), b.N)
	b.ReportMetric(float64(median.Nanoseconds()), "scope-build-median-ns")
	b.ReportMetric(float64(work), "scope-build-work")
	b.ReportMetric(float64(errs), "scope-build-errs")
	b.ReportMetric(1, "scope-build-limiter-cleanpath")
}
