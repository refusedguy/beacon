---
id: T-b8d8c6f8
title: Add strict TUI wire contract fixtures
status: available
scope:
    - ui/tui/wire_contract_test.go
    - cmd/beacon/http.go
depends:
  - T-3f0a09be
priority: 78
kind: task
require_review: llm
---

Goal: A new daemon field or event shape fails a test.

Evidence: final-15. Unknown JSON fields are ignored and only the harness WireEvent shape is pinned, while the events route carries three shapes.

Steps: Add a strict decoder that rejects unknown keys and fixtures for ping and pr_changed.

Check: The contract test fails when a field is added to the daemon DTO.

Accept: No wire shape change.
