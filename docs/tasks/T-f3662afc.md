---
id: T-f3662afc
title: Extract guarded branch deletion helper
status: done
scope:
    - internal/infra/workspace/git_worktree.go
priority: 68
kind: task
require_review: llm
---

Goal: One deleteBranch helper for Remove and removeForce.

Evidence: final-04. Both paths build the same guarded branch delete and the same error, differing only in the git flag.

Steps: Extract deleteBranch(ctx, branch, force) and keep -d versus -D.

Check: Workspace tests pass.

Accept: cleanupRun in finalize.go and idle.go stays unchanged.
