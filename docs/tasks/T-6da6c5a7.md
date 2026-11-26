---
id: T-6da6c5a7
title: Resolve beacon command once per root
status: available
scope:
    - .opencode/plugins/beacon-project/mcp/mcp.js
    - .opencode/plugins/beacon-project/mcp/mcp_test.js
depends:
    - T-c03ccdeb
priority: 84
kind: task
require_review: llm
---

Goal: Stop rechecking BEACON_BIN, local binary, repo binary and PATH on every exec.

Evidence: dispatch-02. beaconCommand memoizes only the rebuild decision, not the resolved command path. The status and dispatch CLI calls both pay the check.

Steps: Memoize the resolved command per canonical root and keep the tests that force a rebuild or a path change.

Check: Ten sequential CLI calls perform one path resolution.

Accept: A changed BEACON_BIN or new local binary must invalidate the memo.
