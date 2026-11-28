---
id: T-d2aa9d37
title: Add worktree reclaim helper for initializing locks
status: available
scope:
    - internal/infra/workspace/git_worktree.go
    - internal/infra/workspace/git_worktree_test.go
priority: 1
kind: task
require_review: llm
---

Goal: a killed git worktree add must not block every future dispatch for that task.
Evidence: no Go code calls git worktree unlock. An interrupted add leaves a lock with reason initializing; clearStalePath at git_worktree.go:220-234, addWorktree at 241-251, Remove at 483-519 and ForceRemove at 536-543 all fail on it. addWorktree uses branch -B and removeForce uses branch -D, so a naive force remove loses unpushed commits.
Steps: add one private helper called before clearStalePath and addWorktree that unlocks only when the lock reason is initializing, checks HasChanges before removal and preserves unpushed commits, and is idempotent; add tests for Create after an initializing lock and for ForceRemove preserving a local commit.
Accept: Create converges after an interrupted add and never deletes a commit that is not on origin.
