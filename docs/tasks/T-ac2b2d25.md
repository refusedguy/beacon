---
id: T-ac2b2d25
title: Assert plugin defaults come from Plugin.define
status: done
scope:
    - .opencode/plugins/beacon-project/index.js
    - .opencode/plugins/beacon-project/ui/ui.js
    - .opencode/plugins/beacon-project/ui/ui_exports_test.js
depends:
    - T-e3a8c320
priority: 85
kind: task
require_review: llm
---

Goal: A missing host plugin cannot silently become an identity fallback.

Evidence: The identity fallback in PR #2492 let the plugin load while hiding that OpenCode had not supplied Plugin.define.

Steps: Use the test loader to stub @opencode/plugin and @opencode/plugin/tui with a define that returns a sentinel. Import both entry modules fresh and assert each default export is that sentinel. Keep test-only doubles out of production code.

Check: The test fails when define is removed or when a fallback replaces the host result. Existing plugin tests still pass.

Accept: No behavior change beyond the assertion.

Open a PR, call beacon_watch_pr, do not self-merge.
