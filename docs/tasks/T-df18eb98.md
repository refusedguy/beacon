---
id: T-df18eb98
title: Merge acp and opencode matrix legs
status: available
scope:
    - .github/workflows/ci.yml
depends:
    - T-b7e0d035
priority: 68
kind: task
require_review: llm
---

Goal: Remove one checkout and setup-go by merging two thin harness legs.

Evidence: ciopt-03 item 3. Both modules are small adapters with one require; running them in sequence on one runner saves little but is safe.

Steps: Combine the two entries and run one go test step per module.

Check: Both harness suites still run with the same flags.

Accept: If wall clock grows, close the card with the measurement.
