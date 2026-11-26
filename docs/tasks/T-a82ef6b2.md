---
id: T-a82ef6b2
title: Remove duplicate bench compile in non-smoke mode
status: done
scope:
    - scripts/bench-e2e.sh
priority: 70
kind: task
require_review: llm
---

Goal: Stop compiling the bench package twice in the nightly measurement path.

Evidence: ciopt-05 item 3. The non-smoke branch runs two go test commands and recompiles ./internal/bench/. The smoke branch already uses one command and PR #2627 added BEACON_BENCH_SMOKE there.

Steps: Combine both bench regexes into one go test invocation. Keep the 5x E2E report in the nightly path and keep the smoke command unchanged.

Check: Five alternating runs report identical work counts and the nightly baseline format is unchanged.

Accept: Do not change the regression gate limits.
