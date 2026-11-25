---
id: T-3bdc4af2
title: Functional test for monitor render after an event
status: available
scope:
  - ui/tui/model_test.go
  - docs/research/functional.md
priority: 90
kind: task
require_review: llm
---
Goal: Prove that an SSE event reaches rendered text and document the flow with a runnable example.

Evidence: No test drives event -> Update(refreshMsg) -> reload -> View, and the first research wave stayed at the level of prose.

Steps: Start an httptest daemon serving /api/v1/events and /api/v1/runs. Build a real Client and model. Feed one Event, run the returned command, feed the runsMsg, then call View. Write docs/research/functional.md with the flow, the minimal code example, the exact test command and the observed assertion output.

Check: cd ui/tui && go test ./... passes. The test fails if View drops the run or if Update(refreshMsg) stops returning reload. The doc contains the captured test output.

Accept: No production code or wire shape changes.

Open a PR, call beacon_watch_pr, do not self-merge.