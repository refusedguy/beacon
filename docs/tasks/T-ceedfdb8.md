---
id: T-ceedfdb8
title: Collapse duplicated TUI header render
status: available
scope:
    - ui/tui/model.go
depends:
  - T-1bf97e62
priority: 54
kind: task
require_review: llm
---

Goal: One header render call.

Evidence: final-15. Two branches both call headerStyle.Render, one with a styled newline and one without.

Steps: Collapse to one render.

Check: Header output stays identical in both states.

Accept: No style change.
