---
id: T-be8aa55b
title: Diagnose perf-smoke 30s timeout
status: in_progress
scope:
    - scripts/bench-e2e.sh
    - scripts/check-bench-regression.sh
    - docs/perf/sqlite-deep.md
priority: 96
kind: task
require_review: llm
---

Goal: Find why perf-smoke exits 124 after 30 seconds and make the smoke bounded again.

Evidence: CI job logs for PRs #2618, #2619 and #2620 show 'Run timeout 30s bash scripts/bench-e2e.sh --smoke' followed by exit code 124 at exactly 30 seconds. The job used to pass. Recent changes added SQLite indexes with measured write cost (10k inserts rose from 209 ms to 259 ms) and a heavier benchmark corpus, so the smoke may now be doing more work than the bound allows.

Steps: Time each stage of bench-e2e.sh --smoke locally with work counts. Identify the stage that crosses 30 seconds. Either cut redundant work in the smoke or set a measured bound with headroom that still catches a regression. Do not simply remove the timeout.

Check: Five alternating smoke runs complete under the new bound with identical work counts and no errors. CI perf-smoke passes on a test PR.

Accept: Do not edit .github/workflows/ci.yml in this card; T-42ae53b1 owns that file. Keep the nightly regression gate unchanged.
