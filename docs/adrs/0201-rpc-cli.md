# ADR 201: Имена спанов и метрик: RPC/CLI семконвы и низкая кардинальность

Статус: accepted

## Контекст

Semantic conventions требуют имя RPC-спана = rpc.method, атрибуты rpc.system/service/method и error.type; CLI-спан имеет process.executable.name, process.exit.code, process.pid. t3code запрещает аргументы инструментов в атрибутах и высокую кардинальность в метриках.

## Решение

MCP-инструменты получают спан mcp.tool.<name> с rpc.system=jsonrpc и rpc.service=beacon.mcp. CLI beacon следует cli-spans. Метрики именуются beacon_* и проецируются в Prometheus как beacon_*_total и *_seconds. Семь требуемых счётчиков: watch.registered, verdict.published, merge.attempt, wake.delivered, rebase.requested, poll.cycles, adapter.errors.

## Последствия

Высокую кардинальность (watchID, session ID, пути, аргументы) нести на спанах, в метриках только role, verdict, outcome, adapter, reason, component. Миграция rpc.server.duration в rpc.server.call.duration требует OTEL_SEMCONV_STABILITY_OPT_IN=rpc. process.command_args собирать только после санитизации.
