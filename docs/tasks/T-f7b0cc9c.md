---
id: T-f7b0cc9c
title: Add overlay view model for perf panel
status: available
scope:
    - .opencode/plugins/beacon-project/ui/ui_overlay_view.js
    - .opencode/plugins/beacon-project/ui/ui_overlay_view_test.js
priority: 90
kind: task
require_review: llm
---

Goal: build the pure view model for a full-screen performance overlay inside the OpenCode plugin.

Evidence: overlay-02 shows the plugin owns no full-screen overlay but has a session.panel slot at ui.js:133-148 with names beacon.detail and beacon.card. overlay-04 gives the layout: table of cards with id, title, state, PR and run id, plus a detail pane with variants and measured rows. The sidebar clips titles at 29 characters, so the overlay must carry full ids.

Steps: add ui_overlay_view.js exporting OVERLAY_PANEL_NAME = 'beacon.overlay', overlayModel(snapshot, now) and overlayCommand(open). Read the normalized snapshot only. Build rows for cards marked perf: true from docs/perf/cards.md if that data is available through the snapshot, otherwise accept a cards argument injected by the caller. Return a title, summary line and rows with full id, title, state, PR and run id. Add a test with a fixed snapshot.

Accept: no node allocation in the view model, no local test or build runs, and no change to existing UI files.
