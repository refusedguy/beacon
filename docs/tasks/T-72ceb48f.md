---
id: T-72ceb48f
title: Reuse a buffer for SSE data lines
status: available
scope:
    - harnesses/opencode/sse.go
    - harnesses/opencode/sse_test.go
priority: 74
kind: task
require_review: llm
---

Goal: One frame append does not allocate per line.

Evidence: quality-09 item 5. frame.data grows with += at :134 and :136.

Steps: Use a strings.Builder reset per frame and keep the newline layout identical.

Check: SSE tests pass with identical event payloads.
