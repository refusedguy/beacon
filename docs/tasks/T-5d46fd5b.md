---
id: T-5d46fd5b
title: Cut SQLite snapshot query time
status: available
scope:
    - internal/infra/sqlite/store.go
    - internal/infra/sqlite/store_test.go
priority: 95
kind: task
require_review: llm
---

Goal: Reduce the query cost that dominates the benchmark.

Evidence: docs/perf/dossier.md reports sqlite-snapshot at 11.37 ms median and 79.2 percent of the 14.36 ms hotspot total. The limiter is WAL page reads. The work count is 54,210 rows.

Scope: internal/infra/sqlite/store.go and store_test.go.

Steps: Inspect ListDecisions, DecisionCounts, ListRunEvents and RecordDecision. Reduce round trips or rows scanned while keeping the production limits of 20 decisions per run and 100 orphan rows. Keep verdicts and metric totals identical.

Check: go test -bench=BenchmarkSQLiteSnapshot -benchmem -count=5 ./internal/bench/ shows a lower median than the baseline in the dossier with identical work counts and zero errors. Targeted sqlite tests pass.

Accept: No wire shape or policy change.

Open a PR, call beacon_watch_pr, do not self-merge.
