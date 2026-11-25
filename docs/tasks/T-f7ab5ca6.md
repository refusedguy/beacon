---
id: T-f7ab5ca6
title: Unify plugin agent resolution and classification
status: available
scope:
    - .opencode/plugins/beacon-project/rules/context.js
    - .opencode/plugins/beacon-project/rules/context_test.js
    - .opencode/plugins/beacon-project/shell_guard.js
    - .opencode/plugins/beacon-project/state/session_identity.js
    - .opencode/plugins/beacon-project/state/session_identity_test.js
priority: 85
kind: task
require_review: llm
---

Goal: Let one function decide worker or orchestrator for rules, guard and MCP.

Evidence: roleFor matches worker as a substring while isWorkerAgent requires an exact name, and three files resolve the session agent differently.

Scope: rules/context.js, shell_guard.js, state/session_identity.js and their tests.

Steps: Extract one resolver. Route rules, guard and MCP through it. Add a table test for worker, build, orchestrator, networker and issue-scout.

Accept: All five names classify identically, and node --test .opencode/plugins/beacon-project passes.

Open a PR, call beacon_watch_pr, do not self-merge.
