---
id: T-a5ae7a69
title: Bound decision and event tables
status: available
scope:
    - internal/infra/sqlite/schema.go
    - internal/infra/sqlite/store.go
    - docs/perf/sqlite-deep.md
depends:
    - T-9a5dc247
priority: 86
kind: task
require_review: llm
---

Goal: Append-only audit tables cannot grow forever.

Evidence: quality-14. decisions and run_events have no retention while deliveries and approvals are pruned at 30 days. Snapshot caps the wire but the database and WAL grow.

Steps: Add retention with the existing prune pattern and record write cost. Keep recent decision history needed by status snapshot.

Check: A seeded old database shrinks after a prune tick and recent decisions stay readable.
