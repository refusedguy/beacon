---
id: T-b0abc363
title: Surface discarded CLI errors
status: in_progress
scope:
    - cmd/beacon/commands.go
    - cmd/beacon/main.go
    - cmd/beacon/http.go
depends:
    - T-57d00a13
    - T-da7b1713
priority: 64
kind: task
require_review: llm
---

Goal: Log errors that are currently dropped.

Evidence: final-10. commands.go drops readConnection, main.go drops filepath.Abs and group.Wait, http.go drops Shutdown.

Steps: Add logDiscard(what, err) and call it at those sites. Leave Store.Close and Shutdown data-loss handling explicit.

Check: Command and HTTP tests pass.

Accept: No startup failure from a formerly advisory error.
