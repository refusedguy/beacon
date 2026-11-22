# Аналог Devin в open source: OpenHands

Дата: 2026-11-22. Клон: `/tmp/opencode/devin-research/OpenHands`, коммит `3e9cf433b0669661833c2025f8d25ac9ba417d31`, глубина 1. Сравнение сделано с beacon на `origin/main` `2d34645b54f45d84dba3a595350f17f8658759da`. Код beacon в этом PR не меняется. `go test`, `go build` и гейты не запускались. Все числа ниже получены чтением кода и командами `rg` по двум явным каталогам: клону и worktree `worktrees/comp-devin`.

Вопрос: какие приёмы planning loop, tool use, state machine, error recovery и human-in-the-loop есть в работающем open-source аналоге Devin и какие из них в beacon отсутствуют или работают иначе.

## 1. Ограничение источника

Main ветка OpenHands теперь содержит веб-клиент и адаптер агент-сервера. Ядро агента живёт в отдельном сервисе, поэтому planning loop и state machine читаются здесь на уровне протокола и клиентской машины состояний. Это достаточно для сравнения контрактов: какие состояния объявляются, кто владеет идентификатором planner, как агент получает ответ человека и как клиент отличает сетевую ошибку от ошибки агента. Утверждения о внутреннем планировщике LLM здесь не делаются.

## 2. Пять приёмов из клона

### 2.1. Planner как отдельное состояние с авторитетным идентификатором

Planner создаётся как отдельная беседа с родительской связью. Идентификатор сначала берётся из ответа сервера `sub_conversation_ids`, а локальная подсказка из localStorage используется только как запасной вариант для старых серверов (`src/hooks/use-handle-plan-click.ts:59-85`, `:126-137`). Это переживает очистку данных браузера и смену машины. Если создание planner не удалось, режим возвращается из plan в code, чтобы пользователь не остался в режиме без собеседника (`use-handle-plan-click.ts:117-123`).

Превью плана показывает только последнее наблюдение за фазу пользователя. Фаза начинается с сообщения пользователя, внутри фазы все наблюдения плана сводятся к одной записи (`src/components/conversation-events/chat/hooks/use-plan-preview-events.ts:16-38`, `:45-98`). Промежуточные правки PLAN.md не создают набор одинаковых карточек.

### 2.2. Переключение режима отклоняется по состоянию и готовности транспорта

Команды `/plan` и `/code` не отправляются как сообщение. Interceptor меняет режим и только потом передаёт задачу. Он возвращается без действия, если агент уже RUNNING, если planner создаётся, если нужный сокет не открыт (`src/hooks/chat/use-plan-mode-interceptor.ts:43-85`, особенно `:55-62` и `:74-76`). Комментарий в коде объясняет, почему `/code` проверяет только основной сокет: кратковременный reconnect planner не должен поглощать команду (`:23-27`).

### 2.3. Подтверждение человека идемпотентно до прихода ответа сервера

Кнопки подтверждения ищут последнее действие агента только в состоянии `AWAITING_USER_CONFIRMATION` (`src/components/shared/buttons/conversation-confirmation-buttons.tsx:30-36`, `:103-109`). Перед вызовом API идентификатор события добавляется в `submittedEventIds` (`:17-21`, `:48-51`). Повторное нажатие, в том числе клавиатурное, игнорируется, пока сервер не подтвердит изменение (`:64-68`, `:91`). Высокий риск показывается отдельным предупреждением до нажатия (`:112-133`). Ответ уходит одной мутацией с полем `accept` (`src/hooks/mutation/use-respond-to-confirmation.ts:12-31`).

Состояния подтверждения объявлены в машине состояний явно: `AWAITING_USER_CONFIRMATION`, `USER_CONFIRMED`, `USER_REJECTED` (`src/types/agent-state.tsx:1-15`). Execution status сервера включает `WAITING_FOR_CONFIRMATION` и отдельный `STUCK` (`src/types/agent-server/core/base/common.ts:58-75`). UI выводит состояние из авторитетного execution status, а REST-ответ используется только для той же беседы и только при отсутствии живого значения (`src/hooks/use-agent-state.ts:52-77`).

### 2.4. Восстановление ошибок различает транспорт, лимит и незавершённость

Транспортные ошибки определяются по списку статусов и по вложенной причине TypeError, затем retry разрешён не более трёх раз (`src/utils/transient-error-retry.ts:3-25`). При 429 базовый delay удваивается и к нему добавляется полный jitter с потолком 30 с, чтобы параллельные вкладки не выходили на лимит синхронно (`src/utils/rate-limit-retry.ts:3-12`, `:31-49`).

Потеря соединения не считается признаком сбоя агента. Hook держит два таймера: 30 с до состояния `reconnecting`, 60 с до `retryable`, и сбрасывает их при смене идентификатора или скрытии вкладки (`src/hooks/use-connection-error-grace-period.ts:4-5`, `:21-40`, `:42-55`). Комментарий в коде прямо отделяет потерю транспорта от отказа агента (`:29`).

### 2.5. Инструмент с асинхронным ответом человека описывает контракт в себе

Инструмент запроса секрета объявляет `idempotentHint: false` и в описании говорит агенту, что вызов подтверждается сразу, ответ придёт отдельным сообщением, ждать в цикле нельзя, один недостающий секрет означает один вызов (`src/api/request-secret-client-tool.ts:7-25`, `:47-53`). Агент получает правило поведения вместе с сигнатурой инструмента, а не из внешнего документа.

## 3. Сравнение с beacon

