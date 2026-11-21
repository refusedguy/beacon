# BCX-60. Проверить ACP wake sequence живым адаптером
Статус: todo
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
