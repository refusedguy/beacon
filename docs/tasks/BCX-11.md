# BCX-11. Развести два контракта literals timeout
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: internal/domain/types.go, internal/infra/mcp/tool_get_result.go, internal/infra/mcp/tool_wait_result.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать коллизию: вердикт timeout и локальный long-poll timeout не должны давать противоположные инструкции агенту.
Контекст: Источник: docs/research/error-codes-audit.md, P0 «Один и тот же literals timeout». domain.VerdictTimeout в types.go:32 даёт nextAction=wait и подсказку «Watch finished» в tool_get_result.go:381-382. WaitStopTimeout в tool_wait_result.go:106 даёт exit code 143 (:134) и Retryable true (:144-146). Базовый замер и приоритет из отчёта: P0.
Шаги: Выбрать разные строковые значения или добавить поле источника в выходной контракт. Обновить nextStepFor и ExitCode. Добавить тест, который различает терминальный вердикт и повторяемое ожидание.
Проверка: CI зелёный. Тесты get_result и wait_result проверяют разные значения. Ни один агент не получает wait для retryable long-poll.
Приёмка: Клиент по строке однозначно определяет, ждать или завершать ход. exit code 143 не сопровождается nextAction=wait.
