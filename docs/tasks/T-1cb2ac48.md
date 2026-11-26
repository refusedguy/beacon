---
id: T-1cb2ac48
title: Show every run state in a thicker sidebar bar
status: done
scope:
    - .opencode/plugins/beacon-project/ui/ui_view.js
    - .opencode/plugins/beacon-project/ui/ui_view_test.js
priority: 90
kind: task
require_review: llm
---

Goal: The state bar must be readable at a glance and must not hide rebase and fix states.

Evidence: ui_view.js uses STATE_BAR_KEYS with only running, waiting_ci, blocked and merged. needs_rebase and needs_agent_fix are invisible. The filled glyph is the thin '━' and the operator says it is hard to see.

Steps: Include all eight states from STATE_VIEW in the bar: pending, running, waiting_ci, needs_rebase, needs_agent_fix, blocked, merged, closed. Use a solid block glyph and a distinct color or letter per state. Keep the total width fixed and the proportional allocation rule. Update the legend to match the exact set.

Check: A fixture with each of the eight states present renders eight visible segments and a legend with eight counts. Plugin UI tests pass and the rendered text stays within the existing sidebar width.

Accept: No wire shape change and no new TUI peer packages.

Open a PR, call beacon_watch_pr, do not self-merge.
