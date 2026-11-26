#!/usr/bin/env bash
# Fail a nightly benchstat comparison when a timing median regresses.
# Input is the CSV from: benchstat -format csv <baseline.txt> <current.txt>
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
  -v min_median_sec=0.0001 '
  BEGIN { FS = ","; failed = 0 }
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
