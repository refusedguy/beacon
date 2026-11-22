# ADR 308: Протокольная композиция Beacon: MCP для управления, CLI для диагностики, push для пробуждения

Статус: proposed

## Контекст

Источник: docs/research/audit-04-protocols.md. В main сосуществуют четыре числа инструментов: 10 дескрипторов в internal/infra/mcp/server.go:141-241, 11 advertised имен с beacon_wake в server.go:258-262 и cmd/beacon/mcp.go:521, 9 в AGENTS.md:107, 7 в remote-карте internal/infra/mcp/client.go:290-308. Исторический замер tools/list дал 17922 байт при девяти инструментах (docs/adrs/0268-task-claim-tool.md:25), позже контракт вырос до 18212 байт (docs/adrs/0270-task-close-tool.md:9). AUD-09 и AUD-10 уже ведут исправление списков.

## Решение

Разделить возможности по каналу. MCP оставить для инструментов, которые меняют состояние цикла внутри хода агента и требуют типизированных ошибок: watch_pr, ack, cancel_watch, task_claim, task_close, defer_wiring, get_result. Диагностику list_running и stats отдать CLI и SKILL.md, где уже есть beacon inspect, beacon watch и beacon board. beacon_wait_result оставить только режимом совместимости. Основное пробуждение идёт через Wake и Ladder, после чего агент читает результат через get_result. beacon_wake не показывать в каталоге агента.

## Последствия

Каталог агента становится короче, а каждое описание имеет одного владельца. Требуется контрактный тест на равенство advertised-набора, remote-карты и документации. Точная экономия токенов не заявлена, её нужно измерить в CI. AUD-09 и AUD-10 сохраняют владельцев своих файлов.
