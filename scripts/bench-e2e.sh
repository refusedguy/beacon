#!/usr/bin/env bash
# Reproduce the operator-path measurements in docs/research/perf-e2e.md.
#
# Modes:
#   --smoke  one bounded pass of the two PR benchmarks with no timing gate
#   --full   the ten heavy plans on the nightly perf runner or manual dispatch
#   default  the two alternating benchmark pairs from the dossier
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

smoke=0
full=0
if [[ "${1:-}" == "--smoke" ]]; then
  smoke=1
  shift
elif [[ "${1:-}" == "--full" ]]; then
  full=1
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

if (( full )); then
  # Ten plans stay off the PR path. SQLiteSnapshot, FullSnapshot and the
  # SnapshotApproaches fixture are minutes long and noisy on shared hosts.
  # The two Node harnesses and the 1001-tick watcher test use their full
  # defaults here. The smoke overrides never apply to this mode.
  run go test -run '^$' -bench 'BenchmarkSQLiteSnapshot|BenchmarkSnapshotApproaches|BenchmarkFullSnapshot|BenchmarkHotPaths|BenchmarkE2EOperatorPaths|BenchmarkPlannerSelectInjectedChecker|BenchmarkSpawnPrompt' -benchtime=1x -count=1 ./internal/bench/
  run node docs/perf/plugin_grouping_bench.mjs
  run node internal/bench/ui_bench.js
  run node --import ./.opencode/plugins/beacon-project/test_support/register_plugin_doubles.js --test .opencode/plugins/beacon-project/run_watcher_test.js

  # Worktree latency proxy. Beacon's workspace uses git worktree add/remove on
  # every dispatch; this measures that cost on an isolated throwaway repo so a
  # full pass never mutates the checkout under test. Five bounded cycles.
  tmp="$(mktemp -d)"
  git init -q "$tmp/repo"
  git -C "$tmp/repo" -c user.email=bench@example.invalid -c user.name=bench \
    commit -q --allow-empty -m init
  for i in 1 2 3 4 5; do
    start_ns="$(date +%s%N)"
    git -C "$tmp/repo" worktree add -q --detach "$tmp/wt-$i" HEAD
    end_ns="$(date +%s%N)"
    printf 'worktree add: %d ms\n' "$(( (end_ns - start_ns) / 1000000 ))"
    git -C "$tmp/repo" worktree remove --force "$tmp/wt-$i"
  done
  rm -rf "$tmp"
  exit 0
fi

if (( smoke )); then
  # The harness runs a full warm-up pass in each benchmark unless this env
  # override is set. That pass is redundant in a smoke check. Do not set
  # GOMAXPROCS here: it serializes the go test compile, and the benchmarks
  # already pin P to one inside the timed loop.
  export BEACON_BENCH_SMOKE=1
  # -vet=off skips vet on this package. The CI vet job already covers it.
  run go test -vet=off -run '^$' -bench 'BenchmarkHotPaths|BenchmarkE2EOperatorPaths' -benchtime=1x -count=1 ./internal/bench/
  exit 0
fi

# One invocation compiles the bench package once for both benchmarks. The
# E2E benchmark runs five alternating pairs: reconcile first on even pairs,
# dispatch first on odd pairs. HotPaths is the micro baseline from
# docs/perf/dossier.md, captured here for context.
run go test -run '^$' -bench 'BenchmarkHotPaths|BenchmarkE2EOperatorPaths' -benchtime=5x -count=1 ./internal/bench/
