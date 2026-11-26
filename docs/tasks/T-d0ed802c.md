---
id: T-d0ed802c
title: Centralize loopback control POST
status: available
scope:
    - cmd/beacon/commands.go
    - cmd/beacon/mcp.go
    - cmd/beacon/commands_test.go
depends:
    - T-707c9922
priority: 78
kind: task
require_review: llm
---

Goal: One transport helper for loopback control requests.

Evidence: final-06. postControlWithPR and remoteClient.call both marshal a control body, build the URL, set Content-Type and Bearer auth, run the client and reject status 300 or more.

Steps: Extract postControlJSON(ctx, info, body, client) and keep payload construction at each call site.

Check: CLI and MCP control tests pass with the same wire body.

Accept: Wire contract stays visible at the call site.
