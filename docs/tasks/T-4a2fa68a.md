---
id: T-4a2fa68a
title: Unify automatic rebase cap constant
status: done
scope:
    - internal/app/reconcile_observation.go
    - internal/infra/config/config.go
priority: 76
kind: task
require_review: llm
---

Goal: One definition of the default automatic rebase cap.

Evidence: quality-07 item 6. Two constants both hold 2. Config now has max_auto_rebases, so the reconciler should reference the exported default.

Steps: Export one constant in config and use it in the reconciler fallback. Keep the zero-field fallback for test-built reconcilers.

Check: Default behaviour stays two.
