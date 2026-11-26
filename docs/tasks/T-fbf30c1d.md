---
id: T-fbf30c1d
title: Inline closed PR verdict check
status: available
scope:
    - internal/app/reconciler.go
    - internal/app/reconcile_observation.go
depends:
    - T-4a2fa68a
priority: 66
kind: task
require_review: llm
---

Goal: Remove a one-caller wrapper around core.DecideFinalState.

Evidence: quality-07 item 7. closedPRVerdict has one caller in Reconcile.

Steps: Inline the classifier call there and delete the wrapper. Keep the same wait and finalize decisions.

Check: Reconciler tests pass with the same verdicts.
