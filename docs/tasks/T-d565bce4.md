---
id: T-d565bce4
title: 'P2: Cut planner scope-key allocations'
status: available
scope:
    - internal/app/planner.go
    - internal/app/planner_test.go
priority: 17
kind: task
require_review: llm
---

Goal: cut roughly 54 percent of planner-select allocations by removing prefixed scope-dir keys.
Evidence: allocs/op baseline 7170 and B/op 814048 from run 46006320501. claimDirKeys at planner.go:301 builds d:+cleanPath for every key, about 3900 of 7170 allocs. Maps in newActiveScopeIndex at planner.go:188-196 grow value slices one element at a time, about 3000 more allocs.
Variants, one commit each: V1 key exact, under and cover maps by bare core.ScopePath(value[:end]) in both newActiveScopeIndex and matching, updating TestClaimDirKeys and the buffer-reuse test together. V2 add a counting pass and capacity hints to the map-value slices while preserving ascending run order. V3 hoist Kind and Value in matching and drop the redundant default-branch cover loop only after proving under is a superset of cover.
Deciding row planner-select-injected-checker, gain (allocs/bytes) at 5 percent with allocs around 360 and B/op around 40 KB. Work in your own worktree off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. Open a PR with the perf label at creation using `gh pr create --label perf` when the card is a perf card, otherwise no perf label. Call beacon_watch_pr. One variant is one commit and one push. Read the beacon-pr-perf comment after each push. Keep a variant only on gain, including gain (allocs/bytes) and gain (calls reduced), with equal work, errs zero, non-empty limiter and warm cache. Revert a losing variant with git reset --hard HEAD~1 and git push --force-with-lease. Never add ready-to-merge before all variants are checked and the final table is in the PR body. CI supplies every check.
