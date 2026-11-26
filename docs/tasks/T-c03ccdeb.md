---
id: T-c03ccdeb
title: Remove synchronous binary rebuild from click path
status: in_progress
scope:
    - .opencode/plugins/beacon-project/mcp/mcp.js
    - .opencode/plugins/beacon-project/mcp/mcp_test.js
    - .opencode/plugins/beacon-project/portability_test.js
priority: 96
kind: task
require_review: llm
---

Goal: A stale local Go binary never blocks the TUI event loop on a click.

Evidence: dispatch-02 and dispatch-08. rebuildLocalIfStale can call execFileSync('bash', [scripts/build-beacon.sh]) during beaconCommand resolution, which sits on the Dispatch path. The contract says generation and builds happen at development time, never at startup or per call.

Steps: Make the freshness check non-blocking or remove it from the runtime resolver and report staleness instead. Keep the explicit build script and the operator instruction to rebuild after Go changes.

Check: A source newer than the binary does not delay a dispatch call.

Accept: No auto rebuild at runtime.
