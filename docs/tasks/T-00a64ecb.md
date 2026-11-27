---
id: T-00a64ecb
title: 'A8: JS plugin sidebar cost'
status: available
scope:
    - .opencode/plugins/beacon-project/ui/ui_model.js
    - .opencode/plugins/beacon-project/ui/ui_alerts.js
    - .opencode/plugins/beacon-project/ui/ui_view.js
    - .opencode/plugins/beacon-project/ui/ui_model_test.js
    - .opencode/plugins/beacon-project/ui/ui_alerts_test.js
priority: 11
kind: task
require_review: llm
---

Goal: cut JS plugin sidebar reads and grouping without changing rendered rows.
Variants, one commit each: V1 remove the second queueGroups call in queueRows and add a fast path in queueBlockers for cards without dependencies. V2 in stateBarFromView fill a prepared literal with two loops over active and recent instead of spreading arrays and Object.fromEntries. V3 in readCard cache parsed cards by path with statSync mtime and size, bypassing the cache when deps lacks statSync, with a readFileSync counter test; skip repeated normalizeSnapshot in RpcStatusSource.snapshot.
Deciding row tui-view. For readCard use the deterministic readFileSync counter as evidence if tui-view stays flat from runner noise. ДISCIPLINE: never run go test, go build, go vet, linters, node tests or benchmarks locally. One variant is exactly one commit and one push. After each push wait for the <!-- beacon-pr-perf --> comment. Keep the variant only if the deciding row reports gain, including gain (allocs/bytes) or gain (calls reduced), with equal work, errs zero, non-empty limiter and cache warm. Otherwise run git reset --hard HEAD~1 && git push --force-with-lease. Add the perf label immediately and never add ready-to-merge until every variant is checked and the final table is in the PR body. Work in your own worktree off current main, never push to main, open a PR and call beacon_watch_pr.
