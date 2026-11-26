---
id: T-fbfa62a8
title: Extract PRAGMA column scanner
status: in_progress
scope:
    - internal/infra/sqlite/schema.go
    - internal/infra/sqlite/tasks.go
priority: 76
kind: task
require_review: llm
---

Goal: One presentColumns helper for table_info reads.

Evidence: final-04. ensureRunColumns and ensureTaskColumns scan the same six values and repeat rows.Close and rows.Err handling.

Steps: Extract presentColumns(db, table) and call it from both migration paths.

Check: Migration tests pass on a fresh database and an existing database.

Accept: Preserve close-on-scan-error behaviour.
