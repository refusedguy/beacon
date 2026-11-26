---
id: T-07f6be5b
title: Replace linear journal touch with an indexed LRU
status: available
scope:
    - internal/infra/mcp/server.go
    - internal/infra/mcp/server_test.go
priority: 85
kind: task
require_review: llm
---

Goal: Duplicate operation claims must not scan and shift 128 entries under a global mutex.

Evidence: opt-mcp.md findings 2 and 3 show opJournal.touch is O(128) inside opJournal.mu.

Steps: Use a map from id to list element plus a bounded LRU list. Keep the same 128-entry limit, idempotency and error semantics.

Check: A benchmark with 128 prefilled ids and 4096 duplicate calls reports lower ns/op and zero errors; concurrency test with 32 goroutines passes -race.

Accept: No tool wire change.

Open a PR, call beacon_watch_pr, do not self-merge.
