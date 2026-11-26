---
id: T-2ad9b0c6
title: Remove unused TUI control seams
status: done
scope:
    - ui/tui/api.go
    - ui/tui/api_test.go
    - ui/tui/control_action_test.go
priority: 74
kind: task
require_review: llm
---

Goal: Delete one identity cast and one unused control method.

Evidence: quality-02. ParseAction at api.go:65 is used only by tests. Client.Control at :202 has no production caller; model.go posts through c.do.

Steps: Remove both and rewrite the affected tests to exercise the real path.

Check: ui/tui tests pass with one control path.
