---
id: T-ffd3f9b0
title: Split workspace Create lifecycle
status: available
scope:
    - internal/infra/workspace/git_worktree.go
priority: 68
kind: task
require_review: llm
---

Goal: Branch creation, worktree add, card rewrite and status checks stop sharing one 60-line method.

Evidence: quality-10 item 8. Create at :140 mixes lifecycle and sync operations.

Steps: Split into helpers inside the same file or sibling file. Keep the same error wrapping.

Check: Workspace tests pass.
