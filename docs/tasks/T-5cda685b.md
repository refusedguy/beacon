---
id: T-5cda685b
title: Persist per-path waitlist instead of dropping scope conflicts
status: dropped
scope:
    - internal/app/planner.go
    - internal/infra/sqlite/schema.go
    - internal/infra/sqlite/store.go
    - internal/app/dispatcher.go
priority: 1
kind: task
require_review: llm
status_reason: Superseded by atomic split cards that satisfy the 3-2-1200 scope cap; intent preserved in the new cards.
---

Goal: a card skipped for scope overlap must be queued and dispatched when the path owner finishes, not skipped forever in the same order.
Evidence: 2446 active_scope_overlap skips and 241 selected_scope_overlap skips out of 3982 decisions. A run claims every path in its card from spawn to terminal, including waiting_ci, so a one-file ci.yml card can be skipped dozens of times by different holders. planner.go:110-138 drops conflicts for one cycle and logs them; nothing carries them forward. readyTasksQuery sorts by priority then ID, so the same cards lose every cycle.
Steps: add a persisted path waitlist keyed by scope path and card ID with waiting_since; when a candidate loses on a write claim, insert or update its wait entry instead of only logging; when a run reaches terminal, release its path claims and promote the longest waiter for those paths into the next dispatch cycle; keep the active-run guard and batch conflict check; add tests for a queued card winning after the holder closes and for aging within equal priority.
Accept: a card skipped for a busy path is dispatched within one cycle after that path is released, without manual retry.
