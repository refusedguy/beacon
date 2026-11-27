---
id: T-caa84f12
title: Wire new benchmarks into advisory job
status: done
scope:
    - .github/workflows/ci.yml
depends:
    - T-c0295fe9
    - T-69f1967e
    - T-1239ad7d
    - T-292b3d2c
    - T-7e50ed4f
priority: 93
kind: task
require_review: llm
---

Goal: run and publish all new benchmark rows in perf-pr-advisory.

After the five benchmark cards land, add their commands to run_side, add their names to wanted and limiters in the comment script, and parse their printed summary lines. Use the exact wiring in benchplan-02, benchplan-03, benchplan-04 and benchplan-05. Keep the job advisory with exit zero and change no required check name.

Accept: a labeled PR shows rows for reconcile, snapshot, worktree, plugin fingerprint and the four hot paths.
