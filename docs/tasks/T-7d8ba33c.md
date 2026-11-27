---
id: T-7d8ba33c
title: 'W1R-3: wire reconcile and dispatch benchmarks into advisory'
status: done
scope:
    - internal/bench/reconcile_bench_test.go
    - internal/bench/prompt_bench_test.go
    - internal/bench/planner_bench_test.go
    - internal/bench/registry.go
    - internal/bench/registry_test.go
    - docs/perf/bench-registry.json
    - .github/workflows/ci.yml
priority: 31
kind: task
require_review: llm
---

Goal: make reconcile and dispatch changes measurable in the advisory comparison.

Evidence: advisory run_side calls BenchmarkReconcileTickCounts, planner and prompt benchmarks, but none of those benchmarks emit a beacon-perf canonical row. Their keys are absent from docs/perf/bench-registry.json. A perf PR that changes internal/app/reconciler.go or internal/app/dispatcher.go therefore gets no deciding row.

Steps: add canonical eight-field rows with fixed work and errs to the reconcile, planner and prompt benchmarks in internal/bench. Register the new keys in internal/bench/registry.go with placement pr and fixed limiter strings, regenerate the JSON, and extend the cross-check. In .github/workflows/ci.yml extend the harness copy list and warmup for any new files, but do not change verdict or noise logic. This is a measurement-only card, no production optimization.

Accept: a no-change PR shows the new keys with equal work, zero errors and warm cache. The PR must NOT carry the perf label so mark-ready sets ready-to-merge. No local execution.
Protocol: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters or benchmarks locally. Open a PR and call beacon_watch_pr. CI supplies every check.
