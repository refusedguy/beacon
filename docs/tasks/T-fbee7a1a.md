---
id: T-fbee7a1a
title: Generate MCP registration and wire field tables
status: in_progress
scope:
    - internal/infra/mcp/registry.go
    - internal/infra/mcp/catalog.go
    - internal/infra/mcp/permissions_test.go
    - docs/wire/loopback.json
    - ui/tui/wire_contract_test.go
priority: 78
kind: task
require_review: llm
---

Goal: Adding a tool or wire field touches one table.

Evidence: quality-15 items 6 and 8. Adding an MCP tool spans five places while the catalog comment names two. Understanding the monitor wire shape crosses five files.

Steps: Use one ToolSpec registration table and generate a field table beside the golden wire file.

Check: Parity tests fail when a tool or field is added without the table.
