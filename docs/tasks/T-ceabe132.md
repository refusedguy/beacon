---
id: T-ceabe132
title: 'W2-5: cut git worktree remove latency'
status: available
scope:
    - internal/infra/workspace/git_worktree.go
    - internal/infra/workspace/git_worktree_test.go
priority: 26
kind: task
require_review: llm
---

Goal: reduce git subprocess calls in Remove while keeping dirty-tree classification.

Evidence: worktree-remove is about 9.33 ms on unchanged code and has a calibrated floor of 2% from run 45987459320. The old PR #2728 was closed before the thermometer worked. Remove currently calls Clean before git worktree remove, so a clean removal spawns three git calls.

Variant: call git worktree remove first and call Clean only on error to classify a dirty tree. Keep the same errors, the same branch cleanup and the same test contract.

Accept: deciding row worktree-remove. Protocol: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. Open a PR, call beacon_watch_pr, add the perf label, and do not add ready-to-merge until every variant is tried. One variant is one commit and one push. Read the beacon-pr-perf comment after each push. Keep a variant only on verdict gain with equal work, errs zero, non-empty limiter and cache warm. Revert a losing variant with git reset --hard HEAD~1 and git push --force-with-lease. Put a final table in the PR body, then add ready-to-merge.
