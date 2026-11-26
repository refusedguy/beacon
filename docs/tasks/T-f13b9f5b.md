---
id: T-f13b9f5b
title: Unify ID normalization in task mutations
status: done
scope:
    - internal/app/taskdelete.go
    - internal/app/taskdelete_test.go
    - internal/app/taskupdate.go
    - internal/app/taskupdate_test.go
priority: 85
kind: task
require_review: llm
---

Goal: Give update and delete one ID normalization rule.

Evidence: normalizeIDs, normalizeUpdates and validateBatchInputs each trim, drop blanks, dedupe and validate IDs.

Scope: internal/app/taskdelete.go, internal/app/taskdelete_test.go, internal/app/taskupdate.go, internal/app/taskupdate_test.go.

Steps: Extract one helper. Call it from both mutation paths. Add cases for padded and duplicate IDs.

Accept: Both paths reject invalid IDs and collapse padded duplicates identically, and go test ./internal/app passes.

Open a PR, call beacon_watch_pr, do not self-merge.
