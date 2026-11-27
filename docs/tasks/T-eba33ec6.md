---
id: T-eba33ec6
title: Align Go cache-dependency-path across all jobs
status: done
scope:
    - .github/workflows/ci.yml
depends:
    - T-01550a9d
priority: 86
kind: task
require_review: llm
---

Goal: Every Go job hashes the same file set so they share one cache.

Evidence: ciopt-06 and ciopt-09. verify, test-modules and perf-smoke list go.sum plus ui/tui/go.sum; perf-nightly lists only go.sum and never reads their cache. A cold go test compile was measured at 138s versus 12s warm.

Steps: Give every Go job the same cache-dependency-path list. Include go.work.sum in the hash if it is added later.

Check: All Go jobs report the same restored key on a repeat run.

Accept: No behavior change.
