---
id: T-8d6bc5a1
title: Accept build agent as orchestrator role
status: done
scope:
    - internal/infra/mcp/registry.go
    - internal/infra/mcp/registry_test.go
priority: 1
kind: task
require_review: llm
status_reason: Delivered directly on main in commit 9ea89c46.
---

Goal: let the operator's `build` agent call orchestrator-only MCP tools after the typed permission fix.

Evidence: commit from T-4c1029ce made `requireOrchestrator` accept only `orchestrator` and refuse absent or worker identity with `CodePermissionDenied`. In this checkout `opencode.json` allows `beacon_beacon_task_create` and the other orchestrator actions for both `build` and `orchestrator`, and AGENTS.md calls `build` the operator's orchestration session. A `build` call arrives with metadata absent, so every orchestrator tool is refused even though the generated permissions allow it. The assistant cannot create cards from its own session.

Steps: add `build` to the accepted roles in `requireOrchestrator` alongside `orchestrator`; keep worker and absent metadata as coded refusals; add a table test for `build`, `orchestrator`, `worker` and absent identity; keep `opencode.json` as the permission source and do not widen worker permissions.

Accept: a call from the operator `build` agent reaches an orchestrator tool and succeeds when `opencode.json` allows it, while worker and absent identity still get `CodePermissionDenied`.
