---
id: T-cda93d9d
title: Unify TUI request plumbing
status: done
scope:
    - ui/tui/api.go
    - ui/tui/sse.go
    - ui/tui/api_test.go
    - ui/tui/sse_test.go
priority: 90
kind: task
require_review: llm
---

Goal: Build auth headers and the stream client in one place.

Evidence: The TUI hardcodes Bearer in regular and stream requests, while the harness uses ApplyAuth, so credentials can behave differently.

Scope: ui/tui/api.go, sse.go and their tests.

Steps: Add one header helper and one stream client helper. Use them from both request paths.

Accept: Changing the helper changes both /api/v1/runs and /api/v1/events headers in tests, and rg finds one Authorization literal in production code.

Open a PR, call beacon_watch_pr, do not self-merge.
