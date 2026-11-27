---
id: T-7bbbfe38
title: 'A1: SQLite VM, memory schema and decode cost'
status: available
scope:
    - internal/infra/sqlite/schema.go
    - internal/infra/sqlite/store.go
    - internal/infra/sqlite/store_test.go
    - internal/infra/sqlite/schema_test.go
priority: 18
kind: task
require_review: llm
---

Goal: cut SQLite VM and decode cost in the snapshot path.
Evidence: snapshot about 20 ms, sqlite-snapshot about 5 ms, floors 6.3% and 11.3% from the latest bench-noise-floor. Deciding rows snapshot and sqlite-snapshot.
Variants, one commit each:
V1 in dsn add _pragma=temp_store(MEMORY), _pragma=cache_size(-16000), _pragma=mmap_size(67108864).
V2 in openDB add a PRAGMA user_version fast path that skips repeated DDL, ensureRunColumns, backfillRunEvents, dropRedundantRunIndexes, ensureSnapshotIndexes and ensureDecisionSummary when the version is current; keep full migration for version 0.
V3 remove ensureTaskColumns from ListQueueTasks in openDB, filter depsQuery with JOIN tasks q ON q.id = d.task_id WHERE q.status IN (...), and replace SELECT COUNT(*) in Approved with SELECT 1 LIMIT 1.
V4 in decodeJSON skip json.Unmarshal for empty strings, [] and {}; in prepareSaveRow return []byte("[]") for empty Scope or Checks; preallocate collectRowsCap capacity from len(runIDs) in ListRunEvents and ListDecisions; preallocate inQuery args.
ДISCIPLINE: never run go test, go build, go vet, linters, node tests or benchmarks locally. One variant is exactly one commit and one push. After each push wait for the <!-- beacon-pr-perf --> comment. Keep the variant only if the deciding row reports gain, including gain (allocs/bytes) or gain (calls reduced), with equal work, errs zero, non-empty limiter and cache warm. Otherwise run git reset --hard HEAD~1 && git push --force-with-lease. Add the perf label immediately. NEVER add ready-to-merge until every variant is checked and the final table is in the PR body. Work in your own worktree off current main, never push to main, open a PR and call beacon_watch_pr.
