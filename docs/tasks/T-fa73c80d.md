---
id: T-fa73c80d
title: Cut per-card dispatch git and model calls
status: done
scope:
    - cmd/beacon/main.go
    - cmd/beacon/config_watch.go
    - internal/app/dispatcher.go
    - internal/infra/workspace/git_worktree.go
    - internal/app/dispatcher_test.go
    - internal/infra/workspace/git_worktree_test.go
priority: 90
kind: task
require_review: llm
---

Goal: Dispatch startup must not repeat model lookup or git probes per card.

Evidence: cicd-07.md shows per-task model GET, RefreshWorktrees on the success path, worktree prune and show-ref per card, and a recursive plugin tree walk.

Steps: Cache the resolved model per parent session and config generation. Move RefreshWorktrees to batch end or removal. Skip prune when the path is absent and use worktree add -B for the unique branch. Replace the tree walk with one build stamp.

Check: A counting fake records at most four git commands for Create on a clean path and one model lookup for a two-card dispatch. Standard-library tests pass.

Accept: No dispatch behavior or wire change.

Open a PR, call beacon_watch_pr, do not self-merge.
