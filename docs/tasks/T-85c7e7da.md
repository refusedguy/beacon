---
id: T-85c7e7da
title: 'W1R-2: cut snapshot decode and assembly cost'
status: available
scope:
    - internal/infra/sqlite/store.go
    - internal/infra/sqlite/store_test.go
    - internal/app/status.go
    - internal/app/status_test.go
priority: 32
kind: task
require_review: llm
---

Goal: cut reflection and repeated allocations in the snapshot read and assembly path.

Evidence: the advisory row snapshot is about 28 ms and sqlite-snapshot about 7 ms on unchanged code. Per-key floors from run 45984052138 are 3.9% for snapshot and 5.7% for sqlite-snapshot. store.go decodeJSON calls json.Unmarshal for empty values on every ListActive and ListDecisions row. collectRows and status.go groupHistory do not preallocate.

Variants, one commit each: skip json.Unmarshal for '{}', '[]' and empty strings; preallocate row and map capacities; compute the PR base once per run. Keep every decoded value and wire field identical.

Accept: deciding rows are snapshot and sqlite-snapshot. Protocol: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. Open a PR, call beacon_watch_pr, add the perf label, and do not add ready-to-merge until every variant is tried. One variant is one commit and one push. Read the beacon-pr-perf comment after each push. Keep a variant only on verdict gain with equal work, errs zero, non-empty limiter and cache warm. Revert a losing variant with git reset --hard HEAD~1 and git push --force-with-lease. Put a final table in the PR body, then add ready-to-merge.
