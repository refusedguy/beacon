---
id: T-b71b84cc
title: Unify agent and session id resolution
status: done
scope:
    - .opencode/plugins/beacon-project/index.js
    - .opencode/plugins/beacon-project/rules/context.js
    - .opencode/plugins/beacon-project/state/session_identity.js
    - .opencode/plugins/beacon-project/shell_guard.js
depends:
    - T-28da250f
priority: 80
kind: task
require_review: llm
---

Goal: One resolver for agent identity and session id.

Evidence: quality-05 items 3, 4 and 10. index.js and rules/context.js repeat the same try/catch chain, four sites repeat sessionID ?? session.id ?? session.sessionID, and role constants are compared as raw strings.

Steps: Add sessionIDOf and one parameterized resolver in session_identity.js. Import role constants in shell_guard and context.

Check: Classification tests pass with the same agent ids.
