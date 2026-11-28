---
id: T-bd62e91b
title: Add orchestrator beacon_task_close with audited status and reason
status: done
scope:
    - internal/app/taskclose.go
    - internal/app/taskclose_test.go
    - internal/infra/mcp/tool_task_close.go
    - internal/infra/mcp/tool_task_close_test.go
    - internal/infra/mcp/server.go
    - internal/infra/mcp/registry.go
    - opencode.json
    - AGENTS.md
priority: 1
kind: task
require_review: llm
---

Goal: let the operator close a task card with an explicit status and reason in one audited commit, so a cancelled or flat perf run is not dispatched again.
Evidence: PR 2861 was cancelled and closed after every advisory row came back flat, but card T-b8fc3701 stays status available because beacon_task_update rejects status at internal/infra/mcp/server.go:502-514 and Control.Cancel never calls SetTaskStatus (internal/app/control.go:239-270). Automation will dispatch the card again. The shell guard blocks a direct push to main, so the operator has no sanctioned path. core.TaskDropped already exists at pkg/core/task.go:153-154, so no new status value is needed. The repo has the transaction and publish path in internal/app/taskdelete.go:59-202, frontmatter rewriting in internal/app/taskupdate.go:224-267, refusal for cards with an active run in activeTaskStates, and the durable operation journal in internal/infra/mcp/server.go:363-418 that makes a mutation idempotent.
Steps: add an orchestrator-only beacon_task_close tool with input {updates:[{id,status:done|dropped,reason}],operation_id}. Validate status against core.TaskDone and core.TaskDropped only and require a non-empty reason. Write status into frontmatter and store the reason as a status_reason key, which the parser already ignores because KnownFields is off. Use loadGraph to update the SQLite status so the card leaves ListAvailable, refuse the batch when any id has an active run, and publish one commit through finishMutation. Return unchanged when the card already has the target status so a retry converges. Register the tool in ToolCatalog with OrchestratorOnly true and requireOrchestrator in the handler, wrap it in mutationTool, add tests for done, dropped, active run refusal, invalid status, missing reason, and replay after restart. Run go generate ./... for opencode.json permissions and update AGENTS.md with the tool signature.
Accept: after closing a card the dispatcher never picks it up again, a retry with the same operation_id returns the stored result without a second commit, and a worker cannot call the tool.
