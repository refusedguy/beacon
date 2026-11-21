# ADR 0032. Четыре идеи из отчёта M-github для ночной смены

Статус: accepted

Этот ADR фиксирует четыре неразложенные идеи из отчёта M-github «Оркестраторы» от 19 ноября 2026 года, раздел «Идеи для night-shift manager» (пункты 3, 5, 9 и 10). Владелец ранее велел завести все задачи, которые хотели найти в аналогах, без дополнительных вопросов. По ADR 0020 карточки создаются только из принятого ADR. Ни одна из четырёх идей не описана в ADR 0024-0031 и в существующих карточках `docs/tasks/`. Карточки этим ADR не создаются.

## Решение

### 1. Структурированная передача между агентами вместо «прочитай весь чат»

Решение. Передача задачи от одного агента к другому несёт фиксированные поля `intent`, `evidence`, `patch_plan`, `verification_plan` и `risk_notes` вместо отсылки к полной истории чата.

Причина. Отчёт M-github, пункт 9 [mcp-hub](https://github.com/imhassla/mcp-hub/blob/main/README.md#L102-L115): mcp-hub предлагает именно поля `intent`, `evidence`, `patch_plan`, `verification_plan` и `risk_notes`, а [pi-dynamic-workflows](https://github.com/QuintinShaw/pi-dynamic-workflows/blob/main/README.md#L17) держит промежуточные результаты в переменных скрипта ([M-github, пункт 9](/home/nbook/Projects/tmp/beacon-research/M-github-orchestrators.md#L417)). Заранее заданный контракт снимает с принимающего агента необходимость читать весь чат и делает передачу проверяемой.

Последствия. Появляется тип структурированной передачи с пятью обязательными полями; история чата перестаёт быть единственным источником контекста для преемника. Владельцы файлов для будущей карточки: `internal/domain/types.go`, `internal/app/taskcli/service.go`, `internal/infra/mcp/schema.go` и тесты к ним.

Отклонённое. Передача всей истории чата как контекста. Объём растёт, а принимающий агент сам восстанавливает намерение, доказательства и план проверки.

### 2. Доставка как подтверждённый факт: «отправил» и «агент проснулся» разные события

Решение. Подтверждение пробуждения агента отделяется от уже существующих событий доставки: факт «агент проснулся» фиксируется отдельным событием и не выводится из `delivered`, `claimed` или `read`.

Причина. Отчёт M-github, пункт 5 [bide](https://github.com/bide-ai/bide/blob/main/README.md#L45): bide записывает, подтвердил ли endpoint получение, и для уведомления ночного менеджера «отправил» и «агент проснулся» это разные события ([M-github, пункт 5](/home/nbook/Projects/tmp/beacon-research/M-github-orchestrators.md#L409)). ADR 0014 уже различает delivered, claimed и read, но подтверждение самого пробуждения в них не описано, поэтому этот ADR уточняет именно его, не дублируя квитанции адаптера.

Последствия. Появляется отдельный факт подтверждения пробуждения рядом с доставкой и прочтением; менеджер может отличить принятие сообщения от начала работы агента. Владельцы файлов для будущей карточки: `.opencode/plugins/beacon-bridge.js`, `internal/infra/mcp/tool_wake.go`, `internal/infra/store/observability.go`, `internal/domain/types.go` и тесты к ним.

Отклонённое. Считать `delivered` или `read` доказательством пробуждения. Сообщение может попасть в историю или быть взято в работу без запуска нового хода агента.

### 3. Воскрешение после падения как нормальный путь

Решение. Зависшая задача восстанавливается штатно: reaper переводит зависшие `running` в ожидание воскрешения, heartbeat подтверждает живой процесс, а результат читается из нового процесса без потери обязательства.

Причина. Отчёт M-github, пункт 10 [TaskBridge](https://github.com/Parithosh-Varma/TaskBridge/blob/main/README.md#L28): в TaskBridge есть reaper для зависших `running` и heartbeat, а [mcp-durable-tasks](https://github.com/AndresSaa/mcp-durable-tasks/blob/main/README.md#L86-L97) в демо убивает сервер `SIGKILL` и читает результат из нового процесса ([M-github, пункт 10](/home/nbook/Projects/tmp/beacon-research/M-github-orchestrators.md#L419)). Падение процесса превращается из исключения в ожидаемый путь с сохранённым результатом.

Последствия. Появляются reaper и heartbeat как часть цикла задачи, а чтение результата не привязано к процессу, который её начал. Владельцы файлов для будущей карточки: `internal/infra/taskstore/store.go`, `internal/domain/transition.go`, `internal/infra/store/db.go`, `internal/infra/mcp/tool_get_result.go`, `internal/infra/mcp/tool_wait_result.go` и тесты к ним.

Отклонённое. Удалять задачу при падении процесса и создавать новую заново. Теряется обязательство и уже полученный результат, что ломает идемпотентность из ADR 0028.

### 4. Атомарный claim как граница ответственности: одна задача один владелец

Решение. Одна задача имеет ровно одного владельца: claim атомарен, второй владелец не может взять ту же задачу, пока первый не освободил её.

Причина. Отчёт M-github, пункт 3 [beads](https://github.com/gastownhall/beads/blob/main/README.md#L75), [light-kanban](https://github.com/light-cheng/light-kanban/blob/main/README.md#L190) и [paperclip](https://github.com/paperclipai/paperclip/blob/master/README.md#L213) делают атомарный claim границей ответственности; для ночной смены это убирает споры «чей это PR» ([M-github, пункт 3](/home/nbook/Projects/tmp/beacon-research/M-github-orchestrators.md#L405)).

Последствия. Введение атомарного claim с единственным владельцем задачи и явным освобождением; ответственность за PR и артефакт связывается с владельцем. Владельцы файлов для будущей карточки: `internal/domain/identity.go`, `internal/infra/taskstore/store.go`, `internal/infra/store/db.go`, `internal/infra/mcp/tool_claim.go`, `internal/app/taskcli/service.go` и тесты к ним.

Отклонённое. Разрешать нескольким агентам одновременно владеть одной задачей. Возникают параллельные правки и споры о том, кто ответственен за результат.
