---
id: T-1dccaa57
title: Benchmark planner and reconcile dedup
status: available
scope:
    - internal/bench/e2e_bench_test.go
    - internal/bench/planner_bench_test.go
priority: 84
kind: task
require_review: llm
---

Goal: Exercise the optimized branches that current fixtures miss.

Evidence: quality-12 items 2 and 4. E2E scopes never overlap, so activeConflict is not called, and the SCM stub returns a fresh SHA so sameObservation never skips writes.

Steps: Add Select with 1000 active and 200 candidates both overlapping and not, and a reconcile tick over 20 PRs with unchanged observations. Report skipped counts and writes.

Check: Benchmarks fail if the injected checker or SHA guard stops being used.
