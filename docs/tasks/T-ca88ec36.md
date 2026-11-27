---
id: T-ca88ec36
title: 'A4: Task sync and dispatcher launch cost'
status: done
scope:
    - internal/infra/sqlite/tasks.go
    - internal/app/dispatcher.go
    - internal/app/dispatcher_test.go
priority: 13
kind: task
require_review: llm
---

Goal: cut repeated task writes and sequential agent launch.
Evidence: dispatch-prompt about 0.11 ms with a 25% capped floor. An earlier version showed 0.228 to 0.153 ms but was rejected by the old spread rule. New rules accept gain (calls reduced) and gain (allocs/bytes).
Variants, one commit each: V1 replace both fmt.Fprintf calls in spawnPrompt with b.WriteString. V2 move ensureTaskColumns into openDB and use tx.PrepareContext for inserts into tasks and task_deps. V3 cache the last SyncTasks fingerprint in Store, reset on SetTaskStatus and transaction error, and skip DELETE plus INSERT when cards are unchanged. V4 parallelize startOne for reserved agent slots with errgroup, protecting out, batch.ensured and batch.modelRefs; keep only with green CI -race.
Deciding row dispatch-prompt. ДISCIPLINE: never run go test, go build, go vet, linters, node tests or benchmarks locally. One variant is exactly one commit and one push. After each push wait for the <!-- beacon-pr-perf --> comment. Keep the variant only if the deciding row reports gain, including gain (allocs/bytes) or gain (calls reduced), with equal work, errs zero, non-empty limiter and cache warm. Otherwise run git reset --hard HEAD~1 && git push --force-with-lease. Add the perf label immediately and never add ready-to-merge until every variant is checked and the final table is in the PR body. Work in your own worktree off current main, never push to main, open a PR and call beacon_watch_pr.
