# BCX-59. Заменить внешний драйвер вердикта демоном полного цикла
Статус: done
Фаза: P1
Тип: task
Владелец файлов: tests/e2e/e2e_test.go, docs/adrs/0061-local-e2e-with-live-opencode.md
Зависимости: 
Ссылка: ADR 0061
Агент: нет
Время: нет
Цель: Сделать verdict полного цикла результатом демона, а не внешнего драйвера теста.
Контекст: Источник: docs/research/e2e-coverage-audit.md, таблица строка 8. live_opencode_full_mcp_cycle пишет вердикт через domain.Transition из внешнего драйвера (e2e_test.go:976-1014). Демона нет. Гейт ShipFacts из ADR 0061 пункт 2 не написан. Базовый замер и приоритет из отчёта: P1.
Шаги: Вынести решение в демон и проверять, что цикл watch, register, wait, get, ack завершается без внешнего вызова Transition. Написать гейт ShipFacts по ADR 0061. Добавить тест на расхождение фактов.
Проверка: CI зелёный. Тест полного цикла не вызывает domain.Transition напрямую. Гейт ShipFacts присутствует в наборе.
Приёмка: Вердикт цикла вычисляется демоном из наблюдаемых фактов.

Итог: Already landed in PR #1355 (commit 83436479ba44c8c2f6cd44e4fb6a43059a2e92e3). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1355 merged 2026-11-22T09:46:52+05:00; commit 83436479ba44c8c2f6cd44e4fb6a43059a2e92e3; git merge-base --is-ancestor 83436479ba44c8c2f6cd44e4fb6a43059a2e92e3 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1355 83436479
Время закрытия: 2026-11-22T11:24:28Z
