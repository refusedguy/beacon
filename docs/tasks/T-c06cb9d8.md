---
id: T-c06cb9d8
title: Add step and logStep helpers
status: done
scope:
    - internal/app/dispatcher.go
    - internal/app/finalize.go
    - internal/app/idle.go
    - internal/app/base_red.go
depends:
  - T-b6aae5a8
  - T-e86b55f7
priority: 58
kind: task
require_review: llm
---

Goal: One wrap-and-log pair for repeated operation steps.

Evidence: final-10. Dispatcher, finalize, idle and base_red repeat fmt.Errorf(label: err) plus a slog call with run and task fields.

Steps: Add step(label, err) and logStep and route the repeated chains through them.

Check: Targeted app tests pass with the same error text.

Accept: Keep the operation name in every label.
