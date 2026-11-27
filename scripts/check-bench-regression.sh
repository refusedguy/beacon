#!/usr/bin/env bash
# Fail a nightly benchstat comparison when a timing median regresses.
# Input is the CSV from: benchstat -format csv <baseline.txt> <current.txt>
# A baseline far above the current measurement is rejected as poisoned: a load
# spike in one night must not become the reference for every later night.
# Keys, thresholds and placement come from docs/perf/bench-registry.json.
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <benchstat.csv>" >&2
  exit 2
fi

csv="$1"
if [[ ! -f "$csv" ]]; then
  echo "comparison file not found: $csv" >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
registry="$root/docs/perf/bench-registry.json"
if [[ ! -f "$registry" ]]; then
  echo "bench registry not found: $registry" >&2
  exit 2
fi

# The nightly job has Go but not Node. Generate a small stdlib-only helper that
# reads the registry and proves every key still resolves to an emitted metric.
gate_dir="$(mktemp -d)"
trap 'rm -rf "$gate_dir"' EXIT
cat > "$gate_dir/registry_gate.go" <<'GO'
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type spec struct {
	Key          string  `json:"key"`
	WorkMetric   string  `json:"workMetric"`
	ErrMetric    string  `json:"errMetric"`
	Placement    string  `json:"placement"`
	ThresholdPct float64 `json:"thresholdPct"`
}

type document struct {
	Specs []spec `json:"specs"`
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "bench registry: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	if len(os.Args) != 3 {
		fail("usage: registry_gate <registry.json> <repo root>")
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fail("read registry: %v", err)
	}
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		fail("parse registry: %v", err)
	}

	benchDir := filepath.Join(os.Args[2], "internal", "bench")
	entries, err := os.ReadDir(benchDir)
	if err != nil {
		fail("read bench source: %v", err)
	}
	var source strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(benchDir, entry.Name()))
		if err != nil {
			fail("read %s: %v", entry.Name(), err)
		}
		source.Write(b)
	}
	src := source.String()

	for _, s := range doc.Specs {
		if s.Placement != "nightly" && s.Placement != "both" {
			continue
		}
		if s.Key == "" || !strings.Contains(src, strconv.Quote(s.Key)) {
			fail("registry key %q has no emitted metric", s.Key)
		}
		if s.ThresholdPct <= 0 {
			fail("registry key %q has non-positive threshold %g", s.Key, s.ThresholdPct)
		}
		for _, metric := range []string{s.WorkMetric, s.ErrMetric} {
			if metric == "" {
				continue
			}
			if strings.Contains(src, strconv.Quote(metric)) {
				continue
			}
			suffix := strings.TrimPrefix(metric, s.Key)
			if suffix == metric || !strings.Contains(src, strconv.Quote(suffix)) {
				fail("registry key %q has no emitted metric %q", s.Key, metric)
			}
		}
		fmt.Printf("spec\t%s\t%s\t%g\n", s.Key, s.Key+"-median-sec", s.ThresholdPct)
	}
	// The registry has no aggregate sec/op key, so the micro and e2e limits
	// stay policy defaults. Changing a limit here does not edit the registry.
	fmt.Println("limit\tmicro\t-\t15")
	fmt.Println("limit\te2e\t-\t20")
}
GO

if ! (cd "$root" && go run "$gate_dir/registry_gate.go" "$registry" "$root") > "$gate_dir/specs.tsv"; then
  echo "failed to read benchmark registry" >&2
  exit 2
fi

awk \
  -v specs="$gate_dir/specs.tsv" \
  -v noise_floor=2 \
  -v min_median_sec=0.0001 \
  -v sanity_factor="${BEACON_BASELINE_SANITY_FACTOR:-3}" '
  BEGIN {
    FS = ","; failed = 0; micro_limit = 0; e2e_limit = 0
    while ((getline line < specs) > 0) {
      if (split(line, f, "\t") < 4) continue
      if (f[1] == "limit") {
        if (f[2] == "micro") micro_limit = f[4] + 0
        else if (f[2] == "e2e") e2e_limit = f[4] + 0
      } else if (f[1] == "spec") {
        threshold[f[3]] = f[4] + 0
      }
    }
    close(specs)
    if (micro_limit <= 0 || e2e_limit <= 0) {
      print "registry gate: no aggregate limits" > "/dev/stderr"
      failed = 1
    }
  }
  /^,/ {
    metric = $2
    next
  }
  {
    if ($1 == "" || $1 == "geomean" || $2 == "" || $4 == "") next
    if (metric != "sec/op" && metric !~ /-median-sec$/) next

    old = $2 + 0
    new = $4 + 0
    if (old <= 0 || old < min_median_sec || new < min_median_sec) next

    # Reject a baseline far above the current measurement without a recorded
    # median table. A spike on one night must not become the next reference.
    ceiling = new * sanity_factor
    if (old > ceiling) {
      printf "POISONED BASELINE: %s %s old=%.6fs exceeds sanity ceiling %.6fs\n", \
        $1, metric, old, ceiling > "/dev/stderr"
      failed = 1
      next
    }

    delta = 100 * (new - old) / old
    if (delta < noise_floor) next

    if (metric == "sec/op") {
      limit = ($1 ~ /E2EOperatorPaths/) ? e2e_limit : micro_limit
    } else {
      limit = threshold[metric]
      if (limit <= 0) limit = micro_limit
    }
    printf "%-22s %-30s old=%.6fs new=%.6fs delta=%+.2f%% limit=%.0f%%\n", \
      $1, metric, old, new, delta, limit
    if (delta + 0.001 > limit) {
      printf "REGRESSION: %s %s %+.2f%% exceeds %.0f%%\n", \
        $1, metric, delta, limit > "/dev/stderr"
      failed = 1
    }
  }
  END { exit failed }
' "$csv"
