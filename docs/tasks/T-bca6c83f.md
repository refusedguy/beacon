---
id: T-bca6c83f
title: Make nightly gate read the registry
status: available
scope:
    - scripts/check-bench-regression.sh
    - scripts/bench-e2e.sh
depends:
    - T-76d66e90
priority: 92
kind: task
require_review: llm
---

Goal: remove the hardcoded expected medians and thresholds from the nightly gate.

Evidence: smart-03. Read keys, thresholds and placement from docs/perf/bench-registry.json through a small Go helper, since the nightly job has Go but not Node. Keep the 15 and 20 percent limits, the two percent noise floor and poisoned baseline rejection unless the registry says otherwise. Fail when a registry key has no emitted metric.

Accept: no new config system, no ci.yml changes, no weakening of the nightly gate.
