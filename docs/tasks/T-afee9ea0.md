---
id: T-afee9ea0
title: Fix task source consistency and dangling dependency resolution
status: done
scope:
    - internal/infra/tasksource/cardindex.go
    - internal/infra/tasksource/parse.go
    - internal/infra/tasksource/markdown.go
    - internal/infra/sqlite/tasks.go
    - internal/infra/sqlite/store.go
    - internal/app/taskdelete.go
    - cmd/beacon/main.go
priority: 2
kind: task
require_review: llm
status_reason: Already delivered in PR 2883 (commit 159d2fbd); duplicate run cancelled
---

Goal: make the card files, the in-memory index and the SQLite graph agree.
Evidence: five cards with title [perf] fail YAML parsing and are invisible to the index and DB; cardindex.go:120 compares mtime only so an unchanged mtime serves stale content; deletion is invisible because noteParsedCard covers writes only and refreshIndexedCard at :124-126 returns a cached entry when ReadFile fails; readyTasksQuery uses inner JOIN so a dangling dependency becomes ready while ListQueueTasks uses LEFT JOIN and marks it waiting_deps; TaskDeleter is wired without Source so the graph is not refreshed; duplicate IDs map first-path-wins in the index but collide on the tasks primary key; SyncTasks deletes Beacon-written claimed and done statuses because ListAvailable filters on file status only; alias fields resolve differently in markdown and issue decoders.
Steps: use size plus a content hash in the index key; invalidate on missing file independent of directory mtime; use temp file plus rename for status writes and re-read after write; pass Source into TaskDeleter and TaskUpdater in cmd/beacon/main.go; rewrite both dependency queries to treat a dangling dependency the same way and surface a clear reason; reject duplicate IDs at ingest; make SyncTasks merge statuses it does not own; route both decoders through one alias decoder; pin the five malformed [perf] cards by fixing their title quoting in a separate data change if needed.
Accept: no deleted card is returned as available, blocked verdicts match between dispatcher and queue, and a Beacon-written status survives a sync.
