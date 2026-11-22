# ATX-M06. Писать канонические поля закрытия в карточку
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/tool_task_close.go, internal/infra/taskstore/close.go
Зависимости: 
Ссылка: docs/research/mcp-close-chain-gaps.md, finding M06; docs/runbooks/CLOSE-PROCEDURE.md
Агент: нет
Время: нет
Цель: Сохранить причину, цель дубликата, автора и время закрытия в самой карточке, а не только в файле состояния.
Контекст: Источник: docs/research/mcp-close-chain-gaps.md, finding M06. `beacon_task_close` держит `reason` и `duplicateOf` в sidecar-ledger (`internal/infra/mcp/tool_task_close.go:13-14`, `:156-169`, `:273-287`). В файл карточки уходит только `Статус`. Парсер `taskstore` читает `Причина`, `Дубликат`, `Закрыл` и `Время закрытия` из файла (`internal/infra/taskstore/store.go:203-206`). Baseline: 0 из 4 полей закрытия заполняются после вызова MCP-инструмента.
Шаги: Вынести запись канонических полей в общий модуль записи, доступный и `taskstore.CloseCard`, и адаптеру MCP. Учесть запрет на импорт соседнего infra: модуль должен жить в слое ниже или быть портом с двумя адаптерами. Передать `reason`, `duplicateOf`, `agentID` и время в вызов записи. Оставить базовые 13 полей без изменения. Покрыть тестом чтение карточки через `taskstore.ParseCard` после MCP-закрытия.
Проверка: Тест проверяет, что все четыре поля закрытия читаются парсером. Тест на duplicate проверяет каноническое поле `Дубликат`, а не `Дублирует`.
Приёмка: Карточка после MCP-закрытия содержит причину и доказательство в видимых полях. Ledger остаётся только как ключ идемпотентности.

Итог: Cardindex resolves done: OwnerFiles overlap with AUD-05 (already done). Supporting merged commit 0efa2344ba84 (PR #1469) touches the card OwnerFiles.
Доказательство: Commit 0efa2344ba84ced85d9dae11d63947613259818c (PR #1469, fix(infra): один лок и формат закрытия карточек (AUD-05) (#1469)) touches OwnerFiles internal/infra/mcp/tool_task_close.go, internal/infra/taskstore/close.go; git merge-base --is-ancestor 0efa2344ba84ced85d9dae11d63947613259818c origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1469 0efa2344ba84
Время закрытия: 2026-11-22T11:27:01Z
