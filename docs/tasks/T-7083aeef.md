---
id: T-7083aeef
title: Add CI perf smoke and nightly regression gate
status: available
scope:
    - .github/workflows/ci.yml
    - scripts/check-bench-regression.sh
    - scripts/bench-e2e.sh
priority: 95
kind: task
require_review: llm
---

Goal: Performance regressions are caught in CI, not on the operator laptop.

Evidence: opt-ci.md proposes a 30 second PR smoke and a nightly benchstat comparison on a fixed runner. Current CI has no perf job.

Steps: Add perf-smoke job with timeout-minutes 2, step timeout 30s, no timing gate. Add nightly job on a pinned perf label with -count=10, benchstat artifacts, 15 percent micro and 20 percent e2e tolerance, ignoring deltas under 2 percent and medians under 100us. Fix bench-e2e.sh so it does not run gofmt during measurement.

Check: PR job finishes under 30 seconds. Gate fails on an injected 20 percent slowdown and passes on 10 percent.

Accept: No developer machine benchmark step.

Open a PR, call beacon_watch_pr, do not self-merge.
