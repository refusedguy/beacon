# BCX-77. Унифицировать разбор булевых env-переменных
Статус: done
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/server.go, cmd/beacon/mcp.go, internal/infra/observe/otel.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Один набор правил для 1/true/yes/on во всех переменных.
Контекст: Источник: docs/research/config-env-audit.md, итог строка 7. server.go:79-86 и mcp.go:384-389 нормализуют регистр и пробелы; otel.go:157 использует EqualFold только для регистра; mcp.go:173-176 для BEACON_INIT_BYPASS не нормализует ничего. TRUE или ' 1' дают тихий precheck failure. Базовый замер и приоритет из отчёта: P1.
Шаги: Вынести общую функцию parseBoolEnv в отдельный пакет. Применить ко всем булевым переменным, включая BEACON_INIT_BYPASS и BEACON_OTEL_ENABLED. Добавить табличный тест на регистр и пробелы.
Проверка: CI зелёный. Табличный тест проходит для всех четырёх форм. Ни одна переменная не зависит от регистра по-своему.
Приёмка: Оператор может писать TRUE и ' 1' без тихого изменения поведения.

Итог: Already landed in PR #1349 (commit 2f4a2a471f4e6a8beeb2023a090d8a3be5bb12f8). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1349 merged 2026-11-22T09:36:49+05:00; commit 2f4a2a471f4e6a8beeb2023a090d8a3be5bb12f8; git merge-base --is-ancestor 2f4a2a471f4e6a8beeb2023a090d8a3be5bb12f8 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1349 2f4a2a47
Время закрытия: 2026-11-22T11:24:28Z
