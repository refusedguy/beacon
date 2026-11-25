---
id: T-3bdc4af2
title: Functional test for monitor render after an event
status: available
scope:
    - ui/tui/model_test.go
priority: 90
kind: task
require_review: llm
---

Goal: Prove that an SSE event reaches the rendered text.

Evidence: No test drives event -> Update(refreshMsg) -> reload -> View. Wire and SSE tests pass even when rendering drops a run.

Scope: ui/tui/model_test.go.

Steps: Start an httptest daemon serving /api/v1/events and /api/v1/runs. Build a real Client and model. Feed one Event, run the returned command, feed the runsMsg, then call View.

Check: cd ui/tui && go test ./... passes. The test fails if View drops the run or if Update(refreshMsg) stops returning reload.

Accept: No production code, wire shape or other card file changes.

Open a PR, call beacon_watch_pr, do not self-merge.
