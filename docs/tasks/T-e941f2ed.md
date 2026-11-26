---
id: T-e941f2ed
title: Render optimistic Dispatching state on click
status: in_progress
scope:
    - .opencode/plugins/beacon-project/ui/ui_actions.js
    - .opencode/plugins/beacon-project/ui/ui_alerts.js
    - .opencode/plugins/beacon-project/ui/ui_sidebar.js
    - .opencode/plugins/beacon-project/ui/ui_view.js
    - .opencode/plugins/beacon-project/ui/ui_actions_test.js
depends:
    - T-a699861c
priority: 95
kind: task
require_review: llm
---

Goal: The panel reacts in the click handler, before any process or RPC finishes.

Evidence: dispatch-01 and dispatch-06. The click handler returns immediately, the first visible change is a toast after the dispatch call exits, and the run row only appears after a second full snapshot. There is no busy flag, so a second click dispatches again.

Steps: Add an in-flight dispatch state with a visible 'Dispatching' row or plate label and ignore repeated clicks while it is set. Clear it on success or failure.

Check: A click changes the rendered row without waiting for the mock dispatch promise.

Accept: No wire shape change.
