---
id: T-4dbbf135
title: Consolidate weak-machine verification rule
status: available
scope:
    - pkg/core/prompt_text.go
    - internal/app/dispatcher.go
    - internal/infra/config/config.go
    - cmd/beacon/init.go
    - internal/app/dispatcher_test.go
    - internal/infra/config/config_test.go
depends:
    - T-c06cb9d8
    - T-64fe8d76
priority: 86
kind: task
require_review: llm
---

Goal: One constant states the weak-machine verification rule.

Evidence: final-13. The rule appears in dispatcher.prePRCheck, DefaultWorkerInstructions and four init stack variants. Without --weak-pc, init tells the worker to run the full suite while prePRCheck calls it advisory, so one prompt can carry two opposing instructions.

Steps: Put the rule in pkg/core and derive the config default, init variants and spawn prompt from it. Keep the required gofmt -l and targeted go test commands.

Check: Config, init and dispatcher tests pass with the required substrings intact.

Accept: High behaviour risk. Do not remove the local format and test gate.
