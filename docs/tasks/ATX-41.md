# ATX-41. Не терять старые записи audit в снимке
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/httpapi/server.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Не делать историю команд старше 200 недоступной.
Контекст: Источник: docs/research/output-limits-audit.md, раздел 4 и строка таблицы 176. internal/infra/httpapi/server.go:52-53 задаёт maxAuditRecords = 200. recordAudit в :421-429 оставляет последние 200 записей и удаляет старые из среза. Continuation token нет. Срез живой, не из БД. Восстановление возможно только если те же факты записаны в постоянный audit store. Baseline: 200 записей.
Шаги: Добавить счётчик total и курсор либо читать записи из постоянного audit store. Обновить контракт и тесты.
Проверка: CI зелёный. Тест с 201 командой показывает total и отдаёт старую запись по курсору.
Приёмка: История команд доступна целиком. Потеря записей в живой памяти не скрывается.

Итог: Already landed in PR #1403 (commit 3366c7a8915ac317e000acf0570b23082acb9e47): fix(httpapi): page audit history with cursor (ATX-41) (#1403)
Доказательство: PR #1403; commit 3366c7a8915ac317e000acf0570b23082acb9e47; git merge-base --is-ancestor 3366c7a8915ac317e000acf0570b23082acb9e47 origin/main exit 0; no duplicate code PR needed.
Причина: already landed in PR #1403 3366c7a8915a
Время закрытия: 2026-11-22T11:27:01Z
