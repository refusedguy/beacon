# ATX-M05. Довести закрытие карточки до main
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/mcp.go, internal/infra/mcp/tool_task_close.go, docs/runbooks/CLOSE-PROCEDURE.md
Зависимости: 
Ссылка: docs/research/mcp-close-chain-gaps.md, finding M05; docs/research/task-close-postmerge-audit.md, раздел 4
Агент: нет
Время: нет
Цель: Сделать так, чтобы статус `done`, который пишет Beacon, попадал в `origin/main`, а не оставался в рабочем дереве.
Контекст: Источник: docs/research/mcp-close-chain-gaps.md, finding M05. `closeLinkedCardFile` пишет карточку через `os.WriteFile` (`cmd/beacon/mcp.go:124`, `:153-180`). `beacon_task_close` меняет статус и ledger на диске (`internal/infra/mcp/tool_task_close.go:280-287`). Коммита и PR нет. Ledger лежит в state dir вне репозитория (`cmd/beacon/doctor.go:258-289`). Baseline: 17 карточек без правки в main по аудиту от `8a766a5d`, 79 изменённых файлов `docs/tasks/` в текущем дереве.
Шаги: Выбрать один способ доставки статуса: docs-PR по образцу ветки 6b скилла `beacon-executor` или отдельный коммит, который создаёт оркестратор после `merged`. Добавить проверку расхождения между статусом карточки в дереве и в `origin/main`. Прописать шаг в `docs/runbooks/CLOSE-PROCEDURE.md`. Отдельно разобрать случай, когда `db.Link` не записал связь: ошибка сейчас только логируется (`cmd/beacon/mcp.go:589-603`).
Проверка: Тест локального закрытия показывает, что после закрытия появляется артефакт для PR. Проверка расхождения ловит карточку, изменённую только в рабочем дереве.
Приёмка: Карточка, закрытая через merge или MCP, доезжает до main вместе со ссылкой на PR. В runbook описан порядок действий для случая, когда PR ещё нет.
