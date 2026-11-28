#!/usr/bin/env bash
# Run ten alternating perf passes over two detached worktrees.
#
# Usage: bash scripts/perf-harness.sh <tree-dir> <output-prefix>
#
# The tree directory holds the perf-base and perf-head worktrees. Canonical
# rows go to <tree-dir>/<prefix>-base.txt and <tree-dir>/<prefix>-head.txt.
# Failed commands go to <tree-dir>/<prefix>-status so the advisory verdict can
# report them without failing the step.

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <tree-dir> <output-prefix>" >&2
  exit 2
fi

dir="$1"
prefix="$2"

: > "$dir/$prefix-base.txt"
: > "$dir/$prefix-head.txt"
: > "$dir/$prefix-status"

allowed="$(awk '/^Cpus_allowed_list:/ {print $2}' /proc/self/status)"
cpu="${allowed%%,*}"
cpu="${cpu%%-*}"
if [[ -z "$cpu" ]]; then
  echo "no allowed CPU in Cpus_allowed_list" >> "$dir/$prefix-status"
  cpu=0
fi
echo "pinned measurement CPU: $cpu"

# Compile every measured package before the pinned passes. This warmup writes
# no canonical row and is not counted as a pass.
for tree in perf-base perf-head; do
  (cd "$dir/$tree" && go test -vet=off -run '^$' -bench '^$' ./internal/bench/) \
    || echo "warmup $tree" >> "$dir/$prefix-status"
  (cd "$dir/$tree" && go test -vet=off -run '^$' ./internal/infra/workspace/) \
    || echo "warmup $tree" >> "$dir/$prefix-status"
  (cd "$dir/$tree" && go test -vet=off -run '^$' -bench '^$' ./cmd/beacon/) \
    || echo "warmup $tree" >> "$dir/$prefix-status"
  (cd "$dir/$tree/ui/tui" && go test -vet=off -run '^$' -bench '^$' ./) \
    || echo "warmup $tree" >> "$dir/$prefix-status"
done
export BEACON_BENCH_CACHE=warm

run_side() {
  local side="$1" tree="$2"
  {
    echo "== pass $side =="
    (cd "$tree" && taskset -c "$cpu" env BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' \
      -bench 'BenchmarkReconcileTickCounts|BenchmarkBaseRedCheck|BenchmarkBaseRedBlockFanout' -benchtime=1x -count=1 -benchmem ./internal/bench/) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree" && taskset -c "$cpu" env BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' \
      -bench '^BenchmarkSQLiteSnapshot$' -benchtime=1x -count=1 -benchmem ./internal/bench/) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree" && taskset -c "$cpu" env BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' \
      -bench '^BenchmarkPlannerSelectInjectedChecker$' -benchtime=1x -count=1 -benchmem ./internal/bench/) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree" && taskset -c "$cpu" env BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' \
      -bench '^BenchmarkSpawnPrompt$' -benchtime=1x -count=1 -benchmem ./internal/bench/) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree" && taskset -c "$cpu" env BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' \
      -bench '^BenchmarkHotPaths$' -benchtime=5x -count=1 -benchmem ./internal/bench/) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree" && taskset -c "$cpu" env BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' \
      -bench '^BenchmarkSnapshotAssemblyInMemory$' -benchtime=1x -count=1 -benchmem ./internal/bench/) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree" && taskset -c "$cpu" env BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' \
      -bench '^BenchmarkJSONControl$' -benchtime=1x -count=1 -benchmem ./internal/bench/) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree" && taskset -c "$cpu" env BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' \
      -bench '^BenchmarkOpenPreparedDB$' -benchtime=1x -count=1 -benchmem ./internal/bench/) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree" && taskset -c "$cpu" env BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' \
      -bench '^BenchmarkStatusCLIPaths$' -benchtime=1x -count=10 -benchmem ./cmd/beacon/) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree" && taskset -c "$cpu" env BEACON_BENCH_CACHE=warm go test -vet=off -run '^$' \
      -bench '^BenchmarkConfigWatchReload$' -benchtime=2000x -count=1 -benchmem ./cmd/beacon/) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree" && taskset -c "$cpu" env BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' \
      -bench '^BenchmarkControlNoDaemon$' -benchtime=1x -count=1 -benchmem ./cmd/beacon/) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree" && taskset -c "$cpu" env BEACON_BENCH_CACHE=warm go test -vet=off -run '^$' \
      -bench '^BenchmarkTelegramNotify$' -benchtime=100x -count=1 -benchmem ./internal/infra/notify/) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree" && taskset -c "$cpu" go test ./internal/infra/workspace/ -run '^TestGitWorktreeRemoveLatencyAlternating$' -count=1 -v) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree" && taskset -c "$cpu" env BENCH_ITERATIONS=50 BENCH_WARMUP=0 node internal/bench/ui_bench.js) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree" && taskset -c "$cpu" env BENCH_TICKS=1001 BENCH_ROUNDS=5 node internal/bench/run_watcher_bench.js) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree" && taskset -c "$cpu" env BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' \
      -bench '^BenchmarkHTTPFramesEmission$' -benchtime=1x -count=1 -benchmem ./cmd/beacon/) \
      || echo "abort $side" >> "$dir/$prefix-status"
    (cd "$tree/ui/tui" && taskset -c "$cpu" env BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' \
      -bench '^BenchmarkGoTUIView$' -benchtime=1x -count=1 -benchmem ./) \
      || echo "abort $side" >> "$dir/$prefix-status"
  } 2>&1 | tee -a "$dir/$prefix-$side.txt"
}

run_side base "$dir/perf-base"
run_side head "$dir/perf-head"
run_side head "$dir/perf-head"
run_side base "$dir/perf-base"
run_side head "$dir/perf-head"
run_side base "$dir/perf-base"
run_side base "$dir/perf-base"
run_side head "$dir/perf-head"
run_side head "$dir/perf-head"
run_side base "$dir/perf-base"
