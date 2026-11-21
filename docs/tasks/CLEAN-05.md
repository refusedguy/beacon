# CLEAN-05. Разобрать 35 дублирующих ADR и ужать docs baseline
Статус: todo
baseline: замер 2026-11-21, origin/main = 3f575e01. В docs/adrs/ 304 md-файла без README. 34 номера ADR имеют более одного файла, всего 35 лишних файлов; у 0185 три версии. Дубли занимают 642 строки. В docs-baseline.json 35 строк DOC-ADR-DUP и baseline_limit = 40. Owner-decision в main, в GitHub и в research-документах не найдена.
Фаза: P2
Тип: task
Владелец файлов: docs/adrs/, tools/beacon-gates/gate/config/docs-baseline.json
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Удалить дубли ADR, оставив по одному файлу на группу, и убрать соответствующие строки DOC-ADR-DUP из docs baseline с снижением baseline_limit с 40 до 5.
Контекст: Источник: docs/research/cleanup-plan.md, задача 6, и docs/research/deadcode-audit.md, первая строка таблицы. 35 групп, 642 строки, примеры 0069, 0094, 0185, 0260. Гейт держит алфавитно первый файл, docsgate/docs.go:288-300, поэтому удаление без правки baseline словит DOC-BASELINE-STALE. Риск: средний. Нужен owner-decision: сохранять ли историю всех редакций ADR и считать ли канонической алфавитно первую версию.
Шаги: Дождаться решения владельца. Сверить группы дублей по заголовкам и содержимому. Удалить поздние копии, поправить docs/adrs/README.md, убрать 35 строк DOC-ADR-DUP и снизить предел baseline. Изменить файлы и baseline одним PR.
Проверка: informational docs job в CI не показывает DOC-ADR-DUP и DOC-BASELINE-STALE для затронутых путей.
Приёмка: Осталась одна версия каждой ADR, индекс ADR обновлён, baseline_limit снижен до 5. Ряд с owner-decision остаётся todo до ответа владельца.
Подтверждение: 2026-11-21, CLEAN-05. Owner-decision не зафиксирована. Коммита с решением в `git log --all --grep` нет, поиск по issue и PR в GitHub пуст, карточка в main остаётся `todo`, а в docs/research/cleanup-plan.md записано только условие «если владелец согласен считать канонической версией алфавитно первую». Владелец не ответил, поэтому docs/adrs/ и docs-baseline.json не изменены. Ниже план на случай решения.

План. Гейт собирает файлы по номеру ADR в docsgate/docs.go:250-300. Алфавитно первый файл группы остаётся, остальные получают finding DOC-ADR-DUP. В таблице указан канонический файл и файл на удаление. «Строка baseline» это номер строки поля `path` в docs-baseline.json. Всего 35 строк, из них две относятся к группе 0185.

| ADR | Строка baseline | Канонический (алфавитно первый) | Удалить |
| --- | --- | --- | --- |
| 0069 | 6 | `0069-mcp-beacon-wake.md` | `0069-opencode-plugin-project-boundary.md` |
| 0070 | 11 | `0070-universal-wake-adapter.md` | `0070-wake.md` |
| 0071 | 16 | `0071-self-hosted-loop.md` | `0071-wakeadapter.md` |
| 0072 | 21 | `0072-autonomous-merge-loop.md` | `0072-spoolsource-mcppullsource.md` |
| 0086 | 26 | `0086-mcp-beacon-wake.md` | `0086-project-scoped-agent-registry.md` |
| 0087 | 31 | `0087-configuration-identity.md` | `0087-wake.md` |
| 0088 | 36 | `0088-wakeadapter.md` | `0088-watch-registration.md` |
| 0089 | 41 | `0089-capability-index.md` | `0089-spoolsource-mcppullsource.md` |
| 0090 | 46 | `0090-registry-lifecycle.md` | `0090-honest-delivery-unchanged-ladder.md` |
| 0091 | 51 | `0091-checked-stop.md` | `0091-mcp.md` |
| 0092 | 56 | `0092-background-server-source.md` | `0092-tui-bubble-tea-ui-beacon.md` |
| 0093 | 61 | `0093-patch-inbox.md` | `0093-registry-limits.md` |
| 0094 | 66 | `0094-delivery-steer-queue.md` | `0094-seven-tools.md` |
| 0095 | 71 | `0095-cli-opencode-api.md` | `0095-restart-reconcile.md` |
| 0155 | 76 | `0155-blocked-watch.md` | `0155-mcp.md` |
| 0156 | 81 | `0156-orchestrator-workload.md` | `0156-tui-bubble-tea-ui-beacon.md` |
| 0157 | 86 | `0157-orchestrator-entity.md` | `0157-patch-inbox.md` |
| 0158 | 91 | `0158-blocked-and-dependencies.md` | `0158-delivery-steer-queue.md` |
| 0185 | 96 | `0185-mcp-beacon-wake.md` | `0185-otel-tracing.md` |
| 0185 | 101 | `0185-mcp-beacon-wake.md` | `0185-background-server-state-owner.md` |
| 0186 | 106 | `0186-mcp.md` | `0186-wake.md` |
| 0187 | 111 | `0187-outbox.md` | `0187-wakeadapter.md` |
| 0188 | 116 | `0188-gh.md` | `0188-spoolsource-mcppullsource.md` |
| 0189 | 121 | `0189-internal-scheduler.md` | `0189-honest-delivery-unchanged-ladder.md` |
| 0190 | 126 | `0190-mcp.md` | `0190-webhook-receiver.md` |
| 0191 | 131 | `0191-tui-bubble-tea-ui-beacon.md` | `0191-tui-http-sse.md` |
| 0192 | 136 | `0192-metrics-fallback-otel-opt-in.md` | `0192-patch-inbox.md` |
| 0193 | 141 | `0193-delivery-steer-queue.md` | `0193-three-idempotency-keys.md` |
| 0194 | 146 | `0194-cli-opencode-api.md` | `0194-connection-json.md` |
| 0196 | 151 | `0196-terminal-guard-snooze-loop.md` | `0196-single-background-server-state-owner.md` |
| 0197 | 156 | `0197-resolved-cwd.md` | `0197-single-operator-chat-control.md` |
| 0198 | 161 | `0198-merge-driver-expectedheadoid.md` | `0198-outbox.md` |
| 0199 | 166 | `0199-capability.md` | `0199-wake.md` |
| 0200 | 171 | `0200-pr.md` | `0200-resolved-cwd.md` |
| 0260 | 176 | `0260-fix-watch.md` | `0260-fixer.md` |

Риск: средний. Удаление 35 файлов убирает 642 строки истории редакций ADR. Если удалить файлы без правки baseline, гейт поднимет DOC-BASELINE-STALE на всех 35 строках, потому что для них не останется findings. Docs job в CI сейчас informational, поэтому required-проверки от этого не сломаются. После ответа владельца нужны три правки одним PR: удалить 35 файлов из таблицы, убрать 35 строк DOC-ADR-DUP из docs-baseline.json, снизить baseline_limit с 40 до 5. Карточка остаётся todo до решения владельца.
