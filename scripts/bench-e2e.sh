#!/usr/bin/env bash
# Reproduce the operator-path measurements in docs/research/perf-e2e.md.
# Pass --smoke for one bounded pass of both benchmarks with no timing gate.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

smoke=0
if [[ "${1:-}" == "--smoke" ]]; then
  smoke=1
  shift
fi

out="${1:-}"
if [[ -n "$out" ]]; then
  mkdir -p "$(dirname "$out")"
  : > "$out"
fi

run() {
  if [[ -n "$out" ]]; then
    "$@" 2>&1 | tee -a "$out"
  else
    "$@"
  fi
}

echo "Go toolchain: $(go version)" >&2

if (( smoke )); then
  # The harness runs a full warm-up pass in each benchmark unless this env
  # override is set. That pass is redundant in a smoke check and is what the
  # 30 s CI bound measured. Pin P to the same value the harness uses inside
  # the timed loop, so a busy runner cannot change which paths are sampled.
  export BEACON_BENCH_SMOKE=1
  export GOMAXPROCS=1
  run go test -run '^$' -bench 'BenchmarkHotPaths|BenchmarkE2EOperatorPaths' -benchtime=1x -count=1 ./internal/bench/
  exit 0
fi

# The first benchmark runs five alternating pairs: reconcile first on even
# pairs, dispatch first on odd pairs. The second is the micro baseline from
# docs/perf/dossier.md, captured here for context.
run go test -run '^$' -bench '^BenchmarkE2EOperatorPaths$' -benchtime=5x -count=1 ./internal/bench/
run go test -run '^$' -bench '^BenchmarkHotPaths$' -benchtime=1x -count=1 ./internal/bench/
