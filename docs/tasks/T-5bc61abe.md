---
id: T-5bc61abe
title: Remove redundant post-spawn store writes
status: done
scope:
    - internal/app/dispatcher.go
    - internal/app/dispatcher_test.go
depends:
    - T-0f1f6228
priority: 78
kind: task
require_review: llm
---

Goal: Stop saving and claiming the same run twice.

Evidence: dispatch-03 and dispatch-04. Save runs at :191 and :339; MarkInWorkspace sets in_progress and SetTaskStatus sets claimed at the end. The comment already says SQLite is authoritative, so the second status write is redundant.

Steps: Measure first, then batch the session ID write with the final status write if failure semantics allow. Keep the active-run guard and the claim ordering.

Check: A dispatched run ends in the same state with one fewer write and five alternating runs show non-overlapping counts.

Accept: If the duplicate write protects a failure path, close with the evidence instead of removing it.
