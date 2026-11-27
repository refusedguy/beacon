---
id: T-da58dba7
title: 'M2: fix advisory parser, cache export and noise-floor runner in ci.yml'
status: done
scope:
    - .github/workflows/ci.yml
priority: 39
kind: task
require_review: llm
---

Goal: make PR #2786 produce real numbers with cache=warm, no duplicate rejections, and a calibrated floor.

Evidence: PR #2786 advisory comment, run 45973256317. All 15 rows are inconclusive. Parser notes contain duplicate keys in all ten passes, json-control drift rejection in seven passes, uncalibrated floor, and cache=unset on every row. Reconnaissance also found that the advisory job searches only event schedule runs for the floor artifact, so a manual workflow_dispatch calibration is never used.

Steps, all in .github/workflows/ci.yml:
1. In both perf-pr-advisory and perf-noise-floor, extend the compile-only warmup loop so each tree also compiles ./internal/infra/workspace/ with `go test -vet=off -run '^$' ./internal/infra/workspace/`. After the warmup loop, export BEACON_BENCH_CACHE=warm before any run_side call. Every canonical row must then report cache=warm.
2. In parseSide, stop treating a repeated key as bad. With -benchtime=5x, testing.B emits one row from the run1 calibration and one from the runN pass. Replace the existing row for that key with the later one instead of pushing a duplicate into pass.bad. Keep the rule that every expected key must appear at least once per pass and that p.rows.length must equal expected.size.
3. Split pass validity from ratio validity. Compute base/head, delta, ranges, maxSpread, work, errs and verdict from all shape-valid passes. Filter passes by json-control drift only for the base/control, head/control and ratio delta columns. When fewer than two drift-valid passes remain on a side, write '-' in those three columns for all rows and add one short note. Do not set inconclusiveAll or drop raw rows when only the control drifts.
4. Change perf-noise-floor from runs-on: [self-hosted, linux, x64, perf] to runs-on: ubuntu-latest so calibration runs on the same runner class as perf-pr-advisory.
5. In the advisory step named 'Find the calibrated noise floor', search both event schedule and event workflow_dispatch success runs, merge the lists, sort by run id descending, and pick the newest run that is not the current one. A manual calibration must be usable by the next PR comparison.

Accept: advisory job stays advisory, exits zero, adds no required Criterion name. The fix PR must NOT carry the perf label so mark-ready sets ready-to-merge and ordinary CI gates it. No local execution.
Protocol: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters or benchmarks locally. Open a PR and call beacon_watch_pr. CI supplies every check.
