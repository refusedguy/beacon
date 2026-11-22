# CSx-07. Происхождение сообщения и корреляция по uuid
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/domain/types.go, internal/infra/wake/deliver.go
Зависимости:
Ссылка: docs/research/comp-claude-sdk.md; клон anthropics/claude-agent-sdk-python 3184360
Агент: нет
Время: 2026-11-22
Цель: Нести provenance сообщения и uuid запроса, чтобы сопоставлять результат с вызвавшим его промптом.
Контекст: SDK определяет `MessageOrigin` с дискриминатором `kind` и полями `server`, `from`, `senderTaskId`, `body` и `verifiedPeerPid` (`src/claude_agent_sdk/types.py:1338-1420`). Тип прямо предупреждает, что `origin` описывает только начало хода, а результат нужно сопоставлять по `user_message_uuid` и `user_message_uuids` (`types.py:1394-1411`, `:1805-1824`). В beacon `WakePayload` хранит четыре идентификатора без provenance (`internal/domain/types.go:546-551`), а `Envelope` сериализует пять ключей (`internal/infra/wake/deliver.go:479-495`). CS-04 восстанавливает сессию из store, но не решает задачу сопоставления хода.
baseline: `rg -n 'MessageOrigin|user_message_uuid|correlationID' internal cmd` на `origin/main` 7244e15b даёт 0 строк.
Шаги: Добавить в `WakePayload` поля `origin` и `correlationID` с проверкой на пустое значение. Передавать uuid запроса в `Envelope` при доставке wake. Принимать сообщения с произвольным `kind` как нечеловеческие, если значение неизвестно. Не использовать `from` как доказательство личности.
Проверка: Тест отправляет два промпта с разными uuid и получает два вставленных сообщения. Каждый результат сопоставляется со своим uuid, а неизвестный `kind` классифицируется как нечеловеческий. Измеримая проверка: число корректно сопоставленных результатов равно 2; `rg -n 'correlationID|MessageOrigin' <ветка> -- internal` даёт не менее 2 строк.
Приёмка: Перекрёстные wake и вставленные turn не подменяют друг друга при сопоставлении результата.
