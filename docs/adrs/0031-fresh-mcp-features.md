# ADR 0031. Шесть фич из свежего отчёта Q

Статус: accepted

Этот ADR фиксирует кандидатов из отчёта Q «Свежие находки MCP: 18 репозиториев вне прежнего списка» от 19 ноября 2026 года. Владелец велел завести все задачи, которые хотели найти в аналогах, без дополнительных вопросов. По ADR 0020 карточки создаются только из принятого ADR. Ни одна из шести фич не описана в ADR 0028 или в существующих карточках `docs/tasks/`.

## Решение

### 1. seq и счётчики missing/duplicates для потока событий

Решение. Каждое событие потока Beacon несёт монотонный `seq`, а финальное событие честно называет `missing` и `duplicates` и при потере чанков возвращает полный результат.

Причина. Отчёт Q, пункт 1.2 [mcp-partials](https://github.com/LeeGyeongPil/mcp-partials/blob/main/README.md#L44-L56): каждый чанк несёт `seq`, финальное событие сообщает `missing` и `duplicates`, а при невозможности доставить чанки клиент всё равно получает полный результат. Это готовая модель надёжности для событий Beacon без нового брокера.

Последствия. Поток событий получает номер и явные счётчики потерь и дублей; клиент может восстановить порядок и не зависит от доставки каждого чанка. Владельцы файлов для будущей карточки: `internal/domain/types.go`, `internal/domain/fanout.go`, `internal/infra/store/observability.go`, `internal/infra/mcp/meta.go` и тесты к ним.

Отклонённое. Сам TypeScript-хелпер mcp-partials: он привязан к SDK v1/v2, не имеет слоя задач и не даёт долговечности ([Q, пункт 1.2](https://github.com/LeeGyeongPil/mcp-partials/blob/main/README.md#L20)).

### 2. Совместимость с тремя диалектами MCP Tasks

Решение. MCP-слой Beacon определяет диалект клиента и отвечает настоящей задачей для ревизии 2025-11-25, `resultType: "task"` после `--after` для ревизии 2026-07-28, а клиенту без Tasks отдаёт обычный job id с инструментами результата, статуса и отмены.

Причина. Отчёт Q, пункт 1.1 [mcp-coatcheck](https://github.com/emrehangorgec/mcp-coatcheck/blob/main/README.md#L34-L42): готовый адаптивный фолбэк на три уровня возможностей клиента снимает проблему, что клиенты говорят на разных версиях протокола. Свой протокол Beacon остаётся, берётся только схема совместимости.

Последствия. Переговоры о диалекте и выбор формы ответа появляются на границе MCP; ядро не знает о версиях протокола. Владельцы файлов для будущей карточки: `internal/infra/mcp/server.go`, `internal/infra/mcp/schema.go`, `internal/infra/mcp/meta.go`, `internal/infra/mcp/tool_get_result.go` и тесты к ним.

Отклонённое. Прокси целиком: mcp-coatcheck хранит билеты в памяти с TTL, не управляет задачей и не обеспечивает долговечность ([Q, пункт 1.1](https://github.com/emrehangorgec/mcp-coatcheck/blob/main/README.md#L79-L83)).

### 3. needs_input состояние задачи с ответом submit(continue_of)

Решение. Задача, которой агент задал вопрос, переходит в `needs_input`; ответ человека возвращается в ту же задачу через `submit(continue_of=…)` с ограничением числа раундов.

Причина. Отчёт Q, пункт 2.2 [agent-ex](https://github.com/wuruofan/agent-ex/blob/main/README.md#L28-L30): состояние `needs_input` паркует задачу, а ответ скармливается обратно через `submit(continue_of=…)` до пяти раундов продолжения. Это проверенная семантика раундов для связки `steer` и `ack` из ADR 0028.

Последствия. Появляется явное состояние задачи и переход из него по ответу; `wait_result` продолжает ожидание после ответа, не создавая новую задачу. Владельцы файлов для будущей карточки: `internal/domain/types.go`, `internal/domain/transition.go`, `internal/infra/mcp/tool_get_result.go`, `internal/infra/mcp/tool_wait_result.go`, `internal/infra/mcp/tool_ack.go` и тесты к ним.

Отклонённое. Воркеры agent-ex целиком: они запускаются с `--dangerously-skip-permissions` и `opencode --auto` без второго подтверждения ([Q, пункт 2.2](https://github.com/wuruofan/agent-ex/blob/main/README.md#L41-L45)).

### 4. Цепочка фолбэка агентов при 429 для CLI-менеджера

Решение. При 429 CLI-менеджер переводит задачу по цепочке Claude Code → Hermes → Gemini, сохраняя один и тот же идентификатор задачи.

Причина. Отчёт Q, пункт 2.3 [cowork](https://github.com/slashman413/cowork/blob/main/README.md#L44): при упирании в квоту задача прозрачно переходит Claude Code → Hermes → Gemini → Ollama, готовое правило маршрутизации при 429 для CLI-менеджера.

Последствия. Маршрутизация живёт в CLI-менеджере, а не в ядре MCP; смена исполнителя не меняет внешний job id и остаётся видимой в статусе. Владельцы файлов для будущей карточки: `internal/app/taskcli/service.go`, `internal/infra/mcp/tool_wake.go`, `internal/domain/types.go` и тесты к ним.

Отклонённое. Менеджер cowork целиком: хранение только JSON и Markdown на диске, 285+ персон и широкий веб-интерфейс ([Q, пункт 2.3](https://github.com/slashman413/cowork/blob/main/README.md#L46), [README.md#L49](https://github.com/slashman413/cowork/blob/main/README.md#L49)).

### 5. RejectDuplicate и idempotency_key для идемпотентного старта

Решение. Повторный старт задачи с тем же `idempotency_key` возвращает уже существующую задачу, а не создаёт вторую; конфликт фиксируется как `RejectDuplicate`.

Причина. Отчёт Q, пункт 1.4 [durable-mcp](https://github.com/nicogranuja/durable-mcp/blob/main/README.md#L58-L62): job id это workflow ID Temporal, а `RejectDuplicate` плюс `idempotency_key` превращают повторный старт в получение уже существующей задачи. Проект на Go, как и Beacon, это прямой ответ на вопрос идемпотентности `ack`.

Последствия. Ключ идемпотентности пишется в одной транзакции с состоянием задачи по ADR 0015 и не ломает правило ADR 0028 о `clientRequestId`. Владельцы файлов для будущей карточки: `internal/domain/identity.go`, `internal/infra/mcp/tool_ack.go`, `internal/infra/mcp/tool_wait_result.go`, `internal/infra/store/receipts.go`, `internal/infra/store/db.go` и тесты к ним.

Отклонённое. Зависимость от Temporal: durable-mcp требует отдельного сервера и помечен как work in progress ([Q, пункт 1.4](https://github.com/nicogranuja/durable-mcp/blob/main/README.md#L11), [README.md#L21-L22](https://github.com/nicogranuja/durable-mcp/blob/main/README.md#L21-L22)).

### 6. human gate с возвратом на конкретный шаг

Решение. Узел human gate ставит задачу на паузу до проверки человеком, который может одобрить, добавить контекст или отправить работу назад на конкретный шаг; последнее даёт семантику `ack` с переделкой.

Причина. Отчёт Q, пункт 3.2 [LoopForge](https://github.com/Tencent/LoopForge/blob/main/README.md#L165-L168): гейт позволяет одобрить, добавить контекст или вернуть работу на конкретный шаг, что богаче бинарного `ack` из ADR 0028.

Последствия. `ack` получает несколько исходов и целевой шаг возврата; лестница из ADR 0017 и состояние из ADR 0025 должны различать одобрение и переделку. Владельцы файлов для будущей карточки: `internal/domain/types.go`, `internal/domain/transition.go`, `internal/infra/mcp/tool_ack.go`, `internal/infra/ladder/transitions.go` и тесты к ним.

Отклонённое. Платформу LoopForge целиком: Docker-песочницы, визуальный дизайнер SOP и собственный сервер ([Q, пункт 3.2](https://github.com/Tencent/LoopForge/blob/main/README.md#L177-L181)), плюс неясная лицензия Other/NOASSERTION ([LICENSE](https://github.com/Tencent/LoopForge/blob/main/LICENSE)). Берётся только семантика гейта.
