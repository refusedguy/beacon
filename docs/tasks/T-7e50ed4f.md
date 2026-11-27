---
id: T-7e50ed4f
title: Add four hot path coverage rows
status: available
scope:
    - internal/bench/bench_test.go
    - internal/bench/report.go
    - internal/bench/scm_bench_test.go
    - internal/bench/cardindex_bench_test.go
    - internal/bench/http_snapshot_bench_test.go
    - internal/bench/harness_spawn_bench_test.go
priority: 94
kind: task
require_review: llm
---

Goal: add SCM observe, local card parse, HTTP snapshot assembly and harness spawn rows to BenchmarkHotPaths.

Read Work in your own worktree and branch off current main. Never push to main. Do not run go test, go build, go vet, linters or benchmarks locally. Open a PR and call beacon_watch_pr. CI supplies all checks. Follow the exact plan in /home/nbook/tmp/backup/opencode/research/benchplan-05-coverage.md and implement all four rows with bounded fixtures, work, errs and limiter strings. Keep the added PR smoke work under 60 seconds. Do not touch ci.yml or production code.

Accept: nightly regression keys are named in the PR body but added by the ci.yml card.
