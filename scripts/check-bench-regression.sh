#!/usr/bin/env bash
# Fail a nightly benchstat comparison when a median regresses past the noise
# measured by the control row on that night.
#
# Input is the CSV from: benchstat -format csv <baseline.txt> <current.txt>
# Keys, per-key thresholds, aggregate sec/op limits and the noise floor come
# from docs/perf/bench-registry.json. The emitters report -median-ns, so the
# gate converts those values to seconds before comparing them with the control.
#
# A baseline far above the current measurement is poisoned. That night is
# reported inconclusive and the script still exits zero, so CI publishes the
# good current measurement as the next baseline instead of downloading the
# poisoned artifact forever.
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

# The nightly job has Go but not Node. Generate a stdlib-only helper that reads
# the registry and proves every key still resolves to an emitted metric. The
# helper writes a tab-separated table the awk gate consumes.
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

type aggregate struct {
	Key          string  `json:"key"`
	Metric       string  `json:"metric"`
	Bench        string  `json:"bench"`
	ThresholdPct float64 `json:"thresholdPct"`
}

type document struct {
	Noise struct {
		DefaultFloorPct float64 `json:"defaultFloorPct"`
	} `json:"noise"`
	Aggregates []aggregate `json:"aggregates"`
	Specs      []spec      `json:"specs"`
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

	// The 2 percent value is a noise floor, not a pass limit. The real sec/op
	// limits are aggregate keys in the registry.
	floor := doc.Noise.DefaultFloorPct
	if floor <= 0 {
		floor = 2
	}
	fmt.Printf("policy\t%g\n", floor)
	for _, a := range doc.Aggregates {
		if a.Key == "" || a.Metric == "" || a.Bench == "" || a.ThresholdPct <= 0 {
			fail("invalid aggregate spec %+v", a)
		}
		fmt.Printf("aggregate\t%s\t%s\t%s\t%g\n", a.Key, a.Metric, a.Bench, a.ThresholdPct)
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

	control := false
	for _, s := range doc.Specs {
		if s.Placement != "nightly" && s.Placement != "both" {
			continue
		}
		if s.Key == "json-control" {
			// The control publishes a canonical row, not a named Go metric.
			fmt.Println("control\tJSONControl\tsec/op")
			control = true
			continue
		}
		if s.Key == "" || !strings.Contains(src, strconv.Quote(s.Key)) {
			fail("registry key %q has no emitted metric", s.Key)
		}
		if s.ThresholdPct <= 0 {
			fail("registry key %q has non-positive threshold %g", s.Key, s.ThresholdPct)
		}
		metric := s.Key + "-median-ns"
		if !strings.Contains(src, strconv.Quote(metric)) {
			suffix := strings.TrimPrefix(metric, s.Key)
			if suffix == metric || !strings.Contains(src, strconv.Quote(suffix)) {
				fail("registry key %q has no emitted metric %q", s.Key, metric)
			}
		}
		for _, m := range []string{s.WorkMetric, s.ErrMetric} {
			if m == "" {
				continue
			}
			if strings.Contains(src, strconv.Quote(m)) {
				continue
			}
			suffix := strings.TrimPrefix(m, s.Key)
			if suffix == m || !strings.Contains(src, strconv.Quote(suffix)) {
				fail("registry key %q has no emitted metric %q", s.Key, m)
			}
		}
		fmt.Printf("spec\t%s\t%s\t%g\t%s\t%s\n", s.Key, metric, s.ThresholdPct, s.WorkMetric, s.ErrMetric)
	}
	if !control {
		fail("registry has no json-control spec")
	}
}
GO

if ! (cd "$root" && go run "$gate_dir/registry_gate.go" "$registry" "$root") > "$gate_dir/specs.tsv"; then
  echo "failed to read benchmark registry" >&2
  exit 2
fi

awk \
  -v specs="$gate_dir/specs.tsv" \
  -v min_median_sec=1e-9 \
  -v sanity_factor="${BEACON_BASELINE_SANITY_FACTOR:-3}" '
  function pct(s) { sub(/%$/, "", s); return s + 0 }
  function max(a, b) { return a > b ? a : b }
  function seconds(metric, value) {
    if (metric ~ /-median-ns$/) return value / 1000000000
    return value
  }
  function check(label, metric, old, new, ciold, cinew, limit,
                 old_s, new_s, old_ratio, new_ratio, delta, spread, effective, ceiling) {
    old_s = seconds(metric, old)
    new_s = seconds(metric, new)
    if (old_s <= 0 || old_s < min_median_sec || new_s < min_median_sec) return
    ceiling = new_s * sanity_factor
    if (old_s > ceiling) {
      printf "INCONCLUSIVE: %s %s poisoned baseline old=%.6fs exceeds ceiling %.6fs\n", \
        label, metric, old_s, ceiling > "/dev/stderr"
      return
    }
    # Both sides carry the pure control row. Dividing by it removes the
    # machine speed difference between the two nights.
    old_ratio = old_s / ctrl_old
    new_ratio = new_s / ctrl_new
    delta = 100 * (new_ratio - old_ratio) / old_ratio
    spread = max(ciold, cinew)
    effective = max(floor, spread)
    if (limit > 0 && effective > limit) effective = limit
    printf "%-26s %-24s old=%.6fs new=%.6fs delta=%+.2f%% floor=%.1f%% spread=%.1f%% limit=%.1f%%\n", \
      label, metric, old_s, new_s, delta, floor, spread, effective
    if (delta > effective + 0.001) {
      printf "REGRESSION: %s %s %+.2f%% exceeds %.1f%%\n", \
        label, metric, delta, effective > "/dev/stderr"
      failed = 1
    }
  }
  function checkCounts(key,    wm, em, i, ow, nw, oe, ne) {
    wm = work_metric[key]
    em = err_metric[key]
    for (i = 1; i <= rows; i++) {
      if (wm != "" && r_metric[i] == wm) { ow = r_old[i]; nw = r_new[i] }
      if (em != "" && r_metric[i] == em) { oe = r_old[i]; ne = r_new[i] }
    }
    if (wm != "" && ow != "" && nw != "" && ow != nw) {
      printf "WORK MISMATCH: %s work old=%g new=%g\n", key, ow, nw > "/dev/stderr"
      failed = 1
    }
    if (em != "" && oe != "" && ne != "" && oe != ne) {
      printf "ERR MISMATCH: %s errs old=%g new=%g\n", key, oe, ne > "/dev/stderr"
      failed = 1
    }
  }
  BEGIN {
    FS = ","; failed = 0; rows = 0; nspec = 0; nagg = 0
    while ((getline line < specs) > 0) {
      n = split(line, f, "\t")
      if (n < 2) continue
      if (f[1] == "policy") floor = f[2] + 0
      else if (f[1] == "control") { ctrl_bench = f[2]; ctrl_metric = f[3] }
      else if (f[1] == "aggregate") {
        agg_limit[f[2]] = f[5] + 0
        agg_metric[f[2]] = f[3]
        agg_bench[f[2]] = f[4]
        agg_keys[++nagg] = f[2]
      } else if (f[1] == "spec") {
        spec_metric[f[2]] = f[3]
        spec_limit[f[2]] = f[4] + 0
        work_metric[f[2]] = f[5]
        err_metric[f[2]] = f[6]
        spec_keys[++nspec] = f[2]
      }
    }
    close(specs)
    if (!(floor > 0) || ctrl_metric == "" || ctrl_bench == "" || nspec == 0 || nagg == 0) {
      print "registry gate: incomplete registry" > "/dev/stderr"
      failed = 1
    }
  }
  /^,/ { metric = $2; next }
  {
    if ($1 == "" || $1 == "geomean" || $2 == "" || $4 == "") next
    rows++
    r_metric[rows] = metric
    r_bench[rows] = $1
    r_old[rows] = $2 + 0
    r_new[rows] = $4 + 0
    r_ciold[rows] = pct($3)
    r_cinew[rows] = pct($5)
  }
  END {
    if (failed) exit failed
    for (i = 1; i <= rows; i++) {
      if (r_metric[i] == ctrl_metric && r_bench[i] ~ ctrl_bench) {
        ctrl_old = r_old[i]
        ctrl_new = r_new[i]
        ctrl_spread = max(r_ciold[i], r_cinew[i])
      }
    }
    if (!(ctrl_old > 0) || !(ctrl_new > 0)) {
      # Without a control the two nights cannot be aligned to the same machine
      # speed. Still gate on raw deltas rather than passing by omission.
      print "INCONCLUSIVE: no usable nightly control row, using raw deltas" > "/dev/stderr"
      ctrl_old = 1
      ctrl_new = 1
    }
    if (ctrl_old > ctrl_new * sanity_factor) {
      print "INCONCLUSIVE: poisoned control row, using raw deltas" > "/dev/stderr"
      ctrl_old = 1
      ctrl_new = 1
    }
    # The control interval is the calibrated floor for this night.
    if (ctrl_spread > floor) floor = ctrl_spread
    for (a = 1; a <= nagg; a++) {
      key = agg_keys[a]
      count = 0
      for (i = 1; i <= rows; i++) {
        if (r_metric[i] == agg_metric[key] && r_bench[i] ~ agg_bench[key]) {
          check(key, r_metric[i], r_old[i], r_new[i], r_ciold[i], r_cinew[i], agg_limit[key])
          count++
        }
      }
      if (count == 0) printf "INCONCLUSIVE: no %s aggregate row\n", key > "/dev/stderr"
    }
    for (s = 1; s <= nspec; s++) {
      key = spec_keys[s]
      count = 0
      for (i = 1; i <= rows; i++) {
        if (r_metric[i] == spec_metric[key]) {
          check(key, r_metric[i], r_old[i], r_new[i], r_ciold[i], r_cinew[i], spec_limit[key])
          count++
        }
      }
      if (count == 0) {
        # A key whose work metric is absent belongs to a benchmark the nightly
        # command does not run. Only a vanished median on a benchmark that did
        # run is worth reporting.
        if (work_metric[key] != "") {
          for (i = 1; i <= rows; i++) {
            if (r_metric[i] == work_metric[key]) {
              printf "INCONCLUSIVE: no %s row\n", spec_metric[key] > "/dev/stderr"
              break
            }
          }
        }
        continue
      }
      checkCounts(key)
    }
    exit failed
  }
' "$csv"
