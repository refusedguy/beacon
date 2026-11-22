# MEM-03. Не терять delivery и resume и возвращать stderr в wake-транспорте
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/wake/deliver.go, internal/infra/wake/deliver_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Передавать resume в HTTP-порт и сохранять stderr CLI-вызовов.
Контекст: Источник: docs/research/audit-02-memory.md, строки таблицы про delivery/resume и Output. internal/infra/wake/deliver.go:36 объявляет PromptFunc без параметра resume. В Deliver переменные считаются в :297, режим меняется в :310 и :312, но вызов в :334 передаёт только delivery. CLI-fallback в :355 и :357 кладёт resume в body, поэтому два транспорта ведут себя по-разному. Три вызова .Output() живут в :370, :906 и :1808. Адаптер в cmd/beacon/mcp.go:1722 обновляется карточкой MEM-04.
Шаги: Расширить PromptFunc до трёх параметров текста плюс delivery и resume. Передавать resume в вызове :334. Заменить .Output() на .CombinedOutput() в :370, :906 и :1808 так, чтобы stderr попадал и в sessionNotFound, и в текст ошибки. Обновить тесты в deliver_test.go на новый тип функции и на проверку текста ошибки. Не трогать opencode/client.go и mcp.go.
Проверка: CI зелёный. Тест с busy-сессией видит resume в вызове порта. Тест с падающим CLI видит stderr в ошибке. Поиск .Output() в deliver.go не даёт совпадений.
Приёмка: Оба транспорта получают delivery и resume. Текст stderr доходит до журнала и не теряется при диагнозе.

Итог: Already landed in PR #1455 (commit 254267c2394dfdb4bc2eeb772ddeaa791b15c989). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1455 merged 2026-11-22T10:00:33+05:00; commit 254267c2394dfdb4bc2eeb772ddeaa791b15c989; git merge-base --is-ancestor 254267c2394dfdb4bc2eeb772ddeaa791b15c989 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1455 254267c2
Время закрытия: 2026-11-22T11:25:31Z
