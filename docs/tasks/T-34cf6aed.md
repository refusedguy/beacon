---
id: T-34cf6aed
title: Add event-specific eligible-state queries
status: available
scope:
    - pkg/core/run.go
    - internal/app/reconcile_observation.go
    - internal/app/base_red.go
    - internal/app/status.go
    - pkg/core/run_test.go
depends:
  - T-e86b55f7
priority: 74
kind: task
require_review: llm
---

Goal: Event-specific CanTake* queries instead of repeated inline state sets.

Evidence: final-11. The set waiting_ci or running appears three times, a different set appears in base_red, and status uses another. Four places encode eligible states.

Steps: Add CanTakeBehind, CanTakeCheckFailed and CanTakeCheckPending on RunState and call them from the four sites.

Check: Transition tests pass with CodeInvalidTransition still returned for rejected events.

Accept: Keep the sets event-specific. Do not widen one set to cover another event.
