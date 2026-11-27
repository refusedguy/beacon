---
id: T-b8bbeb4d
title: 'M3: skip self-hosted perf-nightly on manual dispatch'
status: available
scope:
    - .github/workflows/ci.yml
priority: 38
kind: task
require_review: llm
---

Goal: let a manual workflow_dispatch run finish so perf-pr-advisory can read the calibrated noise floor artifact.

Evidence: run 45976646949. perf-noise-floor completed and published bench-noise-floor with floorPct 13.3 from 15 samples, but perf-nightly stayed queued on [self-hosted, linux, x64, perf]. No self-hosted runner is registered, so the run never reaches status success. The advisory step 'Find the calibrated noise floor' filters runs by status success and therefore ignores the artifact.

Steps:
1. In .github/workflows/ci.yml, change the perf-nightly job condition from `github.event_name == 'schedule' || github.event_name == 'workflow_dispatch'` to `github.event_name == 'schedule'` only.
2. Leave runs-on, thresholds, baseline publishing and every other job unchanged. This card does not move the gate to hosted hardware and does not change the advisory job.

Accept: a manual workflow_dispatch run completes with status success and no queued jobs. The fix PR must NOT carry the perf label so mark-ready sets ready-to-merge. No local execution.
Protocol: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters or benchmarks locally. Open a PR and call beacon_watch_pr. CI supplies every check.
