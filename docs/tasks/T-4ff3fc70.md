---
id: T-4ff3fc70
title: Centralize prop-write and toast fallbacks
status: available
scope:
    - .opencode/plugins/beacon-project/ui/ui_card_view.js
    - .opencode/plugins/beacon-project/ui/ui_detail_view.js
    - .opencode/plugins/beacon-project/ui/ui_sidebar.js
    - .opencode/plugins/beacon-project/ui/ui_actions.js
    - .opencode/plugins/beacon-project/ui/ui_test_helpers.js
depends:
    - T-120022a8
priority: 76
kind: task
require_review: llm
---

Goal: One propSetter and one toastShow helper for the test-double path.

Evidence: final-05. The setProp fallback appears in three mounts and the toast fallback twice.

Steps: Extract both helpers into the test support module and update the five call sites.

Check: Plugin UI tests pass with a real host fixture, including props in card, detail and sidebar.

Accept: Props delivered to the host stay identical.
