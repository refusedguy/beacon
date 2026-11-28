---
id: T-0fe7ce45
title: Keep Beacon-owned done rows through SyncTasks prune
status: available
scope:
    - internal/infra/sqlite/tasks.go
    - internal/infra/sqlite/tasks_test.go
priority: 1
kind: task
require_review: llm
---

Goal: a card marked done by Beacon must stay unavailable to dispatch after the next task sync.
Evidence: planDispatch syncs only Source.ListAvailable, which returns cards with status available, then writeSyncRows calls pruneSyncRows and deletes every row not in that set. SetTaskStatus(done) is erased on the next sync, so a card whose frontmatter reappears as available is reinserted and becomes ready. T-79faadd4, T-afee9ea0 and T-d0ffe6da were dispatched twice because of this.
Steps: pass the owned set already computed by SyncTasks into writeSyncRows; exclude owned IDs from the DELETE in pruneSyncRows; add a test that a done row survives a sync whose source still lists the card as available, and that ListReadyTasks excludes it.
Accept: after SetTaskStatus(done) the card stays out of ListReadyTasks across any number of SyncTasks calls.
