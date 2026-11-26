---
id: T-17afc03a
title: Unify workspace conflict marker
status: done
scope:
    - internal/infra/workspace/git_worktree.go
    - pkg/core/errors.go
    - internal/app/dispatcher.go
    - internal/app/dispatcher_test.go
depends:
    - T-f3662afc
priority: 70
kind: task
require_review: llm
---

Goal: One core.WorkspaceConflict helper for dirty worktree errors.

Evidence: final-10. One site returns a CodeConflict error and another returns the same text as an untyped fmt.Errorf. Dispatcher and MCP tests depend on that text.

Steps: Add the helper and call it from both sites.

Check: Dispatcher tests pass with the same code and message.

Accept: CodeOf callers keep working.
