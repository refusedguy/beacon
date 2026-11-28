---
id: T-ef17cf6f
title: Retry pending cleanup on operation journal replay
status: available
scope:
    - internal/infra/mcp/server.go
    - internal/infra/mcp/server_test.go
priority: 1
kind: task
require_review: llm
---

Goal: replaying a mutation must retry side work that was only logged as failed, without reapplying the committed mutation.
Evidence: mutationTool at internal/infra/mcp/server.go:317-328 returns a stored result before running the function. Cancel saves closed then stops the session and removes the worktree through bestEffort; a failure is logged and the id is still reported ok. finishMutation stores rollback errors and replay returns them without another rollback. task_close no-op paths skip SQLite graph sync.
Steps: add a recovery callback to the journal outcome that runs after lookup finds a stored result and before returning it; retry Cancel session stop and workspace cleanup for ids with pending cleanup and retry rollback plus graph restore for failed task mutations; never reapply the committed state mutation; add tests for cancel replay completing cleanup and task mutation replay repairing a failed rollback.
Accept: a replayed operation converges the workspace and graph without repeating the committed change.
