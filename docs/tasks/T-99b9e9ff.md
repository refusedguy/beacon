---
id: T-99b9e9ff
title: Pin ACP RPC request and response routing
status: done
scope:
    - harnesses/acp/rpc_test.go
priority: 86
kind: task
require_review: llm
---

Goal: A response envelope with a method field is never consumed as a call response.

Evidence: final-08. rpc.go checks env.Method before matching response IDs. The only test reaches the collision incidentally through a hardcoded fake ID.

Steps: Add a direct test with a colliding agent request ID and a notification, assert p.answer and onNote receive them, and assert the pending call still resolves.

Check: Targeted ACP tests pass with five consecutive runs.

Accept: No production change needed unless the test finds a bug.
