---
id: T-f88f0d8e
title: Single config default path
status: available
scope:
    - internal/infra/config/config.go
    - cmd/beacon/init.go
    - internal/infra/config/config_test.go
priority: 74
kind: task
require_review: llm
---

Goal: Defaults are filled once.

Evidence: quality-04 item 4. applyDefaults and the init config literal repeat state DB and workspace values.

Steps: Let init write only detected values and call applyDefaults. Keep explicit base branch, poll interval and worker instructions.

Check: Generated configs are byte-identical for the existing init fixtures.
