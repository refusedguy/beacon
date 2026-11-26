---
id: T-2277edee
title: Cut per-row work in sidebar queue rendering
status: done
scope:
    - .opencode/plugins/beacon-project/ui/ui_alerts.js
    - .opencode/plugins/beacon-project/ui/ui_view.js
    - .opencode/plugins/beacon-project/ui/ui_nodes.js
    - .opencode/plugins/beacon-project/ui/ui_alerts_test.js
priority: 85
kind: task
require_review: llm
---

Goal: Rendering 500 rows must not re-read cards, rebuild the epic tree and sweep every pooled row.

Evidence: opt-js-ui.md shows card reads per queue item, queue tree rebuilt per render, a full pooled-row sweep, and truncation regex per row.

Steps: Build the tree once, reuse card text from the normalized snapshot, only update rows whose content changed, and cache truncation results by string and width.

Check: A 500-run fixture in the Node harness reports lower time and work with identical rendered lines and zero errors.

Accept: No visible behavior or wire change.

Open a PR, call beacon_watch_pr, do not self-merge.
