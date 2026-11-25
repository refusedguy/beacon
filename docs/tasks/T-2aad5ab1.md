---
id: T-2aad5ab1
title: Collapse decision log wrappers
status: available
scope:
    - internal/app/decisionlog.go
    - internal/app/decisionlog_test.go
priority: 80
kind: task
require_review: llm
---

Goal: Replace four near-identical decision transition wrappers with one emit helper.

Evidence: Four logTransition* wrappers repeat the slog.InfoContext and Record sequence.

Scope: internal/app/decisionlog.go, internal/app/decisionlog_test.go.

Steps: Add one emit helper and route all four transitions through it. Keep the Decision fields unchanged.

Accept: Tests for watch_pr, wake, merge and parent_notify pass, and go test ./internal/app -run Decision passes.

Open a PR, call beacon_watch_pr, do not self-merge.
