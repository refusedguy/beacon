---
id: T-4536fbaa
title: 'P16: Wire notification benchmarks into advisory'
status: available
scope:
    - internal/infra/notify/telegram.go
    - internal/infra/notify/telegram_test.go
    - internal/bench/registry.go
    - internal/bench/registry_test.go
    - docs/perf/bench-registry.json
    - .github/workflows/ci.yml
depends:
    - T-df77d6ed
priority: 7
kind: task
require_review: llm
---

Goal: make Telegram send-path changes measurable in CI.
Evidence: BenchmarkTelegramNotify exists at telegram_bench_test.go:134 but emits no canonical row and has no registry key or run_side command. telegram.go:42 rebuilds endpoint and url.PathEscape on every send; :38 uses json.Marshal(map[string]string) with reflection and a map allocation.
Variants, one commit each: V1 compute endpoint once in NewTelegram; V2 replace the map with a tagged struct and keep byte-identical output; V3 add canonical row, registry key and run_side command first so V1 and V2 can be judged.
Deciding row telegram-notify. This card owns ci.yml while it runs, after P5.

Work in your own worktree off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. One variant is one commit and one push, read the beacon-pr-perf comment after each push, keep only gain including gain (allocs/bytes) and gain (calls reduced) with equal work, errs zero, non-empty limiter and warm cache, revert with git reset --hard HEAD~1 and git push --force-with-lease. Add perf label at PR creation with gh pr create --label perf. Never add ready-to-merge before all variants are checked and the final table is in the PR body. Call beacon_watch_pr. CI supplies every check.
