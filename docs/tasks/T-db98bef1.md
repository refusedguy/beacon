---
id: T-db98bef1
title: Add overlay mount for perf panel
status: available
scope:
    - .opencode/plugins/beacon-project/ui/ui_overlay_mount.js
    - .opencode/plugins/beacon-project/ui/ui_overlay_mount_test.js
priority: 89
kind: task
require_review: llm
---

Goal: build the node tree and apply function for the full-screen overlay.

Evidence: overlay-02 shows createPanelChrome, createCardPlate, mountLines, checkRow and checksSummary in ui_alerts.js, node pools in ui_slots.js and ui_nodes.js, and open and close through ctx.ui.panel.open and input.close.

Steps: add ui_overlay_mount.js exporting createOverlayMount(ui, deps). Use createPanelChrome and existing line mount helpers. Allocate nodes only inside render. apply(model) must update props in place, never rebuild the tree. Wire the close glyph to the stored close callback and show key hints. Add a mount test using solidModule and themeStub from ui_test_helpers.js.

Accept: no new poll timer, no second CLI process, and no local test or build runs.
