---
id: T-c894c2fa
title: Add bounded smoke mode for benchmark harness
status: done
scope:
    - internal/bench/bench_test.go
    - internal/bench/e2e_bench_test.go
    - internal/bench/fixtures.go
priority: 95
kind: task
require_review: llm
---

Goal: One fast command runs every benchmark once with bounded work and zero errors.

Evidence: opt-ci.md shows scripts/bench-e2e.sh runs gofmt before measuring, and CI needs a short smoke mode with no timing gate.

Steps: Add a smoke flag or env override so BenchmarkHotPaths and BenchmarkE2EOperatorPaths run once with the same seeded fixtures. Keep report format and work/error counts unchanged. Do not change fixture sizes.

Check: GOMAXPROCS=1 go test -run '^$' -bench 'BenchmarkHotPaths|BenchmarkE2EOperatorPaths' -benchtime=1x -count=1 ./internal/bench/ finishes under 5 seconds with every errs count zero and work positive.

Accept: No production file changes.

Open a PR, call beacon_watch_pr, do not self-merge.
