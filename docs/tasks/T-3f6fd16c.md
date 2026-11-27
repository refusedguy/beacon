---
id: T-3f6fd16c
title: Partition plugin UI tests after barrel removal
status: available
scope:
    - .github/workflows/ci.yml
    - .opencode/plugins/beacon-project/ui/ui_exports_test.js
depends: []
priority: 82
kind: task
require_review: llm
---

Goal: Run a narrow plugin test set for narrow plugin changes.

Evidence: ciopt-02 item 3 and ciopt-10. The UI group cannot be split until ui_test_helpers stops re-exporting the barrel. Full suite stays mandatory for index.js, ui.js, ui_test_helpers.js, status_rpc.js, session_identity.js, shell_guard.js, exports and loader doubles.

Steps: Add matrix entries for config/peer tests, mcp/context tests and the UI group. Gate the full suite on the shared paths.

Check: A leaf plugin change runs its group only; an export change runs everything.

Accept: False green is unacceptable for shared helpers.
