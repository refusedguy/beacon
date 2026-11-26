---
id: T-9cd5e85c
title: Share SSE frame parser across modules
status: available
scope:
    - harnesses/opencode/sse.go
    - ui/tui/sse.go
    - pkg/core/sse_frame.go
    - pkg/core/sse_frame_test.go
priority: 80
kind: task
require_review: llm
---

Goal: One parser for the 30 matching lines in two readSSE copies.

Evidence: quality-04 item 1. Both use a 64 KB buffer, strip \r, split on colon and carry event and data. They differ at EOF, so the helper must preserve that decision as a parameter.

Steps: Put a parser in pkg/core and call it from both. Keep ui/tui importing no Beacon package issue in mind: if that breaches the module rule, add a tiny shared module instead.

Check: Both SSE suites pass with identical frames and EOF behaviour.
