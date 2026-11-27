---
id: T-4e5db70d
title: '[bench] Dispatcher promotion and slot reservation'
status: done
scope:
    - internal/bench/dispatch_paths_bench_test.go
priority: 88
kind: task
require_review: llm
---

Goal: add benchmark coverage for Dispatcher promotion and slot reservation.

Evidence: section 2 of /home/nbook/tmp/backup/opencode/research/cardplan-02-planner-dispatch.md. Implement that specification exactly.

CI command: BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' -bench '^BenchmarkDispatchPromotionSlots$' -benchtime=1x -count=1 -benchmem ./internal/bench/

Check: the benchmark prints work, errs and a named limiter under CI.

Accept: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters or benchmarks locally. Open a PR and call beacon_watch_pr. CI supplies every check and number. Do not touch ci.yml or any parser wiring; that belongs to the next wave.
