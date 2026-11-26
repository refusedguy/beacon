---
id: T-5d46fd5b
title: Cut SQLite snapshot query time
status: done
scope:
    - internal/infra/sqlite/store.go
    - internal/infra/sqlite/store_test.go
priority: 95
kind: task
require_review: llm
---
Goal: Find the real floor of the SQLite snapshot path at 500k decisions and push it there. Do not stop at pagination. Evidence: the dossier used only 400 decisions and still measured 11.37 ms median, 79.2 percent of the hotspot total, limiter WAL page reads. Steps: build 500k decisions across 100k runs and 2M events; capture pprof CPU and alloc profiles; try a maintained summary table, covering indexes, streaming rows, and a bounded cache with invalidation; measure each with five alternating runs; keep only changes that lower end-to-end `beacon status --snapshot` latency with identical verdicts and row counts; write docs/perf/sqlite-deep.md with every attempt, numbers, rejected approaches, and why the remaining limiter cannot be removed. Check: go test -bench=BenchmarkSQLiteSnapshot -benchmem -count=5 ./internal/bench/ reports median and range at 500k decisions with work counts and zero errors; doc contains profiles and at least three rejected approaches with numbers. Accept: no verdict change, no wire change, no hidden result reduction.