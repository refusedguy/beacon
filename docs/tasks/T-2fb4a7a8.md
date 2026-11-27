---
id: T-2fb4a7a8
title: Auto-dispatch on daemon tick with config gate
status: available
scope:
    - internal/infra/config/config.go
    - internal/app/dispatcher.go
    - internal/app/reconciler.go
    - cmd/beacon/main.go
    - internal/infra/config/config_test.go
priority: 1
kind: task
require_review: llm
---

Goal: let the daemon refill agent slots on each tick so an unattended operator does not need an external dispatch loop.
Evidence: Reconciler.Run at internal/app/reconciler.go:328 calls only PromotePending. Dispatcher.Dispatch has two callers, cmd/beacon/commands.go and internal/infra/mcp/tool_dispatch.go, so a queue that empties stays empty. Agent slots idle while the operator is away. Two independent audits proposed the same seam: an AutoTopUp or Dispatch port on Reconciler next to Promoter, called after PromotePending, gated by config.
Steps:
1. Add a config section with three fields in internal/infra/config/config.go: auto_dispatch.enabled (default false), auto_dispatch.batch_size (default equal to free agent slots), auto_dispatch.dispatch_open (default true). The first is a new capability; the last is the brake so a self-feeding queue can be frozen for maintenance or a red base. Hot-reload fields must not appear in restartFields at cmd/beacon/config_watch.go.
2. Add a Dispatch port to Reconciler in internal/app/reconciler.go and call it from Reconciler.Run after PromotePending only when enabled and dispatch_open is true. Pass a budget from Dispatcher.Available so no cap is exceeded.
3. Apply the same dispatch_open predicate at the top of Dispatcher.planDispatch and planPromotion in internal/app/dispatcher.go so manual dispatch cannot bypass the brake.
4. Wire the port in cmd/beacon/main.go wireServices using the existing Dispatcher instance; no new table or store is needed.
5. Emit a decision row on each auto dispatch with count of started runs and skips, so the operator can see what the daemon started.
6. Tests: disabled by default, enabled starts up to budget, dispatch_open false starts nothing including manual path, promotion and auto dispatch do not double start the same task.
Accept: with auto_dispatch enabled the daemon keeps slots occupied without an external loop, and dispatch_open false freezes intake while active runs keep reconciling.
