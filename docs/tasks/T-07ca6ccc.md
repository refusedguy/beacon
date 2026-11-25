---
id: T-07ca6ccc
title: Register MCP tools from the catalog and derive control verbs
status: done
scope:
    - internal/infra/mcp/registry.go
    - internal/infra/mcp/catalog.go
    - internal/infra/mcp/tool_control.go
    - internal/infra/mcp/tool_watch_pr.go
    - internal/infra/mcp/tool_dispatch.go
    - internal/infra/mcp/tool_status.go
    - internal/infra/mcp/tool_wait.go
    - internal/infra/mcp/tool_task_create.go
    - internal/infra/mcp/catalog_test.go
    - cmd/beacon/wire_vocabulary_test.go
priority: 90
kind: task
require_review: llm
---

Goal: Make ToolCatalog the single source for tool names, descriptions and control verbs.

Evidence: Seven hand-written msdk.Tool literals repeat catalog names, and the control description omits nudge.

Scope: MCP tool files, registry.go, catalog.go, catalog_test.go and cmd/beacon/wire_vocabulary_test.go.

Steps: Build each tool from catalogTool. Derive the control description from core.ControlActionNames. Add a test comparing core, TUI and plugin verb lists.

Accept: rg finds no &msdk.Tool{ outside catalog.go, dropping nudge from core fails the test, and go test ./internal/infra/mcp passes.

Open a PR, call beacon_watch_pr, do not self-merge.
