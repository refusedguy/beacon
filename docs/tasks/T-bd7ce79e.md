---
id: T-bd7ce79e
title: Add build and vet coverage for acp module
status: available
scope:
    - .github/workflows/ci.yml
depends:
    - T-40a82de6
priority: 72
kind: task
require_review: llm
---

Goal: Close the gap where acp is tested but never built or vetted.

Evidence: ciopt-03. verify builds and vets root, opencode and tui but skips harnesses/acp.

Steps: Add acp build and vet steps to verify with the same toolchain setup as the other modules.

Check: A deliberate compile error in acp fails CI.

Accept: No new third-party action.
