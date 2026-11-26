---
id: T-9c5fb22d
title: Avoid full TUI reload per event
status: done
scope:
    - ui/tui/model.go
    - ui/tui/model_test.go
    - ui/tui/api.go
priority: 88
kind: task
require_review: llm
---

Goal: An SSE event must not trigger a full runs read and full render when only one run changed.

Evidence: opt-tui.md shows main.go sends one refresh per event and reload fetches all runs.

Steps: Keep an indexed run map in the model and apply event deltas when the payload is sufficient. Fall back to a full reload for unknown events. Preserve rendered text.

Check: A fake daemon serving one event plus one runs response proves one GET per generation and identical View output. Targeted TUI tests pass.

Accept: No wire shape change and no Beacon import in ui/tui.

Open a PR, call beacon_watch_pr, do not self-merge.
