#!/usr/bin/env bash
# Fail a nightly benchstat comparison when a timing median regresses.
# Input is the CSV from: benchstat -format csv <baseline.txt> <current.txt>
# A baseline far above the recorded medians is rejected as poisoned: a load
# spike in one night must not become the reference for every later night.
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

awk \
  -v micro_limit=15 \
  -v e2e_limit=20 \
  -v noise_floor=2 \
  -v min_median_sec=0.0001 \
  -v sanity_factor="${BEACON_BASELINE_SANITY_FACTOR:-3}" '
  BEGIN {
    FS = ","; failed = 0
    # Medians recorded in docs/perf/dossier.md and docs/research/perf-e2e.md.
    # Only these names have a reference; other rows skip the sanity check.
    expected["sqlite-snapshot-median-sec"] = 0.0082
    expected["card-parse-median-sec"] = 0.0020
    expected["wire-encode-median-sec"] = 0.000056
    expected["redaction-median-sec"] = 0.00016
    expected["mcp-journal-median-sec"] = 0.00012
    expected["sse-fanout-median-sec"] = 0.00050
    expected["reconcile-tick-median-sec"] = 0.0105
    expected["dispatch-prompt-median-sec"] = 0.0055
    expected["hot-paths-sec/op"] = 0.017
    expected["e2e-sec/op"] = 0.043
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

    # Reject a poisoned baseline before it can hide a regression or be
    # published as the next reference. The factor allows machine variance.
    ceiling = 0
    if (metric == "sec/op") {
      if ($1 ~ /E2EOperatorPaths/) ceiling = expected["e2e-sec/op"] * sanity_factor
      else if ($1 ~ /HotPaths/) ceiling = expected["hot-paths-sec/op"] * sanity_factor
    } else if (expected[metric] > 0) {
      ceiling = expected[metric] * sanity_factor
    }
    if (ceiling > 0 && old > ceiling) {
      printf "POISONED BASELINE: %s %s old=%.6fs exceeds sanity ceiling %.6fs\n", \
        $1, metric, old, ceiling > "/dev/stderr"
      failed = 1
      next
    }

    delta = 100 * (new - old) / old
    if (delta < noise_floor) next

    limit = ($1 ~ /E2EOperatorPaths/) ? e2e_limit : micro_limit
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
