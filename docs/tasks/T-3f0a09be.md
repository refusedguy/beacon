---
id: T-3f0a09be
title: Add SSE reconnect resume and force a read
status: done
scope:
    - ui/tui/sse.go
    - cmd/beacon/http.go
    - ui/tui/sse_test.go
depends:
    - T-1213ce6c
    - T-da7b1713
priority: 84
kind: task
require_review: llm
---

Goal: A reconnect does not silently lose state changes.

Evidence: final-15. readSSE parses frame id but streamOnce discards it and Stream never sends Last-Event-ID. The daemon writes no id field. A partial frame at EOF can emit a malformed event.

Steps: Send Last-Event-ID on reconnect and have the daemon emit monotonic ids. Until that works, force one runs read after every successful reconnect. Drop partial frames at EOF.

Check: A reconnect after a dropped frame recovers the missing event.

Accept: Wire frames keep their documented shape.
