package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// This file is a control for the advisory perf comparison. It imports only the
// standard library and marshals a fixed struct, so a change to Beacon code
// cannot change what this benchmark measures. The canonical row shape is
// repeated here instead of reusing perfRow for the same reason.

const (
	controlPrefix  = "beacon-perf: "
	controlKey     = "json-control"
	controlLimiter = "encoding/json reflection over a fixed struct"

	// Work and errs are constants. The control reports wall clock alone so a
	// Beacon hotspot can be compared against it as a ratio.
	controlWork = 1
	controlErrs = 0
)

// controlPlan is the fixed payload. No Beacon type appears in it.
type controlPlan struct {
	ID     int      `json:"id"`
	Name   string   `json:"name"`
	Active bool     `json:"active"`
	Score  float64  `json:"score"`
	Tags   []string `json:"tags"`
}

var controlFixture = controlPlan{
	ID:     42,
	Name:   "fixed-control-plan",
	Active: true,
	Score:  0.75,
	Tags:   []string{"alpha", "beta", "gamma"},
}

// controlRow is the canonical payload shared by every perf harness, declared
// locally so the control has no dependency on a Beacon package.
type controlRow struct {
	Key      string  `json:"key"`
	MedianMS float64 `json:"median_ms"`
	MinMS    float64 `json:"min_ms"`
	MaxMS    float64 `json:"max_ms"`
	Work     int64   `json:"work"`
	Errs     int64   `json:"errs"`
	Limiter  string  `json:"limiter"`
	Cache    string  `json:"cache"`
}

// BenchmarkJSONControl times encoding/json over a fixed struct. It emits its
// own canonical row with fixed work and errs, and stamps the same cache state
// as every other harness.
func BenchmarkJSONControl(b *testing.B) {
	start := time.Now()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(controlFixture); err != nil {
			b.Fatal(err)
		}
	}
	ms := float64(time.Since(start)) / float64(time.Millisecond)
	b.StopTimer()

	cache := os.Getenv("BEACON_BENCH_CACHE")
	if cache == "" {
		cache = "unset"
	}
	row := controlRow{
		Key:      controlKey,
		MedianMS: ms,
		MinMS:    ms,
		MaxMS:    ms,
		Work:     controlWork,
		Errs:     controlErrs,
		Limiter:  controlLimiter,
		Cache:    cache,
	}
	data, err := json.Marshal(row)
	if err != nil {
		b.Fatal(err)
	}
	fmt.Printf("%s%s\n", controlPrefix, data)
}

// TestJSONControlCanonicalRow checks the shape the benchmark publishes. The
// field set is local on purpose so the control stays independent of bench code.
func TestJSONControlCanonicalRow(t *testing.T) {
	data, err := json.Marshal(controlRow{
		Key:      controlKey,
		MedianMS: 1,
		MinMS:    1,
		MaxMS:    1,
		Work:     controlWork,
		Errs:     controlErrs,
		Limiter:  controlLimiter,
		Cache:    "unset",
	})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"key", "median_ms", "min_ms", "max_ms", "work", "errs", "limiter", "cache"}
	if len(fields) != len(want) {
		t.Fatalf("control row has %d fields, want %d: %s", len(fields), len(want), data)
	}
	for _, name := range want {
		if _, ok := fields[name]; !ok {
			t.Fatalf("control row is missing %q: %s", name, data)
		}
	}
}
