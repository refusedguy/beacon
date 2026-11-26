---
id: T-bfbd0512
title: Import build script constant and share cleanup wrapper
status: available
scope:
    - .opencode/plugins/beacon-project/mcp/mcp.js
    - .opencode/plugins/beacon-project/rules/context.js
    - .opencode/plugins/beacon-project/index.js
    - .opencode/plugins/beacon-project/ui/ui.js
depends:
    - T-f65221e3
    - T-32289431
priority: 58
kind: task
require_review: llm
---

Goal: The build script path is spelled once and the disposer wrapper is shared.

Evidence: final-05. mcp.js and rules/context.js both spell the path, and index.js and ui.js repeat the try/catch console.debug disposer.

Steps: Import BEACON_BUILD_SCRIPT in context.js and move the disposer into the test support module.

Check: Node syntax check and plugin tests pass.

Accept: No build script path change.
