---
id: T-ac47cc54
title: Fix sidebar queue filter using canonical project root
status: available
scope:
    - .opencode/plugins/beacon-project/ui/ui_model.js
    - .opencode/plugins/beacon-project/state/location.js
    - .opencode/plugins/beacon-project/ui/ui_model_test.js
depends:
    - T-254577a6
priority: 96
kind: task
require_review: llm
---

Goal: The sidebar queue is never emptied by a root that cannot see docs/tasks.

Evidence: opencode.log shows POST /api/rpc/beacon.project.status/snapshot returning 400 every 10 seconds, so the client falls back to CLI. A direct beacon status --snapshot returns 50 queue items with 18 ready, normalizeSnapshot keeps all 50, and cardIsPending(projectRoot, id) returns true for ready cards. The same log shows the plugin loading from .beacon/worktrees/T-* worktrees, whose docs/tasks do not contain those cards. createStatusSource applies cardIsPending with the root from ctx.location, so a worktree root filters the whole queue away while active and recent rows still render.

Steps: Resolve the canonical project root once in createStatusSource and use it for cardIsPending. When the resolved root has no docs/tasks directory, treat the location as unverified and skip the filter instead of returning an empty queue. Add a test with a worktree-shaped location and a snapshot with ready cards; assert the queue survives.

Check: With a location pointing at a worktree and 18 ready cards in the store, the panel renders Queue 18 and Dispatch (18 ready).

Accept: Keep RPC control and dispatch paths unchanged. Do not remove the pending-card filter for a verified main root.
