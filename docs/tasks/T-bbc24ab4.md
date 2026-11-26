---
id: T-bbc24ab4
title: Benchmark full snapshot assembly
status: done
scope:
    - internal/bench/hotspots.go
    - internal/bench/report.go
    - internal/bench/hotspots_test.go
priority: 90
kind: task
require_review: llm
---

Goal: Measure the CPU work the SQLite benchmark omits.

Evidence: quality-12 item 1. SnapshotService.Snapshot plus json.Encode is untimed while sqlite-snapshot times store reads only.

Steps: Add a benchmark at 1000 active, 20 recent and large decision/event histories. Report median, bytes and allocations with work counts.

Check: Five alternating runs are repeatable and the report explains the share of snapshot CPU.
