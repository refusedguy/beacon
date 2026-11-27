---
id: T-4c86bfc2
title: '[bench] Core verdict decision benchmark'
status: done
scope:
    - internal/bench/verdict_bench_test.go
priority: 90
kind: task
require_review: llm
---

Goal: add benchmark coverage for Core verdict decision benchmark.

Evidence: section Card 3 of /home/nbook/tmp/backup/opencode/research/cardplan-01-core.md. Implement that specification exactly.

CI command: BEACON_BENCH_SMOKE=1 GOMAXPROCS=1 go test -vet=off -run '^$' -bench '^BenchmarkDecideInvariants$|^BenchmarkDecisionActions$' -benchtime=1x -count=1 -benchmem ./internal/bench/

Check: the benchmark prints work, errs and a named limiter under CI.

Accept: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters or benchmarks locally. Open a PR and call beacon_watch_pr. CI supplies every check and number. Do not touch ci.yml or any parser wiring; that belongs to the next wave.
