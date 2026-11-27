---
id: T-0e1fc7c7
title: 'A7: Tasksource local card scan'
status: available
scope:
    - internal/infra/tasksource/markdown.go
    - internal/infra/tasksource/cardindex.go
    - internal/infra/tasksource/parse.go
    - internal/infra/tasksource/markdown_test.go
priority: 12
kind: task
require_review: llm
---

Goal: cut local card scan cost. Do NOT touch the card-parse row at 0.02 ms; the deciding row is card-local-parse.
Evidence: card-local-parse about 4.5 ms with a 25% capped floor. ListAvailable and scopeForPath repeat os.Stat across hundreds of cards.
Variants, one commit each: V1 cache os.Stat results for scopeForPath and directories within one ListAvailable call. V2 in parse.go scan lines with strings.Cut and trim carriage returns in place instead of strings.ReplaceAll plus strings.Split over the whole body. V3 in cardindex.go skip reparsing when mtime and size are unchanged.
Deciding row card-local-parse. ДISCIPLINE: never run go test, go build, go vet, linters, node tests or benchmarks locally. One variant is exactly one commit and one push. After each push wait for the <!-- beacon-pr-perf --> comment. Keep the variant only if the deciding row reports gain, including gain (allocs/bytes) or gain (calls reduced), with equal work, errs zero, non-empty limiter and cache warm. Otherwise run git reset --hard HEAD~1 && git push --force-with-lease. Add the perf label immediately and never add ready-to-merge until every variant is checked and the final table is in the PR body. Work in your own worktree off current main, never push to main, open a PR and call beacon_watch_pr.
