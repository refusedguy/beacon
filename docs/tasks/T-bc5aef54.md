---
id: T-bc5aef54
title: Wire full-screen perf panel into plugin navigation
status: available
scope:
    - .opencode/plugins/beacon-project/ui/ui.js
    - .opencode/plugins/beacon-project/ui/ui_slots.js
    - .opencode/plugins/beacon-project/ui/ui_navigation.js
    - .opencode/plugins/beacon-project/ui/ui_overlay_setup_test.js
depends:
    - T-f7b0cc9c
    - T-db98bef1
priority: 88
kind: task
require_review: llm
---

Goal: open the overlay through the existing session.panel mechanism, using the fullscreen presentation if the host supports it.

Evidence: ui.js:133-148 switches on input.name and returns pooled slot trees. ui_slots.js:11 holds SLOT_KEYS. ui_navigation.js:11-26 handles panelIsOpen and closePanel and currently knows only two names. overlay-02 warns that unknown names render null and slots outside SLOT_KEYS never refresh. overlay-05 gives the smallest viable edit.

Steps: add a third branch for beacon.overlay in ui.js, add overlay to SLOT_KEYS with ensureSlot and applySlot branches, and teach panelIsOpen, closePanel and the toggle about the new name. Register a keymap command beside ui.js:121-129, bound to ctrl+o, with palette and slash command beacon-overlay. Open with ctx.ui.panel.open(name, { presentation: 'fullscreen' }) and fall back to ctx.ui.panel.open(name) if the host rejects the option. Keep ctrl+g and ctrl+b on their current panels. Capture input.close into a flag as ui.js does. Add a setup test asserting the render branch and the command.

Accept: the existing detail and card panels keep their behaviour, the nudge dialog is untouched, and no local test or build runs.
