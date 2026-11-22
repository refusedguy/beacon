# BCX-26. Унифицировать коды выхода для ошибок валидации
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/taskstore/store.go, cmd/beacon/catalog.go, docs/adrs/0016-build-system.md
Зависимости: 
Ссылка: ADR 0016
Агент: нет
Время: нет
Цель: Одинаковая ошибка валидации должна давать одинаковый код во всех командах.
Контекст: Источник: docs/research/cli-ux-audit.md, таблица 5 строка 6. taskstore.ExitValidation в store.go:25 даёт 1; остальной CLI даёт 2 и для валидации. task list --format yaml даёт 1, list даёт 2. ADR 0016 и store.go:22-27 FIX 1 = валидация, 2 = ошибка запуска. Базовый замер и приоритет из отчёта: P1.
Шаги: Зафиксировать правило в ADR 0016 или CLI-спецификации. Применить его ко всем раннерам, включая catalog. Добавить тест, который сравнивает код для неверного --format в двух командах.
Проверка: CI зелёный. Оба вызова неверного --format дают одинаковый код. Тест контракта кодов выхода проходит.
Приёмка: Пользовательский провал валидации имеют один код во всём CLI.

Итог: Already landed in PR #1285 (commit 2ad5f5a6c2709b6f83a89feb6911fa323522eebf). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1285 merged 2026-11-22T09:07:34+05:00; commit 2ad5f5a6c2709b6f83a89feb6911fa323522eebf; git merge-base --is-ancestor 2ad5f5a6c2709b6f83a89feb6911fa323522eebf origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1285 2ad5f5a6
Время закрытия: 2026-11-22T11:24:28Z