| Приём OpenHands | Разрыв в beacon | Карточка |
| --- | --- | --- |
| Последнее наблюдение плана на фазу, отдельный planner | `SlotArtifact` хранит имя файла, `ArtifactVerifier` проверяет присутствие; чтения содержимого PLAN.md и редакции плана в коде нет | CV-01 |
| Отклонение переключения режима в неверном состоянии | `planModeOf` превращает любое неизвестное значение в `fast`, ошибки нет | CV-04 |
| Идемпотентность ответа человека и явные состояния подтверждения | Механизм есть, но MCP-ответ `beacon_ack` отдаёт только `ok`; поле `Resumed` из `AckReceipt` до агента не доходит | CV-02 |
| Разделение транспортной ошибки, лимита и паузы | Классификация и `NextRetryAt` есть; в описании `beacon_wait_result` нет правила, что `needs_input` означает остановку ожидания и ответ через `ack(continueOf)` | CV-05 |
| Jitter при повторной попытке после лимита | В GitHub normalizer jitter есть; в backoff провайдера ghci его нет | CV-03 |

## 4. Уже есть, новых карточек не нужно

| Проверенная идея | Где лежит в beacon | Почему дубль не нужен |
| --- | --- | --- |
| Один ответ человека на Watch | `DecisionRegistry` в `internal/infra/mcp/tool_ack.go:194-210`, проверка reviewer и decisionID в `internal/infra/mcp/meta.go:134-154` | Первый ответ побеждает, повтор не меняет состояние, чужой reviewer отклоняется |
| Парковка Watch пока ждём человека | `parkForInput` в `internal/domain/transition.go:348-357`, ранний возврат в `internal/infra/mcp/tool_wait_result.go:1197-1203` | CI-вердикт не будит такую Watch, read не пишется |
| Время следующей попытки доставки | `Obligation.NextRetryAt` в `internal/domain/types.go:455-457`, запись в `internal/infra/ladder/transitions.go:466-468` | Лестница доставки уже различает posted, indeterminate и transport-failed и имеет предел попыток |
| Jitter и классификация ответов GitHub | `internal/infra/github/errors.go:31-35`, `:65-91`, `:165-195`, `:282-290` | 429, 502, 504 и вторичный лимит уже разделены, задержка с jitter есть |
| Проверка адреса получателя | `internal/infra/mcp/tool_watch_pr.go:420-427`, `:681-697`, `ValidSessionID` в `internal/domain/identity.go:341-353` | Идентификатор карточки не проходит как `notify.target` |
| Первая ошибка не затирается повтором | `ON CONFLICT DO NOTHING` в `internal/infra/mcp/tool_get_result.go:205-206` | Повторный опрос не меняет кэш выдержки CI, как и first-error-wins в OpenHands |
| Две живые Watch на один PR не создаются | `closeSuperseded` и проверка live статусов в `internal/infra/mcp/tool_watch_pr.go:745-754` | Повторная регистрация закрывает предыдущую Watch |
| Артефакт слота и токены | `SlotLedger.Burn` в `internal/app/orchestrator/decision.go:629-641` | Карточки RS-CMP-02 и AUTOSP-07 уже описывают токен слота и проверку артефакта; CV-01 смотрит только на содержимое плана и его редакцию |

## 5. Измеримые проверки

Baseline снят на `2d34645b` в worktree `comp-devin`.

| Карточка | Команда baseline | Значение сейчас | Ожидаемое после исправления |
| --- | --- | --- | --- |
| CV-01 | `rg -n 'PLAN.md' internal/app/orchestrator/decision.go` | 2, обе строки комментарии 518 и 571 | Появляется чтение плана, пустой PLAN.md не гасит токен |
| CV-01 | `rg -n 'os.ReadFile|fs.ReadFile' internal/app/orchestrator --glob '*.go'` | 2, оба чтения конфигурации | Появляется отдельный reader плана |
| CV-02 | `rg -n 'resumed|Resumed|AckReceipt' internal/infra/mcp/schema.go` | 0 | Поле расписки есть в схеме ответа |
| CV-03 | `rg -n 'jitter|rand' internal/infra/ghci/provider.go` | 0 | Не меньше одного внедрения jitter |
| CV-04 | Вызов `planModeOf("ful")` в тесте | Возвращает `fast` | Возвращает ошибку, валидные значения не меняются |
| CV-05 | `rg -n 'do not wait|wait in a loop|follow-up|one call per' internal/infra/mcp/server.go` | 0 | Описание содержит явное правило для `needs_input` |

Тесты, предложенные в карточках, в этом PR не запускались по правилу AGENTS.md: проверки идут в CI на PR.

## 6. Источники

- <https://github.com/All-Hands-AI/OpenHands>, клон на коммите `3e9cf433b0669661833c2025f8d25ac9ba417d31`.
- `src/hooks/use-handle-plan-click.ts`, `src/hooks/chat/use-plan-mode-interceptor.ts`, `src/components/conversation-events/chat/hooks/use-plan-preview-events.ts`.
- `src/components/shared/buttons/conversation-confirmation-buttons.tsx`, `src/hooks/mutation/use-respond-to-confirmation.ts`, `src/types/agent-state.tsx`, `src/types/agent-server/core/base/common.ts`, `src/hooks/use-agent-state.ts`.
- `src/utils/transient-error-retry.ts`, `src/utils/rate-limit-retry.ts`, `src/hooks/use-connection-error-grace-period.ts`, `src/api/request-secret-client-tool.ts`.
- Локальные файлы beacon по адресам из разделов 3 и 4 на коммите `2d34645b54f45d84dba3a595350f17f8658759da`.
