---
id: T-4bb04b0c
title: Show task and blocker titles in blocked queue groups
status: in_progress
scope:
    - .opencode/plugins/beacon-project/ui/ui_view.js
    - .opencode/plugins/beacon-project/ui/ui_alerts.js
    - .opencode/plugins/beacon-project/ui/ui_view_test.js
    - .opencode/plugins/beacon-project/ui/ui_alerts_test.js
priority: 96
kind: task
require_review: llm
---

Goal: A blocked card is readable without expanding every group.

Evidence: The operator sees 'blocked by T-8e34a917' and a clipped title like 'Add share...NTS.md gap'. Each group header names only the blocker id, so understanding what waits on what requires opening all groups. The queue already normalizes card titles through readTitle, and the blocker id is available on each queue item.

Steps: Put the blocked task title and the blocker title on the group header, using readTitle for ids that resolve. Keep the count and priority. Cap each title with the existing truncation helper so the row still fits. Add a fixture with three blocked groups and assert the rendered text names both sides.

Check: With groups collapsed, the operator can tell which task waits on which blocker by title.

Accept: No wire shape change. Keep the expand/collapse interaction.
