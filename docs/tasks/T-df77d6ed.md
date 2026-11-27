---
id: T-df77d6ed
title: 'P5: Populate HotPaths allocation columns and snapshot assembly row'
status: available
scope:
  - internal/bench/report.go
  - internal/bench/bench_test.go
  - internal/bench/registry.go
  - internal/bench/registry_test.go
  - docs/perf/bench-registry.json
  - .github/workflows/ci.yml
priority: 14
kind: task
require_review: llm
depends:
  - T-d6e1dcca
---

Goal: make B/op and allocs/op visible for the ten HotPaths keys so allocation-only wins can be judged.
Evidence: printReport writes the human report inside BenchmarkHotPaths before the testing result line, so benchstat drops that benchmark from compare.csv and all ten allocation columns show -. The raw log has 18490738 B/op and allocs/op per pass. The parser fix in c65996e2 handled per-config CSV tables but did not create a HotPaths row.
Steps: buffer the report and print it after the benchmark function returns, or emit it to stderr, so the result line stays clean; add a golden CSV test for readCompareCSV, benchNameMatches and metricOf; verify that snapshot, card-parse and the other nine keys show real B/op and allocs/op on PR #2786. Also add a canonical row for BenchmarkSnapshotAssemblyInMemory with Repo set and blocked decisions so status.go and metrics.go variants can be measured, registering its key and adding it to run_side.
This card owns ci.yml while it runs. No perf label; mark-ready handles merge. Work in your own worktree off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. Open a PR with the perf label at creation using `gh pr create --label perf` when the card is a perf card, otherwise no perf label. Call beacon_watch_pr. One variant is one commit and one push. Read the beacon-pr-perf comment after each push. Keep a variant only on gain, including gain (allocs/bytes) and gain (calls reduced), with equal work, errs zero, non-empty limiter and warm cache. Revert a losing variant with git reset --hard HEAD~1 and git push --force-with-lease. Never add ready-to-merge before all variants are checked and the final table is in the PR body. CI supplies every check.
