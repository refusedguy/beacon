---
id: T-d9f4c86b
title: 'W1R-1: cut plugin sidebar read and grouping cost'
status: available
scope:
    - .opencode/plugins/beacon-project/ui/ui_model.js
    - .opencode/plugins/beacon-project/ui/ui_alerts.js
    - .opencode/plugins/beacon-project/ui/ui_model_test.js
    - .opencode/plugins/beacon-project/ui/ui_alerts_test.js
priority: 33
kind: task
require_review: llm
---

Goal: cut synchronous card reads and repeated queue grouping in the plugin sidebar.

Evidence: the advisory row tui-view is about 7.5 ms on unchanged code and carries a calibrated per-key floor of 54.2% from run 45984052138. ui_model.js readCard calls existsSync, readFileSync and frontmatter parsing on every poll. ui_alerts.js calls queueGroups twice on the same array. RpcStatusSource normalizes an already normalized snapshot.

Variants, one commit each: cache mtime and size in readCard and bypass when deps lacks statSync; pass the queueGroups result into queueLineItems; skip normalizeSnapshot in RpcStatusSource when the source is already normalized. Keep every rendered row and field identical.

Accept: deciding row is tui-view. Protocol: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. Open a PR, call beacon_watch_pr, add the perf label, and do not add ready-to-merge until every variant is tried. One variant is one commit and one push. Read the beacon-pr-perf comment after each push. Keep a variant only on verdict gain with equal work, errs zero, non-empty limiter and cache warm. Revert a losing variant with git reset --hard HEAD~1 and git push --force-with-lease. Put a final table in the PR body, then add ready-to-merge.
