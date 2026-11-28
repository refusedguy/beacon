---
id: T-1bde70ab
title: Split store.go into single-owner seam files
status: done
scope:
    - internal/infra/sqlite/store.go
    - internal/infra/sqlite/store_runs.go
    - internal/infra/sqlite/store_decisions.go
    - internal/infra/sqlite/store_queue.go
    - internal/infra/sqlite/store_test.go
priority: 1
kind: task
require_review: llm
---

Goal: let retention, status explain and CAS work proceed in parallel instead of all claiming store.go.
Evidence: store.go is 910 lines and named by 4 available cards. The audit groups row writes and reads at :115-520, decisions and incidents at :524-812, and queue queries at :813-end.
Steps: move the three groups into the named files with no behavior change; keep the package API stable; add tests that moved methods satisfy their interfaces and that a query from each group works after the split.
Accept: three independent cards can own store_runs.go, store_decisions.go and store_queue.go without overlapping scope.
