---
id: T-a4246bfd
title: 'P7: Reconcile and sync write cuts'
status: available
scope:
    - internal/app/reconciler.go
    - internal/infra/sqlite/tasks.go
    - internal/app/reconciler_test.go
depends:
  - T-df77d6ed
priority: 15
kind: task
require_review: llm
---

Goal: cut redundant writes in reconcile and task sync.
Evidence: reconciler.go:120 saves unconditionally before sameObservation guards, so an unchanged tick still rewrites a seven-index row; syncFingerprint at tasks.go:59-61 marshals every card on each SyncTasks and SetTaskStatus clears the cache even when zero rows changed; syncCaches is keyed by *sql.DB so daemon and CLI hold independent fingerprints.
Variants, one commit each: V1 move the early Save after NoteObservation and skip it when head, base and PR number are unchanged; V2 skip fingerprint invalidation when RowsAffected is zero; V3 key the cache by database file path and clear all handles on SetTaskStatus.
Deciding row reconcile-tick-counts allocs/op and B/op after P1 and P4 wire the columns.

Work in your own worktree off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. One variant is one commit and one push, read the beacon-pr-perf comment after each push, keep only gain including gain (allocs/bytes) and gain (calls reduced) with equal work, errs zero, non-empty limiter and warm cache, revert with git reset --hard HEAD~1 and git push --force-with-lease. Add perf label at PR creation with gh pr create --label perf. Never add ready-to-merge before all variants are checked and the final table is in the PR body. Call beacon_watch_pr. CI supplies every check.
