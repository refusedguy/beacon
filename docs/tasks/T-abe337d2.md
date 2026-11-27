---
id: T-abe337d2
title: Fix worktree branch collision and cleanup gaps
status: available
scope:
    - internal/infra/workspace/git_worktree.go
    - internal/infra/workspace/git_worktree_test.go
priority: 3
kind: task
require_review: llm
---

Goal: close worktree lifecycle gaps found in the edge-case audit, without changing performance paths.
Evidence: unsafeBranchChars at git_worktree.go:55 maps T-a/b and T-a-b to the same branch and worktree path, and GreedyPlanner compares task identity not branch names, so two cards can collide; the second Create runs git worktree add -B on a branch already checked out and handleStartFailure can remove the first run's worktree. ForceRemove at :477-482 deletes the branch only when git worktree remove --force succeeds, so an already-missing directory leaves a stale local branch that beacon gc cannot remove. cleanupRun logs a missing-path error and returns nil at finalize.go:409-413, leaving a store row naming a dead path.
Steps: detect branch and worktree path collisions before Create and fail with a clear WorkspaceConflict naming both task IDs; make ForceRemove attempt branch deletion even when the worktree is already gone; add a test for colliding IDs and one for a missing directory with a live branch. Keep the existing commandHook seam and all current git call counts on the clean paths.
Accept: a colliding ID cannot silently remove another run's worktree, and a manually deleted worktree leaves no stale branch after gc. Work in your own worktree off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. Open a PR without the perf label so mark-ready sets ready-to-merge. Call beacon_watch_pr. CI supplies every check.
