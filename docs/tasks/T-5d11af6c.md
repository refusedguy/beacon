---
id: T-5d11af6c
title: '[perf] Planner allocation cut after dispatch merge'
status: available
scope:
    - internal/app/planner.go
    - internal/app/planner_test.go
    - internal/bench/planner_bench_test.go
perf: true
priority: 95
kind: task
require_review: llm
---

Goal: reduce per-candidate allocations in planner selection on top of the merged #2722 change.

Evidence: planner.go:209 matching allocates a map per candidate and builds out without preallocation. planner.go:291 leadingWildcard uses strings.Split. PR #2722 already removed claimDirKeys splits and merged with allocs 16465 to 11265, bytes 1229776 to 1094576.

Variants: V1 rewrite leadingWildcard with strings.IndexByte and ContainsAny, and apply the same treatment to any remaining claimDirKeys split. V2 replace the per-candidate map with a reusable epoch buffer and preallocated out slice using slices.Sort. V3 preallocate newActiveRunIndex and newActiveScopeIndex without changing index order.

CI commands: BenchmarkPlannerSelectInjectedChecker, BenchmarkPlannerSelectLargeDispatch. Limiter: map and slice allocations and core.ScopeDir checks.

Protocol: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. Open a PR, call beacon_watch_pr with your run ID, add the `perf` label, and do not add `ready-to-merge` until every variant is tried. One variant is one commit and one push. After each push wait for the `<!-- beacon-pr-perf -->` comment and check that work counts match, errors are zero, a limiter is named, ranges do not overlap and the head median is strictly better. Keep a winning variant and move to the next. Revert a losing or inconclusive variant with `git reset --hard HEAD~1` and `git push --force-with-lease`. Put a final table in the PR body with base median, head median, ranges, work, errors and limiter, then add `ready-to-merge`.
