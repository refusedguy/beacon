---
id: T-267fffec
title: Flatten failed-run matching in dispatch summary
status: available
scope:
    - internal/infra/mcp/tool_dispatch.go
    - internal/infra/mcp/tool_dispatch_test.go
priority: 80
kind: task
require_review: llm
---

Goal: Dispatch summary must not compare every failure against every recent run.

Evidence: opt-mcp.md finding 4 shows failedRuns does F×R comparisons with F=50.

Steps: Index recent runs by run ID once and look up failures. Preserve summary order and fields.

Check: A stub store with 200 runs and 50 failures reports bounded lookups and identical JSON with zero errors.

Accept: No tool wire change.

Open a PR, call beacon_watch_pr, do not self-merge.
