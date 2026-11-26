---
id: T-e32bea1a
title: Narrow the store port used by control
status: available
scope:
    - internal/app/control.go
    - cmd/beacon/main_test.go
depends:
    - T-94f32745
priority: 78
kind: task
require_review: llm
---

Goal: A service declares only the store methods it calls.

Evidence: quality-11 item 1. internal/app/control.go takes core.RunStore with 19 methods but calls four.

Steps: Declare a ControlStore port in control.go and pass the real store at composition.

Check: Control tests pass.
