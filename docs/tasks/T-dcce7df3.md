---
id: T-dcce7df3
title: 'Q1: Remove redundant patch card reparse'
status: available
scope:
    - internal/infra/tasksource/cardindex.go
    - internal/infra/tasksource/markdown.go
    - internal/infra/tasksource/markdown_test.go
priority: 7
kind: task
require_review: llm
---

Goal: cut card-local-parse allocations by reusing the already parsed task when only the status line changes.
Evidence: noteIndexedCard at cardindex.go:152 calls parseCardFields again on text that indexedPatchStatus already parsed at :188-217; on the next ListAvailable refreshIndexedCard re-parses the same changed cards. The 20 patches per pass in cardindex_bench_test.go therefore pay roughly 40 uncached YAML decodes plus 1000 os.Stat calls. A hand YAML parser was rejected as too risky because of aliases, nested plans and malformed input; this removes the decodes without touching YAML semantics.
Variants, one commit each: V1 pass the parsed task from indexedPatchStatus into noteIndexedCard and store it in the index; V2 make refreshIndexedCard reuse that task when only status changed; V3 skip the second mtime walk for cards whose parse result is unchanged.
Deciding row card-local-parse with allocs/op and B/op from the shared HotPaths CSV.

Work in your own worktree off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. One variant is one commit and one push, read the beacon-pr-perf comment after each push, keep only gain including gain (allocs/bytes) with equal work, errs zero, non-empty limiter and warm cache, revert with git reset --hard HEAD~1 and git push --force-with-lease. Add the perf label at PR creation with gh pr create --label perf. Never add ready-to-merge before all variants are checked and the final table is in the PR body. Call beacon_watch_pr. CI supplies every check.
