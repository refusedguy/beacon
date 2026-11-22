# PROTO-07. Поднять graphReady до типизированного сигнала
Статус: done
Premise: PR #1457, коммит 058f943014719b3daf64b4479398feeffbe21068, добавил типизированный сигнал GraphReadySource в internal/infra/mcp/tool_stats.go. git merge-base --is-ancestor 058f943014719b3daf64b4479398feeffbe21068 origin/main вернул 0. git grep -n GraphReadySource origin/main -- internal/infra/mcp/tool_stats.go даёт tool_stats.go:568. Строковый маркер graphReady сохранён в loadErrors как совместимый вывод.
baseline: было 0 типизированных полей, стал 1 тип GraphReadySource. Проверка: git grep -n 'type GraphReadySource' origin/main -- internal/infra/mcp/tool_stats.go даёт tool_stats.go:568.
Фаза: P2
Тип: task
Владелец файлов: internal/infra/mcp/tool_stats.go, internal/infra/mcp/tool_stats_graph_test.go
Зависимости: 
Ссылка: 
Агент: exe-mp04
Время: 2026-11-22T06:25:35Z
Цель: Сделать готовность графа читаемым полем, а не строкой в loadErrors.
Контекст: Источник: docs/research/audit-04-protocols.md, таблица строка 4. tool_stats.go:45 задаёт префикс graphReady, :461 добавляет запись в loadErrors. Помощники graphReadyEntry и graphReady в :557-568 используются только в тестах tool_stats_graph_test.go:29-120. AGENTS.md:52 обещает маркер в ответе claim или stats. Baseline: 0 типизированных полей.
Шаги: Добавить поле в схему ответа stats или в ответ task_claim. Сохранить fail-closed: отсутствие читателя или ошибка чтения дают false. Оставить строковый маркер как совместимый вывод на переходный период.
Проверка: CI зелёный. Тест на свежий индекс, устаревший индекс и отсутствие читателя. Тест на то, что старый маркер всё ещё читается.
Приёмка: Изменения только в OwnerFiles карточки. Server.go, AGENTS.md и шаблоны не трогаются.
