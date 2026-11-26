---
id: T-d9db96f0
title: Stabilize plugin build hash memo key
status: done
scope:
    - .opencode/plugins/beacon-project/mcp/mcp.js
    - .opencode/plugins/beacon-project/mcp/mcp_test.js
priority: 90
kind: task
require_review: llm
---

Goal: beaconServerConfig reuses the rebuild memo instead of rewalking source trees on every config request.

Evidence: opt-js-ops.md shows beaconServerConfig builds a fresh effective deps object, which defeats the WeakMap key in beaconCommand.

Steps: Use a stable registrar-owned key for the memo. Keep fake fs deps in tests. Preserve the literal build inputs and rebuild behavior.

Check: A test with a 2000-file fake source and 500 config calls asserts zero repeated freshness walks and zero execFileSync calls. Plugin MCP tests pass.

Accept: No TUI peer packages added.

Open a PR, call beacon_watch_pr, do not self-merge.
