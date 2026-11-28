---
id: T-e1d8bdce
title: Validate task dependency cycles and @N references
status: done
scope:
    - internal/infra/sqlite/tasks.go
    - internal/app/taskcreate_batch.go
    - internal/app/taskcreate.go
    - internal/app/taskid.go
    - internal/app/idempotency.go
    - pkg/core/task_rules.go
priority: 2
kind: task
require_review: llm
---

Goal: reject impossible dependency graphs at ingest instead of leaving cards silently blocked.
Evidence: readyTasksQuery computes readiness from direct unresolved dependencies only, so a two-node cycle, a self-dependency and a chain past depth 64 all leave the queue showing waiting_deps with no diagnostic. A cycle walks the recursive CTE to depth 64 producing unused rows. writeSyncTask stores (A,A) and prunes nothing. @N is validated only in in.Depends, not in body text, plan child Depends or single-card Create; @0 and @01 escape the localRefPattern and become dangling IDs. epicTaskID and batchSeed hash child titles only, so editing a child's Depends on a repeat create returns the existing cards and drops the change.
Steps: add a DFS cycle and self-dependency check in SyncTasks that returns CodeInvalidInput naming one cycle before any row is written; extend resolveBatchInputs to scan in.Body and every plan child Depends with localRefIndex and rewrite accepted @N to ids[n-1] with the n <= i rule; apply the same validation to single-card Create; include child dependency lists in epicTaskID, idempotencyKey and batchSeed hashes; replace the recursive readyTasksQuery with an equivalent one-hop NOT EXISTS query to remove the unused depth work. Do not change which tasks are ready.
Accept: a cycle or self-dependency fails at create or sync with a named ID, and @N in body or plan resolves the same way as in depends.
