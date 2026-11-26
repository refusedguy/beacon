---
id: T-789fc3ef
title: Trim tautological JS tests
status: available
scope:
    - .opencode/plugins/beacon-project/ui/ui_exports_test.js
    - .opencode/agents/issue-scout_test.js
    - .opencode/plugins/beacon-project/ui/ui_nodes_test.js
    - .opencode/plugins/beacon-project/mcp/mcp_cache_test.js
    - .opencode/plugins/beacon-project/mcp/mcp_test.js
    - .opencode/plugins/beacon-project/rules/context_test.js
priority: 72
kind: task
require_review: llm
---

Goal: Remove duplicate and tautological JS test assertions.

Evidence: final-03 and final-07. ui_exports_test has a truthy namespace assertion, issue-scout_test repeats a deny rule, ui_nodes_test only asserts doesNotThrow, mcp_cache_test duplicates mcp_test cases, mcp_test counts readdir and stat calls, and context_test scans the same constant twice.

Steps: Merge tautologies into the tests that already assert the outcome. Drop duplicate cases and call-count assertions.

Check: Plugin UI tests and agent rule tests pass.

Accept: No behaviour coverage is lost.
