---
id: T-c2c020fc
title: 'P17: Wire CLI benchmarks into advisory'
status: available
scope:
    - cmd/beacon/status_cli_bench_test.go
    - cmd/beacon/config_watch_bench_test.go
    - cmd/beacon/control_dispatch_bench_test.go
    - internal/bench/registry.go
    - internal/bench/registry_test.go
    - docs/perf/bench-registry.json
    - .github/workflows/ci.yml
depends:
    - T-df77d6ed
priority: 6
kind: task
require_review: llm
---

Goal: price the CLI-only hot paths that sit outside BenchmarkSQLiteSnapshot.
Evidence: BenchmarkOpenPreparedDB, BenchmarkStatusCLIPaths, BenchmarkConfigWatchReload and BenchmarkControlNoDaemon emit ReportMetric but no beacon-perf row, have no registry key and are absent from run_side. BenchmarkStatusCLIPaths is the only one driving the real cobra command end to end.
Steps: add canonical rows and registry keys for the four benchmarks, regenerate the JSON, copy their test files into both trees and add them to hash_tree, and add run_side commands with the same taskset and env wrapper.
This is measurement only, no perf label. It owns ci.yml while it runs, after P5.

Work in your own worktree off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. One variant is one commit and one push, read the beacon-pr-perf comment after each push, keep only gain including gain (allocs/bytes) and gain (calls reduced) with equal work, errs zero, non-empty limiter and warm cache, revert with git reset --hard HEAD~1 and git push --force-with-lease. Add perf label at PR creation with gh pr create --label perf. Never add ready-to-merge before all variants are checked and the final table is in the PR body. Call beacon_watch_pr. CI supplies every check.
