---
id: T-01cedbec
title: '[bench] Duplicate mutation encode on MCP journal'
status: available
scope:
    - internal/infra/mcp/duplicate_encode_bench_test.go
priority: 75
kind: task
require_review: llm
---

Goal: add benchmark coverage for Duplicate mutation encode on MCP journal.

Evidence: section Card 1 of /home/nbook/tmp/backup/opencode/research/cardplan-07-scm-mcp.md. Implement that specification exactly.

CI command: BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' -bench '^BenchmarkDuplicateMutationEncode$' -benchtime=200x -count=5 -benchmem ./internal/infra/mcp/

Check: the benchmark prints work, errs and a named limiter under CI.

Accept: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters or benchmarks locally. Open a PR and call beacon_watch_pr. CI supplies every check and number. Do not touch ci.yml or any parser wiring; that belongs to the next wave.
