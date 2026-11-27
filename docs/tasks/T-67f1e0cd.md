---
id: T-67f1e0cd
title: '[bench] Frontmatter validation and alias normalization'
status: in_progress
scope:
    - internal/bench/card_frontmatter_bench_test.go
priority: 78
kind: task
require_review: llm
---

Goal: add benchmark coverage for Frontmatter validation and alias normalization.

Evidence: section Card 1 of /home/nbook/tmp/backup/opencode/research/cardplan-06-tasksource.md. Implement that specification exactly.

CI command: See report section Card 1

Check: the benchmark prints work, errs and a named limiter under CI.

Accept: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters or benchmarks locally. Open a PR and call beacon_watch_pr. CI supplies every check and number. Do not touch ci.yml or any parser wiring; that belongs to the next wave.
