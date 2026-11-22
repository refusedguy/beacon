# BCX-60. Проверить ACP wake sequence живым адаптером
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/wake/acp_smoke_test.go, docs/evals/w06-acp-wake-sequence.md, docs/evals/w07-acp-role-matrix.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Расширить smoke-тест ACP от handshake до реального wake.
Контекст: Источник: docs/research/e2e-coverage-audit.md, таблица строка 9. acp_smoke_test.go только делает initialize, session/new, session/load. Сценарии W06 и W07 описаны только в документах. session/prompt, session/resume и роль клиента против агента не проверяются живым адаптером. Базовый замер и приоритет из отчёта: P2.
Шаги: Вызвать session/prompt через ACP и проверить доставку steer. Проверить resume и матрицу ролей из W07. Добавить негатив с неверной ролью.
Проверка: CI зелёный. Живой ACP-сценарий проходит prompt и resume. Матрица ролей покрыта тестом.
Приёмка: ACP подтверждён как канал wake, а не только как handshake.

Итог: Already landed in PR #1319 (commit b8c81e1fb5fcba66db422978272c67dd78bf5cd3). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1319 merged 2026-11-21T22:52:25+05:00; commit b8c81e1fb5fcba66db422978272c67dd78bf5cd3; git merge-base --is-ancestor b8c81e1fb5fcba66db422978272c67dd78bf5cd3 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1319 b8c81e1f
Время закрытия: 2026-11-22T11:24:28Z
