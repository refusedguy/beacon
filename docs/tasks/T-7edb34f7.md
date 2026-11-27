---
id: T-7edb34f7
title: '[bench] Card index invalidation'
status: available
scope:
    - internal/infra/tasksource/cardindex_bench_test.go
priority: 77
kind: task
require_review: llm
---

Goal: add benchmark coverage for Card index invalidation.

Evidence: section Card 2 of /home/nbook/tmp/backup/opencode/research/cardplan-06-tasksource.md. Implement that specification exactly.

CI command: See report section Card 2

Check: the benchmark prints work, errs and a named limiter under CI.

Accept: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters or benchmarks locally. Open a PR and call beacon_watch_pr. CI supplies every check and number. Do not touch ci.yml or any parser wiring; that belongs to the next wave.
