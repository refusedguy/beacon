---
id: T-624ffbf6
title: Unify transition logging
status: available
scope:
    - internal/app/decisionlog.go
    - internal/app/decisionlog_test.go
priority: 74
kind: task
require_review: llm
---

Goal: One emitTransition path handles all logged transitions.

Evidence: final-04. spawn, rebase and delivery-claim handlers hand-write the same slog.InfoContext plus Record pair.

Steps: Extend emitTransition with optional extra attrs and fields, then route the three siblings through it.

Check: decisionlog tests pass with exact Decision.Fields shapes, including spawn branch and rebase attempt and new_sha.

Accept: Log output stays semantically identical.
