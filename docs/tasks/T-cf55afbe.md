---
id: T-cf55afbe
title: 'P19: MCP dispatch response encoding'
status: available
scope:
    - internal/infra/mcp/tool_dispatch.go
    - internal/bench/mcp_dispatch_bench_test.go
    - internal/bench/registry.go
    - internal/bench/registry_test.go
    - docs/perf/bench-registry.json
    - .github/workflows/ci.yml
depends:
  - T-c2c020fc
priority: 4
kind: task
require_review: llm
---

Goal: avoid serializing runs and skipped items twice in beacon_dispatch response.
Evidence: tool_dispatch.go:45-69 builds a compact summary and also returns full runs, skipped and failures, then server.go:415-427 marshals the whole map with reflection. With 500 ready tasks and two free slots the duplicate JSON can dominate the call. No key or benchmark covers it.
Steps: add a benchmark counting bytes of the map versus the summary alone, register a canonical key, add a run_side command, then make the first variant that removes the duplicate serialization.
Deciding row mcp-dispatch-encode with gain (allocs/bytes). This card owns ci.yml while it runs, after P5.

Work in your own worktree off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. One variant is one commit and one push, read the beacon-pr-perf comment after each push, keep only gain including gain (allocs/bytes) and gain (calls reduced) with equal work, errs zero, non-empty limiter and warm cache, revert with git reset --hard HEAD~1 and git push --force-with-lease. Add perf label at PR creation with gh pr create --label perf. Never add ready-to-merge before all variants are checked and the final table is in the PR body. Call beacon_watch_pr. CI supplies every check.
