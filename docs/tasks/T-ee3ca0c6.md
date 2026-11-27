---
id: T-ee3ca0c6
title: '[bench] Status CLI path benchmark'
status: available
scope:
    - cmd/beacon/status_cli_bench_test.go
priority: 81
kind: task
require_review: llm
---

Goal: add benchmark coverage for Status CLI path benchmark.

Evidence: section Card S3 of /home/nbook/tmp/backup/opencode/research/cardplan-04-status.md. Implement that specification exactly.

CI command: go test -vet=off -run '^$' -bench '^BenchmarkStatusCLIPaths$' -benchtime=1x -count=10 -benchmem ./cmd/beacon/

Check: the benchmark prints work, errs and a named limiter under CI.

Accept: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters or benchmarks locally. Open a PR and call beacon_watch_pr. CI supplies every check and number. Do not touch ci.yml or any parser wiring; that belongs to the next wave.
