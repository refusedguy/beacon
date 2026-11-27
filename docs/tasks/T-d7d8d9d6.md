---
id: T-d7d8d9d6
title: 'W2-7: cut local card scan cost'
status: available
scope:
    - internal/infra/tasksource/markdown.go
    - internal/infra/tasksource/cardindex.go
    - internal/infra/tasksource/markdown_test.go
priority: 24
kind: task
require_review: llm
---

Goal: cut local card scan cost without changing any parsed card field.

Evidence: card-local-parse is about 4.84 ms with a calibrated floor of 53.9% from run 45987459320. ListAvailable re-reads unchanged cards and scopeForPath repeats os.Stat calls. The index already stores mtime, so unchanged files can be reused.

Variant: in ListAvailable, MarkInWorkspace and scopeForPath, reuse indexed cards whose mtime has not moved, and remove redundant os.Stat calls on paths already resolved by the index. Keep every alias, scope and cache behavior identical.

Accept: deciding row card-local-parse. Protocol: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters or benchmarks locally. Open a PR, call beacon_watch_pr, add the perf label, and do not add ready-to-merge until every variant is tried. One variant is one commit and one push. Read the beacon-pr-perf comment after each push. Keep a variant only on verdict gain with equal work, errs zero, non-empty limiter and cache warm. Revert a losing variant with git reset --hard HEAD~1 and git push --force-with-lease. Put a final table in the PR body, then add ready-to-merge.
