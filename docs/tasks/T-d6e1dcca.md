---
id: T-d6e1dcca
title: 'P4: Wire real base-red counters into advisory'
status: available
scope:
  - internal/bench/reconcile_bench_test.go
  - internal/bench/registry.go
  - internal/bench/registry_test.go
  - docs/perf/bench-registry.json
priority: 15
kind: task
require_review: llm
depends:
  - T-0f2cdb4d
---

Goal: make the base-red call reductions measurable so a reconcile optimization can earn gain (calls reduced).
Evidence: BenchmarkBaseRedCheck and BenchmarkBaseRedBlockFanout publish prefixed metrics but no canonical row. Live code produces lists 2 and observes 26 per tick in the block fixture; a safe change can drop fanout observes from 25 to 1 by reusing persisted other.Checks. ci.yml:973 accepts falling counters only for reconcile- keys and only lists, observes and gets per tick, and maps them to ReconcileTickCounts which has red=false.
Steps: register canonical keys base-red-check and base-red-block with fixed limiter strings and work and err metrics, regenerate the JSON and extend the cross-check. Map them in allocBench and the counterFalls rule so their own lists-per-tick and observes-per-tick drive the verdict. Add a saves-per-tick counter to countStore and publish it as part of the same report. Keep reconcile-tick-counts as a no-red regression guard.
This card owns ci.yml while it runs. No production reconcile changes in this card. No perf label; mark-ready handles merge. Work in your own worktree off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. Open a PR with the perf label at creation using `gh pr create --label perf` when the card is a perf card, otherwise no perf label. Call beacon_watch_pr. One variant is one commit and one push. Read the beacon-pr-perf comment after each push. Keep a variant only on gain, including gain (allocs/bytes) and gain (calls reduced), with equal work, errs zero, non-empty limiter and warm cache. Revert a losing variant with git reset --hard HEAD~1 and git push --force-with-lease. Never add ready-to-merge before all variants are checked and the final table is in the PR body. CI supplies every check.
