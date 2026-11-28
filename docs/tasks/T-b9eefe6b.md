---
id: T-b9eefe6b
title: Block direct state db reads for every agent
status: done
scope:
    - .opencode/plugins/beacon-project/shell_guard.js
    - .opencode/plugins/beacon-project/index_test.js
priority: 1
kind: task
require_review: llm
status_reason: "Delivered in PR #2916."
---

Goal: no agent can read .beacon/state.db directly; the only state view becomes a tool result with generated_at.
Evidence: guardAppliesToAgent applies shell guard rules only to workers, so the orchestrator bypassed them and queried sqlite by hand repeatedly. Those raw rows became the basis for guessed claims.
Steps: add a role-independent shell guard rule matching .beacon/state.db with replacement beacon status --snapshot or beacon report; assert in index_test.js that a sqlite command on state.db is blocked for orchestrator events and that beacon status --snapshot stays allowed.
Accept: a direct state.db read is rejected at the tool boundary for every agent.
