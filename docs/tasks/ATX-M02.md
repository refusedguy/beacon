# ATX-M02. Привязать golden tools/list к рабочему серверу
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/contract_test.go, cmd/beacon/mcp.go, tests/e2e/e2e_test.go
Зависимости: 
Ссылка: docs/research/mcp-close-chain-gaps.md, finding M02; BCX-52
Агент: нет
Время: нет
Цель: Сделать золотой ответ `tools/list` описанием того набора инструментов, который отдаёт рабочий сервер.
Контекст: Источник: docs/research/mcp-close-chain-gaps.md, finding M02. Golden меряется на `NewServer()` в `internal/infra/mcp/contract_test.go:119-152`. Рабочий `cmd/beacon/mcp.go:507-515` дополнительно вызывает `mcp.AttachWake`, и `internal/infra/mcp/tool_wake.go:1801-1810` регистрирует одиннадцатый инструмент. E2E уже ждёт 11 имён. Baseline: golden 19467 байт и SHA `5d1d226c…` для 10 инструментов, production отдаёт 11.
Шаги: Выбрать один composition root как источник золотого ответа. Добавить в контрактный тест проверку обоих наборов, если сервер без wake остаётся публичным API. Обновить байты, SHA, комментарии в `contract_test.go` и `schema.go`. Сверить ожидания E2E. Зафиксировать решение об одиннадцатом имени отдельным ADR, если оно ещё не принято.
Проверка: Контрактный тест и E2E зелёные в CI. Любая правка описания wake меняет golden и падает до обновления констант.
Приёмка: В репозитории описан один источник истины для рабочего набора инструментов. Число имён в golden, E2E и документах совпадает.

Итог: Cardindex resolves done: OwnerFiles overlap with ATX-03 (already done). Supporting merged commit eb2de93e6c2b (PR #1623) touches the card OwnerFiles.
Доказательство: Commit eb2de93e6c2bff9088ace68edfbe08e3e1a1d62f (PR #1623, fix(e2e): retry opencode 503 on session create to remove startup flake (#1623)) touches OwnerFiles cmd/beacon/mcp.go, internal/infra/mcp/contract_test.go, tests/e2e/e2e_test.go; git merge-base --is-ancestor eb2de93e6c2bff9088ace68edfbe08e3e1a1d62f origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1623 eb2de93e6c2b
Время закрытия: 2026-11-22T11:27:01Z
