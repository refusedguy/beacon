# Beacon roadmap status

Триаж `docs/tasks/` по состоянию main после Waves 1–4 (2026-11-20).

Дата пересчёта: 2026-11-21
Baseline SHA: 800eea13496573cc750856d067f2729912664cf2

- Закрыто done: **655**
- Переведено dropped: **326**
- Осталось реальных todo: **11**

## Приоритеты
- P1: 2
- P2: 9

## Сырой счёт статусных строк

Счёт по строкам `Статус:` в `docs/tasks/*.md` на baseline выше, без разрешения по git log:

| Статус | Файлов |
|---|---|
| done | 607 |
| dropped | 326 |
| todo | 45 |

Фазы среди todo: P0 3, P2 4, P3 33, P5 5.

Команда: `git grep -l '^Статус: done\|^Статус: todo\|^Статус: dropped' <baseline> -- 'docs/tasks/*.md'`


## Реестр оставшихся todo

| ID | Приоритет | OwnerFiles |
|---|---|---|
| BS.11 | P1 | internal/infra/store/observability.go, internal/infra/store/db.go |
| BS.12 | P1 | internal/infra/store/db.go, internal/infra/store/atomic_write.go |
| BS.13 | P1 | internal/infra/store/receipts.go, internal/infra/store/db.go |
| BS.14 | P1 | internal/app/orchestrator/daemon.go, internal/infra/store/db.go |
| BS.15 | P1 | internal/app/orchestrator/workload.go, internal/infra/store/db.go |
| BS.16 | P1 | internal/infra/wake/notify.go, internal/infra/store/db.go |
| BS.17 | P1 | internal/infra/observe/query.go, internal/infra/store/observability.go |
| BS.18 | P1 | internal/infra/store/observability.go, internal/infra/store/archive.go |
| BS.19 | P1 | internal/infra/mcp/server.go, internal/infra/mcp/tool_wait_result.go |
| BS.21 | P1 | internal/domain/identity.go |
| BS.22 | P1 | internal/infra/store/observability.go, internal/infra/store/redaction.go |
| HD-070 | P1 | AGENTS.md, tools/beacon-gates/gate/gate.go |
| HD-085 | P1 | .github/workflows/docs.yml, tools/beacon-gates/gate/gate.go, tools/beacon-gates/gate/config/baseline.json |
| T3.235 | P1 | internal/infra/opencode/server.go, internal/domain/capabilities.go |
| T3.237 | P1 | internal/app/orchestrator/daemon.go, internal/infra/store/db.go |
| T5.55 | P1 | cmd/beacon/registry_sync.go, cmd/beacon/registry_sync_test.go |
| S2.005 | P1 | internal/infra/httpapi/server.go, internal/infra/httpapi/client.go |
| BS.1 | P2 | cmd/beacon/daemon.go, internal/infra/store/db.go |
| BS.2 | P2 | cmd/beacon/daemon.go, internal/infra/observe/metrics.go |
| BS.23 | P2 | cmd/beacon/task.go, internal/app/orchestrator/workload.go |
| BS.24 | P2 | cmd/beacon/task.go, internal/app/orchestrator/workload.go |
| BS.25 | P2 | internal/infra/cibridge/bridge.go, internal/infra/registry/secretguard.go |
| BS.26 | P2 | internal/infra/store/receipts.go, internal/infra/store/db.go |
| BS.27 | P2 | internal/infra/cibridge/bridge.go, internal/app/orchestrator/store.go |
| BS.28 | P2 | internal/infra/cibridge/bridge.go, internal/infra/github/points.go |
| BS.29 | P2 | internal/infra/store/watch_bindings.go, internal/infra/wake/registry.go |
| BS.31 | P2 | internal/app/orchestrator/workload.go, cmd/beacon/task.go |
| BS.32 | P2 | cmd/beacon/task.go, internal/infra/mcp/server.go |
| BS.33 | P2 | internal/infra/store/db.go, internal/app/orchestrator/store.go |
| BS.63 | P2 | .opencode/skills/night-shift/SKILL.md, internal/app/orchestrator/daemon.go |
| BS.79 | P2 | opencode.json, .opencode/plugins/beacon-project/mcp.js |
| HD-006 | P2 | docs/templates/executor-task.md, docs/templates/orchestrator-task.md |
| HD-009 | P2 | docs/templates/executor-task.md, .opencode/skills/red-verdict-triage/SKILL.md |
| HD-011 | P2 | AGENTS.md, tools/worktree/ |
| HD-018 | P2 | docs/templates/executor-task.md, AGENTS.md |
| HD-023 | P2 | cmd/beacon/commands.go, tools/beacon-gates/gate/gate.go |
| HD-029 | P2 | AGENTS.md, docs/templates/executor-task.md |
| HD-037 | P2 | tools/beacon-gates/gate/gate.go, evals/main.go |
| HD-057 | P2 | tools/beacon-gates/gate/gate.go, AGENTS.md |
| HD-063 | P2 | tools/beacon-gates/gate/config/baseline.json, evals/main.go |
| HD-064 | P2 | docs/templates/orchestrator-task.md, AGENTS.md |
| HD-071 | P2 | skills-lock.json, tools/beacon-gates/gate/gate.go, tools/beacon-gates/gate/config/baseline.json |
| HD-077 | P2 | docs/templates/executor-task.md, docs/templates/orchestrator-task.md, AGENTS.md |
| HD-080 | P2 | tools/beacon-gates/gate/gate.go, tools/beacon-gates/gate/exceptions.go, AGENTS.md |
| HD-081 | P2 | .github/pull_request_template.md, docs/templates/executor-task.md |
| HD-086 | P2 | tools/beacon-gates/gate/gate.go, tools/beacon-gates/gate/gate_test.go, .github/workflows/docs.yml |
| HD-088 | P2 | docs/FACTS.md, AGENTS.md, tools/beacon-gates/gate/gate.go |
| HD-092 | P2 | tools/beacon-gates/gate/gate.go, tools/beacon-gates/gate/gate_test.go, tools/beacon-gates/gate/config/baseline.json |
| HD-105 | P2 | docs/SKILL-MAP.md, docs/adrs/0253-harbor-beacon.md |
| HD-107 | P2 | .opencode/skills/gate-result/SKILL.md, tools/beacon-gates/gate/gate.go, docs/templates/executor-task.md |
| HD-119 | P2 | tools/beacon-gates/gate/gate.go, tools/beacon-gates/gate/gate_test.go, docs/AGENT-RULES.md |
| S3.005 | P2 | internal/infra/wake/acp.go, internal/infra/wake/acp_test.go |
| S6.008 | P2 | internal/infra/httpapi/server.go, internal/infra/httpapi/server_test.go |
| T3.234 | P2 | internal/infra/mcp/server.go, internal/infra/mcp/tool_wait_result.go |
| T3.236 | P2 | internal/infra/mcp/server.go, internal/infra/mcp/tool_get_result.go |
| T3.238 | P2 | internal/infra/mcp/tool_wake.go, internal/domain/types.go |
| T3.239 | P2 | internal/infra/mcp/tool_wake.go, internal/infra/mcp/tool_wake_test.go |
| T3.241 | P2 | internal/domain/identity.go, internal/infra/wake/registry.go |
| T3.242 | P2 | internal/infra/mcp/tool_claim.go, internal/infra/wake/registry.go |
| T3.248 | P2 | internal/infra/wake/registry.go, internal/infra/wake/notify.go |
| T3.278 | P2 | internal/infra/wake/acp.go, internal/infra/wake/acp_test.go |
| T3.283 | P2 | internal/infra/wake/notify.go, internal/infra/wake/notify_test.go |
| T3.341 | P2 | evals/README.md, tests/e2e/README.md |
| T3.344 | P2 | internal/infra/registry/secretguard.go, tools/beacon-gates/gate/gate.go |
| T3.346 | P2 | internal/infra/store/migrations/, internal/infra/store/db.go |
| T3.348 | P2 | internal/infra/mcp/contract_test.go, cmd/beacon/commands.go |
| T3.375 | P2 | internal/infra/store/webhook_events.go, internal/infra/store/db.go, internal/domain/identity.go, docs/adrs/0183-webhook-event-id.md |
| T3.376 | P2 | internal/infra/webhook/receiver.go, internal/infra/webhook/signature.go, cmd/beacon/webhook.go |
| T3.377 | P2 | internal/infra/webhook/match.go, internal/infra/webhook/match_test.go, internal/infra/cibridge/bridge.go |
| T3.384 | P2 | internal/app/orchestrator/subscribe.go, internal/app/orchestrator/subscribe_test.go, cmd/beacon/subscribe.go |
| T3.386 | P2 | internal/app/orchestrator/daemon.go, internal/app/orchestrator/daemon_test.go, cmd/beacon/daemon.go |
| T3.401 | P2 | internal/infra/mcp/tool_get_result.go |
| T3.406 | P2 | .opencode/skills/red-verdict-triage/SKILL.md, AGENTS.md |
| T3.412 | P2 | internal/app/orchestrator/orchestrator.go, internal/infra/mcp/tool_ack.go |
| T3.413 | P2 | internal/app/orchestrator/daemon.go |
| T3.414 | P2 | internal/infra/cibridge/bridge.go, internal/infra/wake/deliver.go, cmd/beacon/mcp.go, internal/app/orchestrator/daemon.go |
| T3.415 | P2 | internal/infra/wake/deliver.go, internal/infra/store/db.go |
| T3.427 | P2 | evals/main.go, docs/evals/blocked-watch.md |
| T3.448 | P2 | docs/templates/executor-task.md, docs/templates/orchestrator-task.md |
| T5.36 | P2 | internal/domain/types.go, internal/infra/mcp/tool_get_result.go |
| T5.49 | P2 | cmd/beacon/registry_sync.go, internal/infra/mcp/tool_wake.go |
| T5.51 | P2 | cmd/beacon/registry_stop.go, cmd/beacon/registry_stop_test.go |
| T5.52 | P2 | cmd/beacon/registry_stop.go, cmd/beacon/registry_stop_test.go |
| T5.53 | P2 | cmd/beacon/registry_list.go, cmd/beacon/registry_list_test.go |
| T5.54 | P2 | cmd/beacon/registry_check.go, cmd/beacon/registry_check_test.go |
| T5.56 | P2 | cmd/beacon/registry_stop.go, cmd/beacon/main.go |
| T5.58 | P2 | cmd/beacon/watch.go, cmd/beacon/watch_test.go |
| T5.60 | P2 | internal/infra/mcp/tool_stats.go, internal/infra/mcp/tool_stats_test.go |
| BS.10 | P3 | docs/adrs/0189-internal-scheduler.md, docs/adrs/0190-mcp.md, docs/adrs/0198-outbox.md |
| BS.3 | P3 | cmd/beacon/doctor.go, cmd/beacon/daemon.go |
| BS.34 | P3 | internal/infra/mcp/tool_list_running.go, internal/infra/mcp/tool_get_result.go |
| BS.35 | P3 | internal/infra/mcp/tool_list_running.go, cmd/beacon/task.go |
| BS.36 | P3 | internal/infra/observe/query.go, cmd/beacon/logs.go |
| BS.37 | P3 | internal/infra/mcp/tool_ack.go, internal/infra/wake/notify.go |
| BS.38 | P3 | cmd/beacon/watch.go, cmd/beacon/attach.go |
| BS.39 | P3 | cmd/beacon/watch.go, internal/infra/mcp/tool_ack.go |
| BS.4 | P3 | cmd/beacon/main.go, internal/infra/mcp/tool_stats.go |
| BS.6 | P3 | cmd/beacon/mcp.go, internal/infra/httpapi/client.go |
| BS.40 | P3 | internal/infra/mcp/server.go, internal/infra/observe/query.go |
| BS.42 | P3 | cmd/beacon/mcp.go, internal/infra/mcp/server.go |
| BS.43 | P3 | internal/infra/mcp/server.go, cmd/beacon/mcp.go |
| BS.45 | P3 | internal/infra/observe/metrics.go, cmd/beacon/observe.go |
| BS.46 | P3 | internal/infra/observe/observe.go, internal/infra/store/observability.go |
| BS.48 | P3 | internal/infra/observe/log.go, internal/infra/observe/otel.go |
| BS.49 | P3 | internal/infra/observe/metrics.go, internal/infra/observe/observe.go |
| BS.50 | P3 | internal/infra/store/observability.go, cmd/beacon/inspect.go |
| BS.51 | P3 | internal/infra/observe/metrics.go, internal/infra/store/observability.go |
| BS.52 | P3 | internal/infra/mcp/contract_test.go, cmd/beacon/checkall.go |
| BS.67 | P3 | internal/domain/capabilities.go, internal/infra/store/registry.go |
| BS.73 | P3 | evals/main.go, evals/README.md |
| HD-007 | P3 | tools/beacon-gates/gate/config/baseline.json, docs/templates/executor-task.md |
| HD-010 | P3 | docs/templates/executor-task.md, docs/templates/orchestrator-task.md |
| HD-012 | P3 | docs/templates/executor-task.md, docs/templates/orchestrator-task.md |
| HD-013 | P3 | docs/templates/executor-task.md, docs/templates/orchestrator-task.md |
| HD-014 | P3 | docs/templates/executor-task.md, docs/templates/orchestrator-task.md |
| HD-015 | P3 | docs/templates/executor-task.md, AGENTS.md |
| HD-016 | P3 | docs/templates/orchestrator-task.md, docs/templates/executor-task.md |
| HD-019 | P3 | cmd/batch-apply/main.go, internal/infra/batchapply/apply.go |
| HD-020 | P3 | evals/main.go, docs/gates/ |
| HD-022 | P3 | AGENTS.md, tools/beacon-gates/gate/config/baseline.json |
| HD-024 | P3 | AGENTS.md, docs/templates/executor-task.md |
| HD-028 | P3 | docs/templates/executor-task.md, AGENTS.md |
| HD-031 | P3 | AGENTS.md, docs/templates/orchestrator-task.md |
| HD-036 | P3 | AGENTS.md, docs/templates/executor-task.md |
| HD-038 | P3 | evals/main.go, evals/README.md |
| HD-039 | P3 | cmd/batch-apply/main.go, internal/infra/batchapply/apply.go |
| HD-040 | P3 | README.md, AGENTS.md |
| HD-041 | P3 | tools/beacon-gates/gate/gate.go, tools/beacon-gates/gate/config/baseline.json |
| HD-043 | P3 | AGENTS.md, docs/templates/orchestrator-task.md |
| HD-045 | P3 | evals/main.go, AGENTS.md |
| HD-046 | P3 | tools/beacon-gates/gate/gate.go, evals/main.go |
| HD-047 | P3 | AGENTS.md, docs/runbooks/verification.md |
| HD-048 | P3 | evals/main.go, evals/README.md |
| HD-051 | P3 | AGENTS.md, docs/gates/ |
| HD-052 | P3 | docs/templates/executor-task.md, docs/templates/orchestrator-task.md |
| HD-053 | P3 | AGENTS.md, docs/templates/executor-task.md |
| HD-054 | P3 | AGENTS.md, README.md |
| HD-055 | P3 | evals/main.go, docs/templates/ |
| HD-058 | P3 | AGENTS.md, docs/templates/orchestrator-task.md |
| HD-059 | P3 | AGENTS.md, docs/adrs/ |
| HD-060 | P3 | tools/beacon-gates/gate/gate.go, AGENTS.md |
| HD-061 | P3 | README.md, AGENTS.md |
| HD-062 | P3 | AGENTS.md, docs/templates/orchestrator-task.md |
| HD-065 | P3 | evals/cmd/evalserver/main.go, evals/run.sh |
| HD-066 | P3 | cmd/batch-apply/main.go, internal/infra/batchapply/apply.go |
| HD-067 | P3 | AGENTS.md, docs/ |
| HD-069 | P3 | .opencode/skills/, AGENTS.md |
| HD-079 | P3 | AGENTS.md, docs/templates/orchestrator-task.md |
| HD-084 | P3 | docs/templates/night-shift-report.md, .opencode/skills/night-shift/SKILL.md, AGENTS.md |
| HD-089 | P3 | docs/INDEX.md, tools/beacon-gates/gate/gate.go, tools/beacon-gates/gate/gate_test.go |
| HD-091 | P3 | docs/archive/README.md, docs/templates/orchestrator-task.md |
| HD-093 | P3 | docs/templates/executor-task.md, docs/templates/orchestrator-task.md, tools/beacon-gates/gate/gate.go |
| HD-098 | P3 | AGENTS.md, docs/templates/executor-task.md |
| HD-100 | P3 | docs/AGENT-RULES.md, AGENTS.md |
| HD-101 | P3 | docs/templates/research-task.md, AGENTS.md |
| HD-104 | P3 | AGENTS.md, docs/templates/executor-task.md |
| HD-106 | P3 | docs/SKILL-MAP.md, AGENTS.md |
| HD-120 | P3 | AGENTS.md, docs/templates/executor-task.md |
| HD-121 | P3 | AGENTS.md |
| T3.217 | P3 | internal/infra/wake/registry.go, cmd/beacon/deliver.go, docs/evals/w05-pi-rpc-wake.md |
| T3.240 | P3 | docs/design/, docs/adrs/ |
| T3.243 | P3 | .opencode/plugins/beacon-bridge.js, internal/infra/wake/notify.go |
| T3.244 | P3 | .opencode/plugins/beacon-bridge.js, internal/infra/wake/notify.go |
| T3.245 | P3 | .opencode/plugins/beacon-bridge.js, internal/infra/wake/notify.go |
| T3.246 | P3 | .opencode/plugins/beacon-bridge.js, internal/infra/wake/notify.go |
| T3.247 | P3 | .opencode/plugins/beacon-bridge.js, internal/infra/wake/notify.go |
| T3.249 | P3 | internal/infra/observe/log.go, internal/infra/observe/query.go |
| T3.250 | P3 | cmd/beacon/logs.go, internal/infra/observe/query.go |
| T3.279 | P3 | internal/infra/wake/acp.go, internal/infra/wake/acp_test.go |
| T3.284 | P3 | internal/infra/httpapi/server.go, internal/infra/httpapi/client.go |
| T3.285 | P3 | internal/infra/wake/acp.go, internal/infra/wake/acp_test.go |
| T3.342 | P3 | tests/chaos/, tests/e2e/ |
| T3.350 | P3 | evals/main.go, evals/README.md |
| T3.360 | P3 | internal/infra/store/redaction.go, internal/infra/observe/log.go |
| T3.378 | P3 | internal/infra/webhook/workflow_run.go, internal/infra/webhook/workflow_run_test.go |
| T3.393 | P3 | internal/infra/observe/otel.go, cmd/beacon/observe.go |
| T3.394 | P3 | internal/app/orchestrator/daemon.go, internal/infra/cibridge/bridge.go, internal/infra/wake/deliver.go |
| T3.395 | P3 | internal/app/orchestrator/orchestrator.go, internal/infra/observe/observe.go |
| T3.399 | P3 | internal/app/orchestrator/daemon.go, internal/domain/types.go |
| T3.400 | P3 | internal/infra/cibridge/bridge.go, internal/app/orchestrator/daemon.go, internal/domain/types.go |
| T3.402 | P3 | internal/infra/mcp/tool_get_result.go |
| T3.404 | P3 | docs/templates/executor-task.md, tests/adapter/executor-template.test.js |
| T3.407 | P3 | .opencode/skills/red-verdict-triage/SKILL.md |
| T3.409 | P3 | docs/templates/executor-task.md |
| T3.416 | P3 | internal/app/orchestrator/daemon.go, internal/infra/wake/deliver.go |
| T3.417 | P3 | internal/infra/observe/observe.go, internal/infra/wake/deliver.go |
| T3.421 | P3 | internal/infra/mcp/tool_ack.go, internal/infra/store/db.go |
| T3.428 | P3 | internal/infra/mcp/tool_ack.go, evals/main.go |
| T3.437 | P3 | internal/app/orchestrator/orchestrator.go, internal/infra/observe/observe.go |
| T3.438 | P3 | internal/infra/observe/observe.go, internal/infra/wake/deliver.go |
| T3.439 | P3 | internal/infra/cibridge/bridge.go, internal/infra/observe/observe.go |
| T3.440 | P3 | internal/infra/mcp/tool_stats.go, internal/domain/types.go |
| T3.441 | P3 | internal/infra/observe/observe.go, internal/infra/wake/deliver.go |
| T3.442 | P3 | evals/main.go, internal/app/orchestrator/daemon.go |
| T3.443 | P3 | evals/main.go, internal/infra/mcp/tool_ack.go, internal/app/orchestrator/daemon.go |
| T3.444 | P3 | evals/main.go, internal/infra/wake/deliver.go |
| T3.445 | P3 | evals/main.go, internal/infra/mcp/tool_get_result.go |
| T3.446 | P3 | docs/templates/executor-task.md, tests/adapter/executor-template.test.js |
| T3.447 | P3 | .opencode/skills/red-verdict-triage/SKILL.md, evals/main.go |
| T3.449 | P3 | docs/adrs/README.md |
| T5.45 | P3 | internal/infra/store/receipts.go, internal/infra/store/receipts_test.go |
| T5.59 | P3 | cmd/beacon/agents.go, cmd/beacon/agents_test.go, internal/infra/opencode/client.go |
| T5.61 | P3 | tests/e2e/e2e_test.go |
| T5.63 | P3 | .github/workflows/ci.yml, tools/beacon-gates/capabilities/capabilities.go |
| T5.64 | P3 | internal/infra/mcp/tool_stats.go, internal/infra/store/observability.go |
| T5.67 | P3 | internal/infra/observe/observe.go, internal/infra/observe/coverage_test.go, internal/infra/observe/observe_test.go |
