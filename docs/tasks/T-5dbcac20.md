---
id: T-5dbcac20
title: 'P21: CI wall clock cleanup after warm-cache'
status: available
scope:
    - .github/workflows/ci.yml
depends:
  - T-73e190ac
priority: 2
kind: task
require_review: llm
---

Goal: remove the remaining dead CI work without changing required-check semantics.
Evidence: median non-perf PR wall fell from 135 s to 53 s after warm-cache removal, saving about 80 s. changes.outputs.perf at ci.yml:40 is defined but never consumed. plugin-tui-smoke gates on shared or plugin, so a workflow-only edit boots the TUI for 11 s of runner time. build, vet, lint and test are echo-only jobs on the critical path. perf-pr-advisory and perf-noise-floor duplicate about 70 lines of warmup and run_side.
Variants, one commit each: V1 delete the dead perf output; V2 drop the shared clause from plugin-tui-smoke, noting that Beacon will stop requiring that check on shared-only diffs; V3 factor the two perf jobs shared warmup and run_side into one script, keeping both job names and the measured commands unchanged.
This card owns ci.yml while it runs, after P19. No perf label.

Work in your own worktree off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. Open a PR without the perf label so mark-ready sets ready-to-merge. Call beacon_watch_pr. CI supplies every check.
