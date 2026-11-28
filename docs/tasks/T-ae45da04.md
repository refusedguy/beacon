---
id: T-ae45da04
title: Make beacon_control batch replay-safe and convergent
status: available
scope:
    - internal/infra/mcp/tool_control.go
    - internal/infra/mcp/tool_control_test.go
    - internal/app/control.go
    - internal/app/control_test.go
    - cmd/beacon/commands.go
    - docs/tasks/T-7c7f30f9.md
priority: 1
kind: task
require_review: llm
---

Goal: make a batch control retry converge instead of turning already-applied runs into errors.
Evidence: controlMany at internal/infra/mcp/tool_control.go:105-113 calls applyControl once per id with no transaction. Cancel on an already closed run returns invalid_transition from Run.Apply at pkg/core/run.go:363-367, so a retry of a partially applied batch reports errors for the ids that were already cancelled. Unlike task mutations, control has no operation_id and never passes through mutationTool, so a declined call leaves no journal record and the retry always re-executes. The orchestrator looped single run_id calls and got one bare ok with no run id, which cannot say which runs were affected. Card T-7c7f30f9 still describes a stale symptom that commit 494e34ff already fixed, and cmd/beacon/commands.go:474-475 drops the daemon reason from a structured 409 into a bare status line.
Steps: add an optional operation_id to ControlArgs and wrap controlMany in mutationTool so a replay returns the stored per-id results without re-applying any cancel, unblock or retry; keep the existing per-id result shape and IsError on partial failure; make a repeat cancel of an already closed run return status ok with a reason such as already_closed instead of invalid_transition so a batch converges; preserve the daemon error text from a 409 in cmd/beacon/commands.go; update the stale text in docs/tasks/T-7c7f30f9 to match current main; add tests for replay after restart, partial batch retry and a repeated cancel.
Accept: the same cancel batch with the same operation_id returns identical results after a restart, and reissuing a partially applied batch does not flip succeeded ids to errors.
