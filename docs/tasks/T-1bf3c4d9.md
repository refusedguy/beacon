---
id: T-1bf3c4d9
title: 'P10: Dispatcher launch and sync cuts'
status: dropped
scope:
    - internal/app/dispatcher.go
    - internal/app/dispatcher_test.go
depends:
    - T-d6e1dcca
    - T-d565bce4
priority: 12
kind: task
require_review: llm
status_reason: "PR 2880 closed: advisory flat on deciding dispatch row and root app -race blocked by pre-existing mocks race"
---

Goal: cut launch serialization and claimSlots reads once counters are wired.
Evidence: dispatcher.go:358-406 starts reserved agent slots one at a time; claimSlots at :336-356 lists active runs once per failed start under slotsMu; startOne writes card status twice through MarkInWorkspace and SetTaskStatus; the fingerprint cache is invalidated by each claim.
Variants, one commit each: V1 move claimSlots out of the failure loop and run one listing after the wave; V2 parallelize startOne for reserved slots with errgroup limited by free agent slots, protecting out and the ensured and modelRefs maps, keep only with green CI -race; V3 drop the redundant status write when SQLite is authoritative.
Deciding row dispatch-prompt for timing plus the dispatch counters after P4 wires them.

Work in your own worktree off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. One variant is one commit and one push, read the beacon-pr-perf comment after each push, keep only gain including gain (allocs/bytes) and gain (calls reduced) with equal work, errs zero, non-empty limiter and warm cache, revert with git reset --hard HEAD~1 and git push --force-with-lease. Add perf label at PR creation with gh pr create --label perf. Never add ready-to-merge before all variants are checked and the final table is in the PR body. Call beacon_watch_pr. CI supplies every check.
