---
id: T-d7ec575a
title: Remove test-only re-export from ui_test_helpers
status: available
scope:
    - .opencode/plugins/beacon-project/ui/ui_test_helpers.js
    - .opencode/plugins/beacon-project/index.js
    - .opencode/plugins/beacon-project/ui/ui_actions.js
    - .opencode/plugins/beacon-project/ui/ui_sidebar.js
    - .opencode/plugins/beacon-project/ui/ui_card_view.js
    - .opencode/plugins/beacon-project/ui/ui_detail_view.js
priority: 80
kind: task
require_review: llm
---

Goal: Stop production modules from importing the whole UI barrel through a test helper.

Evidence: ciopt-02. ui_test_helpers.js re-exports all of ui.js and is imported by index.js and four view modules. Every UI test that imports it evaluates every UI module, so splitting tests by file gives false confidence.

Steps: Keep only propSetter, toastShow and cleanupWrapper in the helper. Update the five production importers to import their real modules directly. Update tests that relied on the barrel.

Check: Importing a leaf view no longer loads the full UI graph. Plugin tests pass.

Accept: No rendered prop or wire shape change.
