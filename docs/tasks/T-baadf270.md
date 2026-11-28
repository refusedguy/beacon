---
id: T-baadf270
title: Bound terminal run retention and decision cache
status: dropped
scope:
    - internal/infra/sqlite/store.go
    - internal/infra/sqlite/schema.go
    - internal/app/decisionlog.go
    - internal/app/reconciler.go
priority: 2
kind: task
require_review: llm
status_reason: Superseded by atomic split cards that satisfy the 3-2-1200 scope cap; intent preserved in the new cards.
---

Goal: keep state.db size predictable in a long-running daemon.
Evidence: the live state.db is 8.2 MB after four days with 685 runs, 12,566 decisions and 3,188 run_events. PruneStale at store.go:269-308 deletes deliveries, approvals, decisions and run_events older than 30 days but never deletes runs. 674 of 685 runs are terminal; at the current rate that adds about 128 MB a year. decisionCache.sigs at decisionlog.go:54-76 stores one signature per run forever, about 80 KB now and 15 to 20 MB at 100,000 runs. Age pruning leaves a high-water mark because page_count stays at 164 with freelist_count 57 after a prune.
Steps: add a bounded terminal-run pass to PruneStale that keeps the newest 20,000 merged, closed or dropped runs and deletes older ones in the existing 1000 row batches; check base_incidents.fixer_run_id before deleting a run; add LRU eviction to decisionCache.sigs capped at a few thousand entries; add a VACUUM or incremental_vacuum policy decision with a measured tradeoff rather than enabling it blindly; add tests for the retention boundary and the cache cap.
Accept: disk growth stops at a bounded high-water mark, snapshot latency is unchanged, and no fixer incident points at a deleted run.
