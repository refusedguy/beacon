---
id: T-a3fbc566
title: '[bench] Dispatcher start-failure cleanup'
status: done
scope:
    - internal/bench/dispatch_failure_bench_test.go
priority: 87
kind: task
require_review: llm
---

Goal: add benchmark coverage for Dispatcher start-failure cleanup.

Evidence: section 3 of /home/nbook/tmp/backup/opencode/research/cardplan-02-planner-dispatch.md. Implement that specification exactly.

CI command: BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' -bench '^BenchmarkDispatchFailureCleanup$' -benchtime=1x -count=1 -benchmem ./internal/bench/

Check: the benchmark prints work, errs and a named limiter under CI.

Accept: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters or benchmarks locally. Open a PR and call beacon_watch_pr. CI supplies every check and number. Do not touch ci.yml or any parser wiring; that belongs to the next wave.
