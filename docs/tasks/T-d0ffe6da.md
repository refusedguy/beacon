---
id: T-d0ffe6da
title: Update init test to namespaced permission actions
status: done
scope:
    - cmd/beacon/init_test.go
    - cmd/beacon/init.go
priority: 1
kind: task
require_review: llm
status_reason: Test expectation updated in PR 2895 (beacon_beacon_task_create)
---

Goal: make go test ./cmd/beacon pass on main again.
Evidence: TestMergeOpenCodeConfigPreservesUserKeys at cmd/beacon/init_test.go:239 expects an unprefixed beacon_task_create entry. After PR 2888 generated permissions use namespaced actions such as beacon_beacon_task_create, which matches what OpenCode v2.4.2 checks. The worker for T-4de758ed reproduced the failure on clean HEAD afe1218b and stopped because the file is outside its scope. This means the root app and command test jobs are red on every branch, blocking unrelated PRs.
Steps: update the expected permission names in the test to the namespaced catalog actions, including beacon_beacon_control, beacon_beacon_reconcile and beacon_beacon_task_start; keep assertions that a custom user permission survives regeneration; check init.go for any remaining unprefixed action string that writes into generated config and update it if present; add a test that a generated config contains no unprefixed beacon_ action names.
Accept: go test ./cmd/beacon passes on clean main and CI command and app jobs turn green.
